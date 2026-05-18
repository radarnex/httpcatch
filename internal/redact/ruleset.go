package redact

import (
	"fmt"
	"net/textproto"
	"slices"
	"strings"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
)

const Redacted = "[REDACTED]"

type cookieMode int

const (
	cookieModeRedact cookieMode = iota
	cookieModeStrip
	cookieModeAllowlist
)

type cookieRule struct {
	mode  cookieMode
	names []string
}

type regexRule struct{}

type Ruleset struct {
	headers     []string
	queryParams []string
	jsonPaths   []string
	regex       []regexRule
	cookies     []cookieRule
}

func NewRuleset(cfg config.RedactionConfig) (*Ruleset, error) {
	headers := make([]string, len(cfg.Headers))
	for i, h := range cfg.Headers {
		headers[i] = strings.ToLower(h)
	}
	queryParams := make([]string, len(cfg.QueryParams))
	copy(queryParams, cfg.QueryParams)

	cookies := make([]cookieRule, 0, len(cfg.Cookies))
	for _, c := range cfg.Cookies {
		mode, err := parseCookieMode(c.Mode)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(c.Names))
		copy(names, c.Names)
		cookies = append(cookies, cookieRule{mode: mode, names: names})
	}

	return &Ruleset{
		headers:     headers,
		queryParams: queryParams,
		cookies:     cookies,
	}, nil
}

func parseCookieMode(s string) (cookieMode, error) {
	switch s {
	case "", "redact":
		return cookieModeRedact, nil
	case "strip":
		return cookieModeStrip, nil
	case "allowlist":
		return cookieModeAllowlist, nil
	default:
		return 0, fmt.Errorf("redaction: cookies: unknown mode %q", s)
	}
}

func (r *Ruleset) IsUnredacted() bool {
	return len(r.headers) == 0 &&
		len(r.queryParams) == 0 &&
		len(r.jsonPaths) == 0 &&
		len(r.regex) == 0 &&
		len(r.cookies) == 0
}

// Redact applies all rule buckets in fixed order: cookie → header → query → JSON-path → regex.
func (r *Ruleset) Redact(rec *capture.CapturedRecord) *capture.CapturedRecord {
	if r.IsUnredacted() {
		return rec
	}
	out := shallowCopyRecord(rec)
	applyCookieRules(out, r.cookies)
	applyHeaderRules(out, r.headers)
	applyQueryRules(out, r.queryParams)
	applyJSONPathRules(out, r.jsonPaths)
	applyRegexRules(out, r.regex)
	return out
}

func applyJSONPathRules(_ *capture.CapturedRecord, _ []string) {}
func applyRegexRules(_ *capture.CapturedRecord, _ []regexRule) {}

func applyQueryRules(out *capture.CapturedRecord, rules []string) {
	for key, vals := range out.Query {
		if slices.Contains(rules, key) {
			for i := range vals {
				vals[i] = Redacted
			}
		}
	}
}

func applyHeaderRules(out *capture.CapturedRecord, rules []string) {
	for key, vals := range out.Headers {
		if slices.Contains(rules, strings.ToLower(key)) {
			for i := range vals {
				vals[i] = Redacted
			}
		}
	}
}

var (
	cookieHeaderKey    = textproto.CanonicalMIMEHeaderKey("cookie")
	setCookieHeaderKey = textproto.CanonicalMIMEHeaderKey("set-cookie")
)

func applyCookieRules(out *capture.CapturedRecord, rules []cookieRule) {
	if len(rules) == 0 || out.Headers == nil {
		return
	}

	cookieKey := findHeaderKey(out.Headers, cookieHeaderKey)
	setCookieKey := findHeaderKey(out.Headers, setCookieHeaderKey)

	for _, rule := range rules {
		if cookieKey != "" {
			vals := out.Headers[cookieKey]
			for i, v := range vals {
				vals[i] = applyRuleToCookieHeader(v, rule)
			}
			if allEmpty(vals) {
				delete(out.Headers, cookieKey)
				cookieKey = ""
			}
		}
		if setCookieKey != "" {
			vals := out.Headers[setCookieKey]
			kept := vals[:0]
			for _, v := range vals {
				redacted, drop := applyRuleToSetCookieHeader(v, rule)
				if drop {
					continue
				}
				kept = append(kept, redacted)
			}
			if len(kept) == 0 {
				delete(out.Headers, setCookieKey)
				setCookieKey = ""
			} else {
				out.Headers[setCookieKey] = kept
			}
		}
	}

	if len(out.Cookies) > 0 {
		out.Cookies = redactCookieSlice(out.Cookies, rules)
	}
}

func findHeaderKey(headers map[string][]string, canonical string) string {
	if _, ok := headers[canonical]; ok {
		return canonical
	}
	for k := range headers {
		if textproto.CanonicalMIMEHeaderKey(k) == canonical {
			return k
		}
	}
	return ""
}

func applyRuleToCookieHeader(value string, rule cookieRule) string {
	if value == "" {
		return value
	}
	parts := strings.Split(value, ";")
	out := parts[:0]
	for _, raw := range parts {
		name := cookieName(raw)
		if name == "" {
			out = append(out, raw)
			continue
		}
		matched := slices.Contains(rule.names, name)
		switch rule.mode {
		case cookieModeRedact:
			if matched {
				out = append(out, replaceCookieValue(raw))
			} else {
				out = append(out, raw)
			}
		case cookieModeStrip:
			if matched {
				continue
			}
			out = append(out, raw)
		case cookieModeAllowlist:
			if matched {
				out = append(out, raw)
			}
		}
	}
	return joinCookieParts(out)
}

// applyRuleToSetCookieHeader processes one Set-Cookie entry. Set-Cookie is
// multi-valued at the header level: each entry is a single cookie whose
// attributes (Path, Domain, etc.) follow the first ';' and must survive
// untouched. The strip and allowlist modes can drop the entry entirely, which
// the caller handles via the returned drop flag.
func applyRuleToSetCookieHeader(value string, rule cookieRule) (string, bool) {
	if value == "" {
		return value, false
	}
	name := cookieName(value)
	if name == "" {
		return value, false
	}
	matched := slices.Contains(rule.names, name)
	switch rule.mode {
	case cookieModeRedact:
		if matched {
			return replaceCookieValue(value), false
		}
		return value, false
	case cookieModeStrip:
		if matched {
			return "", true
		}
		return value, false
	case cookieModeAllowlist:
		if matched {
			return value, false
		}
		return "", true
	}
	return value, false
}

func cookieName(token string) string {
	trimmed := strings.TrimLeft(token, " \t")
	semi := strings.IndexByte(trimmed, ';')
	if semi >= 0 {
		trimmed = trimmed[:semi]
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq < 0 {
		return strings.TrimSpace(trimmed)
	}
	return strings.TrimSpace(trimmed[:eq])
}

// replaceCookieValue keeps every byte before and after the first '=' (including
// leading whitespace and any trailing attributes) intact and substitutes only
// the value portion. That preserves operator-debuggable formatting.
func replaceCookieValue(token string) string {
	leadingEnd := 0
	for leadingEnd < len(token) && (token[leadingEnd] == ' ' || token[leadingEnd] == '\t') {
		leadingEnd++
	}
	rest := token[leadingEnd:]
	eq := strings.IndexByte(rest, '=')
	if eq < 0 {
		return token
	}
	valueStart := leadingEnd + eq + 1
	attrStart := strings.IndexByte(token[valueStart:], ';')
	if attrStart < 0 {
		return token[:valueStart] + Redacted
	}
	return token[:valueStart] + Redacted + token[valueStart+attrStart:]
}

func joinCookieParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	parts[0] = strings.TrimLeft(parts[0], " \t")
	return strings.Join(parts, ";")
}

func allEmpty(vals []string) bool {
	for _, v := range vals {
		if v != "" {
			return false
		}
	}
	return true
}

func redactCookieSlice(in []capture.Cookie, rules []cookieRule) []capture.Cookie {
	out := make([]capture.Cookie, len(in))
	copy(out, in)
	for _, rule := range rules {
		filtered := out[:0]
		for _, c := range out {
			matched := slices.Contains(rule.names, c.Name)
			switch rule.mode {
			case cookieModeRedact:
				if matched {
					c.Value = Redacted
				}
				filtered = append(filtered, c)
			case cookieModeStrip:
				if matched {
					continue
				}
				filtered = append(filtered, c)
			case cookieModeAllowlist:
				if matched {
					filtered = append(filtered, c)
				}
			}
		}
		out = filtered
	}
	return out
}

func shallowCopyRecord(rec *capture.CapturedRecord) *capture.CapturedRecord {
	out := *rec
	out.Headers = copyStringSliceMap(rec.Headers)
	out.Query = copyStringSliceMap(rec.Query)
	if rec.Cookies != nil {
		out.Cookies = make([]capture.Cookie, len(rec.Cookies))
		copy(out.Cookies, rec.Cookies)
	}
	return &out
}

func copyStringSliceMap(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, vs := range m {
		vsCopy := make([]string, len(vs))
		copy(vsCopy, vs)
		out[k] = vsCopy
	}
	return out
}
