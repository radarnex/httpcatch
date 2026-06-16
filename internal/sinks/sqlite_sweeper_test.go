package sinks

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
)

func sampleResponseEvent(id string, ts time.Time) *capture.ResponseEvent {
	return &capture.ResponseEvent{
		ID:                id,
		Timestamp:         ts,
		CorrelationID:     "corr-" + id,
		CorrelationSource: capture.CorrelationSourceTraceparent,
		Service:           "orders",
		ServiceSource:     capture.ServiceSourceHeader,
		Status:            200,
		Headers:           map[string][]string{"Content-Type": {"application/json"}},
		Body:              []byte(`{"ok":true}`),
		BodyTruncated:     false,
		BodyOriginalSize:  11,
		ContentType:       "application/json",
		DurationMS:        5,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, nil))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// insertRequestAt inserts a captured_request row with the given id and timestamp
// directly via the sink's prepared statement so tests control timing precisely.
func insertRequestAt(t *testing.T, s *SQLiteSink, id string, ts time.Time) {
	t.Helper()
	rec := sampleRecord(id)
	rec.Timestamp = ts
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatalf("insertRequestAt %s: %v", id, err)
	}
}

// insertEventAt inserts a response event row with the given id and timestamp.
func insertEventAt(t *testing.T, s *SQLiteSink, id string, ts time.Time) {
	t.Helper()
	evt := sampleResponseEvent(id, ts)
	if err := s.Write(context.Background(), evt); err != nil {
		t.Fatalf("insertEventAt %s: %v", id, err)
	}
}

func countRows(t *testing.T, s *SQLiteSink, table string) int {
	t.Helper()
	var n int
	//nolint:gosec // table name is a test-local constant, never user-supplied
	if err := s.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("countRows %s: %v", table, err)
	}
	return n
}

func newestTimestamps(t *testing.T, s *SQLiteSink, table string, limit int) []int64 {
	t.Helper()
	//nolint:gosec // table name is a test-local constant, never user-supplied
	rows, err := s.db.Query(fmt.Sprintf(
		"SELECT timestamp FROM %s ORDER BY timestamp DESC LIMIT ?", table), limit)
	if err != nil {
		t.Fatalf("newestTimestamps %s: %v", table, err)
	}
	defer rows.Close()
	var ts []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		ts = append(ts, v)
	}
	return ts
}

func TestSQLiteSink_Sweep_ByMaxAge(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)

	// Five records spaced 1 minute apart, oldest first.
	base := time.Now().UTC()
	for i := range 5 {
		ts := base.Add(-time.Duration(4-i) * time.Minute)
		insertRequestAt(t, s, fmt.Sprintf("req-%d", i), ts)
		insertEventAt(t, s, fmt.Sprintf("evt-%d", i), ts)
	}

	// Sweep with maxAge = 90 seconds: rows older than 90s ago are deleted.
	// The two oldest are > 90s old; the three newest are within 90s.
	// base-4m and base-3m are beyond the cutoff.
	// base-2m is ~120s old, also beyond.
	// base-1m is ~60s, within. base+0 is ~0s, within.
	// Expecting the 2 newest to survive.
	deleted, err := s.Sweep(context.Background(), SweeperPolicy{MaxAge: 90 * time.Second})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// 3 requests deleted + 3 events deleted = 6
	if deleted != 6 {
		t.Errorf("deleted: got %d want 6", deleted)
	}
	if n := countRows(t, s, "captured_requests"); n != 2 {
		t.Errorf("captured_requests remaining: got %d want 2", n)
	}
	if n := countRows(t, s, "events"); n != 2 {
		t.Errorf("events remaining: got %d want 2", n)
	}
}

func TestSQLiteSink_Sweep_ByMaxCount(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)

	base := time.Now().UTC()
	for i := range 10 {
		ts := base.Add(time.Duration(i) * time.Second)
		insertRequestAt(t, s, fmt.Sprintf("req-%d", i), ts)
		insertEventAt(t, s, fmt.Sprintf("evt-%d", i), ts)
	}

	deleted, err := s.Sweep(context.Background(), SweeperPolicy{MaxCount: 3})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// 7 requests + 7 events = 14 deleted
	if deleted != 14 {
		t.Errorf("deleted: got %d want 14", deleted)
	}
	if n := countRows(t, s, "captured_requests"); n != 3 {
		t.Errorf("captured_requests remaining: got %d want 3", n)
	}
	if n := countRows(t, s, "events"); n != 3 {
		t.Errorf("events remaining: got %d want 3", n)
	}

	// Verify survivors are the 3 newest.
	reqTS := newestTimestamps(t, s, "captured_requests", 3)
	if len(reqTS) != 3 {
		t.Fatalf("expected 3 timestamps, got %d", len(reqTS))
	}
	// Oldest of the 3 survivors should correspond to i=7 (base+7s).
	minSurvivor := base.Add(7 * time.Second).UnixNano()
	for _, ts := range reqTS {
		if ts < minSurvivor {
			t.Errorf("survivor timestamp %d is older than expected minimum %d", ts, minSurvivor)
		}
	}
}

func TestSQLiteSink_Sweep_CascadeBothTables(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)

	base := time.Now().UTC()
	for i := range 5 {
		ts := base.Add(time.Duration(i) * time.Second)
		insertRequestAt(t, s, fmt.Sprintf("r-%d", i), ts)
		insertEventAt(t, s, fmt.Sprintf("e-%d", i), ts)
	}

	// Keep 2 in each table.
	_, err := s.Sweep(context.Background(), SweeperPolicy{MaxCount: 2})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	reqCount := countRows(t, s, "captured_requests")
	evtCount := countRows(t, s, "events")
	if reqCount != 2 {
		t.Errorf("captured_requests: got %d want 2", reqCount)
	}
	if evtCount != 2 {
		t.Errorf("events: got %d want 2 — cascade trim must apply to both tables", evtCount)
	}
}

func TestSQLiteSink_Sweep_NoOpWhenDisabled(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)

	base := time.Now().UTC()
	for i := range 5 {
		insertRequestAt(t, s, fmt.Sprintf("r-%d", i), base.Add(time.Duration(i)*time.Second))
	}

	deleted, err := s.Sweep(context.Background(), SweeperPolicy{})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted: got %d want 0 (no-op)", deleted)
	}
	if n := countRows(t, s, "captured_requests"); n != 5 {
		t.Errorf("captured_requests: got %d want 5 (untouched)", n)
	}
}

func TestSQLiteSink_Sweep_Cancellation(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)

	ctx, cancel := context.WithCancel(context.Background())

	// StartSweeper spawns a goroutine internally. We track exit via a WaitGroup
	// by wrapping the internal goroutine using a channel the sweeper closes.
	// Since we cannot observe the internal goroutine directly, we instead confirm
	// that: (a) StartSweeper does not block, (b) cancellation does not panic,
	// and (c) the sink remains usable after cancellation.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.StartSweeper(ctx, SweeperPolicy{MaxCount: 100}, 10*time.Millisecond, discardLogger())
	}()
	wg.Wait() // StartSweeper is non-blocking; this returns immediately

	// Let the sweeper tick a few times.
	time.Sleep(50 * time.Millisecond)

	// Cancel and give the goroutine time to observe ctx.Done.
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Sink must remain usable after cancellation.
	insertRequestAt(t, s, "post-cancel", time.Now().UTC())
	if n := countRows(t, s, "captured_requests"); n < 1 {
		t.Error("sink should still accept writes after sweeper context is cancelled")
	}
}
