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
