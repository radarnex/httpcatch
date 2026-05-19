package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// newInspectServer builds a test httptest.Server with the given readers wired in.
func newInspectServer(t *testing.T, readers admin.ReadSources) *httptest.Server {
	t.Helper()
	cfg := config.AdminConfig{
		Bind:          "127.0.0.1:0",
		Token:         testAdminToken,
		SessionTTL:    time.Hour,
		SessionSecure: false,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{}, readers)
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
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
	resp, err := http.DefaultClient.Do(req)
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
	resp, err := http.DefaultClient.Do(req)
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
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q want application/json", ct)
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
			ID:               fmt.Sprintf("r%d", i),
			Timestamp:        base.Add(time.Duration(i) * time.Second),
			Service:          "svc",
			Method:           "GET",
			Path:             "/",
			CorrelationID:    fmt.Sprintf("c%d", i),
			SourceIP:         "x",
			Headers:          map[string][]string{"Host": {"example.com"}},
			Query:            map[string][]string{},
			Cookies:          []capture.Cookie{},
			Body:             []byte{},
			ServiceSource:    capture.ServiceSourceHeader,
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
		ID:               "shared-id",
		Timestamp:        ts2,
		Service:          "svc",
		Method:           "GET",
		Path:             "/",
		CorrelationID:    "corr",
		SourceIP:         "x",
		Headers:          map[string][]string{"Host": {"example.com"}},
		Query:            map[string][]string{},
		Cookies:          []capture.Cookie{},
		Body:             []byte{},
		ServiceSource:    capture.ServiceSourceHeader,
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
