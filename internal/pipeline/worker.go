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

type WorkerPool struct {
	size          int
	queue         *capture.Queue
	redactor      redact.Redactor
	sinks         []sinks.Sink
	logger        *slog.Logger
	wg            sync.WaitGroup
	panics        atomic.Uint64
	errorCounters *SinkErrorCounters
}

func NewWorkerPool(size int, q *capture.Queue, r redact.Redactor, ss []sinks.Sink, logger *slog.Logger, errorCounters *SinkErrorCounters) *WorkerPool {
	if size < 1 {
		size = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkerPool{
		size:          size,
		queue:         q,
		redactor:      r,
		sinks:         ss,
		logger:        logger,
		errorCounters: errorCounters,
	}
}

// Start launches workers. Workers drain the queue until it is closed; ctx is
// only passed to Sink.Write so slow sinks can observe cancellation. To stop
// workers, close the queue and call Wait.
func (p *WorkerPool) Start(ctx context.Context) {
	for range p.size {
		p.wg.Add(1)
		go p.run(ctx)
	}
}

func (p *WorkerPool) run(ctx context.Context) {
	defer p.wg.Done()
	for rec := range p.queue.Receive() {
		if rec == nil {
			continue
		}
		p.process(ctx, rec)
	}
}

func (p *WorkerPool) process(ctx context.Context, rec capture.Record) {
	defer func() {
		if v := recover(); v != nil {
			p.panics.Add(1)
			p.logger.Error("worker recovered from panic",
				"id", rec.RecordID(),
				"panic", v)
		}
	}()
	redacted := p.redactor.Redact(rec)
	for _, s := range p.sinks {
		if err := s.Write(ctx, redacted); err != nil {
			p.logger.Error("sink write failed",
				"sink", s.Name(),
				"id", redacted.RecordID(),
				"err", err)
			if p.errorCounters != nil {
				p.errorCounters.IncBySinkName(s.Name())
			}
		}
	}
}

// PanicsCount returns the total number of panics recovered across all workers.
func (p *WorkerPool) PanicsCount() uint64 { return p.panics.Load() }

func (p *WorkerPool) Wait() { p.wg.Wait() }
