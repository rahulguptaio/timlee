// Package authsvc is the Timlee control plane: browser sessions, OAuth and
// OpenID Connect sign-in, organisations, API keys, the prompt allowlist and
// the audit trail.
//
// It is deliberately separate from the website. Other services keep no
// database of their own and ask this one to resolve a key or a relayed cookie
// over /internal/*.
package authsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	mailocales "github.com/rahulguptaio/timlee/internal/authsvc/locales"
	"github.com/rahulguptaio/timlee/internal/buildinfo"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/store"
)

// maxBodyBytes bounds a request body. Everything this service accepts is a
// handful of short fields, so anything larger is a mistake or an attack.
const maxBodyBytes = 64 << 10

// dataPlaneRateLimit is what the inference endpoint is told to enforce per
// caller, for keys and sessions alike.
var dataPlaneRateLimit = map[string]int{"requestsPerMinute": 600, "burst": 60}

// Service is the control plane's HTTP surface.
type Service struct {
	cfg       config.Config
	logger    *slog.Logger
	store     *store.Store
	sessions  *sessions
	mail      mailer
	i18n      *i18n.Bundle
	providers map[string]*provider
	limits    limiters
	started   time.Time
	// version is the build /health reports, the same one --version prints.
	// Resolved once: a stamp set at link time cannot change while the
	// process runs, and probes call /health often.
	version string
	handler http.Handler
	// readiness builds /readyz's handler from a check. It is installed by
	// whoever owns the listener, because only the server knows whether it
	// has begun draining. Until then /readyz answers the dependency half of
	// the question on its own.
	readiness atomic.Pointer[func(check func() error) http.Handler]
}

type limiters struct {
	general   *httpx.Limiter
	auth      *httpx.Limiter
	magicLink *httpx.Limiter
	internal  *httpx.Limiter
}

// New builds the service. Provider discovery is network I/O and happens here,
// so the process is either ready to serve sign-ins or has already said which
// ones it cannot.
func New(ctx context.Context, cfg config.Config, st *store.Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}

	mailBundle, err := i18n.Load(mailocales.FS(), cfg.Locales, cfg.PseudoLocales)
	if err != nil {
		logger.Error("email locale catalogs unavailable; using English", "error", err)
		mailBundle, err = i18n.Load(mailocales.FS(), nil, false)
		if err != nil {
			panic("authsvc: load English email catalog: " + err.Error())
		}
	}

	s := &Service{
		cfg:       cfg,
		logger:    logger,
		store:     st,
		sessions:  newSessions(st, cfg.SessionSecret, cfg.SessionTTL, cfg.IsProduction(), logger),
		mail:      mailer{outbox: cfg.MailOutbox, from: "Timlee <no-reply@timlee.example>"},
		i18n:      mailBundle,
		providers: registerProviders(ctx, cfg, logger),
		started:   time.Now(),
		version:   buildinfo.Read().Version,
		limits: limiters{
			// Broad protection for everything this service exposes.
			general: httpx.NewLimiter(300, time.Minute, nil),
			// Sign-in is the expensive, attackable surface: credential
			// stuffing, link spam and provider-side limits all land here.
			// Every attempt counts, successful ones included, which makes
			// this ceiling slightly tighter than the Node service's.
			auth: httpx.NewLimiter(20, 15*time.Minute, nil),
			// Sending mail costs money and annoys recipients. Keyed by target
			// address as well as source, so one attacker cannot flood one
			// victim from many addresses.
			magicLink: httpx.NewLimiter(5, time.Hour, func(r *http.Request) string {
				return httpx.ClientIP(r) + ":" + magicTargetOf(r)
			}),
			// Verification is a guessing oracle, so it gets a ceiling well
			// above legitimate data-plane traffic but far below what a
			// brute-force search would need. It is kept separate because
			// every caller there is the data plane on one address, and
			// sharing the browser budget would throttle all users at once.
			internal: httpx.NewLimiter(1000, time.Minute, nil),
		},
	}

	s.handler = httpx.Chain(s.routes(),
		httpx.RequestID(),
		httpx.Logging(logger),
		httpx.Recover(logger),
		httpx.SecurityHeaders(httpx.SecurityOptions{Production: cfg.IsProduction(), CSP: s.csp}),
		httpx.CORS(cfg.AllowedOrigins()),
		s.generalLimit(),
		httpx.OriginGuard(httpx.OriginGuardOptions{
			Allowed: cfg.AllowedOrigins(),
			// /internal/* is exempt: it authenticates with the shared token,
			// carries no ambient credentials of its own, and is reachable
			// only from inside the deployment. OAuth callbacks are GETs and
			// never reach the check.
			Protect:     cookieAuthenticated,
			RequireJSON: isAPI,
			Logger:      logger,
		}),
		s.withSession(),
	)

	return s
}

func (s *Service) mailLocale(id string) *i18n.Locale {
	id = strings.ToLower(strings.TrimSpace(id))
	if loc, ok := s.i18n.ByID(id); ok {
		return loc
	}
	return s.i18n.Default()
}

func (s *Service) localizedPublicPath(locale, path string) string {
	loc := s.mailLocale(locale)
	if loc.ID == i18n.DefaultID {
		return s.cfg.PublicOrigin + path
	}
	return s.cfg.PublicOrigin + "/" + loc.ID + path
}

// ServeHTTP makes the service the handler for a server.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// StartBackground runs periodic housekeeping until ctx ends.
func (s *Service) StartBackground(ctx context.Context) {
	go s.sessions.prune(ctx, 10*time.Minute)
}

// SetReadiness installs the constructor for /readyz's handler, normally the
// server's ReadinessHandler. The server owns the draining half of readiness;
// the check it is handed is the dependency half, bound to each probe's own
// context.
func (s *Service) SetReadiness(handler func(check func() error) http.Handler) {
	s.readiness.Store(&handler)
}

// Ready reports whether this instance can do its job.
//
// The database is the one dependency it genuinely cannot work without: every
// request resolves a session, and a session lives in SQLite. /health
// deliberately does not test it — a brief outage should take instances out of
// rotation, not have every one of them killed and restarted at once.
//
// The ping runs under the caller's context, so a probe that gives up stops
// it, and is capped at 2s for a caller that would otherwise wait forever.
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	return nil
}

func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	check := func() error { return s.Ready(r.Context()) }
	if handler := s.readiness.Load(); handler != nil {
		(*handler)(check).ServeHTTP(w, r)
		return
	}
	if err := check(); err != nil {
		s.logger.Warn("readiness check failed", "error", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, "Not ready.", "not_ready")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
}

// Providers lists the sign-in methods that actually registered, as opposed to
// merely being configured.
func (s *Service) Providers() []string { return providerNames(s.providers) }

func cookieAuthenticated(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/auth/")
}

func isAPI(path string) bool { return strings.HasPrefix(path, "/api/") }

// generalLimit applies the broad ceiling everywhere except /internal/, whose
// one caller would otherwise exhaust a budget shared with every browser, and
// the probe endpoints, where a 429 is read as a failure: refusing readiness
// takes the instance out of rotation and refusing liveness gets it restarted.
func (s *Service) generalLimit() httpx.Middleware {
	limited := s.limits.general.Middleware(s.logger)
	probes := map[string]bool{"/health": true, "/readyz": true}

	return func(next http.Handler) http.Handler {
		guarded := limited(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if probes[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/internal/") {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// csp picks a policy per surface: a JSON endpoint renders nothing, while the
// pages this service redirects through need a policy tied to this response.
func (s *Service) csp(r *http.Request) string {
	if isAPI(r.URL.Path) {
		return httpx.APICSP(r)
	}
	return s.pageCSP(httpx.NonceOf(r))
}

func (s *Service) pageCSP(nonce string) string {
	directives := []string{
		"default-src 'none'",
		// Only our own scripts, and only those carrying this response's nonce.
		"script-src 'self' 'nonce-" + nonce + "'",
		"style-src 'self' 'nonce-" + nonce + "'",
		"img-src 'self' data: https:",
		"font-src 'self' data:",
		"connect-src 'self' " + s.cfg.PublicOrigin,
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}
	if s.cfg.IsProduction() {
		directives = append(directives, "upgrade-insecure-requests")
	}
	return strings.Join(directives, "; ")
}

// withSession resolves the caller once per request.
//
// The cookie holds only a session id; the user, their organisations and their
// role are read back here, so a disabled account or a revoked membership
// takes effect on the next request rather than at the next sign-in.
func (s *Service) withSession() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			current, user, err := s.sessions.resolve(r, sessionParts(r))
			if err != nil {
				// Answering as signed out would send the console to sign-in,
				// and have the data plane cache a refusal, over a fault in
				// the database rather than in the session.
				s.fail(w, r, err)
				return
			}
			if current == nil {
				next.ServeHTTP(w, r)
				return
			}

			ctx := context.WithValue(r.Context(), sessionKey, current)
			if user != nil && user.DisabledAt == nil {
				ctx = context.WithValue(ctx, userKey, user)
				logx.Identify(ctx, logx.IDs{UserID: user.ID, OrgID: current.data.OrgID})
			}

			// Rolling expiry, but never on a relayed lookup: the data plane
			// presents the browser's cookie verbatim, and extending or
			// re-issuing it there would take control of the session away from
			// the browser that owns it.
			if !strings.HasPrefix(r.URL.Path, "/internal/") {
				s.sessions.touch(w, r, current)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

/* ------------------------------------------------------------- routes -- */

// sessionParts is what a route reads about the signed-in user besides the
// user, so that withSession reads it with the session, from the same
// snapshot, rather than the handler reading it again: the page view's
// session lookup and the data plane's relayed one are the busiest routes
// here.
func sessionParts(r *http.Request) store.SessionParts {
	switch r.URL.Path {
	case "/auth/session":
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return store.WithMemberships | store.WithIdentities
		}
	case "/internal/verify-session":
		if r.Method == http.MethodPost {
			return store.WithMemberships
		}
	}
	return 0
}

func (s *Service) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)

	mux.HandleFunc("GET /auth/providers", s.handleProviders)
	mux.HandleFunc("GET /auth/session", s.handleSession)
	mux.HandleFunc("GET /auth/failed", s.handleFailed)
	mux.Handle("GET /auth/{provider}/start", s.authLimited(http.HandlerFunc(s.handleOAuthStart)))
	mux.Handle("GET /auth/{provider}/callback", s.authLimited(http.HandlerFunc(s.handleOAuthCallback)))
	mux.HandleFunc("POST /auth/magic-link", s.handleMagicLinkRequest)
	mux.Handle("GET /auth/magic-link/consume", s.authLimited(http.HandlerFunc(s.handleMagicLinkConsume)))
	mux.Handle("POST /auth/desktop/session", s.authLimited(http.HandlerFunc(s.handleDesktopSession)))
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("POST /auth/org", s.handleSwitchOrg)
	if s.cfg.AllowDevLogin {
		mux.HandleFunc("POST /auth/dev", s.handleDevLogin)
	}

	mux.Handle("PATCH /api/v1/me", s.authed(s.handleUpdateMe))
	mux.Handle("GET /api/v1/me/identities", s.authed(s.handleListIdentities))
	mux.Handle("DELETE /api/v1/me/identities/{id}", s.authed(s.handleUnlinkIdentity))
	mux.Handle("GET /auth/sessions", s.authed(s.handleSessions))
	mux.Handle("POST /auth/sessions/end-others", s.authed(s.handleEndOtherSessions))
	mux.Handle("GET /api/v1/org", s.scoped(store.RoleViewer, s.handleOrg))
	mux.Handle("PATCH /api/v1/org", s.scoped(store.RoleAdmin, s.handleRenameOrg))
	mux.Handle("DELETE /api/v1/org", s.scoped(store.RoleOwner, s.handleDeleteOrg))
	mux.Handle("POST /api/v1/org/leave", s.scoped(store.RoleViewer, s.handleLeaveOrg))
	mux.Handle("POST /api/v1/org/invitations", s.scoped(store.RoleAdmin, s.handleInviteMember))
	mux.Handle("GET /api/v1/org/invitations", s.scoped(store.RoleAdmin, s.handleListInvitations))
	mux.Handle("DELETE /api/v1/org/invitations/{id}", s.scoped(store.RoleAdmin, s.handleRevokeInvitation))
	// Not org-scoped: whoever is accepting is not a member yet, and the
	// organisation comes from the invitation rather than from their session.
	mux.Handle("POST /api/v1/org/invitations/{token}/accept", s.authed(s.handleAcceptInvitation))
	mux.Handle("PATCH /api/v1/org/members/{userId}", s.scoped(store.RoleAdmin, s.handleSetMemberRole))
	mux.Handle("DELETE /api/v1/org/members/{userId}", s.scoped(store.RoleAdmin, s.handleRemoveMember))
	mux.Handle("GET /api/v1/keys", s.scoped(store.RoleAdmin, s.handleListKeys))
	mux.Handle("POST /api/v1/keys", s.scoped(store.RoleAdmin, s.handleCreateKey))
	mux.Handle("POST /api/v1/keys/{id}/rotate", s.scoped(store.RoleAdmin, s.handleRotateKey))
	mux.Handle("DELETE /api/v1/keys/{id}", s.scoped(store.RoleAdmin, s.handleRevokeKey))
	mux.Handle("GET /api/v1/audit", s.scoped(store.RoleAdmin, s.handleAudit))

	mux.Handle("POST /internal/verify-key", s.internalOnly(http.HandlerFunc(s.handleVerifyKey)))
	mux.Handle("POST /internal/verify-session", s.internalOnly(http.HandlerFunc(s.handleVerifySession)))

	// Anything unmatched, including a known path with the wrong method.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, "No route for "+r.Method+" "+r.URL.Path, "not_found")
	})

	return mux
}

func (s *Service) authLimited(next http.Handler) http.Handler {
	return s.limits.auth.Middleware(s.logger)(next)
}

/* ------------------------------------------------------------- health -- */

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"status":        "ok",
		"version":       s.version,
		"providers":     s.Providers(),
		"uptimeSeconds": int(time.Since(s.started).Seconds()),
	})
}

/* ------------------------------------------------------------ helpers -- */

// decodeJSON reads a bounded JSON body. An absent or empty body is not an
// error: several routes accept one with every field defaulted.
func decodeJSON(w http.ResponseWriter, r *http.Request, into any) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	return json.Unmarshal(body, into)
}

// safeReturnTo resolves a requested landing page, refusing anything that is
// not on our own site. Without this check the sign-in flow is an open
// redirect, and an open redirect on an authentication endpoint is how
// credentials are phished.
func (s *Service) safeReturnTo(value string) string {
	fallback := s.cfg.PublicOrigin + "/apps"
	if value == "" {
		return fallback
	}
	base, err := url.Parse(s.cfg.PublicOrigin)
	if err != nil {
		return fallback
	}
	target, err := base.Parse(value)
	if err != nil || target.Scheme != base.Scheme || target.Host != base.Host {
		return fallback
	}
	return target.String()
}

func magicTargetOf(r *http.Request) string {
	target, _ := r.Context().Value(magicTargetKey).(string)
	return target
}
