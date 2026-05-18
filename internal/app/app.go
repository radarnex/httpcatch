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
	Cfg     config.Config
	Logger  *slog.Logger
	Queue   *capture.Queue
	Workers *pipeline.WorkerPool
	Handler http.Handler
	Sinks   []sinks.Sink
}

func Build(cfg config.Config, logger *slog.Logger, stdoutWriter io.Writer, extraSinks ...sinks.Sink) *App {
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
	ss = append(ss, extraSinks...)

	q := capture.NewQueue(cfg.QueueSize)
	workers := pipeline.NewWorkerPool(cfg.Workers, q, redact.NoOp{}, ss, logger)
	handler := capture.NewCaptureHandler(q, cfg.BodyCap, logger)

	return &App{
		Cfg:     cfg,
		Logger:  logger,
		Queue:   q,
		Workers: workers,
		Handler: handler,
		Sinks:   ss,
	}
}

func (a *App) EmitStartupWarnings() {
	if len(a.Sinks) == 0 {
		a.Logger.Warn(ZeroSinksWarning + " — captured records will be discarded after dequeue; enable at least one sink (stdout/memory/sqlite) to persist captures")
	}
	a.Logger.Warn(UnredactedWarning + " — no redaction is applied to captured payloads; configure a redactor before exposing this instance to production traffic")
}

// Serve binds the capture port and runs until ctx is cancelled or the server
// fails. On shutdown the queue is closed and the worker pool is given
// shutdownDrainTimeout to drain; a stuck sink cannot wedge shutdown forever.
func (a *App) Serve(ctx context.Context) error {
	a.Workers.Start(ctx)

	addr := fmt.Sprintf("0.0.0.0:%d", a.Cfg.CapturePort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.shutdown()
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	server := &http.Server{Handler: a.Handler}
	a.Logger.Info("capture port listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ln) }()

	var serveErr error
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
		_ = server.Shutdown(shutCtx)
		cancel()
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
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
}
