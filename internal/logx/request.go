package logx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

// IDs are who a request acts for: opaque identifiers the stores mint, never
// a name, an email address or a label.
type IDs struct {
	UserID string
	OrgID  string
	KeyID  string
}

type scopeKey struct{}

// scope is one HTTP request as the event channel sees it. It is created at
// the request's boundary and filled in as the request is served, which is
// why it is shared rather than copied into each derived context: ids learned
// when a handler authenticates the caller, deep inside the chain, must reach
// the event the boundary emits after the handler returns.
type scope struct {
	requestID string
	traceID   string

	mu       sync.Mutex
	ids      IDs
	route    string
	err      error
	panicked bool
	stack    string
}

func scopeOf(ctx context.Context) *scope {
	s, _ := ctx.Value(scopeKey{}).(*scope)
	return s
}

// attrs are the ids every event of the request carries.
func (s *scope) attrs() []slog.Attr {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	ids := s.ids
	s.mu.Unlock()

	attrs := make([]slog.Attr, 0, 5)
	for _, attr := range []slog.Attr{
		slog.String("request_id", s.requestID),
		slog.String("trace_id", s.traceID),
		slog.String("user_id", ids.UserID),
		slog.String("org_id", ids.OrgID),
		slog.String("key_id", ids.KeyID),
	} {
		if attr.Value.String() != "" {
			attrs = append(attrs, attr)
		}
	}
	return attrs
}

// maxRequestID bounds a client-supplied X-Request-Id before an event carries
// it.
const maxRequestID = 128

// Begin starts r's event scope, identified by requestID: httpx.Logging calls
// it as a request arrives. While events are discarded it returns r as it is,
// so the request path pays nothing for a channel nobody reads.
func Begin(r *http.Request, requestID string) *http.Request {
	if !Enabled() {
		return r
	}
	if len(requestID) > maxRequestID {
		requestID = requestID[:maxRequestID]
	}
	s := &scope{requestID: requestID, traceID: traceID(r.Header.Get("traceparent"), requestID)}
	return r.WithContext(context.WithValue(r.Context(), scopeKey{}, s))
}

// End closes the scope Begin started once the response is written. A request
// that answered 5xx, or that a handler marked with Fail or Panic, emits one
// request.failed event: the single place a failure is reported, however many
// layers saw it.
func End(r *http.Request, status int, elapsed time.Duration) {
	if !Enabled() {
		return
	}
	s := scopeOf(r.Context())
	if s == nil {
		return
	}
	s.mu.Lock()
	failure, route, panicked, stack := s.err, s.route, s.panicked, s.stack
	s.mu.Unlock()
	if status < http.StatusInternalServerError && failure == nil {
		return
	}

	// The route a handler reported wins: a middleware that wraps the request
	// before it reaches the mux keeps the matched pattern from this copy.
	if route == "" {
		route = r.Pattern
	}
	if route == "" {
		route = "unknown"
	}
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("route", route),
		slog.Int("status", status),
		slog.Int64("duration_ms", elapsed.Milliseconds()),
	}
	if failure != nil {
		attrs = append(attrs, Err(failure))
	}
	if panicked {
		attrs = append(attrs, slog.Bool("panic", true), slog.String("stack", stack))
	}
	Event(r.Context(), "request.failed", attrs...)
}

// Identify records who the request in ctx acts for, once it has been
// authenticated. Every event the request emits afterwards carries the ids,
// its request.failed event included. Empty fields leave what is known as it
// is. Outside a scope it does nothing.
func Identify(ctx context.Context, ids IDs) {
	s := scopeOf(ctx)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ids.UserID != "" {
		s.ids.UserID = ids.UserID
	}
	if ids.OrgID != "" {
		s.ids.OrgID = ids.OrgID
	}
	if ids.KeyID != "" {
		s.ids.KeyID = ids.KeyID
	}
}

// Fail records why r is failing, for the request.failed event its boundary
// emits: err's chain and the route r matched. Call it where an error becomes
// a response, instead of emitting an event of your own. The first failure is
// kept; later ones are its consequences.
func Fail(r *http.Request, err error) {
	s := scopeOf(r.Context())
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
		s.route = r.Pattern
	}
}

// maxStack bounds the stack a panic's event carries.
const maxStack = 8 << 10

// Panic records a recovered panic for the request.failed event: its value and
// the stack of the goroutine that panicked. Call it from the deferred
// function that recovered, so the stack still holds the panic.
func Panic(r *http.Request, value any) {
	s := scopeOf(r.Context())
	if s == nil {
		return
	}
	err, ok := value.(error)
	if ok {
		err = fmt.Errorf("panic: %w", err)
	} else {
		err = errors.New("panic: " + truncate(fmt.Sprint(value), maxMessage))
	}
	stack := debug.Stack()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.err, s.panicked = err, true
	s.stack = truncate(string(stack), maxStack)
	if s.route == "" {
		s.route = r.Pattern
	}
}

// traceID is the W3C trace id a traceparent header carries or, without a
// valid one, one derived from the request id. Every service sees the same
// X-Request-Id for one reader's request, so the derived id correlates them
// too.
func traceID(traceparent, requestID string) string {
	// version "-" trace-id "-" parent-id "-" flags, e.g.
	// 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
	if len(traceparent) >= 55 && traceparent[2] == '-' && traceparent[35] == '-' && traceparent[52] == '-' {
		version, id, parent := traceparent[:2], traceparent[3:35], traceparent[36:52]
		if isLowerHex(version) && version != "ff" && isLowerHex(id) && !allZero(id) &&
			isLowerHex(parent) && !allZero(parent) {
			return id
		}
	}
	if requestID == "" {
		return ""
	}
	if len(requestID) == 32 && isLowerHex(requestID) {
		return requestID
	}
	sum := sha256.Sum256([]byte(requestID))
	return hex.EncodeToString(sum[:16])
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}

func allZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}
