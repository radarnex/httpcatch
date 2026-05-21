package sinks

import (
	"context"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
	"github.com/radarnex/httpcatch/internal/searchql"
)

// writeAll writes a slice of CapturedRequests to the sink.
func writeAll(t *testing.T, s *MemorySink, reqs ...*capture.CapturedRequest) {
	t.Helper()
	ctx := context.Background()
	for _, r := range reqs {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}
}

// mustParseQuery parses q for tests and fails fast on parse errors. The
// returned value is shared by InspectQuery construction in both filter test
// files in this package.
func mustParseQuery(t *testing.T, q string) searchql.Query {
	t.Helper()
	parsed, err := searchql.Parse(q)
	if err != nil {
		t.Fatalf("searchql.Parse(%q): %v", q, err)
	}
	return parsed
}

func TestMemoryFilter_Since(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := makeRequest("r1", base.Add(-time.Hour), "svc", "GET", "/", "c1", "x")
	r2 := makeRequest("r2", base, "svc", "GET", "/", "c2", "x")
	r3 := makeRequest("r3", base.Add(time.Hour), "svc", "GET", "/", "c3", "x")
	writeAll(t, s, r1, r2, r3)

	since := base
	q := inspect.InspectQuery{Since: &since}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	// r2 and r3 are at or after base; r1 is before.
	if len(rows) != 2 {
		t.Fatalf("since filter: got %d rows want 2", len(rows))
	}
	for _, row := range rows {
		if row.ID != "r2" && row.ID != "r3" {
			t.Errorf("since filter: unexpected id %q", row.ID)
		}
	}
}

func TestMemoryFilter_Until(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := makeRequest("r1", base.Add(-time.Hour), "svc", "GET", "/", "c1", "x")
	r2 := makeRequest("r2", base, "svc", "GET", "/", "c2", "x")
	r3 := makeRequest("r3", base.Add(time.Hour), "svc", "GET", "/", "c3", "x")
	writeAll(t, s, r1, r2, r3)

	// strictly before base means only r1
	until := base
	q := inspect.InspectQuery{Until: &until}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "r1" {
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		t.Errorf("until filter: got %v want [r1]", ids)
	}
}

func TestMemoryFilter_SinceAndUntil(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := makeRequest("r1", base.Add(-time.Hour), "svc", "GET", "/", "c1", "x")
	r2 := makeRequest("r2", base, "svc", "GET", "/", "c2", "x")
	r3 := makeRequest("r3", base.Add(time.Hour), "svc", "GET", "/", "c3", "x")
	writeAll(t, s, r1, r2, r3)

	// half-open [base, base+1h) → only r2
	since := base
	until := base.Add(time.Hour)
	q := inspect.InspectQuery{Since: &since, Until: &until}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "r2" {
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		t.Errorf("since+until: got %v want [r2]", ids)
	}
}

// memoryWildcardSetup writes four CapturedRequests with varied hosts/bodies
// for wildcard/quoted/negation predicate tests. The host fixture mirrors
// sqliteWildcardSetup so the two readers exercise the same shape.
func memoryWildcardSetup(t *testing.T) *MemorySink {
	t.Helper()
	s := NewMemorySink(20)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	build := func(id string, ts time.Time, host, path string, body []byte) *capture.CapturedRequest {
		return &capture.CapturedRequest{
			ID:            id,
			Timestamp:     ts,
			Service:       "svc",
			Method:        "POST",
			Path:          path,
			CorrelationID: "c-" + id,
			SourceIP:      "10.0.0.1",
			Headers:       map[string][]string{capture.HostHeader: {host}},
			Body:          body,
		}
	}

	writeAll(t, s,
		build("h1", base, "billing-api.svc.local", "/api/orders/1", []byte(`{"msg":"login failed"}`)),
		build("h2", base.Add(time.Second), "billing-api-staging", "/api/orders/2", []byte(`{"msg":"ok"}`)),
		build("h3", base.Add(2*time.Second), "orders-api.svc.local", "/api/orders/3", []byte(`{"msg":"login failed"}`)),
		build("h4", base.Add(3*time.Second), "foo bar", "/signup/*", []byte(`{"hello":"world"}`)),
	)
	return s
}

func memoryRowIDs(rows []inspect.RootRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func TestMemoryFilter_HostPrefixWildcard(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, "host:billing-api*")}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := memoryRowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("host:billing-api*: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "h1" && id != "h2" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestMemoryFilter_HostLeadingWildcard(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, "host:*api*")}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := memoryRowIDs(rows)
	if len(ids) != 3 {
		t.Fatalf("host:*api*: got %v want 3", ids)
	}
}

func TestMemoryFilter_BodyWildcardsCollapseToSubstring(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	for _, input := range []string{"body:login", "body:login*", "body:*login", "body:*login*"} {
		q := inspect.InspectQuery{Query: mustParseQuery(t, input)}
		rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
		if err != nil {
			t.Fatalf("ReadRoots(%q): %v", input, err)
		}
		ids := memoryRowIDs(rows)
		if len(ids) != 2 {
			t.Errorf("%q: got %v want 2 (h1, h3)", input, ids)
		}
	}
}

func TestMemoryFilter_QuotedExactMatch(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, `host:"foo bar"`)}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := memoryRowIDs(rows)
	if len(ids) != 1 || ids[0] != "h4" {
		t.Errorf(`host:"foo bar": got %v want [h4]`, ids)
	}
}

func TestMemoryFilter_QuotedPathWithLiteralAsterisk(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, `path:"/signup/*"`)}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := memoryRowIDs(rows)
	if len(ids) != 1 || ids[0] != "h4" {
		t.Errorf(`path:"/signup/*": got %v want [h4]`, ids)
	}
}

func TestMemoryFilter_QuotedBodyPhrase(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, `body:"login failed"`)}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := memoryRowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf(`body:"login failed": got %v want 2`, ids)
	}
}

func TestMemoryFilter_NegatedExact(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, "-host:billing-api*")}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := memoryRowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("-host:billing-api*: got %v want 2 (h3, h4)", ids)
	}
}

func TestMemoryFilter_MultiTokenWildcardCompose(t *testing.T) {
	t.Parallel()
	s := memoryWildcardSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:svc -method:GET host:billing-api*")}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := memoryRowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("multi-term: got %v want 2", ids)
	}
}
