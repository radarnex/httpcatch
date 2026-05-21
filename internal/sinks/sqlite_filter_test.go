package sinks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
	"github.com/radarnex/httpcatch/internal/searchql"
)

// sqliteFilterTestSetup writes three requests + response events into a sink and
// returns the sink. Layout:
//
//	r1: service=orders,  method=POST, path=/api/orders/1, ip=10.0.0.1, corr=c1, ts=base+0
//	r2: service=payments,method=GET,  path=/api/payments, ip=10.0.0.2, corr=c2, ts=base+1s
//	r3: service=orders,  method=POST, path=/api/orders/2, ip=10.0.0.1, corr=c3, ts=base+2s
//
//	ev1 (response, corr=c1, status=200)
//	ev2 (response, corr=c2, status=500)
//	  r3 has no event (corr=c3 has no event)
func sqliteFilterTestSetup(t *testing.T) (*SQLiteSink, time.Time) {
	t.Helper()
	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	recs := []*capture.CapturedRequest{
		sqliteRequest("r1", base, "orders", "POST", "/api/orders/1", "c1", "10.0.0.1"),
		sqliteRequest("r2", base.Add(time.Second), "payments", "GET", "/api/payments", "c2", "10.0.0.2"),
		sqliteRequest("r3", base.Add(2*time.Second), "orders", "POST", "/api/orders/2", "c3", "10.0.0.1"),
	}
	for _, r := range recs {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}

	events := []*capture.ResponseEvent{
		{
			ID: "ev1", Timestamp: base.Add(100 * time.Millisecond),
			CorrelationID: "c1", Service: "orders", ServiceSource: "explicit",
			Status: 200, Headers: map[string][]string{}, Body: []byte{},
		},
		{
			ID: "ev2", Timestamp: base.Add(time.Second + 100*time.Millisecond),
			CorrelationID: "c2", Service: "payments", ServiceSource: "explicit",
			Status: 500, Headers: map[string][]string{}, Body: []byte{},
		},
	}
	for _, ev := range events {
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write ev %s: %v", ev.ID, err)
		}
	}

	return s, base
}

func rowIDs(rows []inspect.RootRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func TestSQLiteFilter_Service(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:orders")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("service=orders: got %v want 2 rows", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_Method(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "method:GET")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r2" {
		t.Errorf("method=GET: got %v want [r2]", ids)
	}
}

func TestSQLiteFilter_StatusExact(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "status:200")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("status=200: got %v want [r1]", ids)
	}
}

func TestSQLiteFilter_StatusClass5xx(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "status:5xx")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r2" {
		t.Errorf("status=5xx: got %v want [r2]", ids)
	}
}

func TestSQLiteFilter_StatusClass2xx(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "status:2xx")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("status=2xx: got %v want [r1]", ids)
	}
}

func TestSQLiteFilter_PathPrefix(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "path:/api/orders*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("path:/api/orders*: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("path prefix: unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_PathExact(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	// Bare path is exact match — only r1's literal /api/orders/1 matches.
	q := inspect.InspectQuery{Query: mustParseQuery(t, "path:/api/orders/1")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("bare path exact match: got %v want [r1]", ids)
	}
}

func TestSQLiteFilter_CorrelationID(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "correlation_id:c2")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r2" {
		t.Errorf("correlation_id=c2: got %v want [r2]", ids)
	}
}

func TestSQLiteFilter_SourceIP(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "source_ip:10.0.0.1")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("source_ip=10.0.0.1: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("source_ip filter: unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_Since(t *testing.T) {
	t.Parallel()

	s, base := sqliteFilterTestSetup(t)
	ctx := context.Background()

	since := base.Add(time.Second) // >= base+1s means r2 and r3
	q := inspect.InspectQuery{Since: &since}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("since filter: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "r2" && id != "r3" {
			t.Errorf("since filter: unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_Until(t *testing.T) {
	t.Parallel()

	s, base := sqliteFilterTestSetup(t)
	ctx := context.Background()

	// strictly before base+1s means only r1
	until := base.Add(time.Second)
	q := inspect.InspectQuery{Until: &until}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("until filter: got %v want [r1]", ids)
	}
}

func TestSQLiteFilter_SinceAndUntil(t *testing.T) {
	t.Parallel()

	s, base := sqliteFilterTestSetup(t)
	ctx := context.Background()

	since := base.Add(time.Second)
	until := base.Add(2 * time.Second)
	q := inspect.InspectQuery{Since: &since, Until: &until}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "r2" {
		t.Errorf("since+until: got %v want [r2]", ids)
	}
}

func TestSQLiteFilter_Combined_MethodAndPath(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "method:POST path:/api/orders*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("POST+/api/orders*: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("combined filter: unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_Combined_ServiceAndStatus5xx(t *testing.T) {
	t.Parallel()

	s, base := sqliteFilterTestSetup(t)
	ctx := context.Background()

	// Add a 5xx for orders (corr=c1 already has 200). Add another orders request
	// with a 5xx response.
	r4 := sqliteRequest("r4", base.Add(3*time.Second), "orders", "GET", "/api/orders/3", "c4", "x")
	if err := s.Write(context.Background(), r4); err != nil {
		t.Fatalf("Write r4: %v", err)
	}
	ev4 := &capture.ResponseEvent{
		ID: "ev4", Timestamp: base.Add(3*time.Second + 100*time.Millisecond),
		CorrelationID: "c4", Service: "orders", ServiceSource: "explicit",
		Status: 503, Headers: map[string][]string{}, Body: []byte{},
	}
	if err := s.Write(context.Background(), ev4); err != nil {
		t.Fatalf("Write ev4: %v", err)
	}

	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:orders status:5xx")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	// Only r4 is orders+5xx. r1=orders+200, r3=orders+no-event.
	if len(ids) != 1 || ids[0] != "r4" {
		t.Errorf("service=orders+5xx: got %v want [r4]", ids)
	}
}

func TestSQLiteFilter_FiltersComposeWithAND(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	// service=orders AND method=GET → no rows (all orders requests are POST)
	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:orders method:GET")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("orders+GET: expected 0 rows, got %v", rowIDs(rows))
	}
}

func TestSQLiteFilter_Pagination_WithFilter(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write 5 "orders" requests and 2 "payments" requests.
	for i := range 5 {
		r := sqliteRequest(fmt.Sprintf("o%02d", i), base.Add(time.Duration(i)*time.Second),
			"orders", "GET", "/api/orders", fmt.Sprintf("co%d", i), "x")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	for i := range 2 {
		r := sqliteRequest(fmt.Sprintf("p%02d", i), base.Add(time.Duration(i+10)*time.Second),
			"payments", "GET", "/api/payments", fmt.Sprintf("cp%d", i), "x")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Paginate with service=orders and limit=2; expect 5 total rows across pages.
	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:orders")}
	var allIDs []string
	var cursor *inspect.Cursor
	for {
		rows, next, err := s.ReadRoots(ctx, q, 2, cursor)
		if err != nil {
			t.Fatalf("ReadRoots: %v", err)
		}
		for _, row := range rows {
			allIDs = append(allIDs, row.ID)
		}
		if next == nil {
			break
		}
		cursor = next
	}

	if len(allIDs) != 5 {
		t.Fatalf("pagination with filter: got %d want 5 rows: %v", len(allIDs), allIDs)
	}
	seen := make(map[string]struct{})
	for _, id := range allIDs {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate id %q in paginated result", id)
		}
		seen[id] = struct{}{}
		if id[0] != 'o' {
			t.Errorf("unexpected non-orders id %q in filtered result", id)
		}
	}
}

// sqliteWildcardSetup writes four requests with varied hosts and bodies so
// the wildcard / quoted / negated filter tests below have distinguishable
// rows to assert against.
//
//	h1: host=billing-api.svc.local,     body={"msg":"login failed"}
//	h2: host=billing-api-staging,        body={"msg":"ok"}
//	h3: host=orders-api.svc.local,       body={"msg":"login failed"}
//	h4: host=foo bar,                    body={"hello":"world"}     (literal space + literal *)
func sqliteWildcardSetup(t *testing.T) *SQLiteSink {
	t.Helper()
	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	build := func(id string, ts time.Time, host string, path string, body []byte) *capture.CapturedRequest {
		r := sqliteRequest(id, ts, "svc", "POST", path, "c-"+id, "10.0.0.1")
		r.Headers = map[string][]string{capture.HostHeader: {host}}
		r.Body = body
		return r
	}

	recs := []*capture.CapturedRequest{
		build("h1", base, "billing-api.svc.local", "/api/orders/1", []byte(`{"msg":"login failed"}`)),
		build("h2", base.Add(time.Second), "billing-api-staging", "/api/orders/2", []byte(`{"msg":"ok"}`)),
		build("h3", base.Add(2*time.Second), "orders-api.svc.local", "/api/orders/3", []byte(`{"msg":"login failed"}`)),
		build("h4", base.Add(3*time.Second), "foo bar", "/signup/*", []byte(`{"hello":"world"}`)),
	}
	for _, r := range recs {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}
	return s
}

func TestSQLiteFilter_HostPrefixWildcard_UsesIndex(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "host:billing-api*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("host:billing-api*: got %v want 2 rows", ids)
	}
	for _, id := range ids {
		if id != "h1" && id != "h2" {
			t.Errorf("unexpected id %q", id)
		}
	}

	plan := explainPlanForQuery(t, s, q)
	if !strings.Contains(plan, "idx_captured_requests_host") {
		t.Errorf("host:billing-api* expected to use idx_captured_requests_host; plan was:\n%s", plan)
	}
}

func TestSQLiteFilter_HostLeadingWildcard_ScansTable(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "host:*api*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 3 {
		t.Fatalf("host:*api*: got %v want 3 rows", ids)
	}

	plan := explainPlanForQuery(t, s, q)
	if strings.Contains(plan, "USING INDEX idx_captured_requests_host") {
		t.Errorf("host:*api* should NOT use idx_captured_requests_host; plan was:\n%s", plan)
	}
}

func TestSQLiteFilter_BodyWildcardsCollapseToSubstring(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	for _, input := range []string{"body:login", "body:login*", "body:*login", "body:*login*"} {
		q := inspect.InspectQuery{Query: mustParseQuery(t, input)}
		rows, _, err := s.ReadRoots(ctx, q, 50, nil)
		if err != nil {
			t.Fatalf("ReadRoots(%q): %v", input, err)
		}
		ids := rowIDs(rows)
		if len(ids) != 2 {
			t.Errorf("%q: got %v want 2 (h1, h3)", input, ids)
		}
		for _, id := range ids {
			if id != "h1" && id != "h3" {
				t.Errorf("%q: unexpected id %q", input, id)
			}
		}
	}
}

func TestSQLiteFilter_QuotedExactMatch(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, `host:"foo bar"`)}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "h4" {
		t.Errorf(`host:"foo bar": got %v want [h4]`, ids)
	}
}

func TestSQLiteFilter_QuotedPathWithLiteralAsterisk(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, `path:"/signup/*"`)}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "h4" {
		t.Errorf(`path:"/signup/*": got %v want [h4] (exact match on literal "/signup/*")`, ids)
	}
}

func TestSQLiteFilter_QuotedBodyPhrase(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, `body:"login failed"`)}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf(`body:"login failed": got %v want 2 (h1, h3)`, ids)
	}
	for _, id := range ids {
		if id != "h1" && id != "h3" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_NegatedExact(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "-service:payments")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("-service:payments: got %v want 2 (r1, r3)", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_NegatedPrefix(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "-host:billing-api*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("-host:billing-api*: got %v want 2 (h3, h4)", ids)
	}
	for _, id := range ids {
		if id != "h3" && id != "h4" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_NegatedQuotedPhrase(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, `-body:"login failed"`)}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf(`-body:"login failed": got %v want 2 (h2, h4)`, ids)
	}
	for _, id := range ids {
		if id != "h2" && id != "h4" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_MultiTokenWildcardCompose(t *testing.T) {
	t.Parallel()

	s := sqliteWildcardSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:svc -method:GET host:billing-api*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("multi-term: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "h1" && id != "h2" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

// sqliteHeadersSetup writes captured requests + correlated events covering the
// header-search cases: single-value User-Agent, multi-value X-Forwarded-For,
// mixed-case header keys (canonicalised by http.Header on capture), an event
// whose User-Agent value matches but whose captured-request header doesn't,
// and a row with no User-Agent at all to anchor the missing-header path.
//
//	h1: captured User-Agent=client/0.3 (probe), X-Forwarded-For=[10.0.0.1, 10.0.0.2]
//	h2: captured User-Agent=other/1.0; one correlated response event whose
//	    response_headers contain User-Agent=client/echoed
//	h3: captured X-Trace-Id absent, User-Agent absent (only Host present)
func sqliteHeadersSetup(t *testing.T) *SQLiteSink {
	t.Helper()
	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	build := func(id string, ts time.Time, headers map[string][]string) *capture.CapturedRequest {
		r := sqliteRequest(id, ts, "svc", "POST", "/p/"+id, "c-"+id, "10.0.0.1")
		r.Headers = headers
		return r
	}

	recs := []*capture.CapturedRequest{
		build("h1", base, map[string][]string{
			"Host":            {"app.example.com"},
			"User-Agent":      {"client/0.3 (probe)"},
			"X-Forwarded-For": {"10.0.0.1", "10.0.0.2"},
		}),
		build("h2", base.Add(time.Second), map[string][]string{
			"Host":       {"app.example.com"},
			"User-Agent": {"other/1.0"},
		}),
		build("h3", base.Add(2*time.Second), map[string][]string{
			"Host": {"app.example.com"},
		}),
	}
	for _, r := range recs {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}

	// One correlated response event for h2 whose response headers echo a
	// User-Agent — exercises the cross-table fanout from cr → events.
	ev := &capture.ResponseEvent{
		ID: "ev-h2", Timestamp: base.Add(time.Second + 100*time.Millisecond),
		CorrelationID: "c-h2", Service: "svc", ServiceSource: "explicit",
		Status: 200,
		Headers: map[string][]string{
			"X-Echoed-User-Agent": {"client/echoed"},
		},
		Body: []byte{},
	}
	if err := s.Write(ctx, ev); err != nil {
		t.Fatalf("Write ev: %v", err)
	}
	return s
}

func TestSQLiteFilter_HeadersAny_MatchesNameOrValue(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	// Substring "client" matches h1 (captured User-Agent value) and h2 (event
	// response header X-Echoed-User-Agent value).
	q := inspect.InspectQuery{Query: mustParseQuery(t, "headers:client")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("headers:client: got %v want 2 (h1, h2)", ids)
	}
	for _, id := range ids {
		if id != "h1" && id != "h2" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_HeadersAny_MatchesHeaderName(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	// "User-Agent" appears as a key in h1, h2 captured headers and as a value
	// substring in h2's event response header name.
	q := inspect.InspectQuery{Query: mustParseQuery(t, "headers:User-Agent")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("headers:User-Agent: got %v want 2 (h1, h2)", ids)
	}
}

func TestSQLiteFilter_HeaderNamed_MatchesValue(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "header.user-agent:client")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "h1" {
		t.Errorf("header.user-agent:client: got %v want [h1]", ids)
	}
}

func TestSQLiteFilter_HeaderNamed_MixedCaseEquivalent(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	inputs := []string{
		"header.User-Agent:client",
		"header.user-agent:client",
		"header.USER-AGENT:client",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			q := inspect.InspectQuery{Query: mustParseQuery(t, in)}
			rows, _, err := s.ReadRoots(ctx, q, 50, nil)
			if err != nil {
				t.Fatalf("ReadRoots: %v", err)
			}
			ids := rowIDs(rows)
			if len(ids) != 1 || ids[0] != "h1" {
				t.Errorf("%s: got %v want [h1]", in, ids)
			}
		})
	}
}

func TestSQLiteFilter_HeaderNamed_MultiValueHeader(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	for _, needle := range []string{"10.0.0.1", "10.0.0.2"} {
		q := inspect.InspectQuery{Query: mustParseQuery(t, "header.x-forwarded-for:"+needle)}
		rows, _, err := s.ReadRoots(ctx, q, 50, nil)
		if err != nil {
			t.Fatalf("ReadRoots: %v", err)
		}
		ids := rowIDs(rows)
		if len(ids) != 1 || ids[0] != "h1" {
			t.Errorf("header.x-forwarded-for:%s: got %v want [h1]", needle, ids)
		}
	}
}

func TestSQLiteFilter_HeaderNamed_MissingHeader_NoMatchPositive(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "header.x-trace-id:foo")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("header.x-trace-id:foo: got %v want []", rowIDs(rows))
	}
}

func TestSQLiteFilter_HeaderNamed_MissingHeader_NegationMatches(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	// All three rows lack X-Trace-Id, so -header.x-trace-id:foo returns all three.
	q := inspect.InspectQuery{Query: mustParseQuery(t, "-header.x-trace-id:foo")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 3 {
		t.Errorf("-header.x-trace-id:foo: got %v want 3 rows", ids)
	}
}

func TestSQLiteFilter_HeaderNamed_QuotedPhrase(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	// h1's User-Agent literally contains "client/0.3 (probe)" with a space.
	q := inspect.InspectQuery{Query: mustParseQuery(t, `header.user-agent:"client/0.3 (probe)"`)}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "h1" {
		t.Errorf(`header.user-agent:"client/0.3 (probe)": got %v want [h1]`, ids)
	}
}

func TestSQLiteFilter_HeaderComposesWithOtherTerms(t *testing.T) {
	t.Parallel()

	s := sqliteHeadersSetup(t)
	ctx := context.Background()

	// service:svc -header.user-agent:other host:app* should keep h1 (UA=client/0.3)
	// and h3 (no UA → negated header is true), exclude h2 (UA=other/1.0).
	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:svc -header.user-agent:other host:app*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("composed: got %v want 2 (h1, h3)", ids)
	}
	for _, id := range ids {
		if id != "h1" && id != "h3" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_HeadersAny_LeadingWildcardIsNotScan(t *testing.T) {
	t.Parallel()

	q, err := searchql.Parse("headers:*foo*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if q.IsUnindexedScan() {
		t.Errorf("headers:*foo* should not be an unindexed-scan candidate (scanned dim)")
	}
}

// explainPlanForQuery runs EXPLAIN QUERY PLAN against the captured-request arm
// SQL and returns the concatenated `detail` column for inspection. Tests use
// this to assert index usage (or non-use) for wildcard predicates.
func explainPlanForQuery(t *testing.T, s *SQLiteSink, q inspect.InspectQuery) string {
	t.Helper()
	where, args := searchql.CompileSQL(q.Query)
	if where == "" {
		where = "1=1"
	}
	stmt := "EXPLAIN QUERY PLAN SELECT cr.id FROM captured_requests cr WHERE " + where
	rows, err := s.db.QueryContext(context.Background(), stmt, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		details = append(details, detail)
	}
	return strings.Join(details, "\n")
}
