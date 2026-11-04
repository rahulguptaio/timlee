package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Server wraps http.Server with the timeouts and shutdown behaviour a service
// needs to survive a real deployment.
type Server struct {
	server     *http.Server
	logger     *slog.Logger
	draining   chan struct{}
	drainDelay time.Duration
	once       sync.Once
}

// ServerOptions configures NewServer.
type ServerOptions struct {
	Addr    string
	Handler http.Handler
	Logger  *slog.Logger

	// ReadHeaderTimeout bounds how long a client may take to send headers,
	// which is the cheap defence against a slow-loris connection.
	ReadHeaderTimeout time.Duration
	// IdleTimeout must exceed the idle timeout of any proxy in front of this
	// service. If the proxy holds a pooled connection that this server has
	// already closed, the next request through it fails — the classic
	// intermittent 502 behind a load balancer.
	IdleTimeout time.Duration
	// WriteTimeout is deliberately zero by default: these services stream
	// Server-Sent Events, and a write deadline would sever a healthy stream
	// mid-response.
	WriteTimeout time.Duration
	// DrainDelay is how long to keep answering after SIGTERM before the
	// listener closes, giving a load balancer time to notice readiness has
	// gone false. Zero disables the pause, which suits local runs.
	DrainDelay time.Duration
}

// NewServer builds a Server with safe defaults.
func NewServer(opts ServerOptions) *Server {
	if opts.ReadHeaderTimeout == 0 {
		opts.ReadHeaderTimeout = 10 * time.Second
	}
	if opts.IdleTimeout == 0 {
		// Longer than the 60s default of common proxies, so this server is
		// never the side that closes a pooled connection first.
		opts.IdleTimeout = 120 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	draining := make(chan struct{})

	return &Server{
		logger:     opts.Logger,
		draining:   draining,
		drainDelay: opts.DrainDelay,
		server: &http.Server{
			Addr:              opts.Addr,
			Handler:           opts.Handler,
			ReadHeaderTimeout: opts.ReadHeaderTimeout,
			IdleTimeout:       opts.IdleTimeout,
			WriteTimeout:      opts.WriteTimeout,
			ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
		},
	}
}

// Draining is closed when shutdown starts. Long-lived handlers — event
// streams above all — select on it to end politely instead of being cut off,
// because a stream has no natural end of its own and would otherwise hold
// shutdown open until the grace period expires.
func (s *Server) Draining() <-chan struct{} { return s.draining }

// IsDraining reports whether shutdown has begun.
func (s *Server) IsDraining() bool {
	select {
	case <-s.draining:
		return true
	default:
		return false
	}
}

// ReadinessHandler reports whether this instance should receive new traffic.
//
// Separate from liveness on purpose. Liveness answers "is this process
// broken, restart it"; readiness answers "should the load balancer send work
// here". They differ in exactly one situation that matters: between SIGTERM
// and the listener closing, the process is perfectly healthy but must stop
// receiving new requests. Reporting 503 there lets the balancer drain this
// instance before the socket goes away, which is what prevents the burst of
// failed requests that otherwise accompanies every deploy.
func (s *Server) ReadinessHandler(check func() error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.IsDraining() {
			w.Header().Set("Cache-Control", "no-store")
			WriteError(w, http.StatusServiceUnavailable, "Shutting down.", "draining")
			return
		}
		if check != nil {
			if err := check(); err != nil {
				s.logger.Warn("readiness check failed", "error", err)
				WriteError(w, http.StatusServiceUnavailable, "Not ready.", "not_ready")
				return
			}
		}
		WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
	})
}

// Run serves until SIGINT or SIGTERM, then drains.
//
// The sequence matters: stop accepting, let in-flight work finish, and only
// then force the remainder. An orchestrator sends SIGTERM and waits; a
// service that exits immediately drops the requests it was already serving.
func (s *Server) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", s.server.Addr)
		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	s.logger.Info("shutting down")
	// Flipped before the listener closes, so a load balancer polling
	// readiness learns to stop sending work while this instance can still
	// answer. Closing the socket first would make in-flight requests fail
	// instead of draining.
	s.once.Do(func() { close(s.draining) })

	// Give the balancer a moment to observe the 503 and take this instance
	// out of rotation before connections start being refused.
	if s.drainDelay > 0 {
		time.Sleep(s.drainDelay)
	}

	// Long enough for a streamed reply to finish, short enough that an
	// orchestrator does not resort to SIGKILL.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		s.logger.Warn("graceful shutdown timed out; closing remaining connections", "error", err)
		return s.server.Close()
	}
	s.logger.Info("stopped")
	return nil
}
