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

	"github.com/radarnex/httpcatch/internal/config"
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

// New validates the bind policy and constructs a Server. An error is returned
// immediately if the policy refuses the bind address, so app composition can
// fail startup before any listener is created.
func New(cfg config.AdminConfig, logger *slog.Logger, sources MetricSources) (*Server, error) {
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

	r := chi.NewRouter()
	r.Get("/healthz", healthzHandler)
	internal := toInternal(sources)
	r.Get("/metrics", metricsHandler(internal))
	r.Get("/login", auth.loginPageHandler)
	r.Post("/auth/login", auth.loginPostHandler)
	r.Post("/auth/logout", auth.logoutHandler)
	r.Get("/static/*", staticHandler(etags))

	r.Group(func(r chi.Router) {
		r.Use(Middleware(cfg.Token, store))
		r.Get("/", indexHandler())
		r.Get("/status", statusHandler(internal, internal.unredacted))
	})

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
