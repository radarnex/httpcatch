package capture

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCaptureHandler_CapturedTotalIncrementsOnEnqueue(t *testing.T) {
	t.Parallel()

	q := NewQueue(8)
	c := NewCounters()
	h := NewCaptureHandler(HandlerOptions{Queue: q, Counters: c})

	for range 3 {
		req := httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("body"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status: got %d want 202", rec.Code)
		}
	}

	if got := c.CapturedTotal(); got != 3 {
		t.Fatalf("captured: got %d want 3", got)
	}
}

func TestCaptureHandler_CapturedTotalNotIncrementedOnDrop(t *testing.T) {
	t.Parallel()

	// Capacity 1 with no consumer: the first request fills the queue, the rest drop.
	q := NewQueue(1)
	c := NewCounters()
	h := NewCaptureHandler(HandlerOptions{Queue: q, Counters: c})

	for range 3 {
		req := httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("body"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status: got %d want 202 (drop is still 202 per ADR-0002)", rec.Code)
		}
	}

	if got := c.CapturedTotal(); got != 1 {
		t.Fatalf("captured: got %d want 1 (drops must not count as captured)", got)
	}
	if got := q.DroppedTotal(); got != 2 {
		t.Fatalf("dropped: got %d want 2", got)
	}

	// These requests carry no traceparent/X-Request-ID, so each would synthesize
	// a correlation id. Only the one that was actually captured may advance the
	// classification counter — the two drops must not — or captured_without_*
	// could exceed captured_total.
	if got := c.CapturedWithoutCorrelationTotal(); got != 1 {
		t.Fatalf("captured_without_correlation: got %d want 1 (drops must not classify)", got)
	}
}
