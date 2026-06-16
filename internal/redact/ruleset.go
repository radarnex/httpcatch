package redact

import (
	"fmt"
	"mime"
	"net/textproto"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

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

type regexRule struct {
	name    string
	pattern *regexp.Regexp
}

type Ruleset struct {
	headers         []string
	queryParams     []string
	jsonPaths       []string
	regex           []regexRule
	cookies         []cookieRule
	redactionErrors atomic.Uint64
}

// RedactionErrorsTotal reports the count of best-effort redaction failures
// (e.g. an unparseable JSON body that declared a JSON content-type, or a
// failed sjson write). The counter is process-local and ticks once per
// failure; records are never dropped on a redaction error.
func (r *Ruleset) RedactionErrorsTotal() uint64 {
	return r.redactionErrors.Load()
}

func NewRuleset(cfg config.RedactionConfig) (*Ruleset, error) {
	headers := make([]string, len(cfg.Headers))
	for i, h := range cfg.Headers {
		headers[i] = strings.ToLower(h)
	}
	queryParams := make([]string, len(cfg.QueryParams))
	for i, q := range cfg.QueryParams {
		queryParams[i] = strings.ToLower(q)
	}

	jsonPaths := make([]string, 0, len(cfg.JSONPaths))
	for _, p := range cfg.JSONPaths {
		if err := validateJSONPath(p); err != nil {
			return nil, err
		}
		jsonPaths = append(jsonPaths, p)
	}

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

	regex := make([]regexRule, 0, len(cfg.Regex))
	for _, r := range cfg.Regex {
		if r.Name == "" {
			return nil, fmt.Errorf("redaction: regex: rule name must not be empty")
		}
		if r.Pattern == "" {
			return nil, fmt.Errorf("redaction: regex: rule %q: pattern must not be empty", r.Name)
		}
		compiled, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("redaction: regex: rule %q: %w", r.Name, err)
		}
		regex = append(regex, regexRule{name: r.Name, pattern: compiled})
	}

	return &Ruleset{
		headers:     headers,
		queryParams: queryParams,
		jsonPaths:   jsonPaths,
		regex:       regex,
		cookies:     cookies,
	}, nil
}

// validateJSONPath rejects empty strings and probes the path with sjson on an
// empty JSON object. sjson exposes no compile-only API; probing on `{}` is the
// cheapest way to surface a syntactically broken path at startup.
func validateJSONPath(path string) error {
	if path == "" {
		return fmt.Errorf("redaction: json_paths: path must not be empty")
	}
	if _, err := sjson.SetBytes([]byte(`{}`), path, "_"); err != nil {
		return fmt.Errorf("redaction: json_paths: invalid path %q: %w", path, err)
	}
	return nil
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

// Redact applies all rule buckets to whichever fields the variant carries.
// Header rules apply to every headers map the variant holds. Regex rules apply
// to every non-empty body regardless of Content-Type; JSON-path rules apply to
// every body that parses as valid JSON regardless of declared type. Cookie and
// query rules only apply to CapturedRequest, which is the only variant that
// carries those fields. Rule application order is fixed:
// cookie → header → query → JSON-path → regex.
func (r *Ruleset) Redact(rec capture.Record) capture.Record {
	if r.IsUnredacted() {
		return rec
	}
	switch v := rec.(type) {
	case *capture.CapturedRequest:
		return r.redactCapturedRequest(v)
	case *capture.ResponseEvent:
		return r.redactResponseEvent(v)
	case *capture.OutboundEvent:
		return r.redactOutboundEvent(v)
	default:
		return rec
	}
}

func (r *Ruleset) redactCapturedRequest(rec *capture.CapturedRequest) *capture.CapturedRequest {
	out := shallowCopyCapturedRequest(rec)
	applyCookieRules(out, r.cookies)
	applyHeaderRulesMap(out.Headers, r.headers)
	applyQueryRules(out, r.queryParams)
	applyJSONPathRulesBody(&out.Body, out.ContentType, r.jsonPaths, &r.redactionErrors)
	applyRegexRulesCaptured(out, r.regex)
	return out
}

func (r *Ruleset) redactResponseEvent(rec *capture.ResponseEvent) *capture.ResponseEvent {
	out := shallowCopyResponseEvent(rec)
	applyCookieRulesToHeaders(out.Headers, r.cookies)
	applyHeaderRulesMap(out.Headers, r.headers)
	applyJSONPathRulesBody(&out.Body, out.ContentType, r.jsonPaths, &r.redactionErrors)
	applyRegexRulesBody(&out.Body, r.regex)
	applyRegexRulesHeaderMap(out.Headers, r.regex)
	return out
}

func (r *Ruleset) redactOutboundEvent(rec *capture.OutboundEvent) *capture.OutboundEvent {
	out := shallowCopyOutboundEvent(rec)
	// Request half
	applyCookieRulesToHeaders(out.Request.Headers, r.cookies)
	applyHeaderRulesMap(out.Request.Headers, r.headers)
	applyJSONPathRulesBody(&out.Request.Body, out.Request.ContentType, r.jsonPaths, &r.redactionErrors)
	applyRegexRulesBody(&out.Request.Body, r.regex)
	applyRegexRulesHeaderMap(out.Request.Headers, r.regex)
	// Response half (only when present)
	if out.Response != nil {
		applyCookieRulesToHeaders(out.Response.Headers, r.cookies)
		applyHeaderRulesMap(out.Response.Headers, r.headers)
		applyJSONPathRulesBody(&out.Response.Body, out.Response.ContentType, r.jsonPaths, &r.redactionErrors)
		applyRegexRulesBody(&out.Response.Body, r.regex)
		applyRegexRulesHeaderMap(out.Response.Headers, r.regex)
	}
	return out
}

// redactedBytes is the body-side replacement buffer; pre-allocating once
// avoids the per-match []byte conversion regexp.ReplaceAll would otherwise
// perform. ReplaceAll treats $ in the replacement as a backreference; the
// literal "[REDACTED]" is $-free, but a ReplaceAllLiteral variant would
// require a string, so the body uses ReplaceAll over a constant []byte and
// the headers/query paths use ReplaceAllLiteralString to immunise both
// against future changes to the marker.
var redactedBytes = []byte(Redacted)

func applyRegexRulesCaptured(out *capture.CapturedRequest, rules []regexRule) {
	if len(rules) == 0 {
		return
	}
	bodyEligible := len(out.Body) > 0
	for _, rule := range rules {
		if bodyEligible {
			out.Body = rule.pattern.ReplaceAll(out.Body, redactedBytes)
		}
		applyRegexRulesHeaderMap(out.Headers, []regexRule{rule})
		for _, vals := range out.Query {
			for i, v := range vals {
				vals[i] = rule.pattern.ReplaceAllLiteralString(v, Redacted)
			}
		}
	}
}

func applyRegexRulesBody(body *[]byte, rules []regexRule) {
	if len(rules) == 0 || len(*body) == 0 {
		return
	}
	for _, rule := range rules {
		*body = rule.pattern.ReplaceAll(*body, redactedBytes)
	}
}

func applyRegexRulesHeaderMap(headers map[string][]string, rules []regexRule) {
	if len(rules) == 0 {
		return
	}
	for _, vals := range headers {
		for i, v := range vals {
			for _, rule := range rules {
				vals[i] = rule.pattern.ReplaceAllLiteralString(v, Redacted)
				v = vals[i]
			}
		}
	}
}

// isTextLikeContentType reports whether the declared media type is text-like:
// JSON, XML, urlencoded form bodies, any text/* type, or any +json / +xml
// structured-syntax suffix.
func isTextLikeContentType(ct string) bool {
	media := parseMediaType(ct)
	if media == "" {
		return false
	}
	switch media {
	case "application/json", "application/xml", "application/x-www-form-urlencoded":
		return true
	}
	if strings.HasPrefix(media, "text/") {
		return true
	}
	if strings.HasSuffix(media, "+json") || strings.HasSuffix(media, "+xml") {
		return true
	}
	return false
}

// parseMediaType strips media-type parameters (e.g. "; charset=utf-8") and
// lowercases the base type. mime.ParseMediaType handles the well-formed
// case; the manual fallback covers malformed parameter sections without
// defeating the gate for an otherwise valid base type.
func parseMediaType(ct string) string {
	if ct == "" {
		return ""
	}
	media, _, err := mime.ParseMediaType(ct)
	if err != nil {
		if idx := strings.IndexByte(ct, ';'); idx >= 0 {
			media = strings.TrimSpace(ct[:idx])
		} else {
			media = strings.TrimSpace(ct)
		}
		media = strings.ToLower(media)
	}
	return media
}

// applyJSONPathRulesBody redacts JSON-path values in any body that parses as
// valid JSON, regardless of the declared Content-Type. When the body fails
// validation and the declared type is JSON, the error counter is incremented
// (a declared-JSON body that is not parseable is a misconfiguration worth
// surfacing). A non-JSON-declared body that is not valid JSON is silently
// skipped — there are no JSON paths to apply. Failures during path rewrite
// are also best-effort.
func applyJSONPathRulesBody(body *[]byte, contentType string, rules []string, errs *atomic.Uint64) {
	if len(rules) == 0 || len(*body) == 0 {
		return
	}
	if !gjson.ValidBytes(*body) {
		if isJSONContentType(contentType) {
			errs.Add(1)
		}
		return
	}
	b := *body
	for _, path := range rules {
		if !gjson.GetBytes(b, path).Exists() {
			continue
		}
		next, err := sjson.SetBytes(b, path, Redacted)
		if err != nil {
			errs.Add(1)
			continue
		}
		b = next
	}
	*body = b
}

// isJSONContentType returns true when the media type is application/json or
// any structured-syntax-suffix variant ending in +json (e.g. application/vnd.api+json).
// Media-type parameters such as charset are stripped before matching.
func isJSONContentType(ct string) bool {
	media := parseMediaType(ct)
	if media == "" {
		return false
	}
	if media == "application/json" {
		return true
	}
	return strings.HasSuffix(media, "+json")
}

func applyQueryRules(out *capture.CapturedRequest, rules []string) {
	for key, vals := range out.Query {
		if slices.Contains(rules, strings.ToLower(key)) {
			for i := range vals {
				vals[i] = Redacted
			}
		}
	}
}

func applyHeaderRulesMap(headers map[string][]string, rules []string) {
	for key, vals := range headers {
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

// applyCookieRulesToHeaders applies cookie rules to the Cookie and Set-Cookie
// entries in the given headers map. It mutates the map in place.
func applyCookieRulesToHeaders(headers map[string][]string, rules []cookieRule) {
	if len(rules) == 0 || headers == nil {
		return
	}

	cookieKey := findHeaderKey(headers, cookieHeaderKey)
	setCookieKey := findHeaderKey(headers, setCookieHeaderKey)

	for _, rule := range rules {
		if cookieKey != "" {
			vals := headers[cookieKey]
			for i, v := range vals {
				vals[i] = applyRuleToCookieHeader(v, rule)
			}
			if allEmpty(vals) {
				delete(headers, cookieKey)
				cookieKey = ""
			}
		}
		if setCookieKey != "" {
			vals := headers[setCookieKey]
			kept := vals[:0]
			for _, v := range vals {
				redacted, drop := applyRuleToSetCookieHeader(v, rule)
				if drop {
					continue
				}
				kept = append(kept, redacted)
			}
			if len(kept) == 0 {
				delete(headers, setCookieKey)
				setCookieKey = ""
			} else {
				headers[setCookieKey] = kept
			}
		}
	}
}

func applyCookieRules(out *capture.CapturedRequest, rules []cookieRule) {
	if len(rules) == 0 || out.Headers == nil {
		return
	}
	applyCookieRulesToHeaders(out.Headers, rules)
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

func shallowCopyCapturedRequest(rec *capture.CapturedRequest) *capture.CapturedRequest {
	out := *rec
	out.Headers = copyStringSliceMap(rec.Headers)
	out.Query = copyStringSliceMap(rec.Query)
	if rec.Cookies != nil {
		out.Cookies = make([]capture.Cookie, len(rec.Cookies))
		copy(out.Cookies, rec.Cookies)
	}
	return &out
}

func shallowCopyResponseEvent(rec *capture.ResponseEvent) *capture.ResponseEvent {
	out := *rec
	out.Headers = copyStringSliceMap(rec.Headers)
	if rec.Body != nil {
		body := make([]byte, len(rec.Body))
		copy(body, rec.Body)
		out.Body = body
	}
	return &out
}

func shallowCopyOutboundEvent(rec *capture.OutboundEvent) *capture.OutboundEvent {
	out := *rec
	out.Request.Headers = copyStringSliceMap(rec.Request.Headers)
	if rec.Request.Body != nil {
		body := make([]byte, len(rec.Request.Body))
		copy(body, rec.Request.Body)
		out.Request.Body = body
	}
	if rec.Response != nil {
		resp := *rec.Response
		resp.Headers = copyStringSliceMap(rec.Response.Headers)
		if rec.Response.Body != nil {
			body := make([]byte, len(rec.Response.Body))
			copy(body, rec.Response.Body)
			resp.Body = body
		}
		out.Response = &resp
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
