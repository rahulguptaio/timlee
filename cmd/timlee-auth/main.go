// Command timlee-auth runs the Timlee control plane: browser sessions,
// OAuth and OpenID Connect sign-in, organisations, API keys and audit.
//
// Configuration comes entirely from the environment (see internal/config).
// The process refuses to start with development defaults when it believes it
// is serving real users, because a service running with a known session
// secret is worse than one that does not run at all.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/rahulguptaio/timlee/internal/authsvc"
	"github.com/rahulguptaio/timlee/internal/buildinfo"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/store"
)

func main() {
	buildinfo.HandleVersionFlag("timlee-auth")
	cfg := config.Load()

	info := buildinfo.Read()
	// Batched: a request line costs a copy, not a write system call made
	// under the logger's lock. Warnings and errors are written at once.
	logOut := httpx.NewLogWriter(os.Stdout)
	defer logOut.Close()
	logger := logx.New(logx.Options{
		Level: levelFor(cfg), Writer: logOut,
		Service: "timlee-auth", Version: info.Version, Commit: info.Commit, Env: cfg.Env,
	})
	slog.SetDefault(logger)

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Declared once, before anything reads a forwarded header: an
	// X-Forwarded-For or X-Forwarded-Proto from an untrusted client is a
	// claim, not a fact, and believing it lets a caller forge its own address
	// or pass a plain-http request off as secure.
	httpx.SetTrustProxy(cfg.TrustProxy)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		logger.Error("could not open the database", "path", cfg.DatabasePath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Provider discovery happens during New, so by the time the listener
	// opens the service already knows which sign-in buttons it can honour.
	service := authsvc.New(ctx, cfg, db, logger)
	service.StartBackground(ctx)

	logger.Info("timlee auth service starting",
		"addr", cfg.ControlAddr,
		"env", cfg.Env,
		"providers", service.Providers(),
		"dev_login", cfg.AllowDevLogin,
		"trust_proxy", cfg.TrustProxy,
	)

	server := httpx.NewServer(httpx.ServerOptions{
		Addr:    cfg.ControlAddr,
		Handler: service,
		Logger:  logger,
		// Long enough for a load balancer to see /readyz turn 503 and take
		// this instance out of rotation before the listener closes. Without
		// the pause it discovers the shutdown by failing requests it has
		// already routed here.
		DrainDelay: drainDelay(cfg),
	})
	// The server owns the draining half of readiness; the service owns the
	// dependency half, checked under each probe's own context. /readyz has
	// to answer both, so they are joined here.
	service.SetReadiness(server.ReadinessHandler)

	if err := server.Run(); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// drainDelay pauses shutdown only where a load balancer is watching. Added to
// the 20-second forced close it must stay under the units' TimeoutStopSec, or
// systemd kills the process mid-drain. Locally nothing polls /readyz, and a
// pause on every Ctrl-C is just friction.
func drainDelay(cfg config.Config) time.Duration {
	if cfg.IsProduction() {
		return 5 * time.Second
	}
	return 0
}

func levelFor(cfg config.Config) slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	if cfg.IsProduction() {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}
