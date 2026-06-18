package capture

import (
	"errors"
	"io"
	"log/slog"
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

// errReader fails every Read, simulating a client connection that breaks
// mid-body so CapBody returns an error.
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated body read failure")
}

func TestCaptureHandler_BodyReadErrorReturns202AndCountsError(t *testing.T) {
	t.Parallel()

	q := NewQueue(8)
	c := NewCounters()
	// Discard logger so the expected Warn line does not spam test output.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewCaptureHandler(HandlerOptions{Queue: q, Counters: c, Logger: logger})

	req := httptest.NewRequest(http.MethodPost, "/anything", errReader{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// ADR-0002: a body-read failure is still acknowledged with 202.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202 (read failure is still 202 per ADR-0002)", rec.Code)
	}
	// The error is counted...
	if got := c.CaptureErrorsTotal(); got != 1 {
		t.Fatalf("capture_errors_total: got %d want 1", got)
	}
	// ...and the record is neither captured nor counted as a queue drop.
	if got := c.CapturedTotal(); got != 0 {
		t.Fatalf("captured_total: got %d want 0 (read failure must not enqueue)", got)
	}
	if got := q.DroppedTotal(); got != 0 {
		t.Fatalf("dropped_total: got %d want 0 (read failure is not a queue drop)", got)
	}
}

func TestCaptureHandler_UnknownServiceIncrementsWithoutService(t *testing.T) {
	t.Parallel()

	q := NewQueue(8)
	c := NewCounters()
	h := NewCaptureHandler(HandlerOptions{Queue: q, Counters: c})

	req := httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("body"))
	// No service header and an empty Host force IdentifyService to "unknown".
	// httptest.NewRequest defaults Host to "example.com", which would otherwise
	// resolve to a host-sourced service, so clear it explicitly.
	req.Host = ""
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", rec.Code)
	}
	if got := c.CapturedTotal(); got != 1 {
		t.Fatalf("captured_total: got %d want 1", got)
	}
	if got := c.CapturedWithoutServiceTotal(); got != 1 {
		t.Fatalf("captured_without_service_total: got %d want 1 (unknown service must classify)", got)
	}
}
