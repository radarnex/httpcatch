package admin

import (
	"net/url"
	"testing"
	"time"
)

// TestParseInspectQuery_ValidForms covers the happy path for every filter.
func TestParseInspectQuery_ValidForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		check func(t *testing.T, q interface{ service() string })
	}{
		{name: "empty", query: ""},
	}
	_ = tests // exercised via invalid forms below; valid forms checked per-field

	// service
	q, errs := parseInspectQuery(url.Values{"service": {"orders"}})
	if len(errs) != 0 {
		t.Fatalf("service: unexpected errors: %v", errs)
	}
	if q.Service != "orders" {
		t.Errorf("service: got %q want orders", q.Service)
	}

	// method — case-insensitive parse, stored uppercase
	for _, raw := range []string{"get", "GET", "Get"} {
		q, errs = parseInspectQuery(url.Values{"method": {raw}})
		if len(errs) != 0 {
			t.Fatalf("method %q: unexpected errors: %v", raw, errs)
		}
		if q.Method != "GET" {
			t.Errorf("method %q: got %q want GET", raw, q.Method)
		}
	}

	// status exact
	q, errs = parseInspectQuery(url.Values{"status": {"200"}})
	if len(errs) != 0 {
		t.Fatalf("status 200: unexpected errors: %v", errs)
	}
	if q.Status == nil || q.Status.Exact != 200 {
		t.Errorf("status 200: got %v", q.Status)
	}

	// status class
	for _, cls := range []string{"1xx", "2xx", "3xx", "4xx", "5xx"} {
		q, errs = parseInspectQuery(url.Values{"status": {cls}})
		if len(errs) != 0 {
			t.Fatalf("status %s: unexpected errors: %v", cls, errs)
		}
		if q.Status == nil || q.Status.Class != cls {
			t.Errorf("status %s: got %v", cls, q.Status)
		}
	}

	// path
	q, errs = parseInspectQuery(url.Values{"path": {"/api/users"}})
	if len(errs) != 0 {
		t.Fatalf("path: unexpected errors: %v", errs)
	}
	if q.Path != "/api/users" {
		t.Errorf("path: got %q want /api/users", q.Path)
	}

	// correlation_id
	q, errs = parseInspectQuery(url.Values{"correlation_id": {"abc-123"}})
	if len(errs) != 0 {
		t.Fatalf("correlation_id: unexpected errors: %v", errs)
	}
	if q.CorrelationID != "abc-123" {
		t.Errorf("correlation_id: got %q want abc-123", q.CorrelationID)
	}

	// source_ip
	q, errs = parseInspectQuery(url.Values{"source_ip": {"10.0.0.1"}})
	if len(errs) != 0 {
		t.Fatalf("source_ip: unexpected errors: %v", errs)
	}
	if q.SourceIP != "10.0.0.1" {
		t.Errorf("source_ip: got %q want 10.0.0.1", q.SourceIP)
	}

	// has_events true
	q, errs = parseInspectQuery(url.Values{"has_events": {"true"}})
	if len(errs) != 0 {
		t.Fatalf("has_events true: unexpected errors: %v", errs)
	}
	if q.HasEvents == nil || !*q.HasEvents {
		t.Errorf("has_events true: got %v", q.HasEvents)
	}

	// has_events false
	q, errs = parseInspectQuery(url.Values{"has_events": {"false"}})
	if len(errs) != 0 {
		t.Fatalf("has_events false: unexpected errors: %v", errs)
	}
	if q.HasEvents == nil || *q.HasEvents {
		t.Errorf("has_events false: got %v", q.HasEvents)
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
}

// TestParseInspectQuery_InvalidForms covers every invalid form, table-driven.
func TestParseInspectQuery_InvalidForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		query      url.Values
		wantField  string
	}{
		{
			name:      "unknown key",
			query:     url.Values{"foo": {"bar"}},
			wantField: "foo",
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
			name:      "method unknown verb",
			query:     url.Values{"method": {"FETCH"}},
			wantField: "method",
		},
		{
			name:      "status not integer or class",
			query:     url.Values{"status": {"ok"}},
			wantField: "status",
		},
		{
			name:      "status out of range",
			query:     url.Values{"status": {"99"}},
			wantField: "status",
		},
		{
			name:      "status bad class digit",
			query:     url.Values{"status": {"6xx"}},
			wantField: "status",
		},
		{
			name:      "has_events not bool",
			query:     url.Values{"has_events": {"yes"}},
			wantField: "has_events",
		},
		{
			name:      "has_events integer",
			query:     url.Values{"has_events": {"1"}},
			wantField: "has_events",
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

// TestParseStatusFilter covers the full range of valid and invalid status strings.
func TestParseStatusFilter_Valid(t *testing.T) {
	t.Parallel()

	exactCases := []struct {
		input string
		want  int
	}{
		{"200", 200},
		{"404", 404},
		{"500", 500},
		{"100", 100},
		{"599", 599},
	}
	for _, tc := range exactCases {
		t.Run("exact_"+tc.input, func(t *testing.T) {
			t.Parallel()
			sf, err := parseStatusFilter(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sf.Exact != tc.want {
				t.Errorf("Exact: got %d want %d", sf.Exact, tc.want)
			}
			if sf.Class != "" {
				t.Errorf("Class should be empty, got %q", sf.Class)
			}
		})
	}

	classCases := []string{"1xx", "2xx", "3xx", "4xx", "5xx"}
	for _, cls := range classCases {
		t.Run("class_"+cls, func(t *testing.T) {
			t.Parallel()
			sf, err := parseStatusFilter(cls)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sf.Class != cls {
				t.Errorf("Class: got %q want %q", sf.Class, cls)
			}
			if sf.Exact != 0 {
				t.Errorf("Exact should be 0, got %d", sf.Exact)
			}
		})
	}
}

func TestParseStatusFilter_Invalid(t *testing.T) {
	t.Parallel()

	invalid := []string{"ok", "200x", "6xx", "0xx", "99", "600", "abc", "", "2X", "2xX"}
	for _, s := range invalid {
		t.Run("invalid_"+s, func(t *testing.T) {
			t.Parallel()
			_, err := parseStatusFilter(s)
			if err == nil {
				t.Errorf("expected error for %q", s)
			}
		})
	}
}

// TestHasNonTemporalFilter verifies the routing predicate.
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
		{"service": {"orders"}},
		{"method": {"GET"}},
		{"status": {"200"}},
		{"path": {"/api"}},
		{"correlation_id": {"abc"}},
		{"source_ip": {"10.0.0.1"}},
		{"has_events": {"true"}},
	}
	for _, vals := range nonTemporal {
		q, _ := parseInspectQuery(vals)
		if !hasNonTemporalFilter(q) {
			t.Errorf("query with %v should have non-temporal filter", vals)
		}
	}
}
