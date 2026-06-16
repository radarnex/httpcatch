package admin

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestParseInspectQuery_ValidForms covers the happy path for each parameter.
func TestParseInspectQuery_ValidForms(t *testing.T) {
	t.Parallel()

	// q with a single field term
	q, errs := parseInspectQuery(url.Values{"q": {"service:orders"}})
	if len(errs) != 0 {
		t.Fatalf("q service: unexpected errors: %v", errs)
	}
	if len(q.Query.Terms) != 1 {
		t.Fatalf("q service: got %d terms want 1", len(q.Query.Terms))
	}
	if got := q.Query.Terms[0]; got.Value != "orders" {
		t.Errorf("q service term: got %+v", got)
	}

	// q with multiple AND'd terms
	q, errs = parseInspectQuery(url.Values{"q": {"service:orders method:POST"}})
	if len(errs) != 0 {
		t.Fatalf("multi-term q: unexpected errors: %v", errs)
	}
	if len(q.Query.Terms) != 2 {
		t.Fatalf("multi-term q: got %d terms want 2", len(q.Query.Terms))
	}

	// since
	q, errs = parseInspectQuery(url.Values{"since": {"2026-05-18T12:00:00Z"}})
	if len(errs) != 0 {
		t.Fatalf("since: unexpected errors: %v", errs)
	}
	if q.Since == nil {
		t.Fatal("since: expected non-nil")
	}
	want := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if !q.Since.Equal(want) {
		t.Errorf("since: got %v want %v", *q.Since, want)
	}

	// until
	q, errs = parseInspectQuery(url.Values{"until": {"2026-05-18T13:00:00Z"}})
	if len(errs) != 0 {
		t.Fatalf("until: unexpected errors: %v", errs)
	}
	if q.Until == nil {
		t.Fatal("until: expected non-nil")
	}

	// limit
	q, errs = parseInspectQuery(url.Values{"limit": {"25"}})
	if len(errs) != 0 {
		t.Fatalf("limit: unexpected errors: %v", errs)
	}
	if q.Limit != 25 {
		t.Errorf("limit: got %d want 25", q.Limit)
	}

	// empty q is a valid no-op.
	q, errs = parseInspectQuery(url.Values{"q": {""}})
	if len(errs) != 0 {
		t.Fatalf("empty q: unexpected errors: %v", errs)
	}
	if len(q.Query.Terms) != 0 {
		t.Errorf("empty q: got %d terms want 0", len(q.Query.Terms))
	}
}

// TestParseInspectQuery_InvalidForms covers every invalid form, table-driven.
func TestParseInspectQuery_InvalidForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     url.Values
		wantField string
	}{
		{
			name:      "unknown key",
			query:     url.Values{"foo": {"bar"}},
			wantField: "foo",
		},
		{
			name:      "old per-field key service is rejected",
			query:     url.Values{"service": {"orders"}},
			wantField: "service",
		},
		{
			name:      "old per-field key method is rejected",
			query:     url.Values{"method": {"GET"}},
			wantField: "method",
		},
		{
			name:      "old per-field key status is rejected",
			query:     url.Values{"status": {"200"}},
			wantField: "status",
		},
		{
			name:      "old per-field key path is rejected",
			query:     url.Values{"path": {"/api"}},
			wantField: "path",
		},
		{
			name:      "old per-field key body is rejected",
			query:     url.Values{"body": {"x"}},
			wantField: "body",
		},
		{
			name:      "old per-field key correlation_id is rejected",
			query:     url.Values{"correlation_id": {"x"}},
			wantField: "correlation_id",
		},
		{
			name:      "old per-field key source_ip is rejected",
			query:     url.Values{"source_ip": {"x"}},
			wantField: "source_ip",
		},
		{
			name:      "old per-field key has_events is rejected",
			query:     url.Values{"has_events": {"true"}},
			wantField: "has_events",
		},
		{
			name:      "limit not integer",
			query:     url.Values{"limit": {"notanumber"}},
			wantField: "limit",
		},
		{
			name:      "limit zero",
			query:     url.Values{"limit": {"0"}},
			wantField: "limit",
		},
		{
			name:      "limit negative",
			query:     url.Values{"limit": {"-1"}},
			wantField: "limit",
		},
		{
			name:      "limit over max",
			query:     url.Values{"limit": {"9999"}},
			wantField: "limit",
		},
		{
			name:      "cursor bad base64",
			query:     url.Values{"cursor": {"!!!notbase64!!!"}},
			wantField: "cursor",
		},
		{
			name:      "since bad format",
			query:     url.Values{"since": {"2026-05-18"}},
			wantField: "since",
		},
		{
			name:      "until bad format",
			query:     url.Values{"until": {"not-a-date"}},
			wantField: "until",
		},
		{
			name:      "q unknown key inside",
			query:     url.Values{"q": {"foo:bar"}},
			wantField: "q",
		},
		{
			name:      "q bad method",
			query:     url.Values{"q": {"method:FETCH"}},
			wantField: "q",
		},
		{
			name:      "q bad status",
			query:     url.Values{"q": {"status:notastatus"}},
			wantField: "q",
		},
		{
			name:      "q bad status class",
			query:     url.Values{"q": {"status:6xx"}},
			wantField: "q",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, errs := parseInspectQuery(tc.query)
			if len(errs) == 0 {
				t.Fatalf("expected validation error for %v, got none", tc.query)
			}
			found := false
			for _, e := range errs {
				if e.field == tc.wantField {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error on field %q, got errors: %v", tc.wantField, errs)
			}
		})
	}
}

// TestParseInspectQuery_QErrorNamesOffendingToken verifies the parse error
// surfaces enough detail to point operators at the bad token.
func TestParseInspectQuery_QErrorNamesOffendingToken(t *testing.T) {
	t.Parallel()
	_, errs := parseInspectQuery(url.Values{"q": {"service:orders bogus:foo"}})
	if len(errs) == 0 {
		t.Fatal("expected error for bogus token")
	}
	if errs[0].field != "q" {
		t.Errorf("field: got %q want q", errs[0].field)
	}
	if !strings.Contains(errs[0].message, "bogus:foo") {
		t.Errorf("message should name offending token; got %q", errs[0].message)
	}
}

// TestHasNonTemporalFilter verifies the memory-vs-sqlite routing predicate.
func TestHasNonTemporalFilter(t *testing.T) {
	t.Parallel()

	q1, _ := parseInspectQuery(url.Values{})
	if hasNonTemporalFilter(q1) {
		t.Error("empty query should not have non-temporal filters")
	}

	q2, _ := parseInspectQuery(url.Values{"since": {"2026-05-18T12:00:00Z"}})
	if hasNonTemporalFilter(q2) {
		t.Error("since-only query should not have non-temporal filters")
	}

	q3, _ := parseInspectQuery(url.Values{"until": {"2026-05-18T13:00:00Z"}})
	if hasNonTemporalFilter(q3) {
		t.Error("until-only query should not have non-temporal filters")
	}

	q4, _ := parseInspectQuery(url.Values{"since": {"2026-05-18T12:00:00Z"}, "until": {"2026-05-18T13:00:00Z"}})
	if hasNonTemporalFilter(q4) {
		t.Error("since+until query should not have non-temporal filters")
	}

	nonTemporal := []url.Values{
		{"q": {"service:orders"}},
		{"q": {"method:GET"}},
		{"q": {"status:200"}},
		{"q": {"path:/api"}},
		{"q": {"correlation_id:abc"}},
		{"q": {"source_ip:10.0.0.1"}},
		{"q": {"body:foo"}},
		{"q": {"host:example.com"}},
	}
	for _, vals := range nonTemporal {
		q, _ := parseInspectQuery(vals)
		if !hasNonTemporalFilter(q) {
			t.Errorf("query with %v should have non-temporal filter", vals)
		}
	}
}
