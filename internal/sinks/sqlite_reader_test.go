package sinks

import (
	"context"
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

func TestSQLiteReader_ReadDetail_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	_, err := s.ReadDetail(context.Background(), "any-id")
	if err != inspect.ErrNotImplemented {
		t.Errorf("ReadDetail: got %v want ErrNotImplemented", err)
	}
}
