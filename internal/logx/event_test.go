package logx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func value(t *testing.T, r Record, key string) string {
	t.Helper()
	v, ok := r.Attr(key)
	if !ok {
		t.Fatalf("%s has no %q: %v", r.Name, key, r.Attrs)
	}
	return v.String()
}

func TestEventsCarryTheProcessAndTheRequestIDs(t *testing.T) {
	events := Capture(t)
	New(Options{Writer: io.Discard, Service: "timlee-test", Version: "v1.2.3", Commit: "abc123", Env: "test"})

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	r = Begin(r, "req-123")
	Identify(r.Context(), IDs{UserID: "usr_1", OrgID: "org_1"})
	// A context derived further down the chain still reaches the request.
	type innerKey struct{}
	inner := context.WithValue(r.Context(), innerKey{}, true)
	Identify(inner, IDs{KeyID: "key_1"})

	Event(inner, "thing.happened", slog.Int("count", 3), slog.String("org_id", "org_explicit"))

	got := events.Records("thing.happened")
	if len(got) != 1 {
		t.Fatalf("want one event, got %v", events.Records())
	}
	rec := got[0]
	host, _ := os.Hostname()
	for key, want := range map[string]string{
		"service": "timlee-test", "version": "v1.2.3", "commit": "abc123", "env": "test", "instance": host,
		"request_id": "req-123", "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"user_id": "usr_1", "key_id": "key_1", "count": "3",
		// The event's own value wins over the request's, and appears once.
		"org_id": "org_explicit",
	} {
		if got := value(t, rec, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if n := strings.Count(fmt.Sprint(rec.Attrs), "org_id="); n != 1 {
		t.Errorf("org_id appears %d times: %v", n, rec.Attrs)
	}

	// Outside a request an event carries the process and its own attributes.
	Event(context.Background(), "thing.happened")
	if _, ok := events.Records("thing.happened")[1].Attr("request_id"); ok {
		t.Error("an event outside a request has a request id")
	}
}

func TestTraceID(t *testing.T) {
	const valid = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	derived := traceID("", "not-hex-request-id")
	cases := []struct{ name, traceparent, requestID, want string }{
		{"traceparent wins", valid, "abc", "4bf92f3577b34da6a3ce929d0e0e4736"},
		{"a later version may append fields", valid + "-extra", "", "4bf92f3577b34da6a3ce929d0e0e4736"},
		{"version ff is invalid", "ff" + valid[2:], "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"},
		{"an all-zero trace id is invalid", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "", ""},
		{"upper-case hex is invalid", strings.ToUpper(valid), "", ""},
		{"a generated request id is already a trace id", "", "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"},
		{"any other request id is hashed", "", "not-hex-request-id", derived},
	}
	for _, tc := range cases {
		if got := traceID(tc.traceparent, tc.requestID); got != tc.want {
			t.Errorf("%s: traceID = %q, want %q", tc.name, got, tc.want)
		}
	}
	if len(derived) != 32 || !isLowerHex(derived) || derived != traceID("", "not-hex-request-id") {
		t.Errorf("derived trace id %q is not a stable 32-digit hex id", derived)
	}
}

func TestEndReportsEachFailedRequestOnce(t *testing.T) {
	start := func(path string) *http.Request {
		return Begin(httptest.NewRequest(http.MethodGet, path, nil), "req-"+path)
	}
	// The mux sets the pattern on the request it hands the handler, which a
	// middleware may have copied: Fail reads it there.
	routed := func(r *http.Request, pattern string) *http.Request {
		inner := r.WithContext(r.Context())
		inner.Pattern = pattern
		return inner
	}

	t.Run("success and client errors emit nothing", func(t *testing.T) {
		events := Capture(t)
		End(start("/ok"), http.StatusOK, time.Millisecond)
		End(start("/missing"), http.StatusNotFound, time.Millisecond)
		if got := events.Records(); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("a 5xx with no recorded cause", func(t *testing.T) {
		events := Capture(t)
		End(start("/readyz"), http.StatusServiceUnavailable, 3*time.Millisecond)
		got := events.Records("request.failed")
		if len(got) != 1 {
			t.Fatalf("got %v", events.Records())
		}
		for key, want := range map[string]string{"route": "unknown", "status": "503", "method": "GET", "request_id": "req-/readyz"} {
			if v := value(t, got[0], key); v != want {
				t.Errorf("%s = %q, want %q", key, v, want)
			}
		}
	})

	t.Run("a recorded failure, even on a stream that began 200", func(t *testing.T) {
		events := Capture(t)
		r := start("/v1/chat/completions")
		cause := fmt.Errorf("upstream: %w", io.ErrUnexpectedEOF)
		Fail(routed(r, "POST /v1/chat/completions"), cause)
		Fail(routed(r, "POST /v1/chat/completions"), errors.New("a consequence"))
		End(r, http.StatusOK, time.Millisecond)

		got := events.Records("request.failed")
		if len(got) != 1 {
			t.Fatalf("got %v", events.Records())
		}
		if v := value(t, got[0], "route"); v != "POST /v1/chat/completions" {
			t.Errorf("route = %q", v)
		}
		errValue, _ := got[0].Attr("error")
		if text := errValue.String(); !strings.Contains(text, "upstream: unexpected EOF") ||
			!strings.Contains(text, "*fmt.wrapError *errors.errorString") {
			t.Errorf("error = %s", text)
		}
	})

	t.Run("a panic carries its stack", func(t *testing.T) {
		events := Capture(t)
		r := start("/boom")
		func() {
			defer func() { Panic(routed(r, "GET /boom"), recover()) }()
			panic("kaboom")
		}()
		End(r, http.StatusInternalServerError, time.Millisecond)

		got := events.Records("request.failed")
		if len(got) != 1 {
			t.Fatalf("got %v", events.Records())
		}
		if v := value(t, got[0], "panic"); v != "true" {
			t.Errorf("panic = %q", v)
		}
		if v := value(t, got[0], "stack"); !strings.Contains(v, "TestEndReportsEachFailedRequestOnce") {
			t.Errorf("stack does not reach the panic: %s", v)
		}
		if v, _ := got[0].Attr("error"); !strings.Contains(v.String(), "panic: kaboom") {
			t.Errorf("error = %s", v)
		}
		if v := value(t, got[0], "route"); v != "GET /boom" {
			t.Errorf("route = %q", v)
		}
	})
}
