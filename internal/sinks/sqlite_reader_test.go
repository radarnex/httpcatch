package sinks

import (
	"context"
	"errors"
	"fmt"
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
	if row.EventCount != 0 {
		t.Errorf("EventCount: got %d want 0", row.EventCount)
	}
	if row.HasEvents {
		t.Error("HasEvents: expected false")
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
