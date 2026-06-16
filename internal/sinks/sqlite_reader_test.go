package sinks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
)

// sqliteRequest builds a CapturedRequest with all NOT NULL fields populated,
// overriding the fields supplied by the caller.
func sqliteRequest(id string, ts time.Time, service, method, path, corrID, sourceIP string) *capture.CapturedRequest {
	r := sampleRecord(id)
	r.Timestamp = ts
	r.Service = service
	r.Method = method
	r.Path = path
	r.CorrelationID = corrID
	r.SourceIP = sourceIP
	return r
}

func writeRequests(t *testing.T, s *SQLiteSink, recs []*capture.CapturedRequest) {
	t.Helper()
	ctx := context.Background()
	for _, r := range recs {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}
}

func TestSQLiteReader_ReadRoots_Empty(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	rows, next, err := s.ReadRoots(context.Background(), inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
	if next != nil {
		t.Errorf("expected nil next cursor, got %v", next)
	}
}

func TestSQLiteReader_ReadRoots_SortOrder(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	recs := []*capture.CapturedRequest{
		sqliteRequest("c", base, "svc", "GET", "/", "c1", "x"),
		sqliteRequest("a", base.Add(time.Second), "svc", "GET", "/", "c2", "x"),
		sqliteRequest("b", base.Add(2*time.Second), "svc", "GET", "/", "c3", "x"),
	}
	writeRequests(t, s, recs)

	rows, _, err := s.ReadRoots(context.Background(), inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// newest first: b, a, c
	want := []string{"b", "a", "c"}
	for i, row := range rows {
		if row.ID != want[i] {
			t.Errorf("rows[%d].ID: got %q want %q", i, row.ID, want[i])
		}
	}
}

func TestSQLiteReader_ReadRoots_LimitAndNextCursor(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	recs := make([]*capture.CapturedRequest, 10)
	for i := range recs {
		recs[i] = sqliteRequest(fmt.Sprintf("r%02d", i), base.Add(time.Duration(i)*time.Second), "svc", "GET", "/", fmt.Sprintf("c%d", i), "x")
	}
	writeRequests(t, s, recs)

	rows, next, err := s.ReadRoots(context.Background(), inspect.InspectQuery{}, 3, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if next == nil {
		t.Fatal("expected next cursor when more rows exist")
	}
	if next.ID != rows[2].ID {
		t.Errorf("next cursor ID: got %q want %q", next.ID, rows[2].ID)
	}
}

func TestSQLiteReader_ReadRoots_CursorPagination(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	const total = 7
	recs := make([]*capture.CapturedRequest, total)
	for i := range recs {
		recs[i] = sqliteRequest(fmt.Sprintf("r%02d", i), base.Add(time.Duration(i)*time.Second), "svc", "GET", "/", fmt.Sprintf("c%d", i), "x")
	}
	writeRequests(t, s, recs)

	var allIDs []string
	var cursor *inspect.Cursor
	for {
		rows, next, err := s.ReadRoots(context.Background(), inspect.InspectQuery{}, 3, cursor)
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

	if len(allIDs) != total {
		t.Fatalf("pagination union: got %d rows want %d", len(allIDs), total)
	}
	seen := make(map[string]struct{})
	for _, id := range allIDs {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate id %q in pagination result", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSQLiteReader_ReadRoots_RowShape(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r := sqliteRequest("id1", ts, "orders", "POST", "/api/orders", "corr-abc", "10.0.0.1")
	writeRequests(t, s, []*capture.CapturedRequest{r})

	rows, _, err := s.ReadRoots(context.Background(), inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]

	if row.Kind != "request" {
		t.Errorf("Kind: got %q want %q", row.Kind, "request")
	}
	if row.Service != "orders" {
		t.Errorf("Service: got %q want %q", row.Service, "orders")
	}
	if row.Method != "POST" {
		t.Errorf("Method: got %q want %q", row.Method, "POST")
	}
	if row.Path != "/api/orders" {
		t.Errorf("Path: got %q want %q", row.Path, "/api/orders")
	}
	if row.CorrelationID != "corr-abc" {
		t.Errorf("CorrelationID: got %q want %q", row.CorrelationID, "corr-abc")
	}
	if row.SourceIP != "10.0.0.1" {
		t.Errorf("SourceIP: got %q want %q", row.SourceIP, "10.0.0.1")
	}
	if row.EventCount == nil || *row.EventCount != 0 {
		t.Errorf("EventCount: expected pointer to 0, got %v", row.EventCount)
	}
	if row.HasEvents == nil || *row.HasEvents {
		t.Error("HasEvents: expected pointer to false")
	}
	if row.Status != nil {
		t.Errorf("Status: expected nil, got %v", *row.Status)
	}
	if !row.Timestamp.Equal(ts) {
		t.Errorf("Timestamp: got %v want %v", row.Timestamp, ts)
	}
}

func TestSQLiteReader_ServicesSeen_AlphabeticalOrder(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	services := []string{"zebra", "alpha", "monkey", "alpha"}
	for i, svc := range services {
		r := sqliteRequest(fmt.Sprintf("id%d", i), ts, svc, "GET", "/", fmt.Sprintf("c%d", i), "x")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	svcs, err := s.ServicesSeen(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ServicesSeen: %v", err)
	}
	want := []string{"alpha", "monkey", "zebra"}
	if len(svcs) != len(want) {
		t.Fatalf("ServicesSeen: got %v want %v", svcs, want)
	}
	for i, svc := range svcs {
		if svc != want[i] {
			t.Errorf("svcs[%d]: got %q want %q", i, svc, want[i])
		}
	}
}

func TestSQLiteReader_ServicesSeen_SinceFilter(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	old := sqliteRequest("old", base.Add(-time.Hour), "old-service", "GET", "/", "c1", "x")
	recent := sqliteRequest("new", base.Add(time.Minute), "new-service", "GET", "/", "c2", "x")
	for _, r := range []*capture.CapturedRequest{old, recent} {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	svcs, err := s.ServicesSeen(ctx, base)
	if err != nil {
		t.Fatalf("ServicesSeen: %v", err)
	}
	if len(svcs) != 1 || svcs[0] != "new-service" {
		t.Errorf("ServicesSeen since filter: got %v want [new-service]", svcs)
	}
}

func TestSQLiteReader_ServiceStats(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	mustWriteSQL(t, ctx, s, sqliteRequest("a1", base, "api", "GET", "/", "c1", "x"))
	mustWriteSQL(t, ctx, s, sqliteRequest("a2", base.Add(time.Second), "api", "GET", "/x", "c2", "x"))
	mustWriteSQL(t, ctx, s, &capture.ResponseEvent{ID: "ae1", Timestamp: base.Add(2 * time.Second), CorrelationID: "c1", Service: "api", Status: 200, Headers: map[string][]string{}, Body: []byte{}})
	mustWriteSQL(t, ctx, s, &capture.ResponseEvent{ID: "ae2", Timestamp: base.Add(3 * time.Second), CorrelationID: "c2", Service: "api", Status: 404, Headers: map[string][]string{}, Body: []byte{}})
	mustWriteSQL(t, ctx, s, sqliteRequest("w1", base.Add(4*time.Second), "web", "GET", "/", "c3", "x"))

	stats, err := s.ServiceStats(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ServiceStats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("ServiceStats: got %d want 2 (%+v)", len(stats), stats)
	}
	api, web := stats[0], stats[1]
	if api.Name != "api" || web.Name != "web" {
		t.Fatalf("ServiceStats order: got %q,%q want api,web", api.Name, web.Name)
	}
	if api.Requests != 2 {
		t.Errorf("api.Requests: got %d want 2", api.Requests)
	}
	if api.S2xx != 1 || api.S4xx != 1 || api.S3xx != 0 || api.S5xx != 0 || api.Other != 0 {
		t.Errorf("api status mix: got 2xx=%d 3xx=%d 4xx=%d 5xx=%d other=%d want 2xx=1 4xx=1",
			api.S2xx, api.S3xx, api.S4xx, api.S5xx, api.Other)
	}
	if !api.LastSeen.Equal(base.Add(3 * time.Second)) {
		t.Errorf("api.LastSeen: got %v want %v", api.LastSeen, base.Add(3*time.Second))
	}
	if web.Requests != 1 || web.S2xx+web.S3xx+web.S4xx+web.S5xx+web.Other != 0 {
		t.Errorf("web stats: got requests=%d responses=%d want 1,0",
			web.Requests, web.S2xx+web.S3xx+web.S4xx+web.S5xx+web.Other)
	}
}

func TestSQLiteReader_ServiceStats_SinceFilter(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	mustWriteSQL(t, ctx, s, sqliteRequest("old", base.Add(-time.Hour), "old-service", "GET", "/", "c1", "x"))
	mustWriteSQL(t, ctx, s, sqliteRequest("new", base.Add(time.Minute), "new-service", "GET", "/", "c2", "x"))

	stats, err := s.ServiceStats(ctx, base)
	if err != nil {
		t.Fatalf("ServiceStats: %v", err)
	}
	if len(stats) != 1 || stats[0].Name != "new-service" {
		t.Fatalf("ServiceStats since filter: got %+v want [new-service]", stats)
	}
}

func mustWriteSQL(t *testing.T, ctx context.Context, s *SQLiteSink, r capture.Record) {
	t.Helper()
	if err := s.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func TestSQLiteReader_ReadDetail_NotFound(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	_, err := s.ReadDetail(context.Background(), "missing-id")
	if !errors.Is(err, inspect.ErrNotFound) {
		t.Errorf("ReadDetail: got %v want ErrNotFound", err)
	}
}

func TestSQLiteReader_ReadDetail_CapturedRequest_NoSiblings(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	r := sqliteRequest("req-1", ts, "svc", "GET", "/", "corr-1", "1.2.3.4")
	if err := s.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	detail, err := s.ReadDetail(ctx, "req-1")
	if err != nil {
		t.Fatalf("ReadDetail: %v", err)
	}
	cr, ok := detail.Root.(*capture.CapturedRequest)
	if !ok {
		t.Fatalf("Root is %T, want *capture.CapturedRequest", detail.Root)
	}
	if cr.ID != "req-1" {
		t.Errorf("Root.ID: got %q want req-1", cr.ID)
	}
	if len(detail.Events) != 0 {
		t.Errorf("Events: got %d want 0", len(detail.Events))
	}
}

func TestSQLiteReader_ReadDetail_CapturedRequest_WithResponseEvent(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := sqliteRequest("req-1", base, "svc", "GET", "/", "corr-1", "1.2.3.4")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}
	ev := &capture.ResponseEvent{
		ID:            "evt-1",
		Timestamp:     base.Add(time.Second),
		CorrelationID: "corr-1",
		Service:       "svc",
		ServiceSource: "app",
		Status:        200,
		Headers:       map[string][]string{},
		Body:          []byte{},
	}
	if err := s.Write(ctx, ev); err != nil {
		t.Fatalf("Write ev: %v", err)
	}

	detail, err := s.ReadDetail(ctx, "req-1")
	if err != nil {
		t.Fatalf("ReadDetail: %v", err)
	}
	if len(detail.Events) != 1 {
		t.Fatalf("Events: got %d want 1", len(detail.Events))
	}
	sibling, ok := detail.Events[0].(*capture.ResponseEvent)
	if !ok {
		t.Fatalf("Events[0] is %T, want *capture.ResponseEvent", detail.Events[0])
	}
	if sibling.ID != "evt-1" {
		t.Errorf("Events[0].ID: got %q want evt-1", sibling.ID)
	}
	if sibling.Status != 200 {
		t.Errorf("Events[0].Status: got %d want 200", sibling.Status)
	}
}

func TestSQLiteReader_ReadDetail_EventRoot(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := sqliteRequest("req-1", base, "svc", "GET", "/", "corr-1", "1.2.3.4")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}
	ev := &capture.ResponseEvent{
		ID:            "evt-1",
		Timestamp:     base.Add(time.Second),
		CorrelationID: "corr-1",
		Service:       "svc",
		ServiceSource: "app",
		Status:        201,
		Headers:       map[string][]string{},
		Body:          []byte{},
	}
	if err := s.Write(ctx, ev); err != nil {
		t.Fatalf("Write ev: %v", err)
	}

	detail, err := s.ReadDetail(ctx, "evt-1")
	if err != nil {
		t.Fatalf("ReadDetail: %v", err)
	}
	re, ok := detail.Root.(*capture.ResponseEvent)
	if !ok {
		t.Fatalf("Root is %T, want *capture.ResponseEvent", detail.Root)
	}
	if re.ID != "evt-1" {
		t.Errorf("Root.ID: got %q want evt-1", re.ID)
	}
	if re.Status != 201 {
		t.Errorf("Root.Status: got %d want 201", re.Status)
	}
	if len(detail.Events) != 1 {
		t.Fatalf("Events: got %d want 1", len(detail.Events))
	}
	sibling, ok := detail.Events[0].(*capture.CapturedRequest)
	if !ok {
		t.Fatalf("Events[0] is %T, want *capture.CapturedRequest", detail.Events[0])
	}
	if sibling.ID != "req-1" {
		t.Errorf("Events[0].ID: got %q want req-1", sibling.ID)
	}
}

func TestSQLiteReader_ReadDetail_OutboundEvent(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	ev := &capture.OutboundEvent{
		ID:            "out-1",
		Timestamp:     base,
		CorrelationID: "corr-2",
		Service:       "svc",
		ServiceSource: "app",
		DurationMS:    42,
		Request: capture.OutboundRequestHalf{
			Method:  "POST",
			Path:    "/payments",
			Headers: map[string][]string{},
			Body:    []byte("{}"),
		},
		Response: &capture.OutboundResponseHalf{
			Status:  201,
			Headers: map[string][]string{},
			Body:    []byte("ok"),
		},
	}
	if err := s.Write(ctx, ev); err != nil {
		t.Fatalf("Write ev: %v", err)
	}

	detail, err := s.ReadDetail(ctx, "out-1")
	if err != nil {
		t.Fatalf("ReadDetail: %v", err)
	}
	oe, ok := detail.Root.(*capture.OutboundEvent)
	if !ok {
		t.Fatalf("Root is %T, want *capture.OutboundEvent", detail.Root)
	}
	if oe.ID != "out-1" {
		t.Errorf("Root.ID: got %q want out-1", oe.ID)
	}
	if oe.Request.Method != "POST" {
		t.Errorf("Root.Request.Method: got %q want POST", oe.Request.Method)
	}
	if oe.Response == nil {
		t.Fatal("Root.Response is nil")
	}
	if oe.Response.Status != 201 {
		t.Errorf("Root.Response.Status: got %d want 201", oe.Response.Status)
	}
	if len(detail.Events) != 0 {
		t.Errorf("Events: got %d want 0", len(detail.Events))
	}
}

func TestSQLiteReader_AggregateRoots_StatusBuckets(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Three requests with paired response events at different status classes.
	cases := []struct {
		reqID, evID, corrID string
		status              int
		offset              time.Duration
	}{
		{"r-2xx", "e-2xx", "c-2xx", 201, 0},
		{"r-4xx", "e-4xx", "c-4xx", 404, time.Second},
		{"r-5xx", "e-5xx", "c-5xx", 503, 2 * time.Second},
	}
	for _, c := range cases {
		req := sqliteRequest(c.reqID, base.Add(c.offset), "svc", "GET", "/", c.corrID, "x")
		if err := s.Write(ctx, req); err != nil {
			t.Fatalf("Write req %s: %v", c.reqID, err)
		}
		ev := &capture.ResponseEvent{
			ID:            c.evID,
			Timestamp:     base.Add(c.offset).Add(100 * time.Millisecond),
			CorrelationID: c.corrID,
			Service:       "svc",
			ServiceSource: "app",
			Status:        c.status,
			Headers:       map[string][]string{},
			Body:          []byte{},
		}
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write ev %s: %v", c.evID, err)
		}
	}

	since := base.Add(-time.Second)
	until := base.Add(10 * time.Second)
	q := inspect.InspectQuery{Since: &since, Until: &until}

	agg, err := s.AggregateRoots(ctx, q, 4)
	if err != nil {
		t.Fatalf("AggregateRoots: %v", err)
	}
	if agg.Total != 3 {
		t.Errorf("Total: got %d want 3", agg.Total)
	}
	if len(agg.Buckets) != 4 {
		t.Fatalf("Buckets: got %d want 4", len(agg.Buckets))
	}
	var s2, s4, s5 int
	for _, b := range agg.Buckets {
		s2 += b.S2xx
		s4 += b.S4xx
		s5 += b.S5xx
	}
	if s2 != 1 || s4 != 1 || s5 != 1 {
		t.Errorf("status class totals: 2xx=%d 4xx=%d 5xx=%d, want 1 each", s2, s4, s5)
	}
}

func TestSQLiteReader_AggregateRoots_CountOnlyWhenRangeMissing(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		req := sqliteRequest(fmt.Sprintf("r%d", i), base.Add(time.Duration(i)*time.Second), "svc", "GET", "/", fmt.Sprintf("c%d", i), "x")
		if err := s.Write(ctx, req); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	agg, err := s.AggregateRoots(ctx, inspect.InspectQuery{}, 5)
	if err != nil {
		t.Fatalf("AggregateRoots: %v", err)
	}
	if agg.Total != 4 {
		t.Errorf("Total: got %d want 4", agg.Total)
	}
	if len(agg.Buckets) != 0 {
		t.Errorf("Buckets: expected empty without since/until, got %d", len(agg.Buckets))
	}
}

func TestSQLiteReader_AggregateRoots_IncludesOrphans(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := sqliteRequest("req-1", base, "svc", "GET", "/", "corr-req", "x")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}
	orphan := orphanResponseEvent("orph-1", "corr-orphan", "svc", 503, base.Add(time.Second))
	if err := s.Write(ctx, orphan); err != nil {
		t.Fatalf("Write orphan: %v", err)
	}

	since := base.Add(-time.Second)
	until := base.Add(5 * time.Second)
	q := inspect.InspectQuery{Since: &since, Until: &until}

	agg, err := s.AggregateRoots(ctx, q, 4)
	if err != nil {
		t.Fatalf("AggregateRoots: %v", err)
	}
	if agg.Total != 2 {
		t.Errorf("Total: got %d want 2 (request + orphan)", agg.Total)
	}
	var s5 int
	for _, b := range agg.Buckets {
		s5 += b.S5xx
	}
	if s5 != 1 {
		t.Errorf("5xx total: got %d want 1 (from orphan response)", s5)
	}
}

// orphanResponseEvent returns a ResponseEvent with no matching captured request
// (an orphan). Used in orphan-detection tests.
func orphanResponseEvent(id, corrID, service string, status int, ts time.Time) *capture.ResponseEvent {
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

// orphanOutboundEvent returns an OutboundEvent with no matching captured request.
func orphanOutboundEvent(id, corrID, service string, ts time.Time) *capture.OutboundEvent {
	return &capture.OutboundEvent{
		ID:            id,
		Timestamp:     ts,
		CorrelationID: corrID,
		Service:       service,
		ServiceSource: "explicit",
		DurationMS:    1,
		Request: capture.OutboundRequestHalf{
			Method:  "GET",
			Path:    "/out",
			Headers: map[string][]string{},
		},
	}
}

func TestSQLiteReader_OrphanRows_AppearInReadRoots(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write a captured request with a correlated event — not an orphan.
	req := sqliteRequest("req-1", base, "svc", "GET", "/", "corr-has-req", "x")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}
	correlated := orphanResponseEvent("ev-correlated", "corr-has-req", "svc", 200, base.Add(time.Second))
	if err := s.Write(ctx, correlated); err != nil {
		t.Fatalf("Write correlated: %v", err)
	}

	// Write two orphan events: one response, one outbound.
	orphResp := orphanResponseEvent("ev-orphan-resp", "corr-orphan-1", "svc", 503, base.Add(2*time.Second))
	orphOut := orphanOutboundEvent("ev-orphan-out", "corr-orphan-2", "svc", base.Add(3*time.Second))
	for _, ev := range []capture.Record{orphResp, orphOut} {
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write orphan: %v", err)
		}
	}

	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}

	byID := make(map[string]inspect.RootRow)
	for _, r := range rows {
		byID[r.ID] = r
	}

	// Correlated event must NOT appear as orphan — only the request row is in the list.
	if _, found := byID["ev-correlated"]; found {
		t.Error("correlated event should not appear as orphan row")
	}

	// Request row must have kind=request.
	if r, ok := byID["req-1"]; !ok || r.Kind != "request" {
		t.Errorf("req-1 not found or wrong kind: %v", byID["req-1"])
	}

	// Orphan response must appear with kind=orphan_response.
	if r, ok := byID["ev-orphan-resp"]; !ok {
		t.Error("orphan response not found in ReadRoots")
	} else {
		if r.Kind != "orphan_response" {
			t.Errorf("orphan response kind: got %q want orphan_response", r.Kind)
		}
		if r.Status == nil || *r.Status != 503 {
			t.Errorf("orphan response status: got %v want 503", r.Status)
		}
		if r.EventCount != nil {
			t.Errorf("orphan response event_count should be nil, got %v", r.EventCount)
		}
		if r.HasEvents != nil {
			t.Errorf("orphan response has_events should be nil, got %v", r.HasEvents)
		}
	}

	// Orphan outbound must appear with kind=orphan_outbound.
	if r, ok := byID["ev-orphan-out"]; !ok {
		t.Error("orphan outbound not found in ReadRoots")
	} else if r.Kind != "orphan_outbound" {
		t.Errorf("orphan outbound kind: got %q want orphan_outbound", r.Kind)
	}
}

func TestSQLiteReader_OrphanReconciliation(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write an orphan response event.
	orphResp := orphanResponseEvent("ev-orphan", "corr-late", "svc", 500, base)
	if err := s.Write(ctx, orphResp); err != nil {
		t.Fatalf("Write orphan: %v", err)
	}

	// Before reconciliation: should appear as orphan_response.
	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots before reconciliation: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.ID == "ev-orphan" && r.Kind == "orphan_response" {
			found = true
		}
	}
	if !found {
		t.Fatal("orphan not found before reconciliation")
	}

	// Now write the matching captured request (late arrival).
	req := sqliteRequest("req-late", base.Add(time.Second), "svc", "POST", "/", "corr-late", "x")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write reconciling request: %v", err)
	}

	// After reconciliation: orphan row must be gone; request row must appear with event_count=1.
	rows2, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots after reconciliation: %v", err)
	}
	for _, r := range rows2 {
		if r.ID == "ev-orphan" {
			t.Errorf("orphan ev-orphan still present after reconciliation (kind=%s)", r.Kind)
		}
	}
	var reqRow *inspect.RootRow
	for _, r := range rows2 {
		if r.ID == "req-late" {
			rc := r
			reqRow = &rc
			break
		}
	}
	if reqRow == nil {
		t.Fatal("reconciled request not found in ReadRoots after reconciliation")
	}
	if reqRow.EventCount == nil || *reqRow.EventCount != 1 {
		t.Errorf("reconciled request event_count: got %v want 1", reqRow.EventCount)
	}
}

func TestSQLiteReader_OrphanFilters(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	orphResp := orphanResponseEvent("ev-resp", "corr-orphan", "my-svc", 500, base)
	orphOut := orphanOutboundEvent("ev-out", "corr-orphan-out", "my-svc", base.Add(time.Second))
	for _, ev := range []capture.Record{orphResp, orphOut} {
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// service filter applies to orphans on their own fields.
	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{Query: mustParseQuery(t, "service:my-svc")}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots service filter: %v", err)
	}
	ids := make(map[string]struct{})
	for _, r := range rows {
		ids[r.ID] = struct{}{}
	}
	if _, ok := ids["ev-resp"]; !ok {
		t.Error("service filter: orphan response not returned")
	}

	// status filter applies to orphan_response on event's own status.
	rows5xx, _, err := s.ReadRoots(ctx, inspect.InspectQuery{Query: mustParseQuery(t, "status:5xx")}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots status filter: %v", err)
	}
	found5xx := false
	for _, r := range rows5xx {
		if r.ID == "ev-resp" {
			found5xx = true
		}
	}
	if !found5xx {
		t.Error("5xx status filter: orphan response not returned")
	}

	// method filter excludes orphan rows.
	rowsMethod, _, err := s.ReadRoots(ctx, inspect.InspectQuery{Query: mustParseQuery(t, "method:GET")}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots method filter: %v", err)
	}
	for _, r := range rowsMethod {
		if r.Kind == "orphan_response" || r.Kind == "orphan_outbound" {
			t.Errorf("method filter: orphan row should not appear; got kind=%s id=%s", r.Kind, r.ID)
		}
	}
}

func TestSQLiteOrphan_ExplainQueryPlan(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Insert a small number of events without matching requests so the planner
	// has real data to work with.
	for i := range 3 {
		ev := orphanResponseEvent(fmt.Sprintf("ev%d", i), fmt.Sprintf("corr%d", i), "svc", 200, base.Add(time.Duration(i)*time.Second))
		if err := s.Write(ctx, ev); err != nil {
			t.Fatalf("Write ev%d: %v", i, err)
		}
	}

	// Run EXPLAIN QUERY PLAN on the orphan LEFT JOIN. The planner must use the
	// idx_events_correlation_id index on events to satisfy the LEFT JOIN probe
	// against captured_requests. We look for "correlation_id" in the plan output.
	rows, err := s.DB().QueryContext(ctx, `EXPLAIN QUERY PLAN
SELECT e.id
FROM events e
LEFT JOIN captured_requests cr ON cr.correlation_id = e.correlation_id
WHERE cr.id IS NULL`)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var planLines []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		planLines = append(planLines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}

	// The plan must reference the correlation_id index somewhere — either
	// idx_captured_requests_correlation_id or idx_events_correlation_id.
	foundCorrelationIdx := false
	for _, line := range planLines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "correlation_id") {
			foundCorrelationIdx = true
			break
		}
	}
	if !foundCorrelationIdx {
		t.Errorf("EXPLAIN QUERY PLAN does not reference a correlation_id index; plan:\n%s",
			strings.Join(planLines, "\n"))
	}
}

func TestSQLiteReader_ReadDetail_SiblingsOrderedByTimestampASC(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := sqliteRequest("req-1", base, "svc", "GET", "/", "corr-1", "x")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}
	ev2 := &capture.ResponseEvent{
		ID:            "evt-2",
		Timestamp:     base.Add(3 * time.Second),
		CorrelationID: "corr-1",
		Service:       "svc",
		ServiceSource: "app",
		Headers:       map[string][]string{},
		Body:          []byte{},
	}
	ev1 := &capture.ResponseEvent{
		ID:            "evt-1",
		Timestamp:     base.Add(time.Second),
		CorrelationID: "corr-1",
		Service:       "svc",
		ServiceSource: "app",
		Headers:       map[string][]string{},
		Body:          []byte{},
	}
	if err := s.Write(ctx, ev2); err != nil {
		t.Fatalf("Write ev2: %v", err)
	}
	if err := s.Write(ctx, ev1); err != nil {
		t.Fatalf("Write ev1: %v", err)
	}

	detail, err := s.ReadDetail(ctx, "req-1")
	if err != nil {
		t.Fatalf("ReadDetail: %v", err)
	}
	if len(detail.Events) != 2 {
		t.Fatalf("Events: got %d want 2", len(detail.Events))
	}
	ids := []string{
		detail.Events[0].(capture.Record).RecordID(),
		detail.Events[1].(capture.Record).RecordID(),
	}
	if ids[0] != "evt-1" || ids[1] != "evt-2" {
		t.Errorf("events order: got %v want [evt-1 evt-2]", ids)
	}
}
