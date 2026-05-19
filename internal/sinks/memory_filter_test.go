package sinks

import (
	"context"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
)

// writeAll writes a slice of CapturedRequests to the sink.
func writeAll(t *testing.T, s *MemorySink, reqs ...*capture.CapturedRequest) {
	t.Helper()
	ctx := context.Background()
	for _, r := range reqs {
		if err := s.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}
}

func TestMemoryFilter_Since(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := makeRequest("r1", base.Add(-time.Hour), "svc", "GET", "/", "c1", "x")
	r2 := makeRequest("r2", base, "svc", "GET", "/", "c2", "x")
	r3 := makeRequest("r3", base.Add(time.Hour), "svc", "GET", "/", "c3", "x")
	writeAll(t, s, r1, r2, r3)

	since := base
	q := inspect.InspectQuery{Since: &since}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	// r2 and r3 are at or after base; r1 is before.
	if len(rows) != 2 {
		t.Fatalf("since filter: got %d rows want 2", len(rows))
	}
	for _, row := range rows {
		if row.ID != "r2" && row.ID != "r3" {
			t.Errorf("since filter: unexpected id %q", row.ID)
		}
	}
}

func TestMemoryFilter_Until(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := makeRequest("r1", base.Add(-time.Hour), "svc", "GET", "/", "c1", "x")
	r2 := makeRequest("r2", base, "svc", "GET", "/", "c2", "x")
	r3 := makeRequest("r3", base.Add(time.Hour), "svc", "GET", "/", "c3", "x")
	writeAll(t, s, r1, r2, r3)

	// strictly before base means only r1
	until := base
	q := inspect.InspectQuery{Until: &until}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "r1" {
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		t.Errorf("until filter: got %v want [r1]", ids)
	}
}

func TestMemoryFilter_SinceAndUntil(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	r1 := makeRequest("r1", base.Add(-time.Hour), "svc", "GET", "/", "c1", "x")
	r2 := makeRequest("r2", base, "svc", "GET", "/", "c2", "x")
	r3 := makeRequest("r3", base.Add(time.Hour), "svc", "GET", "/", "c3", "x")
	writeAll(t, s, r1, r2, r3)

	// half-open [base, base+1h) → only r2
	since := base
	until := base.Add(time.Hour)
	q := inspect.InspectQuery{Since: &since, Until: &until}
	rows, _, err := s.ReadRoots(context.Background(), q, 50, nil)
	if err != nil {
		t.Fatalf("ReadRoots: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "r2" {
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		t.Errorf("since+until: got %v want [r2]", ids)
	}
}
