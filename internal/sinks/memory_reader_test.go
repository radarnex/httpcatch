package sinks

import (
	"context"
	"errors"
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
	if row.EventCount == nil || *row.EventCount != 0 {
		t.Errorf("EventCount: expected pointer to 0, got %v", row.EventCount)
	}
	if row.HasEvents == nil || *row.HasEvents {
		t.Error("HasEvents: expected pointer to false")
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

func TestMemoryReader_ReadDetail_NotFound(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	_, err := s.ReadDetail(context.Background(), "missing-id")
	if !errors.Is(err, inspect.ErrNotFound) {
		t.Errorf("ReadDetail: got %v want ErrNotFound", err)
	}
}

func TestMemoryReader_ReadDetail_CapturedRequest_NoSiblings(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	ctx := context.Background()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	r := makeRequest("req-1", ts, "svc", "GET", "/", "corr-1", "1.2.3.4")
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

func TestMemoryReader_ReadDetail_CapturedRequest_WithSiblings(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := makeRequest("req-1", base, "svc", "GET", "/", "corr-1", "1.2.3.4")
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

	// Unrelated record with different correlation.
	other := makeRequest("req-other", base.Add(2*time.Second), "svc", "POST", "/x", "corr-other", "x")
	if err := s.Write(ctx, other); err != nil {
		t.Fatalf("Write other: %v", err)
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
}

func TestMemoryReader_ReadDetail_EventRoot(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := makeRequest("req-1", base, "svc", "GET", "/", "corr-1", "1.2.3.4")
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

	// Resolve by event id.
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
	// Sibling is the captured request.
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

func TestMemoryReader_ReadDetail_SiblingsOrderedByTimestampASC(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := makeRequest("req-1", base, "svc", "GET", "/", "corr-1", "x")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}
	// Write two events out of order.
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

func TestMemoryReader_OrphanRows_AppearInReadRoots(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write a captured request with a correlated event (not an orphan).
	req := makeRequest("req-1", base, "svc", "GET", "/", "corr-1", "x")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}
	correlated := &capture.ResponseEvent{
		ID: "ev-correlated", Timestamp: base.Add(time.Second),
		CorrelationID: "corr-1", Service: "svc", ServiceSource: "x",
		Status: 200, Headers: map[string][]string{}, Body: []byte{},
	}
	if err := s.Write(ctx, correlated); err != nil {
		t.Fatalf("Write correlated: %v", err)
	}

	// Write two orphan events.
	orphResp := &capture.ResponseEvent{
		ID: "ev-orphan-resp", Timestamp: base.Add(2 * time.Second),
		CorrelationID: "corr-orphan-1", Service: "svc", ServiceSource: "x",
		Status: 503, Headers: map[string][]string{}, Body: []byte{},
	}
	orphOut := &capture.OutboundEvent{
		ID: "ev-orphan-out", Timestamp: base.Add(3 * time.Second),
		CorrelationID: "corr-orphan-2", Service: "svc", ServiceSource: "x",
		DurationMS: 1,
		Request:    capture.OutboundRequestHalf{Method: "GET", Path: "/", Headers: map[string][]string{}},
	}
	if err := s.Write(ctx, orphResp); err != nil {
		t.Fatalf("Write orphResp: %v", err)
	}
	if err := s.Write(ctx, orphOut); err != nil {
		t.Fatalf("Write orphOut: %v", err)
	}

	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}

	byID := make(map[string]inspect.RootRow)
	for _, r := range rows {
		byID[r.ID] = r
	}

	// Correlated event must NOT appear as orphan.
	if _, found := byID["ev-correlated"]; found {
		t.Error("correlated event should not appear as orphan row")
	}

	// Orphan response must appear with kind=orphan_response.
	if r, ok := byID["ev-orphan-resp"]; !ok {
		t.Error("orphan response not found")
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
		t.Error("orphan outbound not found")
	} else if r.Kind != "orphan_outbound" {
		t.Errorf("orphan outbound kind: got %q want orphan_outbound", r.Kind)
	}
}

func TestMemoryReader_OrphanReconciliation(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write orphan event first.
	orphResp := &capture.ResponseEvent{
		ID: "ev-orphan", Timestamp: base,
		CorrelationID: "corr-late", Service: "svc", ServiceSource: "x",
		Status: 500, Headers: map[string][]string{}, Body: []byte{},
	}
	if err := s.Write(ctx, orphResp); err != nil {
		t.Fatalf("Write orphan: %v", err)
	}

	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots before reconciliation: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == "ev-orphan" && r.Kind == "orphan_response" {
			found = true
		}
	}
	if !found {
		t.Fatal("orphan not found before reconciliation")
	}

	// Late-arriving request with same correlation_id reconciles the orphan.
	req := makeRequest("req-late", base.Add(time.Second), "svc", "GET", "/", "corr-late", "x")
	if err := s.Write(ctx, req); err != nil {
		t.Fatalf("Write request: %v", err)
	}

	rows2, _, err := s.ReadRoots(ctx, inspect.InspectQuery{}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots after reconciliation: %v", err)
	}
	for _, r := range rows2 {
		if r.ID == "ev-orphan" {
			t.Errorf("orphan still present after reconciliation (kind=%s)", r.Kind)
		}
	}
}

func TestMemoryReader_OrphanFilters_MethodExcludesOrphans(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	orphResp := &capture.ResponseEvent{
		ID: "ev-orphan", Timestamp: base,
		CorrelationID: "corr-orphan", Service: "svc", ServiceSource: "x",
		Status: 200, Headers: map[string][]string{}, Body: []byte{},
	}
	if err := s.Write(ctx, orphResp); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// method filter: orphans must not appear when method is set.
	rows, _, err := s.ReadRoots(ctx, inspect.InspectQuery{Method: "GET"}, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	for _, r := range rows {
		if r.Kind == "orphan_response" || r.Kind == "orphan_outbound" {
			t.Errorf("method filter: orphan row should not appear; kind=%s id=%s", r.Kind, r.ID)
		}
	}
}

func TestMemoryOrphanCounts(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(20)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// No orphans yet.
	resp0, out0 := s.OrphanCounts()
	if resp0 != 0 || out0 != 0 {
		t.Errorf("empty: got resp=%d out=%d want 0 0", resp0, out0)
	}

	// Add one orphan response and one orphan outbound.
	if err := s.Write(ctx, &capture.ResponseEvent{
		ID: "ov-r", Timestamp: base, CorrelationID: "co1",
		Service: "s", ServiceSource: "x", Status: 500,
		Headers: map[string][]string{}, Body: []byte{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, &capture.OutboundEvent{
		ID: "ov-o", Timestamp: base.Add(time.Second), CorrelationID: "co2",
		Service: "s", ServiceSource: "x", DurationMS: 1,
		Request: capture.OutboundRequestHalf{Method: "GET", Path: "/", Headers: map[string][]string{}},
	}); err != nil {
		t.Fatal(err)
	}

	resp1, out1 := s.OrphanCounts()
	if resp1 != 1 || out1 != 1 {
		t.Errorf("orphan counts: got resp=%d out=%d want 1 1", resp1, out1)
	}

	// Reconcile the response orphan by adding a matching request.
	if err := s.Write(ctx, makeRequest("req-r", base.Add(2*time.Second), "s", "GET", "/", "co1", "x")); err != nil {
		t.Fatal(err)
	}
	resp2, out2 := s.OrphanCounts()
	if resp2 != 0 || out2 != 1 {
		t.Errorf("after reconcile: got resp=%d out=%d want 0 1", resp2, out2)
	}
}
