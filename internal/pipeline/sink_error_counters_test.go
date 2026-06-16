package pipeline_test

import (
	"context"
	"errors"
	"testing"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/pipeline"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// failSink is a sink whose Write always returns a fixed error.
type failSink struct {
	name string
}

func (s *failSink) Name() string { return s.name }
func (s *failSink) Write(_ context.Context, _ capture.Record) error {
	return errors.New("write failed")
}

func TestWorkerPool_SinkErrorCounter_IncrementsOnWriteFailure(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	sink := &failSink{name: sinks.NameSQLite}
	counters := pipeline.NewSinkErrorCounters()

	pool := pipeline.NewWorkerPool(1, q, &panicRedactor{}, []sinks.Sink{sink}, nil, counters)
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	q.Enqueue(makeRequest("fail-me"))
	q.Close()
	pool.Wait()
	cancel()

	if got := counters.SQLiteErrorsTotal(); got != 1 {
		t.Errorf("SQLiteErrorsTotal: got %d want 1", got)
	}
	if got := counters.MemoryErrorsTotal(); got != 0 {
		t.Errorf("MemoryErrorsTotal: got %d want 0", got)
	}
	if got := counters.StdoutErrorsTotal(); got != 0 {
		t.Errorf("StdoutErrorsTotal: got %d want 0", got)
	}
}
