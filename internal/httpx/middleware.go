// Package httpx holds the HTTP middleware every Timlee service shares.
//
// These are the guarantees that must hold identically on the inference
// endpoint and the auth service: the same security headers, the same
// cross-site refusal, the same request identity in logs. Keeping one
// implementation means a fix lands everywhere at once.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rahulguptaio/timlee/internal/logx"
)

type contextKey int

const (
	requestIDKey contextKey = iota
	nonceKey
)

// Middleware decorates a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so the first listed runs outermost.
func Chain(h http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

/* ------------------------------------------------------------ identity -- */

// RequestID attaches an id to every request and echoes it back, so one line
// in a log can be tied to one response a user saw.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if id == "" {
				id = randomHex(16)
			}
			w.Header().Set("X-Request-Id", id)

			ctx := context.WithValue(r.Context(), requestIDKey, id)
			// A per-response nonce lets the CSP allow exactly the inline
			// script this response carries and nothing else.
			ctx = context.WithValue(ctx, nonceKey, randomHex(16))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// IDOf returns the request id, or "" outside a RequestID chain.
func IDOf(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey).(string)
	return id
}

// NonceOf returns this response's CSP nonce.
func NonceOf(r *http.Request) string {
	nonce, _ := r.Context().Value(nonceKey).(string)
	return nonce
}

/* ------------------------------------------------------------- logging -- */

// statusRecorder captures what was actually written, since net/http offers no
// way to read it back.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer, which is what
// makes Flush work for event streams through this wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Logging emits one structured line per request after it completes.
//
// It is also the request's boundary on the event channel (internal/logx):
// it opens the scope that events emitted while serving carry, and closes it
// with one request.failed event if the request failed. Neither costs
// anything while events are discarded.
func Logging(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			r = logx.Begin(r, IDOf(r))
			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(started).Milliseconds(),
				"request_id", IDOf(r),
			)
			logx.End(r, rec.status, time.Since(started))
		})
	}
}

/* ------------------------------------------------------------ recovery -- */

// Recover turns a panic into a 500 instead of a dropped connection, and logs
// the stack rather than returning it.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					// A panic after the stream started cannot be turned into
					// a status; the connection simply ends.
					logger.Error("panic", "error", v, "path", r.URL.Path, "request_id", IDOf(r))
					logx.Panic(r, v)
					WriteError(w, http.StatusInternalServerError, "Internal server error.", "server_error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

/* ---------------------------------------------------- security headers -- */

// SecurityOptions configures SecurityHeaders.
type SecurityOptions struct {
	// Production enables HSTS, which is meaningless and harmful over plain
	// http because a browser would pin a scheme the service cannot serve.
	Production bool
	// CSP is the policy for this surface. API services and HTML services
	// have genuinely different needs, so it is supplied rather than guessed.
	CSP func(r *http.Request) string
}

// SecurityHeaders sets the headers a browser needs to treat this service
// safely. This is the equivalent of Helmet, written out so each decision is
// visible and justified rather than inherited from a default.
func SecurityHeaders(opts SecurityOptions) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()

			// Stop content-type sniffing turning JSON into script.
			h.Set("X-Content-Type-Options", "nosniff")
			// Never framed: clickjacking a sign-in page is how consent is stolen.
			h.Set("X-Frame-Options", "DENY")
			// Send the origin but never the path: OAuth URLs carry code and state.
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			// Isolate the browsing context so a popup cannot reach back in.
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			h.Set("X-DNS-Prefetch-Control", "off")
			h.Set("Origin-Agent-Cluster", "?1")
			h.Set("X-Permitted-Cross-Domain-Policies", "none")

			if opts.Production {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
			}
			if opts.CSP != nil {
				h.Set("Content-Security-Policy", opts.CSP(r))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// APICSP denies everything: a JSON endpoint renders nothing.
func APICSP(*http.Request) string {
	return "default-src 'none'; frame-ancestors 'none'; base-uri 'none'"
}

/* ----------------------------------------------------------------- CORS -- */

// CORS echoes an allowed origin back with credentials enabled.
//
// Credentialed CORS cannot use "*", so the origin is matched against an
// allow-list and echoed only on a hit.
func CORS(allowed []string) Middleware {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && set[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-CSRF-Token, X-Request-Id")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Max-Age", "600")
			}
			// Caches must not hand one origin's response to another.
			w.Header().Add("Vary", "Origin")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

/* --------------------------------------------------------- cross-site -- */

// OriginGuardOptions configures OriginGuard.
type OriginGuardOptions struct {
	Allowed []string
	// Protect reports whether a path needs the check. Paths authenticated by
	// a bearer token rather than a cookie are not a CSRF target and are
	// normally excluded.
	Protect func(path string) bool
	// RequireJSON reports whether a path must receive a JSON body. A
	// cross-site HTML form can only send urlencoded, multipart or
	// text/plain, so demanding JSON forces a preflight only our origins pass.
	RequireJSON func(path string) bool
	Logger      *slog.Logger
}

// OriginGuard refuses state-changing requests that did not come from us.
//
// SameSite=Lax already keeps the session cookie off cross-site posts from
// other sites, but not from a sibling subdomain, and Lax is only a default.
// So an unsafe request must also prove its origin.
func OriginGuard(opts OriginGuardOptions) Middleware {
	allowed := make(map[string]bool, len(opts.Allowed))
	for _, o := range opts.Allowed {
		allowed[o] = true
	}
	unsafe := map[string]bool{
		http.MethodPost: true, http.MethodPut: true,
		http.MethodPatch: true, http.MethodDelete: true,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !unsafe[r.Method] || (opts.Protect != nil && !opts.Protect(r.URL.Path)) {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			switch {
			case origin != "":
				if !allowed[origin] {
					if opts.Logger != nil {
						opts.Logger.Warn("cross-site request refused",
							"path", r.URL.Path, "origin", origin, "request_id", IDOf(r))
					}
					WriteError(w, http.StatusForbidden, "Cross-site request refused.", "forbidden_origin")
					return
				}
			case r.Header.Get("Sec-Fetch-Site") == "cross-site":
				WriteError(w, http.StatusForbidden, "Cross-site request refused.", "forbidden_origin")
				return
			}

			if opts.RequireJSON != nil && opts.RequireJSON(r.URL.Path) && hasBody(r) {
				mediaType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
				if !strings.EqualFold(mediaType, "application/json") {
					WriteError(w, http.StatusUnsupportedMediaType,
						"Send a JSON body (Content-Type: application/json).", "unsupported_media_type")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// hasBody reports whether the request carries one. A POST without a body
// sends Content-Length: 0, which has nothing to type-check.
func hasBody(r *http.Request) bool {
	return r.ContentLength > 0 || r.Header.Get("Transfer-Encoding") != ""
}

/* ----------------------------------------------------------- limiting -- */

// Limiter is a fixed-window rate limiter keyed by caller.
//
// A fixed window is chosen over a token bucket because the limits here are
// coarse abuse ceilings, not traffic shaping: the extra precision would not
// change any decision, and the simpler structure is easier to reason about.
type Limiter struct {
	mu      sync.Mutex
	hits    map[string]*window
	limit   int
	window  time.Duration
	keyFunc func(*http.Request) string
	now     func() time.Time

	nextSweep time.Time
	sweeps    int // collections run, for tests
}

// limiterSweepThreshold is the map size past which Allow collects expired
// windows; below it the garbage is too small to be worth a scan.
const limiterSweepThreshold = 4096

type window struct {
	count int
	reset time.Time
}

// NewLimiter builds a limiter allowing limit requests per window per key.
func NewLimiter(limit int, per time.Duration, key func(*http.Request) string) *Limiter {
	if key == nil {
		key = ClientIP
	}
	return &Limiter{hits: map[string]*window{}, limit: limit, window: per, keyFunc: key, now: time.Now}
}

// Allow reports whether this request is within its caller's budget.
func (l *Limiter) Allow(r *http.Request) bool {
	key := l.keyFunc(r)

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	// Expired windows are collected, or the map would grow with every
	// distinct caller for the lifetime of the process — but at most once per
	// window. Scanning on every call once the map was large made each request
	// cost O(callers) under the lock, a slowdown any caller with enough
	// addresses could impose on everyone.
	if len(l.hits) > limiterSweepThreshold && !now.Before(l.nextSweep) {
		for k, w := range l.hits {
			if now.After(w.reset) {
				delete(l.hits, k)
			}
		}
		l.nextSweep = now.Add(l.window)
		l.sweeps++
	}

	w, ok := l.hits[key]
	if !ok || now.After(w.reset) {
		l.hits[key] = &window{count: 1, reset: now.Add(l.window)}
		return true
	}
	w.count++
	return w.count <= l.limit
}

// Middleware refuses requests over budget with a 429.
func (l *Limiter) Middleware(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(r) {
				if logger != nil {
					logger.Warn("rate limited", "path", r.URL.Path, "ip", ClientIP(r))
				}
				w.Header().Set("Retry-After", "60")
				WriteError(w, http.StatusTooManyRequests,
					"Too many requests. Slow down and try again shortly.", "rate_limit")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP resolves the caller's address.
//
// X-Forwarded-For is client-supplied, so it is believed only when a proxy is
// known to be in front and to be rewriting it. Trusting it unconditionally
// would let any caller forge an address and mint a fresh rate-limit bucket
// per request, which defeats every limiter at once.
func ClientIP(r *http.Request) string {
	return clientIP(r, trustProxy.Load())
}

func clientIP(r *http.Request, trusted bool) string {
	if trusted {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// The rightmost entry is the one our own proxy appended; anything
			// to the left of it came from the caller and may be invented.
			parts := strings.Split(forwarded, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	// RemoteAddr is host:port, but an IPv6 host is itself full of colons and
	// arrives bracketed, so it cannot be split on the first one.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// trustProxy records whether a forwarding proxy is in front of this process.
// Stored as a package-level flag because ClientIP is passed as a bare
// key function to limiters that have no configuration of their own.
var trustProxy atomic.Bool

// SetTrustProxy declares whether X-Forwarded-* headers may be believed. Call
// it once at startup from configuration; it defaults to false.
func SetTrustProxy(trust bool) { trustProxy.Store(trust) }

// TrustingProxy reports the current setting, for handlers that need to make
// the same judgement about X-Forwarded-Proto.
func TrustingProxy() bool { return trustProxy.Load() }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; a timestamp keeps the id
		// unique enough for correlation if it ever did.
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}
