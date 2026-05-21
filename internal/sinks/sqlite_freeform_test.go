package sinks

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
	"github.com/radarnex/httpcatch/internal/searchql"
)

// sqliteFreeformSetup writes one captured request per Tier-1 column carrying
// the needle "billing-api" in a different field, so each acceptance criterion
// for the union expansion can be asserted independently.
func sqliteFreeformSetup(t *testing.T) (*SQLiteSink, map[string]string) {
	t.Helper()
	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	mkReq := func(id, host, path, service string, body []byte, hdrs map[string][]string) *capture.CapturedRequest {
		r := sqliteRequest(id, base.Add(time.Duration(len(id))*time.Millisecond), service, "POST", path, "corr-"+id, "10.0.0.1")
		if hdrs == nil {
			hdrs = map[string][]string{}
		}
		hdrs[capture.HostHeader] = []string{host}
		r.Headers = hdrs
		r.Body = body
		return r
	}

	matches := map[string]string{
		"by-host":         "host=billing-api (indexed exact)",
		"by-path":         "path=billing-api (indexed exact)",
		"by-service":      "service=billing-api (indexed exact)",
		"by-body":         "body contains billing-api",
		"by-headers":      "headers contains billing-api",
		"by-event-rpath":  "event request_path=billing-api (indexed exact)",
		"by-event-rbody":  "event request_body contains billing-api",
		"by-event-rhdrs":  "event request_headers contains billing-api",
		"by-event-respb":  "event response_body contains billing-api",
		"by-event-resphd": "event response_headers contains billing-api",
		"miss":            "no billing-api anywhere",
	}

	recs := []*capture.CapturedRequest{
		mkReq("by-host", "billing-api", "/svc/x", "svc-a", []byte(`{"x":1}`), nil),
		mkReq("by-path", "other.local", "billing-api", "svc-b", []byte(`{"x":2}`), nil),
		mkReq("by-service", "other.local", "/y", "billing-api", []byte(`{"x":3}`), nil),
		mkReq("by-body", "other.local", "/z", "svc-c", []byte(`{"msg":"billing-api was here"}`), nil),
		mkReq("by-headers", "other.local", "/h", "svc-d", []byte(`{"x":5}`),
			map[string][]string{"X-Trace-Id": {"billing-api-token"}}),
		mkReq("by-event-rpath", "other.local", "/ep", "svc-e", []byte(`{"x":6}`), nil),
		mkReq("by-event-rbody", "other.local", "/eb", "svc-f", []byte(`{"x":7}`), nil),
		mkReq("by-event-rhdrs", "other.local", "/eh", "svc-g", []byte(`{"x":8}`), nil),
		mkReq("by-event-respb", "other.local", "/ev", "svc-h", []byte(`{"x":9}`), nil),
		mkReq("by-event-resphd", "other.local", "/evh", "svc-i", []byte(`{"x":10}`), nil),
		mkReq("miss", "elsewhere.local", "/none", "svc-j", []byte(`{"msg":"unrelated"}`), nil),
	}
	for _, r := range recs {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}

	// Outbound events that carry the needle in fields that only exist on the
	// events table.
	mkOutbound := func(corrID, reqPath string, reqBody []byte, reqHdrs, respHdrs map[string][]string, respBody []byte) *capture.OutboundEvent {
		ob := &capture.OutboundEvent{
			ID:            "out-" + corrID,
			Timestamp:     base.Add(2 * time.Second),
			CorrelationID: corrID,
			Service:       "downstream",
			ServiceSource: "explicit",
			DurationMS:    1,
			Request: capture.OutboundRequestHalf{
				Method:  "GET",
				Path:    reqPath,
				Headers: reqHdrs,
				Body:    reqBody,
			},
		}
		if respHdrs != nil || respBody != nil {
			ob.Response = &capture.OutboundResponseHalf{
				Status:  200,
				Headers: respHdrs,
				Body:    respBody,
			}
		}
		return ob
	}

	events := []*capture.OutboundEvent{
		mkOutbound("corr-by-event-rpath", "billing-api", nil, map[string][]string{}, nil, nil),
		mkOutbound("corr-by-event-rbody", "/x", []byte("billing-api in request body"), map[string][]string{}, nil, nil),
		mkOutbound("corr-by-event-rhdrs", "/x", nil, map[string][]string{"X-Audit": {"billing-api-token"}}, nil, nil),
		mkOutbound("corr-by-event-respb", "/x", nil, map[string][]string{},
			map[string][]string{}, []byte("billing-api in response body")),
		mkOutbound("corr-by-event-resphd", "/x", nil, map[string][]string{},
			map[string][]string{"X-Source": {"billing-api-token"}}, []byte{}),
	}
	for _, e := range events {
		if err := s.Write(ctx, e); err != nil {
			t.Fatalf("Write event %s: %v", e.ID, err)
		}
	}

	return s, matches
}

func TestSQLiteFreeform_MatchesEveryTier1Arm(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFreeformSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "billing-api")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	got := make(map[string]bool, len(ids))
	for _, id := range ids {
		got[id] = true
	}

	wantHits := []string{
		"by-host", "by-path", "by-service", "by-body", "by-headers",
		"by-event-rpath", "by-event-rbody", "by-event-rhdrs",
		"by-event-respb", "by-event-resphd",
	}
	for _, id := range wantHits {
		if !got[id] {
			t.Errorf("freeform billing-api did not match %s (rows: %v)", id, ids)
		}
	}
	if got["miss"] {
		t.Error("freeform billing-api should not match the 'miss' row")
	}
}

func TestSQLiteFreeform_PrefixMatchesIndexedArmsOnly(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFreeformSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "billing-api*")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	got := make(map[string]bool)
	for _, r := range rows {
		got[r.ID] = true
	}

	// Indexed arms (host/path/service) prefix-match billing-api.
	// Scanned arms (body/headers/event_*) substring-match — they should still
	// match for any row whose scanned field contains "billing-api".
	wantHits := []string{
		"by-host", "by-path", "by-service",
		"by-body", "by-headers",
		"by-event-rpath", "by-event-rbody", "by-event-rhdrs",
		"by-event-respb", "by-event-resphd",
	}
	for _, id := range wantHits {
		if !got[id] {
			t.Errorf("freeform billing-api* did not match %s", id)
		}
	}
	if got["miss"] {
		t.Error("freeform billing-api* should not match the 'miss' row")
	}
}

func TestSQLiteFreeform_Negated(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFreeformSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{Query: mustParseQuery(t, "-billing-api")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	got := make(map[string]bool)
	for _, r := range rows {
		got[r.ID] = true
	}
	if !got["miss"] {
		t.Error("negated freeform must keep the 'miss' row")
	}
	for _, id := range []string{
		"by-host", "by-path", "by-service", "by-body", "by-headers",
		"by-event-rpath", "by-event-rbody", "by-event-rhdrs",
		"by-event-respb", "by-event-resphd",
	} {
		if got[id] {
			t.Errorf("negated freeform should drop %s", id)
		}
	}
}

func TestSQLiteFreeform_MultiTokenAND(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFreeformSetup(t)
	ctx := context.Background()

	// "billing-api" matches every "by-*" row; "orders" doesn't appear anywhere
	// in the fixture, so the AND must return no rows.
	q := inspect.InspectQuery{Query: mustParseQuery(t, "billing-api orders")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("billing-api AND orders: got %v want 0 rows", rowIDs(rows))
	}
}

func TestSQLiteFreeform_MixedWithFieldQualified(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFreeformSetup(t)
	ctx := context.Background()

	// service:svc-a is exact, so only "by-host" matches that filter; freeform
	// "billing-api" must also match, which it does via the host arm.
	q := inspect.InspectQuery{Query: mustParseQuery(t, "service:svc-a billing-api")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 1 || ids[0] != "by-host" {
		t.Errorf("service:svc-a + billing-api: got %v want [by-host]", ids)
	}
}

func TestSQLiteFreeform_StructuredFieldsAreExcluded(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFreeformSetup(t)
	ctx := context.Background()

	// Every row in the fixture has method=POST. Typing the bare word "POST"
	// must NOT match those rows — method is not Tier-1.
	q := inspect.InspectQuery{Query: mustParseQuery(t, "POST")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("freeform POST: got %v want 0 rows (method is not Tier-1)", rowIDs(rows))
	}

	// Same for source_ip: 10.0.0.1 is the source_ip on every row, but
	// freeform doesn't look at source_ip.
	q = inspect.InspectQuery{Query: mustParseQuery(t, "10.0.0.1")}
	rows, _, err = s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("freeform 10.0.0.1: got %v want 0 rows (source_ip is not Tier-1)", rowIDs(rows))
	}
}

func TestSQLiteFreeform_OrphanArmMatchesEventColumns(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Each orphan event puts the needle "needle-x" in a different column.
	// Indexed arms (service, request_path) require exact equality with the
	// needle in non-wildcard mode; scanned arms (body, headers) substring-
	// match anywhere within the column.
	events := []*capture.OutboundEvent{
		{
			ID: "orph-svc", Timestamp: base, CorrelationID: "co1",
			Service: "needle-x", ServiceSource: "explicit", DurationMS: 1,
			Request: capture.OutboundRequestHalf{Method: "GET", Path: "/", Headers: map[string][]string{}},
		},
		{
			ID: "orph-path", Timestamp: base.Add(time.Second), CorrelationID: "co2",
			Service: "downstream", ServiceSource: "explicit", DurationMS: 1,
			Request: capture.OutboundRequestHalf{Method: "GET", Path: "needle-x", Headers: map[string][]string{}},
		},
		{
			ID: "orph-rbody", Timestamp: base.Add(2 * time.Second), CorrelationID: "co3",
			Service: "downstream", ServiceSource: "explicit", DurationMS: 1,
			Request: capture.OutboundRequestHalf{Method: "GET", Path: "/", Headers: map[string][]string{}, Body: []byte("needle-x in body")},
		},
		{
			ID: "orph-rhdrs", Timestamp: base.Add(3 * time.Second), CorrelationID: "co4",
			Service: "downstream", ServiceSource: "explicit", DurationMS: 1,
			Request: capture.OutboundRequestHalf{Method: "GET", Path: "/", Headers: map[string][]string{"X-Trace": {"needle-x"}}},
		},
		{
			ID: "orph-miss", Timestamp: base.Add(4 * time.Second), CorrelationID: "co5",
			Service: "downstream", ServiceSource: "explicit", DurationMS: 1,
			Request: capture.OutboundRequestHalf{Method: "GET", Path: "/", Headers: map[string][]string{}},
		},
	}
	for _, e := range events {
		if err := s.Write(ctx, e); err != nil {
			t.Fatalf("Write %s: %v", e.ID, err)
		}
	}

	q := inspect.InspectQuery{Query: mustParseQuery(t, "needle-x")}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	got := make(map[string]bool)
	for _, r := range rows {
		got[r.ID] = true
	}
	for _, id := range []string{"orph-svc", "orph-path", "orph-rbody", "orph-rhdrs"} {
		if !got[id] {
			t.Errorf("freeform needle-x missed orphan %s (rows: %v)", id, rowIDs(rows))
		}
	}
	if got["orph-miss"] {
		t.Error("freeform needle-x should not match orph-miss")
	}
}

func TestSQLiteFreeform_PrefixUsesIndexes(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFreeformSetup(t)
	q := inspect.InspectQuery{Query: mustParseQuery(t, "billing-api*")}

	plan := explainPlanForQuery(t, s, q)
	if !strings.Contains(plan, "idx_captured_requests_path") {
		t.Errorf("freeform billing-api* should use idx_captured_requests_path; plan was:\n%s", plan)
	}

	// The orphan arm: build the EXPLAIN against the events table directly,
	// using the orphan-compile output.
	where, args := searchql.CompileSQLOrphans(q.Query)
	stmt := "EXPLAIN QUERY PLAN SELECT e.id FROM events e WHERE " + where
	rows, err := s.db.QueryContext(context.Background(), stmt, args...)
	if err != nil {
		t.Fatalf("EXPLAIN orphan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		details = append(details, detail)
	}
	orphanPlan := strings.Join(details, "\n")
	if !strings.Contains(orphanPlan, "idx_events_request_path") {
		t.Errorf("freeform billing-api* orphan arm should use idx_events_request_path; plan was:\n%s", orphanPlan)
	}
}

func TestSQLiteSink_SchemaMigration_AddsIdxEventsRequestPath(t *testing.T) {
	t.Parallel()

	// Create an SQLite database without idx_events_request_path, simulating a
	// database from before this migration. Then open it via NewSQLiteSink and
	// confirm the index now exists.
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	priorSchema := `
CREATE TABLE IF NOT EXISTS captured_requests (
    id TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    service TEXT NOT NULL,
    service_source TEXT NOT NULL,
    host TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    correlation_source TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    source_ip TEXT NOT NULL,
    content_type TEXT NOT NULL,
    query TEXT NOT NULL,
    headers TEXT NOT NULL,
    cookies TEXT NOT NULL,
    body BLOB NOT NULL,
    body_truncated INTEGER NOT NULL,
    body_original_size INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    type TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    service TEXT NOT NULL,
    service_source TEXT NOT NULL,
    status INTEGER,
    duration_ms INTEGER NOT NULL,
    request_method TEXT,
    request_path TEXT,
    request_headers TEXT,
    request_body BLOB,
    request_body_truncated INTEGER,
    request_body_original_size INTEGER,
    response_status INTEGER,
    response_headers TEXT,
    response_body BLOB,
    response_body_truncated INTEGER,
    response_body_original_size INTEGER
);
`
	if _, err := legacy.Exec(priorSchema); err != nil {
		t.Fatalf("write legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	verifyIndex := func(db *sql.DB) bool {
		t.Helper()
		rows, err := db.Query("PRAGMA index_list(events)")
		if err != nil {
			t.Fatalf("PRAGMA index_list: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				seq     int
				name    string
				unique  int
				origin  string
				partial int
			)
			if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if name == "idx_events_request_path" {
				return true
			}
		}
		return false
	}

	pre, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen pre: %v", err)
	}
	if verifyIndex(pre) {
		t.Fatal("legacy database unexpectedly already has idx_events_request_path")
	}
	_ = pre.Close()

	sink, err := NewSQLiteSink(path)
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sink.Close() })

	if !verifyIndex(sink.DB()) {
		t.Error("idx_events_request_path was not created on schema apply against a legacy database")
	}
}
