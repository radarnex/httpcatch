package redact_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/redact"
)

func makeRecord(headers map[string][]string) *capture.CapturedRecord {
	return &capture.CapturedRecord{
		Headers: headers,
	}
}

func makeRecordWithQuery(query map[string][]string) *capture.CapturedRecord {
	return &capture.CapturedRecord{
		Query: query,
	}
}

func TestHeaderRules_CaseInsensitive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		headerKey string
	}{
		{"canonical", "Authorization"},
		{"lower", "authorization"},
		{"upper", "AUTHORIZATION"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rs, err := redact.NewRuleset(config.RedactionConfig{Headers: []string{"authorization"}})
			if err != nil {
				t.Fatalf("NewRuleset: %v", err)
			}

			rec := makeRecord(map[string][]string{
				tc.headerKey: {"Bearer secret-token"},
			})
			out := rs.Redact(rec)

			vals := out.Headers[tc.headerKey]
			if len(vals) != 1 || vals[0] != redact.Redacted {
				t.Errorf("header %q: got %v, want [%q]", tc.headerKey, vals, redact.Redacted)
			}
		})
	}
}

func TestHeaderRules_MissingHeader(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{Headers: []string{"x-secret"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecord(map[string][]string{
		"X-Other": {"visible"},
	})
	out := rs.Redact(rec)

	vals := out.Headers["X-Other"]
	if len(vals) != 1 || vals[0] != "visible" {
		t.Errorf("X-Other: got %v, want [visible]", vals)
	}
	if _, exists := out.Headers["x-secret"]; exists {
		t.Error("x-secret should not be present in output")
	}
}

func TestHeaderRules_NonMatchingPassThrough(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{Headers: []string{"authorization"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecord(map[string][]string{
		"X-Custom": {"keep-me"},
	})
	out := rs.Redact(rec)

	vals := out.Headers["X-Custom"]
	if len(vals) != 1 || vals[0] != "keep-me" {
		t.Errorf("X-Custom: got %v, want [keep-me]", vals)
	}
}

func TestHeaderRules_MultipleRulesDeclarationOrder(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{Headers: []string{"authorization", "x-api-key"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecord(map[string][]string{
		"Authorization": {"Bearer secret"},
		"X-Api-Key":     {"key-value"},
		"X-Safe":        {"visible"},
	})
	out := rs.Redact(rec)

	if vals := out.Headers["Authorization"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("Authorization: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := out.Headers["X-Api-Key"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("X-Api-Key: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := out.Headers["X-Safe"]; len(vals) != 1 || vals[0] != "visible" {
		t.Errorf("X-Safe: got %v, want [visible]", vals)
	}
}

func TestHeaderRules_MultipleValuesRedacted(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{Headers: []string{"x-multi"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecord(map[string][]string{
		"X-Multi": {"value-one", "value-two", "value-three"},
	})
	out := rs.Redact(rec)

	vals := out.Headers["X-Multi"]
	if len(vals) != 3 {
		t.Fatalf("X-Multi: expected 3 values, got %d", len(vals))
	}
	for i, v := range vals {
		if v != redact.Redacted {
			t.Errorf("X-Multi[%d]: got %q, want %q", i, v, redact.Redacted)
		}
	}
}

func TestQueryRules_MatchingParamRedacted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rules    []string
		query    map[string][]string
		wantKey  string
		wantVals []string
	}{
		{
			name:     "single matching param",
			rules:    []string{"token"},
			query:    map[string][]string{"token": {"abc123"}},
			wantKey:  "token",
			wantVals: []string{redact.Redacted},
		},
		{
			name:     "multi-value param all redacted",
			rules:    []string{"password"},
			query:    map[string][]string{"password": {"first", "second", "third"}},
			wantKey:  "password",
			wantVals: []string{redact.Redacted, redact.Redacted, redact.Redacted},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rs, err := redact.NewRuleset(config.RedactionConfig{QueryParams: tc.rules})
			if err != nil {
				t.Fatalf("NewRuleset: %v", err)
			}

			out := rs.Redact(makeRecordWithQuery(tc.query))

			got := out.Query[tc.wantKey]
			if len(got) != len(tc.wantVals) {
				t.Fatalf("%s: got %d values, want %d", tc.wantKey, len(got), len(tc.wantVals))
			}
			for i, v := range got {
				if v != tc.wantVals[i] {
					t.Errorf("%s[%d]: got %q, want %q", tc.wantKey, i, v, tc.wantVals[i])
				}
			}
		})
	}
}

func TestQueryRules_NonMatchingPassThrough(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{QueryParams: []string{"token"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecordWithQuery(map[string][]string{
		"page": {"2"},
	})
	out := rs.Redact(rec)

	vals := out.Query["page"]
	if len(vals) != 1 || vals[0] != "2" {
		t.Errorf("page: got %v, want [2]", vals)
	}
	if _, exists := out.Query["token"]; exists {
		t.Error("token should not be present in output")
	}
}

func TestQueryRules_CaseSensitive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		queryKey string
		want     string
	}{
		{"exact match redacted", "token", redact.Redacted},
		{"capitalized untouched", "Token", "secret"},
		{"upper untouched", "TOKEN", "secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rs, err := redact.NewRuleset(config.RedactionConfig{QueryParams: []string{"token"}})
			if err != nil {
				t.Fatalf("NewRuleset: %v", err)
			}

			rec := makeRecordWithQuery(map[string][]string{
				tc.queryKey: {"secret"},
			})
			out := rs.Redact(rec)

			vals := out.Query[tc.queryKey]
			if len(vals) != 1 || vals[0] != tc.want {
				t.Errorf("query %q: got %v, want [%q]", tc.queryKey, vals, tc.want)
			}
		})
	}
}

func TestQueryRules_NoQueryPassThrough(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{QueryParams: []string{"token"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecordWithQuery(nil)
	out := rs.Redact(rec)

	if len(out.Query) != 0 {
		t.Errorf("expected empty query map, got %v", out.Query)
	}
}

func TestQueryRules_RuleOnAbsentParamIsNoOp(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{QueryParams: []string{"missing"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecordWithQuery(map[string][]string{
		"keep": {"yes"},
	})
	out := rs.Redact(rec)

	if vals := out.Query["keep"]; len(vals) != 1 || vals[0] != "yes" {
		t.Errorf("keep: got %v, want [yes]", vals)
	}
	if _, exists := out.Query["missing"]; exists {
		t.Error("missing should not be present in output")
	}
}

func TestNewRuleset_WithQueryParams(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{QueryParams: []string{"token"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}
	if rs.IsUnredacted() {
		t.Error("config with query_params should yield IsUnredacted() == false")
	}
}

func TestNewRuleset_EmptyConfig(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}
	if !rs.IsUnredacted() {
		t.Error("empty config should yield IsUnredacted() == true")
	}
}

func TestNewRuleset_WithHeaders(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{Headers: []string{"authorization"}})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}
	if rs.IsUnredacted() {
		t.Error("config with headers should yield IsUnredacted() == false")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noEnv(string) string { return "" }

func TestConfigLoad_UnknownRedactionKey(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
redaction:
  headers:
    - authorization
  unknown_field: bad
`)
	_, err := config.Load(path, noEnv)
	if err == nil {
		t.Fatal("expected error for unknown redaction key, got nil")
	}
	if !strings.Contains(err.Error(), "redaction") {
		t.Errorf("error %q does not contain 'redaction'", err)
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Errorf("error %q does not contain 'unknown_field'", err)
	}
}

func TestConfigLoad_EmptyRedactionBlock(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
redaction:
`)
	cfg, err := config.Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Redaction.Headers) != 0 {
		t.Errorf("expected empty headers, got %v", cfg.Redaction.Headers)
	}
}

func TestCookieRules_Modes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		cookies   []config.CookieRuleConfig
		headerKey string
		input     []string
		wantKey   string
		wantVals  []string
		wantGone  bool
	}{
		{
			name:      "redact replaces value keeps name preserves others",
			cookies:   []config.CookieRuleConfig{{Mode: "redact", Names: []string{"session_id"}}},
			headerKey: "Cookie",
			input:     []string{"session_id=secret; user_pref=dark; tracking=abc"},
			wantKey:   "Cookie",
			wantVals:  []string{"session_id=" + redact.Redacted + "; user_pref=dark; tracking=abc"},
		},
		{
			name:      "default mode is redact",
			cookies:   []config.CookieRuleConfig{{Names: []string{"session_id"}}},
			headerKey: "Cookie",
			input:     []string{"session_id=secret; user_pref=dark"},
			wantKey:   "Cookie",
			wantVals:  []string{"session_id=" + redact.Redacted + "; user_pref=dark"},
		},
		{
			name:      "strip removes named cookies keeps others",
			cookies:   []config.CookieRuleConfig{{Mode: "strip", Names: []string{"tracking"}}},
			headerKey: "Cookie",
			input:     []string{"session_id=keep; tracking=drop; user_pref=dark"},
			wantKey:   "Cookie",
			wantVals:  []string{"session_id=keep; user_pref=dark"},
		},
		{
			name:      "strip emptying header removes header",
			cookies:   []config.CookieRuleConfig{{Mode: "strip", Names: []string{"only"}}},
			headerKey: "Cookie",
			input:     []string{"only=gone"},
			wantGone:  true,
		},
		{
			name:      "allowlist keeps only named cookies",
			cookies:   []config.CookieRuleConfig{{Mode: "allowlist", Names: []string{"session_id"}}},
			headerKey: "Cookie",
			input:     []string{"session_id=keep; tracking=drop; user_pref=drop"},
			wantKey:   "Cookie",
			wantVals:  []string{"session_id=keep"},
		},
		{
			name:      "rule on unknown cookie name is a no-op",
			cookies:   []config.CookieRuleConfig{{Mode: "redact", Names: []string{"absent"}}},
			headerKey: "Cookie",
			input:     []string{"session_id=keep; user_pref=dark"},
			wantKey:   "Cookie",
			wantVals:  []string{"session_id=keep; user_pref=dark"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rs, err := redact.NewRuleset(config.RedactionConfig{Cookies: tc.cookies})
			if err != nil {
				t.Fatalf("NewRuleset: %v", err)
			}

			rec := makeRecord(map[string][]string{tc.headerKey: tc.input})
			out := rs.Redact(rec)

			if tc.wantGone {
				if _, ok := out.Headers[tc.headerKey]; ok {
					t.Errorf("header %q should have been removed; got %v", tc.headerKey, out.Headers[tc.headerKey])
				}
				return
			}
			got := out.Headers[tc.wantKey]
			if len(got) != len(tc.wantVals) {
				t.Fatalf("%s: got %d values, want %d (%v)", tc.wantKey, len(got), len(tc.wantVals), got)
			}
			for i, v := range got {
				if v != tc.wantVals[i] {
					t.Errorf("%s[%d]: got %q, want %q", tc.wantKey, i, v, tc.wantVals[i])
				}
			}
		})
	}
}

func TestCookieRules_SetCookie(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cookies  []config.CookieRuleConfig
		input    []string
		wantVals []string
		wantGone bool
	}{
		{
			name:     "redact preserves attributes",
			cookies:  []config.CookieRuleConfig{{Mode: "redact", Names: []string{"sid"}}},
			input:    []string{"sid=secret; Path=/; HttpOnly", "pref=dark; Path=/"},
			wantVals: []string{"sid=" + redact.Redacted + "; Path=/; HttpOnly", "pref=dark; Path=/"},
		},
		{
			name:     "strip drops named entries",
			cookies:  []config.CookieRuleConfig{{Mode: "strip", Names: []string{"sid"}}},
			input:    []string{"sid=secret; Path=/", "pref=dark; Path=/"},
			wantVals: []string{"pref=dark; Path=/"},
		},
		{
			name:     "allowlist keeps only named entries",
			cookies:  []config.CookieRuleConfig{{Mode: "allowlist", Names: []string{"sid"}}},
			input:    []string{"sid=keep; Path=/", "tracking=drop", "pref=drop"},
			wantVals: []string{"sid=keep; Path=/"},
		},
		{
			name:     "strip emptying Set-Cookie removes header",
			cookies:  []config.CookieRuleConfig{{Mode: "strip", Names: []string{"only"}}},
			input:    []string{"only=gone; Path=/"},
			wantGone: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rs, err := redact.NewRuleset(config.RedactionConfig{Cookies: tc.cookies})
			if err != nil {
				t.Fatalf("NewRuleset: %v", err)
			}

			rec := makeRecord(map[string][]string{"Set-Cookie": tc.input})
			out := rs.Redact(rec)

			if tc.wantGone {
				if _, ok := out.Headers["Set-Cookie"]; ok {
					t.Errorf("Set-Cookie should have been removed; got %v", out.Headers["Set-Cookie"])
				}
				return
			}
			got := out.Headers["Set-Cookie"]
			if len(got) != len(tc.wantVals) {
				t.Fatalf("Set-Cookie: got %d values, want %d (%v)", len(got), len(tc.wantVals), got)
			}
			for i, v := range got {
				if v != tc.wantVals[i] {
					t.Errorf("Set-Cookie[%d]: got %q, want %q", i, v, tc.wantVals[i])
				}
			}
		})
	}
}

func TestCookieRules_BothHeadersProcessed(t *testing.T) {
	t.Parallel()

	rs, err := redact.NewRuleset(config.RedactionConfig{
		Cookies: []config.CookieRuleConfig{{Mode: "redact", Names: []string{"sid"}}},
	})
	if err != nil {
		t.Fatalf("NewRuleset: %v", err)
	}

	rec := makeRecord(map[string][]string{
		"Cookie":     {"sid=req-secret; other=keep"},
		"Set-Cookie": {"sid=resp-secret; Path=/", "tracking=keep"},
	})
	out := rs.Redact(rec)

	wantReq := "sid=" + redact.Redacted + "; other=keep"
	if vals := out.Headers["Cookie"]; len(vals) != 1 || vals[0] != wantReq {
		t.Errorf("Cookie: got %v, want [%q]", vals, wantReq)
	}
	wantResp := []string{"sid=" + redact.Redacted + "; Path=/", "tracking=keep"}
	got := out.Headers["Set-Cookie"]
	if len(got) != 2 || got[0] != wantResp[0] || got[1] != wantResp[1] {
		t.Errorf("Set-Cookie: got %v, want %v", got, wantResp)
	}
}

func TestCookieRules_UnknownModeIsStartupError(t *testing.T) {
	t.Parallel()

	_, err := redact.NewRuleset(config.RedactionConfig{
		Cookies: []config.CookieRuleConfig{{Mode: "wipe", Names: []string{"sid"}}},
	})
	if err == nil {
		t.Fatal("expected error for unknown cookie mode, got nil")
	}
	if !strings.Contains(err.Error(), "cookies") {
		t.Errorf("error %q does not mention cookies", err)
	}
	if !strings.Contains(err.Error(), "wipe") {
		t.Errorf("error %q does not mention bad mode %q", err, "wipe")
	}
}

func TestConfigLoad_AbsentRedactionBlock(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
capture_port: 8080
`)
	cfg, err := config.Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Redaction.Headers) != 0 {
		t.Errorf("expected empty headers, got %v", cfg.Redaction.Headers)
	}
}
