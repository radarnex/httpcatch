package sinks

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/radarnex/httpcatch/internal/capture"
)

func mustWrite(t *testing.T, s *MemorySink, r *capture.CapturedRecord) {
	t.Helper()
	if err := s.Write(context.Background(), r); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func TestMemorySink_NameAndCapacity(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(7)
	if s.Name() != NameMemory {
		t.Errorf("Name: got %q want %q", s.Name(), NameMemory)
	}
	if s.Capacity() != 7 {
		t.Errorf("Capacity: got %d want 7", s.Capacity())
	}
	if s.Len() != 0 {
		t.Errorf("Len: got %d want 0", s.Len())
	}
}

func TestMemorySink_FillsBelowCapacity(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(5)
	for i := range 3 {
		mustWrite(t, s, &capture.CapturedRecord{ID: strconv.Itoa(i)})
	}
	if s.Len() != 3 {
		t.Errorf("Len: got %d want 3", s.Len())
	}
	recs := s.Recent(10)
	if len(recs) != 3 {
		t.Fatalf("Recent: got %d want 3", len(recs))
	}
	for i, want := range []string{"2", "1", "0"} {
		if recs[i].ID != want {
			t.Errorf("Recent[%d]: got %q want %q", i, recs[i].ID, want)
		}
	}
}

func TestMemorySink_EvictsOldestWhenFull(t *testing.T) {
	t.Parallel()

	const capacity = 3
	s := NewMemorySink(capacity)
	for i := range 7 {
		mustWrite(t, s, &capture.CapturedRecord{ID: strconv.Itoa(i)})
		if got := s.Len(); got > capacity {
			t.Fatalf("after write %d: Len()=%d exceeds capacity %d", i, got, capacity)
		}
	}
	if s.Len() != capacity {
		t.Fatalf("final Len: got %d want %d", s.Len(), capacity)
	}
	recs := s.Recent(capacity)
	if len(recs) != capacity {
		t.Fatalf("Recent: got %d want %d", len(recs), capacity)
	}
	for i, want := range []string{"6", "5", "4"} {
		if recs[i].ID != want {
			t.Errorf("Recent[%d]: got %q want %q", i, recs[i].ID, want)
		}
	}
}

func TestMemorySink_RecentRespectsRequestedCount(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(10)
	for i := range 10 {
		mustWrite(t, s, &capture.CapturedRecord{ID: strconv.Itoa(i)})
	}
	cases := []struct {
		n    int
		want []string
	}{
		{0, nil},
		{-1, nil},
		{3, []string{"9", "8", "7"}},
		{10, []string{"9", "8", "7", "6", "5", "4", "3", "2", "1", "0"}},
		{20, []string{"9", "8", "7", "6", "5", "4", "3", "2", "1", "0"}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("n=%d", c.n), func(t *testing.T) {
			t.Parallel()
			recs := s.Recent(c.n)
			if len(recs) != len(c.want) {
				t.Fatalf("len: got %d want %d", len(recs), len(c.want))
			}
			for i, id := range c.want {
				if recs[i].ID != id {
					t.Errorf("[%d]: got %q want %q", i, recs[i].ID, id)
				}
			}
		})
	}
}

func TestMemorySink_RecentDoesNotRemove(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(5)
	for i := range 5 {
		mustWrite(t, s, &capture.CapturedRecord{ID: strconv.Itoa(i)})
	}
	for range 3 {
		recs := s.Recent(5)
		if len(recs) != 5 {
			t.Fatalf("Recent shrank the ring: got %d want 5", len(recs))
		}
		if s.Len() != 5 {
			t.Fatalf("Len after Recent: got %d want 5", s.Len())
		}
	}
}

func TestMemorySink_SnapshotImmuneToLaterWrites(t *testing.T) {
	t.Parallel()

	s := NewMemorySink(3)
	mustWrite(t, s, &capture.CapturedRecord{ID: "a"})
	mustWrite(t, s, &capture.CapturedRecord{ID: "b"})
	snap := s.Recent(3)
	mustWrite(t, s, &capture.CapturedRecord{ID: "c"})
	mustWrite(t, s, &capture.CapturedRecord{ID: "d"})

	if len(snap) != 2 {
		t.Fatalf("snapshot len: got %d want 2", len(snap))
	}
	if snap[0].ID != "b" || snap[1].ID != "a" {
		t.Errorf("snapshot mutated by later writes: got %q,%q", snap[0].ID, snap[1].ID)
	}
}

func TestMemorySink_ConcurrentWritesAndReads(t *testing.T) {
	t.Parallel()

	const (
		capacity   = 64
		writers    = 8
		perWriter  = 500
		readers    = 4
		readsEach  = 200
		totalWrite = writers * perWriter
	)
	s := NewMemorySink(capacity)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				mustWrite(t, s, &capture.CapturedRecord{
					ID: fmt.Sprintf("w%d-%d", w, i),
				})
			}
		}(w)
	}
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range readsEach {
				recs := s.Recent(capacity)
				if len(recs) > capacity {
					t.Errorf("Recent returned %d > capacity %d", len(recs), capacity)
					return
				}
				for _, r := range recs {
					if r == nil {
						t.Error("Recent returned a nil record")
						return
					}
				}
			}
		}()
	}
	wg.Wait()

	if got := s.Len(); got != capacity {
		t.Errorf("final Len: got %d want %d (after %d writes)", got, capacity, totalWrite)
	}
	recs := s.Recent(capacity)
	if len(recs) != capacity {
		t.Fatalf("final Recent: got %d want %d", len(recs), capacity)
	}
	seen := make(map[string]struct{}, capacity)
	for _, r := range recs {
		if _, dup := seen[r.ID]; dup {
			t.Errorf("duplicate record %q in snapshot", r.ID)
		}
		seen[r.ID] = struct{}{}
	}
}
