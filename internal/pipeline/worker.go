package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/redact"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// WorkerPool drains a Queue, runs each record through the Redactor,
// then fans out to every enabled Sink. Per-sink errors are counted and
// logged but do not stop other sinks from receiving the same record.
type WorkerPool struct {
	size     int
	queue    *capture.Queue
	redactor redact.Redactor
	sinks    []sinks.Sink
	logger   *slog.Logger
	wg       sync.WaitGroup
	workCtx  context.Context

	processed atomic.Uint64
	sinkErrs  atomic.Uint64
}

func NewWorkerPool(size int, q *capture.Queue, r redact.Redactor, ss []sinks.Sink, logger *slog.Logger) *WorkerPool {
	if size < 1 {
		size = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkerPool{
		size:     size,
		queue:    q,
		redactor: r,
		sinks:    ss,
		logger:   logger,
	}
}

// Start launches the worker goroutines. Workers drain the queue until it is
// closed. The supplied ctx is passed to each Sink.Write call so slow sinks can
// observe cancellation; ctx cancellation does NOT stop the worker loop. To
// stop workers, close the queue and call Wait.
func (p *WorkerPool) Start(ctx context.Context) {
	p.workCtx = ctx
	for range p.size {
		p.wg.Add(1)
		go p.run()
	}
}

func (p *WorkerPool) run() {
	defer p.wg.Done()
	for rec := range p.queue.Receive() {
		if rec == nil {
			continue
		}
		redacted := p.redactor.Redact(rec)
		for _, s := range p.sinks {
			if err := s.Write(p.workCtx, redacted); err != nil {
				p.sinkErrs.Add(1)
				p.logger.Error("sink write failed",
					"sink", s.Name(),
					"id", redacted.ID,
					"err", err)
			}
		}
		p.processed.Add(1)
	}
}

// Wait blocks until every worker has exited. Callers typically close the queue first.
func (p *WorkerPool) Wait() { p.wg.Wait() }

func (p *WorkerPool) RecordsProcessed() uint64 { return p.processed.Load() }
func (p *WorkerPool) SinkErrorsTotal() uint64  { return p.sinkErrs.Load() }
func (p *WorkerPool) Size() int                { return p.size }
