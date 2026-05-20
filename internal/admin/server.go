package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/inspect"
)

// shutdownDrainTimeout is the maximum time given to in-flight requests when
// the admin port is shutting down.
const shutdownDrainTimeout = 5 * time.Second

// Server holds the admin HTTP server and its router so later slices can mount
// additional routes without re-implementing bind policy or server lifecycle.
type Server struct {
	cfg    config.AdminConfig
	logger *slog.Logger
	router chi.Router
	store  *SessionStore
}

// ReadSources holds the optional Reader implementations wired at app
// composition time. Fields may be nil; the inspect handler degrades gracefully.
type ReadSources struct {
	Memory inspect.Reader
	SQLite inspect.Reader
}

// EventsSources wires the queue and configuration the Events API handler needs.
// Queue may be nil; the events handler returns 503 when no queue is configured.
type EventsSources struct {
	Queue            *capture.Queue
	BodyCap          int
	MaxEventsPayload int
	Counters         *EventsCounters
}

// ServerOptions bundles the optional dependencies that various route groups need.
// Fields have sensible zero values (nil = feature disabled).
type ServerOptions struct {
	Readers ReadSources
	Events  EventsSources
}

// New validates the bind policy and constructs a Server. An error is returned
// immediately if the policy refuses the bind address, so app composition can
// fail startup before any listener is created.
func New(cfg config.AdminConfig, logger *slog.Logger, sources MetricSources, opts ...ServerOptions) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	reason, err := Guard(cfg.Bind, cfg.Token != "", cfg.InsecureListen)
	if err != nil {
		return nil, err
	}
	logger.Info("admin port bind policy", "bind", cfg.Bind, "reason", string(reason))

	if cfg.Token == "" && reason == ReasonLoopbackDefault {
		logger.Warn("admin token is empty on loopback bind; admin endpoints (except /healthz) are unreachable until admin.token is configured")
	}

	store := NewSessionStore(time.Now)
	auth := &authHandlers{cfg: cfg, store: store, logger: logger}

	etags := buildEtags(uiFS)

	var serverOpts ServerOptions
	if len(opts) > 0 {
		serverOpts = opts[0]
	}
	rs := serverOpts.Readers
	es := serverOpts.Events

	sources.normalize()

	r := chi.NewRouter()
	r.Get("/healthz", healthzHandler)
	r.Get("/metrics", metricsHandler(sources))
	r.Get("/login", auth.loginPageHandler)
	r.Post("/auth/login", auth.loginPostHandler)
	r.Post("/auth/logout", auth.logoutHandler)
	r.Get("/static/*", staticHandler(etags))

	r.Group(func(r chi.Router) {
		r.Use(Middleware(cfg.Token, store))
		r.Get("/", rootRedirectHandler())
		r.Get("/status", statusHandler(sources))
		r.Get("/requests", requestsHandler(rs.Memory, rs.SQLite))
		r.Get("/requests/{id}", requestDetailHandler(rs.Memory, rs.SQLite))
		r.Get("/ui/requests", requestListHandler(rs.Memory, rs.SQLite))
		r.Get("/ui/requests/{id}", requestDetailUIHandler(rs.Memory, rs.SQLite))
		r.Get("/ui/services", servicesUIHandler(rs.Memory, rs.SQLite))
	})

	// POST /events uses bearer-only auth (no session cookie) to eliminate CSRF risk.
	// App middleware calling this endpoint always uses a bearer token, never a browser cookie.
	r.With(Middleware(cfg.Token, store, WithCookieAuth(false))).
		Post("/events", eventsHandler(es.Queue, es.BodyCap, es.MaxEventsPayload, es.Counters))

	return &Server{
		cfg:    cfg,
		logger: logger,
		router: r,
		store:  store,
	}, nil
}

// Router returns the chi router so later slices can register additional routes.
func (s *Server) Router() chi.Router {
	return s.router
}

// Serve binds the admin port and runs until ctx is cancelled or the server
// fails. On context cancellation, Serve calls http.Server.Shutdown with the
// shared drain timeout before returning.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Bind)
	if err != nil {
		return fmt.Errorf("admin: listen %s: %w", s.cfg.Bind, err)
	}

	s.store.StartSweeper(ctx, time.Minute)

	srv := &http.Server{Handler: s.router}
	s.logger.Info("admin port listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
		_ = srv.Shutdown(shutCtx)
		cancel()
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
