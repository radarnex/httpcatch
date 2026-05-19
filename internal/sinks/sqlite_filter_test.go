package sinks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
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

	q := inspect.InspectQuery{Service: "orders"}
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

	q := inspect.InspectQuery{Method: "GET"}
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

	q := inspect.InspectQuery{Status: &inspect.StatusFilter{Exact: 200}}
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

	q := inspect.InspectQuery{Status: &inspect.StatusFilter{Class: "5xx"}}
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

	q := inspect.InspectQuery{Status: &inspect.StatusFilter{Class: "2xx"}}
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

	q := inspect.InspectQuery{Path: "/api/orders"}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("path=/api/orders: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r3" {
			t.Errorf("path prefix: unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_CorrelationID(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	q := inspect.InspectQuery{CorrelationID: "c2"}
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

	q := inspect.InspectQuery{SourceIP: "10.0.0.1"}
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

func TestSQLiteFilter_HasEvents_True(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	v := true
	q := inspect.InspectQuery{HasEvents: &v}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	// r1 and r2 have events; r3 does not.
	if len(ids) != 2 {
		t.Fatalf("has_events=true: got %v want 2", ids)
	}
	for _, id := range ids {
		if id != "r1" && id != "r2" {
			t.Errorf("has_events=true: unexpected id %q", id)
		}
	}
}

func TestSQLiteFilter_HasEvents_False(t *testing.T) {
	t.Parallel()

	s, _ := sqliteFilterTestSetup(t)
	ctx := context.Background()

	v := false
	q := inspect.InspectQuery{HasEvents: &v}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	// Only r3 has no events.
	if len(ids) != 1 || ids[0] != "r3" {
		t.Errorf("has_events=false: got %v want [r3]", ids)
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

	q := inspect.InspectQuery{Method: "POST", Path: "/api/orders"}
	rows, _, err := s.ReadRoots(ctx, q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	ids := rowIDs(rows)
	if len(ids) != 2 {
		t.Fatalf("POST+/api/orders: got %v want 2", ids)
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

	q := inspect.InspectQuery{Service: "orders", Status: &inspect.StatusFilter{Class: "5xx"}}
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
	q := inspect.InspectQuery{Service: "orders", Method: "GET"}
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
	q := inspect.InspectQuery{Service: "orders"}
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
