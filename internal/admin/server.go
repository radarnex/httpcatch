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
	cfg      config.AdminConfig
	timeouts config.TimeoutsConfig
	logger   *slog.Logger
	router   chi.Router
	store    *SessionStore
	limiter  *AuthLimiter
}

// ReadSources holds the optional Reader implementations wired at app
// composition time. Fields may be nil; the inspect handler degrades gracefully.
// QueryTimeout is the per-request deadline applied to inspect reads; zero
// disables the timeout.
type ReadSources struct {
	Memory       inspect.Reader
	SQLite       inspect.Reader
	QueryTimeout time.Duration
}

// EventsSources wires the queue and configuration the Events API handler needs.
// Queue may be nil; the events handler returns 503 when no queue is configured.
type EventsSources struct {
	Queue             *capture.Queue
	BodyCap           int
	MaxEventsPayload  int
	MaxEventsPerBatch int
	Counters          *EventsCounters
}

// ServerOptions bundles the optional dependencies that various route groups need.
// Fields have sensible zero values (nil = feature disabled).
type ServerOptions struct {
	Readers ReadSources
	Events  EventsSources
	// Effective is the post-defaults, post-env, post-validation snapshot of the
	// running config. Rendered verbatim on the Configuration page. When zero,
	// the page renders an empty effective config block.
	Effective config.Config
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
	limiter := NewAuthLimiter()

	// Wire auth failure counters into metric sources before registering the
	// metrics handler.
	sources.AuthFailuresInvalidTokenTotal = limiter.InvalidTokenTotal
	sources.AuthFailuresRateLimitedTotal = limiter.RateLimitedTotal
	sources.AuthFailuresCSRFBlockedTotal = limiter.CSRFBlockedTotal

	auth := &authHandlers{cfg: cfg, store: store, logger: logger, limiter: limiter}

	etags := buildEtags(uiFS)

	var serverOpts ServerOptions
	if len(opts) > 0 {
		serverOpts = opts[0]
	}
	rs := serverOpts.Readers
	es := serverOpts.Events

	sources.normalize()

	r := chi.NewRouter()
	r.With(jsonSecurityHeaders()).Get("/healthz", healthzHandler)
	r.With(jsonSecurityHeaders()).Get("/metrics", metricsHandler(sources))
	r.With(htmlSecurityHeaders()).Get("/login", auth.loginPageHandler)
	r.With(htmlSecurityHeaders(), csrfOriginCheck(limiter)).Post("/auth/login", auth.loginPostHandler)
	r.With(htmlSecurityHeaders(), csrfOriginCheck(limiter)).Post("/auth/logout", auth.logoutHandler)
	r.With(jsonSecurityHeaders()).Get("/static/*", staticHandler(etags))

	r.Group(func(r chi.Router) {
		r.Use(htmlSecurityHeaders())
		r.Use(Middleware(cfg.Token, store, limiter))
		r.Get("/", rootRedirectHandler())
		r.Get("/status", statusHandler(sources))
		r.Get("/requests", requestsHandler(rs))
		r.Get("/requests/aggregate", requestsAggregateHandler(rs))
		r.Get("/requests/{id}", requestDetailHandler(rs))
		r.Get("/ui/requests", requestListHandler(rs))
		r.Get("/ui/requests/{id}", requestDetailUIHandler(rs))
		r.Get("/ui/services", servicesUIHandler(rs))
		r.Get("/ui/configuration", configurationUIHandler(serverOpts.Effective, sources.Unredacted))
	})

	// POST /events uses bearer-only auth (no session cookie) to eliminate CSRF risk.
	// App middleware calling this endpoint always uses a bearer token, never a browser cookie.
	r.With(jsonSecurityHeaders(), Middleware(cfg.Token, store, limiter, WithCookieAuth(false))).
		Post("/events", eventsHandler(es.Queue, es.BodyCap, es.MaxEventsPayload, es.MaxEventsPerBatch, es.Counters))

	return &Server{
		cfg:      cfg,
		timeouts: serverOpts.Effective.Timeouts,
		logger:   logger,
		router:   r,
		store:    store,
		limiter:  limiter,
	}, nil
}

// Router returns the chi router so later slices can register additional routes.
func (s *Server) Router() chi.Router {
	return s.router
}

// AuthLimiter returns the rate limiter used by this server's auth handlers.
func (s *Server) AuthLimiter() *AuthLimiter {
	return s.limiter
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
	s.limiter.StartSweeper(ctx, 5*time.Minute)

	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: s.timeouts.ReadHeader,
		ReadTimeout:       s.timeouts.Read,
		WriteTimeout:      s.timeouts.Write,
		IdleTimeout:       s.timeouts.Idle,
	}
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
