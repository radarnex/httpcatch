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

// UnredactedWarning is a substring tests grep for to confirm the warning fired.
const UnredactedWarning = "running in unredacted mode"

// ZeroSinksWarning is a substring tests grep for to confirm the warning fired.
const ZeroSinksWarning = "zero sinks enabled"

type App struct {
	Cfg     config.Config
	Logger  *slog.Logger
	Queue   *capture.Queue
	Workers *pipeline.WorkerPool
	Handler http.Handler
	Sinks   []sinks.Sink
}

// Build wires every component but does not bind any network listener.
// stdoutWriter is where the stdout sink (if enabled) writes its JSON lines.
// extraSinks are appended after the configured sinks for tests.
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
	handler := capture.NewCaptureHandler(q, cfg.BodyCap)

	return &App{
		Cfg:     cfg,
		Logger:  logger,
		Queue:   q,
		Workers: workers,
		Handler: handler,
		Sinks:   ss,
	}
}

// EmitStartupWarnings prints the prominent warnings the operator must see.
func (a *App) EmitStartupWarnings() {
	if len(a.Sinks) == 0 {
		a.Logger.Warn(ZeroSinksWarning + " — captured records will be discarded after dequeue; enable at least one sink (stdout/memory/sqlite) to persist captures")
	}
	a.Logger.Warn(UnredactedWarning + " — no redaction is applied to captured payloads; configure a redactor before exposing this instance to production traffic")
}

// Serve binds the capture port on 0.0.0.0:<port>, starts the worker pool,
// and blocks until ctx is cancelled or the server fails. On shutdown the
// queue is closed and the worker pool drains before returning.
func (a *App) Serve(ctx context.Context) error {
	defer func() {
		a.Queue.Close()
		a.Workers.Wait()
	}()
	a.Workers.Start(ctx)

	addr := fmt.Sprintf("0.0.0.0:%d", a.Cfg.CapturePort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	server := &http.Server{Handler: a.Handler}
	a.Logger.Info("capture port listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
