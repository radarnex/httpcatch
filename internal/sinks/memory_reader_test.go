package sinks

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
)

func makeRequest(id string, ts time.Time, service, method, path, corrID, sourceIP string) *capture.CapturedRequest {
	return &capture.CapturedRequest{
		ID:            id,
		Timestamp:     ts,
		Service:       service,
		Method:        method,
		Path:          path,
		CorrelationID: corrID,
		SourceIP:      sourceIP,
	}
}

func TestMemoryReader_ReadRoots_Empty(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
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

func TestMemoryReader_ReadRoots_SortOrder(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	ids := []string{"c", "a", "b"}
	for i, id := range ids {
		r := makeRequest(id, base.Add(time.Duration(i)*time.Second), "svc", "GET", "/", "corr", "1.2.3.4")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", id, err)
		}
	}

	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// Newest first: b (i=2), a (i=1), c (i=0)
	want := []string{"b", "a", "c"}
	for i, row := range rows {
		if row.ID != want[i] {
			t.Errorf("rows[%d].ID: got %q want %q", i, row.ID, want[i])
		}
	}
}

func TestMemoryReader_ReadRoots_LimitAndNextCursor(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	for i := range 10 {
		id := fmt.Sprintf("r%02d", i)
		r := makeRequest(id, base.Add(time.Duration(i)*time.Second), "svc", "GET", "/", "corr", "1.2.3.4")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	rows, next, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 3, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if next == nil {
		t.Fatal("expected next cursor when more rows exist")
	}
	// Last row in page is rows[2]; its cursor encodes that position.
	if next.ID != rows[2].ID {
		t.Errorf("next cursor ID: got %q want %q", next.ID, rows[2].ID)
	}
}

func TestMemoryReader_ReadRoots_CursorPagination(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	const total = 7
	for i := range total {
		id := fmt.Sprintf("r%02d", i)
		r := makeRequest(id, base.Add(time.Duration(i)*time.Second), "svc", "GET", "/", "corr", "1.2.3.4")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Paginate through all rows 3 at a time.
	var allIDs []string
	var cursor *inspect.Cursor
	for {
		rows, next, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 3, cursor)
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
	// IDs should be unique.
	seen := make(map[string]struct{})
	for _, id := range allIDs {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate id %q in pagination result", id)
		}
		seen[id] = struct{}{}
	}
}

func TestMemoryReader_ReadRoots_RowShape(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	ctx := context.Background()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r := makeRequest("id1", ts, "orders", "POST", "/api/orders", "corr-abc", "10.0.0.1")
	if err := s.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]

	if row.ID != "id1" {
		t.Errorf("ID: got %q want %q", row.ID, "id1")
	}
	if row.Kind != "request" {
		t.Errorf("Kind: got %q want %q", row.Kind, "request")
	}
	if !row.Timestamp.Equal(ts) {
		t.Errorf("Timestamp: got %v want %v", row.Timestamp, ts)
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
}

func TestMemoryReader_ServicesSeen_AlphabeticalOrder(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	services := []string{"zebra", "alpha", "monkey", "alpha"} // alpha duplicated
	for i, svc := range services {
		r := makeRequest(fmt.Sprintf("id%d", i), ts.Add(time.Duration(i)*time.Second), svc, "GET", "/", "corr", "1.2.3.4")
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

func TestMemoryReader_ServicesSeen_SinceFilter(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	old := makeRequest("old", base.Add(-time.Hour), "old-service", "GET", "/", "c1", "1.2.3.4")
	recent := makeRequest("new", base.Add(time.Minute), "new-service", "GET", "/", "c2", "1.2.3.4")
	for _, r := range []capture.Record{old, recent} {
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

func TestMemoryReader_ReadRoots_NoNextCursorWhenExhausted(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	ctx := context.Background()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	for i := range 3 {
		r := makeRequest(fmt.Sprintf("r%d", i), ts.Add(time.Duration(i)*time.Second), "svc", "GET", "/", "c", "x")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Request more rows than exist.
	rows, next, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(rows))
	}
	if next != nil {
		t.Errorf("expected nil next cursor, got %v", next)
	}
}

func TestMemoryReader_ReadDetail_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	_, err := s.ReadDetail(context.Background(), "any-id")
	if err != inspect.ErrNotImplemented {
		t.Errorf("ReadDetail: got %v want ErrNotImplemented", err)
	}
}

func TestMemoryReader_SortStability_SameTimestamp(t *testing.T) {
	t.Parallel()

	// When timestamps are equal, order is by id DESC.
	s := NewMemorySink(10)
	ctx := context.Background()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	for _, id := range []string{"aaa", "zzz", "mmm"} {
		r := makeRequest(id, ts, "svc", "GET", "/", "c", "x")
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	// Must be sorted DESC by ID when timestamps tie.
	if !sort.StringsAreSorted(reverseStrings(ids)) {
		t.Errorf("expected id DESC sort for tied timestamps, got %v", ids)
	}
}

func reverseStrings(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[len(ss)-1-i] = s
	}
	return out
}
