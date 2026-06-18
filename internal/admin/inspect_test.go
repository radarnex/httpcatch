package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// newInspectServer builds a test httptest.Server with the given readers wired in.
func newInspectServer(t *testing.T, readers admin.ReadSources) *httptest.Server {
	t.Helper()
	return newAdminTestServer(t, testAdminToken, readers)
}

// getRequests sends an authenticated GET /requests request and returns the
// response.
func getRequests(t *testing.T, ts *httptest.Server, query string) *http.Response {
	t.Helper()
	url := ts.URL + "/requests"
	if query != "" {
		url += "?" + query
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET /requests: %v", err)
	}
	return resp
}

type requestsBody struct {
	Records    []map[string]any `json:"records"`
	NextCursor *string          `json:"next_cursor"`
}

func decodeRequestsBody(t *testing.T, resp *http.Response) requestsBody {
	t.Helper()
	defer resp.Body.Close()
	var b requestsBody
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatalf("decode /requests body: %v", err)
	}
	return b
}

func TestRequests_NoAuth_Returns401(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/requests", nil)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET /requests: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", resp.StatusCode)
	}
}

func TestRequests_NoReaders_StdoutOnly_EmptyList(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if h := resp.Header.Get("X-Httpcatch-Read-Source"); h != "none" {
		t.Errorf("X-Httpcatch-Read-Source: got %q want none", h)
	}
	body := decodeRequestsBody(t, resp)
	if len(body.Records) != 0 {
		t.Errorf("records: got %d want 0", len(body.Records))
	}
	if body.NextCursor != nil {
		t.Errorf("next_cursor: expected null")
	}
}

func TestRequests_ContentTypeJSON(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: got %q want application/json; charset=utf-8", ct)
	}
}

func TestRequests_MemoryOnly_ReturnsList(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(100)
	ctx := context.Background()
	ts2 := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		r := &capture.CapturedRequest{
			ID:            fmt.Sprintf("r%d", i),
			Timestamp:     ts2.Add(time.Duration(i) * time.Second),
			Service:       "svc",
			Method:        "GET",
			Path:          "/api",
			CorrelationID: fmt.Sprintf("c%d", i),
			SourceIP:      "1.2.3.4",
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequests(t, ts, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if h := resp.Header.Get("X-Httpcatch-Read-Source"); h != "memory" {
		t.Errorf("X-Httpcatch-Read-Source: got %q want memory", h)
	}
	body := decodeRequestsBody(t, resp)
	if len(body.Records) != 3 {
		t.Errorf("records: got %d want 3", len(body.Records))
	}
	// Verify newest first.
	for i, rec := range body.Records {
		wantID := fmt.Sprintf("r%d", 2-i)
		if rec["id"] != wantID {
			t.Errorf("records[%d].id: got %v want %q", i, rec["id"], wantID)
		}
		if rec["kind"] != "request" {
			t.Errorf("records[%d].kind: got %v want request", i, rec["kind"])
		}
	}
}

func TestRequests_InvalidLimit_NotInteger_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "limit=notanumber")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "limit" {
		t.Errorf("field: got %v want limit", body["field"])
	}
}

func TestRequests_InvalidLimit_OutOfRange_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "limit=9999")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "limit" {
		t.Errorf("field: got %v want limit", body["field"])
	}
}

func TestRequests_InvalidCursor_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "cursor=!!!notbase64!!!")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "cursor" {
		t.Errorf("field: got %v want cursor", body["field"])
	}
}

func TestRequests_Pagination_MemoryOnly(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(100)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	const total = 7
	for i := range total {
		r := &capture.CapturedRequest{
			ID:            fmt.Sprintf("r%02d", i),
			Timestamp:     base.Add(time.Duration(i) * time.Second),
			Service:       "svc",
			Method:        "GET",
			Path:          "/",
			CorrelationID: fmt.Sprintf("c%d", i),
			SourceIP:      "x",
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})

	var allIDs []string
	var cursorParam string
	for {
		q := "limit=3"
		if cursorParam != "" {
			q += "&cursor=" + cursorParam
		}
		resp := getRequests(t, ts, q)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d", resp.StatusCode)
		}
		body := decodeRequestsBody(t, resp)
		for _, rec := range body.Records {
			allIDs = append(allIDs, rec["id"].(string))
		}
		if body.NextCursor == nil {
			break
		}
		cursorParam = *body.NextCursor
	}

	if len(allIDs) != total {
		t.Fatalf("pagination union: got %d want %d rows", len(allIDs), total)
	}
	seen := make(map[string]struct{})
	for _, id := range allIDs {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestRequests_ReadSourceHeader_MemoryAndSQLite(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(2) // tiny buffer so SQLite fallthrough is triggered
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write 3 records — only 2 fit in memory; SQLite gets all 3.
	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	for i := range 3 {
		r := &capture.CapturedRequest{
			ID:                fmt.Sprintf("r%d", i),
			Timestamp:         base.Add(time.Duration(i) * time.Second),
			Service:           "svc",
			Method:            "GET",
			Path:              "/",
			CorrelationID:     fmt.Sprintf("c%d", i),
			SourceIP:          "x",
			Headers:           map[string][]string{"Host": {"example.com"}},
			Query:             map[string][]string{},
			Cookies:           []capture.Cookie{},
			Body:              []byte{},
			ServiceSource:     capture.ServiceSourceHeader,
			CorrelationSource: capture.CorrelationSourceTraceparent,
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("mem.Write: %v", err)
		}
		if err := sqliteSink.Write(ctx, r); err != nil {
			t.Fatalf("sqlite.Write: %v", err)
		}
	}

	// Memory has cap 2 so holds only the 2 newest. SQLite holds all 3.
	// With limit=3, memory yields 2, falls through to SQLite for remainder.
	ts := newInspectServer(t, admin.ReadSources{Memory: mem, SQLite: sqliteSink})
	resp := getRequests(t, ts, "limit=3")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	src := resp.Header.Get("X-Httpcatch-Read-Source")
	body := decodeRequestsBody(t, resp)

	// With dedup, we expect 3 unique rows total.
	if len(body.Records) != 3 {
		t.Errorf("records: got %d want 3", len(body.Records))
	}
	// Source must indicate both were used.
	if src != "memory+sqlite" {
		t.Errorf("X-Httpcatch-Read-Source: got %q want memory+sqlite", src)
	}
}

func TestRequests_DeduplicatesAcrossMemoryAndSQLite(t *testing.T) {
	t.Parallel()

	// Populate both memory and SQLite with the same record.
	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	ts2 := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	r := &capture.CapturedRequest{
		ID:                "shared-id",
		Timestamp:         ts2,
		Service:           "svc",
		Method:            "GET",
		Path:              "/",
		CorrelationID:     "corr",
		SourceIP:          "x",
		Headers:           map[string][]string{"Host": {"example.com"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}
	if err := sqliteSink.Write(ctx, r); err != nil {
		t.Fatalf("sqlite.Write: %v", err)
	}

	// With limit=50, memory yields 1 row (cap not hit), but it's < limit,
	// so we fall through to SQLite. Dedup must produce exactly 1 row.
	ts := newInspectServer(t, admin.ReadSources{Memory: mem, SQLite: sqliteSink})
	resp := getRequests(t, ts, "limit=50")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	body := decodeRequestsBody(t, resp)
	if len(body.Records) != 1 {
		t.Errorf("records: got %d want 1 (dedup across sinks)", len(body.Records))
	}
}

func TestRequests_DefaultLimit(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(200)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	// Write 55 records (more than default limit of 50).
	for i := range 55 {
		r := &capture.CapturedRequest{
			ID:            fmt.Sprintf("r%03d", i),
			Timestamp:     base.Add(time.Duration(i) * time.Second),
			Service:       "svc",
			Method:        "GET",
			Path:          "/",
			CorrelationID: fmt.Sprintf("c%d", i),
			SourceIP:      "x",
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequests(t, ts, "") // no limit param → default 50
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	body := decodeRequestsBody(t, resp)
	if len(body.Records) != 50 {
		t.Errorf("default limit: got %d want 50", len(body.Records))
	}
	if body.NextCursor == nil {
		t.Error("expected next_cursor when more rows exist")
	}
}

func TestRequests_RowFieldsPresent(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	ts2 := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	r := &capture.CapturedRequest{
		ID:            "test-id",
		Timestamp:     ts2,
		Service:       "orders",
		Method:        "POST",
		Path:          "/api/orders",
		CorrelationID: "corr-xyz",
		SourceIP:      "10.1.2.3",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequests(t, ts, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	body := decodeRequestsBody(t, resp)
	if len(body.Records) != 1 {
		t.Fatalf("records: got %d want 1", len(body.Records))
	}
	rec := body.Records[0]

	requiredFields := []string{"id", "kind", "timestamp", "service", "method", "path", "correlation_id", "source_ip", "event_count", "has_events", "status"}
	for _, f := range requiredFields {
		if _, ok := rec[f]; !ok {
			t.Errorf("missing field %q in row", f)
		}
	}
	if rec["kind"] != "request" {
		t.Errorf("kind: got %v want request", rec["kind"])
	}
	if rec["service"] != "orders" {
		t.Errorf("service: got %v want orders", rec["service"])
	}
	if rec["method"] != "POST" {
		t.Errorf("method: got %v want POST", rec["method"])
	}
	if rec["event_count"] != float64(0) {
		t.Errorf("event_count: got %v want 0", rec["event_count"])
	}
	if rec["has_events"] != false {
		t.Errorf("has_events: got %v want false", rec["has_events"])
	}
	if rec["status"] != nil {
		t.Errorf("status: got %v want null", rec["status"])
	}
}

// TestRequests_ScanHeader walks the case table for the unindexed-scan response
// header. The handler must set X-Httpcatch-Scan: leading-wildcard-indexed iff
// IsUnindexedScan() returns true for the parsed query; absent otherwise.
// Unindexed-scan queries must carry a narrowing constraint (since/until or an
// exact service: term) to pass the guard; this test supplies since/until for
// those cases.
func TestRequests_ScanHeader(t *testing.T) {
	t.Parallel()

	narrowing := "since=2026-01-01T00:00:00Z&until=2026-12-31T23:59:59Z"

	cases := []struct {
		name   string
		q      string
		extra  string // additional query params
		expect bool
	}{
		{"freeform_substring", "*foo*", narrowing, true},
		{"freeform_leading_only", "*foo", narrowing, true},
		{"host_substring", "host:*api*", narrowing, true},
		{"service_leading_only", "service:*svc", narrowing, true},
		{"path_substring", "path:*signup*", narrowing, true},
		{"trailing_only_no_header", "billing-api*", "", false},
		{"host_trailing_only_no_header", "host:billing-api*", "", false},
		{"host_exact_no_header", "host:billing-api", "", false},
		{"body_scanned_no_header", "body:*foo*", "", false},
		{"headers_keyword_no_header", "headers:foo", "", false},
		{"per_header_no_header", "header.user-agent:foo", "", false},
		{"negated_substring_still_scans", "-host:*foo*", narrowing, true},
		{"multi_term_with_scan", "service:foo host:*api*", "", true}, // exact service: satisfies guard
		{"multi_term_no_scan", "service:foo host:api*", "", false},
		{"empty_query_no_header", "", "", false},
	}

	ts := newInspectServer(t, admin.ReadSources{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var qp string
			if tc.q != "" {
				qp = "q=" + url.QueryEscape(tc.q)
			}
			if tc.extra != "" {
				if qp != "" {
					qp += "&"
				}
				qp += tc.extra
			}
			resp := getRequests(t, ts, qp)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d want 200", resp.StatusCode)
			}
			got := resp.Header.Get("X-Httpcatch-Scan")
			if tc.expect {
				if got != "leading-wildcard-indexed" {
					t.Errorf("X-Httpcatch-Scan: got %q want %q", got, "leading-wildcard-indexed")
				}
			} else {
				if got != "" {
					t.Errorf("X-Httpcatch-Scan: got %q want empty", got)
				}
			}
		})
	}
}

// TestRequests_ScanHeader_ParseErrorAbsent ensures that the scan header is not
// set on 400 responses — there is no parsed query to inspect.
func TestRequests_ScanHeader_ParseErrorAbsent(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "q="+url.QueryEscape("unknown_key:foo"))
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Httpcatch-Scan"); got != "" {
		t.Errorf("X-Httpcatch-Scan on 400: got %q want empty", got)
	}
}

// getRequestDetail sends an authenticated GET /requests/{id} and returns the response.
func getRequestDetail(t *testing.T, ts *httptest.Server, id string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/requests/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET /requests/%s: %v", id, err)
	}
	return resp
}

type detailBody struct {
	Root   map[string]any   `json:"root"`
	Events []map[string]any `json:"events"`
}

func decodeDetailBody(t *testing.T, resp *http.Response) detailBody {
	t.Helper()
	defer resp.Body.Close()
	var b detailBody
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatalf("decode detail body: %v", err)
	}
	return b
}

func TestRequestDetail_NoAuth_Returns401(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/requests/some-id", nil)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET /requests/some-id: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", resp.StatusCode)
	}
}

func TestRequestDetail_StdoutOnly_Returns404(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequestDetail(t, ts, "any-id")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}
}

func TestRequestDetail_UnknownID_Returns404(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequestDetail(t, ts, "missing-id")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] == nil {
		t.Errorf("expected error field in 404 body")
	}
}

func TestRequestDetail_MemoryOnly_CapturedRequest_EmptyEvents(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	ts2 := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	r := &capture.CapturedRequest{
		ID:            "req-abc",
		Timestamp:     ts2,
		Service:       "svc",
		Method:        "GET",
		Path:          "/api",
		CorrelationID: "corr-abc",
		SourceIP:      "1.2.3.4",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequestDetail(t, ts, "req-abc")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}
	body := decodeDetailBody(t, resp)
	if body.Root == nil {
		t.Fatal("root is nil")
	}
	if body.Root["id"] != "req-abc" {
		t.Errorf("root.id: got %v want req-abc", body.Root["id"])
	}
	if body.Events == nil {
		t.Error("events must not be nil (should be [])")
	}
	if len(body.Events) != 0 {
		t.Errorf("events: got %d want 0", len(body.Events))
	}
}

func TestRequestDetail_ContentTypeJSON(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "req-ct",
		Timestamp:     time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Service:       "svc",
		Method:        "GET",
		Path:          "/",
		CorrelationID: "corr-ct",
		SourceIP:      "x",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequestDetail(t, ts, "req-ct")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}
}

func TestRequestDetail_SQLiteOnly_CapturedRequest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()
	ts2 := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	r := &capture.CapturedRequest{
		ID:                "sql-req-1",
		Timestamp:         ts2,
		Service:           "svc",
		Method:            "POST",
		Path:              "/api/orders",
		CorrelationID:     "corr-sql-1",
		SourceIP:          "10.0.0.1",
		Headers:           map[string][]string{"Host": {"example.com"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte("{}"),
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := sqliteSink.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: sqliteSink})
	resp := getRequestDetail(t, ts, "sql-req-1")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	body := decodeDetailBody(t, resp)
	if body.Root == nil {
		t.Fatal("root is nil")
	}
	if body.Root["id"] != "sql-req-1" {
		t.Errorf("root.id: got %v want sql-req-1", body.Root["id"])
	}
	if len(body.Events) != 0 {
		t.Errorf("events: got %d want 0", len(body.Events))
	}
}

func TestRequestDetail_MemoryAndSQLite_Merge(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write a captured request to both sinks.
	req := &capture.CapturedRequest{
		ID:                "merge-req-1",
		Timestamp:         base,
		Service:           "svc",
		Method:            "GET",
		Path:              "/",
		CorrelationID:     "corr-merge",
		SourceIP:          "x",
		Headers:           map[string][]string{"Host": {"h"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := mem.Write(ctx, req); err != nil {
		t.Fatalf("mem.Write req: %v", err)
	}
	if err := sqliteSink.Write(ctx, req); err != nil {
		t.Fatalf("sqlite.Write req: %v", err)
	}

	// Write a response event to SQLite only (simulating an event SQLite persisted
	// but which has not (yet) aged into memory for this test).
	ev := &capture.ResponseEvent{
		ID:            "merge-evt-1",
		Timestamp:     base.Add(time.Second),
		CorrelationID: "corr-merge",
		Service:       "svc",
		ServiceSource: "app",
		Status:        200,
		Headers:       map[string][]string{},
		Body:          []byte{},
	}
	if err := sqliteSink.Write(ctx, ev); err != nil {
		t.Fatalf("sqlite.Write ev: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem, SQLite: sqliteSink})
	resp := getRequestDetail(t, ts, "merge-req-1")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	body := decodeDetailBody(t, resp)
	if body.Root == nil {
		t.Fatal("root is nil")
	}
	if body.Root["id"] != "merge-req-1" {
		t.Errorf("root.id: got %v want merge-req-1", body.Root["id"])
	}
	// The event from SQLite should appear in events list (merged from secondary).
	if len(body.Events) != 1 {
		t.Errorf("events: got %d want 1 (event merged from sqlite)", len(body.Events))
	}
}

func TestRequestDetail_RootFields_CapturedRequest(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:                "field-req",
		Timestamp:         time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Service:           "orders",
		Method:            "POST",
		Path:              "/api/orders",
		CorrelationID:     "corr-fields",
		SourceIP:          "10.0.0.2",
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
		Headers:           map[string][]string{"Content-Type": {"application/json"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte(`{"order_id":"123"}`),
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequestDetail(t, ts, "field-req")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	body := decodeDetailBody(t, resp)
	root := body.Root

	// Verify canonical variant fields present on the root.
	for _, f := range []string{"id", "service", "service_source", "correlation_id", "correlation_source", "method", "path", "headers", "body"} {
		if _, ok := root[f]; !ok {
			t.Errorf("root missing field %q", f)
		}
	}
	if root["service"] != "orders" {
		t.Errorf("root.service: got %v want orders", root["service"])
	}
	if root["correlation_source"] != capture.CorrelationSourceTraceparent {
		t.Errorf("root.correlation_source: got %v want %s", root["correlation_source"], capture.CorrelationSourceTraceparent)
	}
}

func TestRequestDetail_EventsAlwaysPresent(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "always-events-req",
		Timestamp:     time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Service:       "svc",
		Method:        "GET",
		Path:          "/",
		CorrelationID: "corr-ae",
		SourceIP:      "x",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})
	resp := getRequestDetail(t, ts, "always-events-req")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	// Decode raw JSON to check that "events" key is present (not omitted).
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	eventsRaw, ok := raw["events"]
	if !ok {
		t.Fatal("events key missing from response JSON")
	}
	eventsSlice, ok := eventsRaw.([]any)
	if !ok {
		t.Fatalf("events is %T, want []any", eventsRaw)
	}
	if len(eventsSlice) != 0 {
		t.Errorf("events: got %d want 0", len(eventsSlice))
	}
}

// TestRequestDetail_Integration_MemoryAndSQLite boots an admin server with both
// memory and sqlite readers enabled, writes a captured request to both sinks,
// and asserts that GET /requests/{id} returns the request with events: [].
func TestRequestDetail_Integration_MemoryAndSQLite(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(100)
	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/integration.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()
	ts2 := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	r := &capture.CapturedRequest{
		ID:                "int-req-1",
		Timestamp:         ts2,
		Service:           "orders",
		Method:            "POST",
		Path:              "/api/orders",
		CorrelationID:     "corr-int-1",
		SourceIP:          "10.0.0.1",
		Headers:           map[string][]string{"Host": {"example.com"}, "Authorization": {"Bearer secret"}},
		Query:             map[string][]string{"page": {"1"}},
		Cookies:           []capture.Cookie{{Name: "session", Value: "abc"}},
		Body:              []byte(`{"item":"widget"}`),
		ContentType:       "application/json",
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}
	if err := sqliteSink.Write(ctx, r); err != nil {
		t.Fatalf("sqlite.Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem, SQLite: sqliteSink})
	resp := getRequestDetail(t, ts, "int-req-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}

	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Root is present.
	root, ok := raw["root"].(map[string]any)
	if !ok {
		t.Fatalf("root is %T, want map", raw["root"])
	}
	if root["id"] != "int-req-1" {
		t.Errorf("root.id: got %v want int-req-1", root["id"])
	}
	if root["service"] != "orders" {
		t.Errorf("root.service: got %v want orders", root["service"])
	}

	// events key is present and empty (no correlated events yet).
	eventsRaw, ok := raw["events"]
	if !ok {
		t.Fatal("events key missing from response")
	}
	eventsSlice, ok := eventsRaw.([]any)
	if !ok {
		t.Fatalf("events is %T, want []any", eventsRaw)
	}
	if len(eventsSlice) != 0 {
		t.Errorf("events: got %d want 0 (no events API yet)", len(eventsSlice))
	}
}

func TestRequestsAggregate_TotalAcrossPages(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(200)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	const total = 60
	for i := range total {
		r := &capture.CapturedRequest{
			ID:            fmt.Sprintf("r%02d", i),
			Timestamp:     base.Add(time.Duration(i) * time.Second),
			Service:       "svc",
			Method:        "GET",
			Path:          "/api",
			CorrelationID: fmt.Sprintf("c%02d", i),
			SourceIP:      "1.2.3.4",
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})

	since := base.Add(-time.Second).Format(time.RFC3339)
	until := base.Add(10 * time.Minute).Format(time.RFC3339)
	u := ts.URL + "/requests/aggregate?" + url.Values{
		"since":   {since},
		"until":   {until},
		"buckets": {"6"},
	}.Encode()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET aggregate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	var body struct {
		Total   int `json:"total"`
		Buckets []struct {
			Start string `json:"start"`
			S2xx  int    `json:"s2xx"`
			Other int    `json:"other"`
		} `json:"buckets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != total {
		t.Errorf("total: got %d want %d (must exceed default page size)", body.Total, total)
	}
	if len(body.Buckets) != 6 {
		t.Errorf("buckets: got %d want 6", len(body.Buckets))
	}
}

func TestRequestsAggregate_InvalidBuckets(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	u := ts.URL + "/requests/aggregate?buckets=0"
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET aggregate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
}

// getAggregate sends an authenticated GET /requests/aggregate request.
func getAggregate(t *testing.T, ts *httptest.Server, query string) *http.Response {
	t.Helper()
	u := ts.URL + "/requests/aggregate"
	if query != "" {
		u += "?" + query
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET /requests/aggregate: %v", err)
	}
	return resp
}

// TestRequests_UnindexedScan_WithoutNarrowing_Returns400 verifies that a
// leading-wildcard query without a time range or exact service: term is
// rejected before the read path runs.
func TestRequests_UnindexedScan_WithoutNarrowing_Returns400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})

	cases := []struct {
		name string
		q    string
	}{
		{"freeform_leading_wildcard", "*foo*"},
		{"host_leading_wildcard", "host:*api*"},
		{"service_leading_wildcard", "service:*svc"},
		{"path_leading_wildcard", "path:*signup*"},
		{"negated_leading_wildcard", "-host:*foo*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := getRequests(t, ts, "q="+url.QueryEscape(tc.q))
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status: got %d want 400", resp.StatusCode)
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["field"] != "q" {
				t.Errorf("field: got %v want q", body["field"])
			}
		})
	}
}

// TestRequests_UnindexedScan_WithTimeRange_Succeeds verifies that a
// leading-wildcard query accompanied by since/until passes the narrowing guard.
func TestRequests_UnindexedScan_WithTimeRange_Succeeds(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	q := url.Values{
		"q":     {"*foo*"},
		"since": {"2026-01-01T00:00:00Z"},
		"until": {"2026-12-31T23:59:59Z"},
	}.Encode()
	resp := getRequests(t, ts, q)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

// TestRequests_UnindexedScan_WithExactService_Succeeds verifies that a
// leading-wildcard query accompanied by an exact service: term passes the guard.
func TestRequests_UnindexedScan_WithExactService_Succeeds(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	// "service:billing" is exact; combined with host:*api* (unindexed-scan)
	// the exact service term satisfies the narrowing requirement.
	q := url.Values{
		"q": {"service:billing host:*api*"},
	}.Encode()
	resp := getRequests(t, ts, q)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

// TestAggregate_UnindexedScan_WithoutNarrowing_Returns400 verifies the same
// narrowing guard applies to the aggregate endpoint.
func TestAggregate_UnindexedScan_WithoutNarrowing_Returns400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getAggregate(t, ts, "q="+url.QueryEscape("*foo*"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "q" {
		t.Errorf("field: got %v want q", body["field"])
	}
}

// TestAggregate_ScanHeader_SetWhenUnindexed verifies that the aggregate handler
// emits X-Httpcatch-Scan when the query is an unindexed scan.
func TestAggregate_ScanHeader_SetWhenUnindexed(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	q := url.Values{
		"q":     {"host:*api*"},
		"since": {"2026-01-01T00:00:00Z"},
		"until": {"2026-12-31T23:59:59Z"},
	}.Encode()
	resp := getAggregate(t, ts, q)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Httpcatch-Scan"); got != "leading-wildcard-indexed" {
		t.Errorf("X-Httpcatch-Scan: got %q want leading-wildcard-indexed", got)
	}
}

// TestAggregate_ScanHeader_AbsentWhenNormal verifies that the aggregate handler
// does not set X-Httpcatch-Scan on a non-scanning query.
func TestAggregate_ScanHeader_AbsentWhenNormal(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	q := url.Values{
		"since": {"2026-01-01T00:00:00Z"},
		"until": {"2026-12-31T23:59:59Z"},
	}.Encode()
	resp := getAggregate(t, ts, q)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Httpcatch-Scan"); got != "" {
		t.Errorf("X-Httpcatch-Scan: got %q want empty", got)
	}
}
