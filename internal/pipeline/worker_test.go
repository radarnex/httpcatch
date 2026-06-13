package pipeline_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/pipeline"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// panicRedactor panics when it sees a record whose ID matches the trigger,
// and passes all others through unchanged.
type panicRedactor struct {
	trigger string
}

func (r *panicRedactor) Redact(rec capture.Record) capture.Record {
	if rec.RecordID() == r.trigger {
		panic("intentional test panic")
	}
	return rec
}

// collectSink accumulates written records in order.
type collectSink struct {
	mu      sync.Mutex
	written []capture.Record
}

func (s *collectSink) Name() string { return "collect" }

func (s *collectSink) Write(_ context.Context, r capture.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, r)
	return nil
}

func (s *collectSink) Records() []capture.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capture.Record, len(s.written))
	copy(out, s.written)
	return out
}

func makeRequest(id string) *capture.CapturedRequest {
	return &capture.CapturedRequest{ID: id}
}

func TestWorkerPool_PanicInRedact_WorkerContinues(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	sink := &collectSink{}
	redactor := &panicRedactor{trigger: "panic-me"}

	pool := pipeline.NewWorkerPool(1, q, redactor, []sinks.Sink{sink}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	// Enqueue a record that will panic the redactor, followed by a normal one.
	q.Enqueue(makeRequest("panic-me"))
	q.Enqueue(makeRequest("normal-1"))

	// Wait for the normal record to arrive in the sink.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.Records()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	q.Close()
	pool.Wait()
	cancel()

	recs := sink.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record in sink, got %d", len(recs))
	}
	if recs[0].RecordID() != "normal-1" {
		t.Errorf("expected normal-1 in sink, got %q", recs[0].RecordID())
	}
	if pool.PanicsCount() != 1 {
		t.Errorf("expected 1 panic counted, got %d", pool.PanicsCount())
	}
}
