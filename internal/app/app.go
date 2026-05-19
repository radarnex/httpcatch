package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/pipeline"
	"github.com/radarnex/httpcatch/internal/redact"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// Greppable substrings that operators and tests can match against logs.
const (
	UnredactedWarning = "running in unredacted mode"
	ZeroSinksWarning  = "zero sinks enabled"
)

const shutdownDrainTimeout = 5 * time.Second

type App struct {
	Cfg      config.Config
	Logger   *slog.Logger
	Queue    *capture.Queue
	Counters *capture.Counters
	Workers  *pipeline.WorkerPool
	Handler  http.Handler
	Sinks    []sinks.Sink
	Memory   *sinks.MemorySink
	SQLite   *sinks.SQLiteSink
	Ruleset  *redact.Ruleset
	Admin    *admin.Server
}

func Build(cfg config.Config, logger *slog.Logger, stdoutWriter io.Writer, extraSinks ...sinks.Sink) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if stdoutWriter == nil {
		stdoutWriter = io.Discard
	}
	var ss []sinks.Sink
	if cfg.Sinks.Stdout {
		ss = append(ss, sinks.NewWriterSink(stdoutWriter))
	}
	var memSink *sinks.MemorySink
	if cfg.Sinks.Memory {
		memSink = sinks.NewMemorySink(cfg.Sinks.MemoryCapacity)
		ss = append(ss, memSink)
	}
	var sqliteSink *sinks.SQLiteSink
	if cfg.Sinks.SQLite {
		s, err := sinks.NewSQLiteSink(cfg.Sinks.SQLitePath)
		if err != nil {
			return nil, fmt.Errorf("sqlite sink: %w", err)
		}
		sqliteSink = s
		ss = append(ss, sqliteSink)
	}
	ss = append(ss, extraSinks...)

	ruleset, err := redact.NewRuleset(cfg.Redaction)
	if err != nil {
		return nil, fmt.Errorf("redaction ruleset: %w", err)
	}

	q := capture.NewQueue(cfg.QueueSize)
	counters := capture.NewCounters()
	workers := pipeline.NewWorkerPool(cfg.Workers, q, ruleset, ss, logger)
	handler := capture.NewCaptureHandler(capture.HandlerOptions{
		Queue:         q,
		Counters:      counters,
		ServiceHeader: cfg.ServiceHeader,
		BodyCap:       cfg.BodyCap,
		Logger:        logger,
	})

	readers := admin.ReadSources{}
	if memSink != nil {
		readers.Memory = memSink
	}
	if sqliteSink != nil {
		readers.SQLite = sqliteSink
	}

	eventsCounters := admin.NewEventsCounters()

	adminSrv, err := admin.New(cfg.Admin, logger, admin.MetricSources{
		DroppedTotal:                    q.DroppedTotal,
		CapturedWithoutCorrelationTotal: counters.CapturedWithoutCorrelationTotal,
		CapturedWithoutServiceTotal:     counters.CapturedWithoutServiceTotal,
		RedactionErrorsTotal:            ruleset.RedactionErrorsTotal,
		Unredacted:                      ruleset.IsUnredacted,

		EventsIngestedResponseTotal:          eventsCounters.EventsIngestedResponseTotal,
		EventsIngestedOutboundTotal:          eventsCounters.EventsIngestedOutboundTotal,
		EventsRejectedInvalidJSONTotal:       eventsCounters.EventsRejectedInvalidJSONTotal,
		EventsRejectedPayloadTooLargeTotal:   eventsCounters.EventsRejectedPayloadTooLargeTotal,
		EventsRejectedUnknownTypeTotal:       eventsCounters.EventsRejectedUnknownTypeTotal,
		EventsRejectedMissingTypeTotal:       eventsCounters.EventsRejectedMissingTypeTotal,
		EventsRejectedMissingRequiredFieldTotal: eventsCounters.EventsRejectedMissingRequiredFieldTotal,
		EventsRejectedEmptyBatchTotal:        eventsCounters.EventsRejectedEmptyBatchTotal,
	}, admin.ServerOptions{
		Readers: readers,
		Events: admin.EventsSources{
			Queue:            q,
			BodyCap:          cfg.BodyCap,
			MaxEventsPayload: cfg.MaxEventsPayload,
			Counters:         eventsCounters,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("admin server: %w", err)
	}

	return &App{
		Cfg:      cfg,
		Logger:   logger,
		Queue:    q,
		Counters: counters,
		Workers:  workers,
		Handler:  handler,
		Sinks:    ss,
		Memory:   memSink,
		SQLite:   sqliteSink,
		Ruleset:  ruleset,
		Admin:    adminSrv,
	}, nil
}

func (a *App) EmitStartupWarnings() {
	if len(a.Sinks) == 0 {
		a.Logger.Warn(ZeroSinksWarning + " — captured records will be discarded after dequeue; enable at least one sink (stdout/memory/sqlite) to persist captures")
	}
	if a.Ruleset.IsUnredacted() {
		a.Logger.Warn(UnredactedWarning + " — no redaction is applied to captured payloads; configure a redactor before exposing this instance to production traffic")
	}
}

// Serve binds both the capture port and the admin port, then runs until ctx is
// cancelled or either server fails. Failure in either listener triggers
// shutdown of the other. On shutdown the queue is closed and the worker pool
// is given shutdownDrainTimeout to drain; a stuck sink cannot wedge shutdown
// forever.
func (a *App) Serve(ctx context.Context) error {
	a.Workers.Start(ctx)

	addr := fmt.Sprintf("0.0.0.0:%d", a.Cfg.CapturePort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.shutdown()
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	captureServer := &http.Server{Handler: a.Handler}
	a.Logger.Info("capture port listening", "addr", ln.Addr().String())

	captureErrCh := make(chan error, 1)
	go func() { captureErrCh <- captureServer.Serve(ln) }()

	adminErrCh := make(chan error, 1)
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	go func() { adminErrCh <- a.Admin.Serve(serveCtx) }()

	var serveErr error
	select {
	case <-ctx.Done():
		// Context cancelled: shut down both servers.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
		_ = captureServer.Shutdown(shutCtx)
		cancel()
		// Admin server will see serveCtx cancelled (which mirrors ctx) and
		// shut itself down.
	case err := <-captureErrCh:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
		// Shut down admin server on capture-port failure.
		cancelServe()
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
		_ = captureServer.Shutdown(shutCtx)
		cancel()
	case err := <-adminErrCh:
		if err != nil {
			serveErr = err
		}
		// Shut down capture server on admin-port failure.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
		_ = captureServer.Shutdown(shutCtx)
		cancel()
	}
	a.shutdown()
	return serveErr
}

func (a *App) shutdown() {
	a.Queue.Close()
	done := make(chan struct{})
	go func() { a.Workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownDrainTimeout):
		a.Logger.Warn("workers did not drain within timeout; exiting with possible record loss",
			"timeout", shutdownDrainTimeout)
	}
	if a.SQLite != nil {
		if err := a.SQLite.Close(); err != nil {
			a.Logger.Warn("sqlite close failed", "err", err)
		}
	}
}
