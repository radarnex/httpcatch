package redact

import (
	"slices"
	"strings"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
)

const Redacted = "[REDACTED]"

type cookieRule struct{}
type regexRule struct{}

type Ruleset struct {
	headers     []string     // lowercased
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
	return &Ruleset{headers: headers}, nil
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

func applyCookieRules(_ *capture.CapturedRecord, _ []cookieRule) {}
func applyQueryRules(_ *capture.CapturedRecord, _ []string)      {}
func applyJSONPathRules(_ *capture.CapturedRecord, _ []string)   {}
func applyRegexRules(_ *capture.CapturedRecord, _ []regexRule)   {}

func applyHeaderRules(out *capture.CapturedRecord, rules []string) {
	for key, vals := range out.Headers {
		if slices.Contains(rules, strings.ToLower(key)) {
			for i := range vals {
				vals[i] = Redacted
			}
		}
	}
}

func shallowCopyRecord(rec *capture.CapturedRecord) *capture.CapturedRecord {
	out := *rec
	out.Headers = copyStringSliceMap(rec.Headers)
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
