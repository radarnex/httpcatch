package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// filterTestDB opens a fresh SQLite sink for filter integration tests.
func filterTestDB(t *testing.T) *sinks.SQLiteSink {
	t.Helper()
	s, err := sinks.NewSQLiteSink(t.TempDir() + "/filter_test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// filterBaseRequest builds a CapturedRequest with required NOT NULL fields.
func filterBaseRequest(id, service, method, path, corrID, sourceIP string, ts time.Time) *capture.CapturedRequest {
	return &capture.CapturedRequest{
		ID:                id,
		Timestamp:         ts,
		Service:           service,
		Method:            method,
		Path:              path,
		CorrelationID:     corrID,
		SourceIP:          sourceIP,
		Headers:           map[string][]string{"Host": {"example.com"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
}

// filterResponseEvent builds a ResponseEvent with required fields.
func filterResponseEvent(id, corrID, service string, status int, ts time.Time) *capture.ResponseEvent {
	return &capture.ResponseEvent{
		ID:            id,
		Timestamp:     ts,
		CorrelationID: corrID,
		Service:       service,
		ServiceSource: "explicit",
		Status:        status,
		Headers:       map[string][]string{},
		Body:          []byte{},
	}
}

// getRequestsWithQuery sends an authenticated GET /requests request with the
// given query string and returns the status code and decoded body.
func getRequestsWithQuery(t *testing.T, ts *httptest.Server, query string) (int, requestsBody) {
	t.Helper()
	resp := getRequests(t, ts, query)
	defer resp.Body.Close()
	var body requestsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /requests body: %v", err)
	}
	return resp.StatusCode, body
}

// recordIDs extracts the ids from a list of records for assertion.
func recordIDs(recs []map[string]any) []string {
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i], _ = r["id"].(string)
	}
	return ids
}

func TestRequests_FilterValidation_UnknownKey_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "unknown_param=foo")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "unknown_param" {
		t.Errorf("field: got %v want unknown_param", body["field"])
	}
}

func TestRequests_FilterValidation_BadSince_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "since=2026-05-18")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "since" {
		t.Errorf("field: got %v want since", body["field"])
	}
}

func TestRequests_FilterValidation_BadUntil_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "until=notadate")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "until" {
		t.Errorf("field: got %v want until", body["field"])
	}
}

func TestRequests_FilterValidation_BadMethod_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "method=FETCH")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "method" {
		t.Errorf("field: got %v want method", body["field"])
	}
}

func TestRequests_FilterValidation_BadStatus_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "status=notastatus")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "status" {
		t.Errorf("field: got %v want status", body["field"])
	}
}

func TestRequests_FilterValidation_BadHasEvents_400(t *testing.T) {
	t.Parallel()

	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "has_events=yes")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["field"] != "has_events" {
		t.Errorf("field: got %v want has_events", body["field"])
	}
}

func TestRequests_Filter_Service(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "orders", "GET", "/", "c1", "x", base)
	r2 := filterBaseRequest("r2", "payments", "GET", "/", "c2", "x", base.Add(time.Second))
	r3 := filterBaseRequest("r3", "orders", "GET", "/", "c3", "x", base.Add(2*time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2, r3} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})
	status, body := getRequestsWithQuery(t, ts, "service=orders")
	if status != http.StatusOK {
		t.Fatalf("status: got %d want 200", status)
	}
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("expected 2 records, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("unexpected id %q in filtered result", id)
		}
	}
}

func TestRequests_Filter_Method(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "svc", "GET", "/", "c1", "x", base)
	r2 := filterBaseRequest("r2", "svc", "POST", "/", "c2", "x", base.Add(time.Second))
	r3 := filterBaseRequest("r3", "svc", "POST", "/", "c3", "x", base.Add(2*time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2, r3} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})
	// method is case-insensitive on parse
	_, body := getRequestsWithQuery(t, ts, "method=post")
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("expected 2 records, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		if id != "r2" && id != "r3" {
			t.Errorf("unexpected id %q", id)
		}
	}
}

func TestRequests_Filter_StatusExact(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Two requests, one with a correlated response at 200, one at 500.
	r1 := filterBaseRequest("r1", "svc", "GET", "/", "corr-1", "x", base)
	r2 := filterBaseRequest("r2", "svc", "GET", "/", "corr-2", "x", base.Add(time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	ev1 := filterResponseEvent("ev1", "corr-1", "svc", 200, base.Add(100*time.Millisecond))
	ev2 := filterResponseEvent("ev2", "corr-2", "svc", 500, base.Add(time.Second+100*time.Millisecond))
	for _, ev := range []*capture.ResponseEvent{ev1, ev2} {
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write ev: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})
	_, body := getRequestsWithQuery(t, ts, "status=200")
	ids := recordIDs(body.Records)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("status=200: got %v want [r1]", ids)
	}
}

func TestRequests_Filter_StatusClass(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "svc", "GET", "/", "corr-1", "x", base)
	r2 := filterBaseRequest("r2", "svc", "GET", "/", "corr-2", "x", base.Add(time.Second))
	r3 := filterBaseRequest("r3", "svc", "GET", "/", "corr-3", "x", base.Add(2*time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2, r3} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	ev1 := filterResponseEvent("ev1", "corr-1", "svc", 200, base.Add(100*time.Millisecond))
	ev2 := filterResponseEvent("ev2", "corr-2", "svc", 500, base.Add(time.Second+100*time.Millisecond))
	ev3 := filterResponseEvent("ev3", "corr-3", "svc", 503, base.Add(2*time.Second+100*time.Millisecond))
	for _, ev := range []*capture.ResponseEvent{ev1, ev2, ev3} {
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write ev: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})

	// 5xx should return r2 and r3
	_, body := getRequestsWithQuery(t, ts, "status=5xx")
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("status=5xx: got %d records want 2: %v", len(ids), ids)
	}
	for _, id := range ids {
		if id != "r2" && id != "r3" {
			t.Errorf("status=5xx: unexpected id %q", id)
		}
	}

	// 2xx should return r1
	_, body = getRequestsWithQuery(t, ts, "status=2xx")
	ids = recordIDs(body.Records)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("status=2xx: got %v want [r1]", ids)
	}
}

func TestRequests_Filter_Path(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "svc", "GET", "/api/users/1", "c1", "x", base)
	r2 := filterBaseRequest("r2", "svc", "GET", "/api/orders/1", "c2", "x", base.Add(time.Second))
	r3 := filterBaseRequest("r3", "svc", "GET", "/api/users/2", "c3", "x", base.Add(2*time.Second))
	r4 := filterBaseRequest("r4", "svc", "GET", "/health", "c4", "x", base.Add(3*time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2, r3, r4} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})
	_, body := getRequestsWithQuery(t, ts, "path=/api/users")
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("path=/api/users: got %d records want 2: %v", len(ids), ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("path filter: unexpected id %q", id)
		}
	}

	// Exact prefix: /api should match all 3 under /api
	_, body = getRequestsWithQuery(t, ts, "path=/api")
	if len(body.Records) != 3 {
		t.Errorf("path=/api: got %d want 3", len(body.Records))
	}
}

func TestRequests_Filter_CorrelationID(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "svc", "GET", "/", "target-corr", "x", base)
	r2 := filterBaseRequest("r2", "svc", "GET", "/", "other-corr", "x", base.Add(time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})
	_, body := getRequestsWithQuery(t, ts, "correlation_id=target-corr")
	ids := recordIDs(body.Records)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("correlation_id filter: got %v want [r1]", ids)
	}
}

func TestRequests_Filter_SourceIP(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "svc", "GET", "/", "c1", "10.0.0.1", base)
	r2 := filterBaseRequest("r2", "svc", "GET", "/", "c2", "10.0.0.2", base.Add(time.Second))
	r3 := filterBaseRequest("r3", "svc", "GET", "/", "c3", "10.0.0.1", base.Add(2*time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2, r3} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})
	_, body := getRequestsWithQuery(t, ts, "source_ip=10.0.0.1")
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("source_ip filter: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("source_ip filter: unexpected id %q", id)
		}
	}
}

func TestRequests_Filter_HasEvents_True(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "svc", "GET", "/", "corr-1", "x", base)
	r2 := filterBaseRequest("r2", "svc", "GET", "/", "corr-2", "x", base.Add(time.Second))
	r3 := filterBaseRequest("r3", "svc", "GET", "/", "corr-3", "x", base.Add(2*time.Second))
	for _, r := range []*capture.CapturedRequest{r1, r2, r3} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	// Only r1 and r3 have events.
	ev1 := filterResponseEvent("ev1", "corr-1", "svc", 200, base.Add(100*time.Millisecond))
	ev3 := filterResponseEvent("ev3", "corr-3", "svc", 200, base.Add(2*time.Second+100*time.Millisecond))
	for _, ev := range []*capture.ResponseEvent{ev1, ev3} {
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write ev: %v", err)
		}
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})

	// has_events=true: only r1, r3
	_, body := getRequestsWithQuery(t, ts, "has_events=true")
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("has_events=true: got %d want 2: %v", len(ids), ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("has_events=true: unexpected id %q", id)
		}
	}

	// has_events=false: only r2
	_, body = getRequestsWithQuery(t, ts, "has_events=false")
	ids = recordIDs(body.Records)
	if len(ids) != 1 || ids[0] != "r2" {
		t.Errorf("has_events=false: got %v want [r2]", ids)
	}
}

func TestRequests_Filter_Since_Until(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := filterBaseRequest("r1", "svc", "GET", "/", "c1", "x", base.Add(-time.Hour))
	r2 := filterBaseRequest("r2", "svc", "GET", "/", "c2", "x", base)
	r3 := filterBaseRequest("r3", "svc", "GET", "/", "c3", "x", base.Add(time.Hour))
	for _, r := range []*capture.CapturedRequest{r1, r2, r3} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	sinceStr := base.Format(time.RFC3339)
	untilStr := base.Add(time.Hour).Format(time.RFC3339)

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})

	// since=base: r2 and r3 (half-open [since, until))
	_, body := getRequestsWithQuery(t, ts, "since="+sinceStr)
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("since filter: got %v want 2 records", ids)
	}

	// until=base+1h: r1 and r2 (strictly before until)
	_, body = getRequestsWithQuery(t, ts, "until="+untilStr)
	ids = recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("until filter: got %v want 2 records", ids)
	}

	// since+until window: only r2 (base <= ts < base+1h)
	_, body = getRequestsWithQuery(t, ts, "since="+sinceStr+"&until="+untilStr)
	ids = recordIDs(body.Records)
	if len(ids) != 1 || ids[0] != "r2" {
		t.Errorf("since+until: got %v want [r2]", ids)
	}
}

func TestRequests_Filter_CombinedAND(t *testing.T) {
	t.Parallel()

	// "POST /api/users with 5xx in the last hour"
	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	now := base

	// r1: POST /api/users + 5xx — should match
	r1 := filterBaseRequest("r1", "svc", "POST", "/api/users/1", "c1", "x", now.Add(-30*time.Minute))
	// r2: GET /api/users + 5xx — wrong method
	r2 := filterBaseRequest("r2", "svc", "GET", "/api/users/1", "c2", "x", now.Add(-20*time.Minute))
	// r3: POST /api/orders + 5xx — wrong path
	r3 := filterBaseRequest("r3", "svc", "POST", "/api/orders/1", "c3", "x", now.Add(-10*time.Minute))
	// r4: POST /api/users + 2xx — wrong status
	r4 := filterBaseRequest("r4", "svc", "POST", "/api/users/2", "c4", "x", now.Add(-5*time.Minute))
	// r5: POST /api/users + 5xx but old
	r5 := filterBaseRequest("r5", "svc", "POST", "/api/users/3", "c5", "x", now.Add(-2*time.Hour))

	for _, r := range []*capture.CapturedRequest{r1, r2, r3, r4, r5} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Response events.
	for i, corrID := range []string{"c1", "c2", "c3", "c5"} {
		ts2 := now.Add(-30*time.Minute + time.Duration(i)*time.Second)
		ev := filterResponseEvent(fmt.Sprintf("ev%d", i+1), corrID, "svc", 500, ts2)
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write ev: %v", err)
		}
	}
	// r4 gets a 200 response
	ev4 := filterResponseEvent("ev-r4", "c4", "svc", 200, now.Add(-4*time.Minute))
	if err := s.Write(ctx, ev4); err != nil {
		t.Fatalf("Write ev4: %v", err)
	}

	sinceStr := now.Add(-time.Hour).Format(time.RFC3339)
	tsSrv := newInspectServer(t, admin.ReadSources{SQLite: s})

	query := "method=POST&path=/api/users&status=5xx&since=" + sinceStr
	_, body := getRequestsWithQuery(t, tsSrv, query)
	ids := recordIDs(body.Records)
	// Only r1 should match all three filters within the last hour.
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("combined filter: got %v want [r1]", ids)
	}
}

func TestRequests_SQLiteOnly_NonTemporalFilter_ReadSource(t *testing.T) {
	t.Parallel()

	// With a non-temporal filter, even when memory reader is present, the
	// response must report X-Httpcatch-Read-Source: sqlite.
	mem := sinks.NewMemorySink(100)
	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r := filterBaseRequest("r1", "orders", "GET", "/", "c1", "x", base)
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}
	if err := s.Write(ctx, r); err != nil {
		t.Fatalf("sqlite.Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem, SQLite: s})

	// Non-temporal filter → must be SQLite-only.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/requests?service=orders", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /requests: %v", err)
	}
	defer resp.Body.Close()

	if src := resp.Header.Get("X-Httpcatch-Read-Source"); src != "sqlite" {
		t.Errorf("X-Httpcatch-Read-Source: got %q want sqlite", src)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

func TestRequests_TemporalOnly_Memory_ReadSource(t *testing.T) {
	t.Parallel()

	// A since/until-only query is memory-eligible. With a large enough memory
	// buffer, the source header must be "memory".
	mem := sinks.NewMemorySink(100)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r := filterBaseRequest("r1", "svc", "GET", "/", "c1", "x", base)
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem})

	sinceStr := base.Add(-time.Hour).Format(time.RFC3339)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/requests?since="+sinceStr, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /requests: %v", err)
	}
	defer resp.Body.Close()

	if src := resp.Header.Get("X-Httpcatch-Read-Source"); src != "memory" {
		t.Errorf("X-Httpcatch-Read-Source: got %q want memory", src)
	}
}

func TestRequests_StdoutOnly_WithFilter_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	// Stdout-only (no readers) with any filter → 200 empty list, source: none.
	ts := newInspectServer(t, admin.ReadSources{})
	resp := getRequests(t, ts, "service=orders")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if h := resp.Header.Get("X-Httpcatch-Read-Source"); h != "none" {
		t.Errorf("X-Httpcatch-Read-Source: got %q want none", h)
	}
	var body requestsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Records) != 0 {
		t.Errorf("records: got %d want 0", len(body.Records))
	}
}

func TestRequests_Dedup_TemporalFilter_MemoryAndSQLite(t *testing.T) {
	t.Parallel()

	// Temporal-only filter with both sinks enabled; record present in both must
	// appear exactly once in the merged result.
	mem := sinks.NewMemorySink(100)
	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r := &capture.CapturedRequest{
		ID:                "shared",
		Timestamp:         base,
		Service:           "svc",
		Method:            "GET",
		Path:              "/",
		CorrelationID:     "c1",
		SourceIP:          "x",
		Headers:           map[string][]string{"Host": {"h"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}
	if err := s.Write(ctx, r); err != nil {
		t.Fatalf("sqlite.Write: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{Memory: mem, SQLite: s})
	sinceStr := base.Add(-time.Hour).Format(time.RFC3339)
	_, body := getRequestsWithQuery(t, ts, "since="+sinceStr)
	if len(body.Records) != 1 {
		t.Errorf("dedup with temporal filter: got %d records want 1", len(body.Records))
	}
}

func TestRequests_OrphanRows_AppearInList(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write a captured request — not an orphan.
	req := filterBaseRequest("r1", "svc", "GET", "/", "corr-req", "x", base)
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}

	// Write an orphan response event (no matching captured request).
	orphan := &capture.ResponseEvent{
		ID: "ev-orphan", Timestamp: base.Add(time.Second),
		CorrelationID: "corr-orphan", Service: "svc", ServiceSource: "explicit",
		Status: 503, Headers: map[string][]string{}, Body: []byte{},
	}
	if err := s.Write(ctx, orphan); err != nil {
		t.Fatalf("Write orphan: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})
	_, body := getRequestsWithQuery(t, ts, "")
	ids := recordIDs(body.Records)
	if len(ids) != 2 {
		t.Fatalf("expected 2 records (req + orphan), got %d: %v", len(ids), ids)
	}

	// Verify the orphan row has kind=orphan_response.
	var orphanRow map[string]any
	for _, r := range body.Records {
		if r["id"] == "ev-orphan" {
			orphanRow = r
			break
		}
	}
	if orphanRow == nil {
		t.Fatal("orphan row not found")
	}
	if orphanRow["kind"] != "orphan_response" {
		t.Errorf("orphan kind: got %v want orphan_response", orphanRow["kind"])
	}
	// event_count and has_events must be null.
	if v, ok := orphanRow["event_count"]; ok && v != nil {
		t.Errorf("orphan event_count should be null, got %v", v)
	}
	if v, ok := orphanRow["has_events"]; ok && v != nil {
		t.Errorf("orphan has_events should be null, got %v", v)
	}
}

func TestRequests_OrphanStatus_Filter(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write an orphan response event with status 503.
	orphan := &capture.ResponseEvent{
		ID: "ev-orphan-503", Timestamp: base,
		CorrelationID: "corr-503", Service: "svc", ServiceSource: "explicit",
		Status: 503, Headers: map[string][]string{}, Body: []byte{},
	}
	if err := s.Write(ctx, orphan); err != nil {
		t.Fatalf("Write orphan: %v", err)
	}

	ts := newInspectServer(t, admin.ReadSources{SQLite: s})

	// status=5xx should include the orphan_response.
	_, body := getRequestsWithQuery(t, ts, "status=5xx")
	ids := recordIDs(body.Records)
	found := false
	for _, id := range ids {
		if id == "ev-orphan-503" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("orphan response not returned by status=5xx filter; got %v", ids)
	}

	// status=2xx should NOT include the orphan.
	_, body2xx := getRequestsWithQuery(t, ts, "status=2xx")
	for _, r := range body2xx.Records {
		if r["id"] == "ev-orphan-503" {
			t.Error("orphan response should not appear in status=2xx filter")
		}
	}
}

func TestRequests_ServicesSeen_AlphabeticalOrder(t *testing.T) {
	t.Parallel()

	s := filterTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	services := []string{"zebra", "alpha", "monkey"}
	for i, svc := range services {
		r := filterBaseRequest(fmt.Sprintf("r%d", i), svc, "GET", "/", fmt.Sprintf("c%d", i), "x", base.Add(time.Duration(i)*time.Second))
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	seen, err := s.ServicesSeen(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ServicesSeen: %v", err)
	}
	want := []string{"alpha", "monkey", "zebra"}
	if len(seen) != len(want) {
		t.Fatalf("ServicesSeen: got %v want %v", seen, want)
	}
	for i, svc := range seen {
		if svc != want[i] {
			t.Errorf("seen[%d]: got %q want %q", i, svc, want[i])
		}
	}
}
