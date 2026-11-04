package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCompressSkipsSmallBodies(t *testing.T) {
	small := []byte(`{"ok":true}`)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	body, encoding := Compress(req, small)
	if encoding != "" {
		t.Fatalf("encoding = %q, want none: compressing %d bytes costs more than it saves", encoding, len(small))
	}
	if string(body) != string(small) {
		t.Fatal("body was altered")
	}
}

func TestCompressHonoursAcceptEncoding(t *testing.T) {
	large := []byte(`{"value":"` + strings.Repeat("compress me ", 200) + `"}`)

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"absent", "", ""},
		{"gzip accepted", "gzip", "gzip"},
		{"gzip among others", "br, gzip, deflate", "gzip"},
		{"gzip refused with q=0", "gzip;q=0", ""},
		{"unrelated encoding", "deflate", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set("Accept-Encoding", tt.header)
			}

			encoded, encoding := Compress(req, large)
			if encoding != tt.want {
				t.Fatalf("encoding = %q, want %q", encoding, tt.want)
			}
			if encoding == "gzip" && len(encoded) >= len(large) {
				t.Fatalf("compressed to %d bytes from %d: no saving", len(encoded), len(large))
			}
		})
	}
}

func TestWriteJSONServes304ForMatchingETag(t *testing.T) {
	payload := map[string]string{"model": "Qwen/Qwen2.5-7B-Instruct"}

	first := httptest.NewRecorder()
	WriteJSON(first, httptest.NewRequest(http.MethodGet, "/v1/models", nil), http.StatusOK, payload)

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag issued")
	}
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag %q is not weak; the same body may be sent gzipped or not, so a strong tag would be a lie", etag)
	}

	second := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("If-None-Match", etag)
	WriteJSON(second, req, http.StatusOK, payload)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 carried %d bytes of body", second.Body.Len())
	}
}

func TestWriteJSONOmitsETagForErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusServiceUnavailable, map[string]string{"error": "down"})

	if got := rec.Header().Get("ETag"); got != "" {
		t.Fatalf("ETag = %q on a %d; an error has no stable identity to revalidate", got, rec.Code)
	}
}

func TestOriginGuard(t *testing.T) {
	guard := OriginGuard(OriginGuardOptions{
		Allowed:     []string{"https://timlee.test"},
		Protect:     func(path string) bool { return strings.HasPrefix(path, "/api/") },
		RequireJSON: func(path string) bool { return strings.HasPrefix(path, "/api/") },
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name        string
		method      string
		path        string
		origin      string
		fetchSite   string
		contentType string
		body        string
		want        int
	}{
		{"get is always safe", http.MethodGet, "/api/v1/keys", "https://evil.test", "cross-site", "", "", http.StatusOK},
		{"allowed origin passes", http.MethodPost, "/api/v1/keys", "https://timlee.test", "same-origin", "application/json", `{"a":1}`, http.StatusOK},
		{"foreign origin refused", http.MethodPost, "/api/v1/keys", "https://evil.test", "cross-site", "application/json", `{"a":1}`, http.StatusForbidden},
		{"no origin but cross-site refused", http.MethodPost, "/api/v1/keys", "", "cross-site", "application/json", `{"a":1}`, http.StatusForbidden},
		{"non-json body refused", http.MethodPost, "/api/v1/keys", "https://timlee.test", "same-origin", "text/plain", "a=1", http.StatusUnsupportedMediaType},
		{"empty body skips type check", http.MethodPost, "/api/v1/keys", "https://timlee.test", "same-origin", "text/plain", "", http.StatusOK},
		{"unprotected path passes", http.MethodPost, "/internal/verify-key", "https://evil.test", "cross-site", "application/json", `{"a":1}`, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body *strings.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			var req *http.Request
			if body != nil {
				req = httptest.NewRequest(tt.method, tt.path, body)
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestLimiterAllowsBurstThenRefuses(t *testing.T) {
	limiter := NewLimiter(3, time.Minute, func(*http.Request) string { return "fixed" })
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	for i := range 3 {
		if !limiter.Allow(req) {
			t.Fatalf("request %d refused inside the budget", i+1)
		}
	}
	if limiter.Allow(req) {
		t.Fatal("fourth request allowed past a limit of 3")
	}
}

// Past the sweep threshold the limiter used to scan its whole map on every
// call, so each request cost O(callers) under the lock. Expired windows are
// still collected, but at most once per window.
func TestLimiterSweepsAtMostOncePerWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := NewLimiter(1<<30, time.Minute, func(r *http.Request) string { return r.Header.Get("X-Caller") })
	limiter.now = func() time.Time { return now }
	call := func(caller int) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Caller", strconv.Itoa(caller))
		limiter.Allow(r)
	}

	for caller := range limiterSweepThreshold + 1000 {
		call(caller)
	}
	if limiter.sweeps > 1 {
		t.Fatalf("%d sweeps within one window, want at most 1", limiter.sweeps)
	}

	// Two windows later every entry has expired, and the next call collects
	// them all.
	now = now.Add(2 * time.Minute)
	call(-1)
	if got := len(limiter.hits); got != 1 {
		t.Fatalf("%d windows tracked after they all expired, want 1 (the new caller)", got)
	}
}

func TestLimiterIsPerCaller(t *testing.T) {
	limiter := NewLimiter(1, time.Minute, ClientIP)

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "10.0.0.1:1234"
	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = "10.0.0.2:1234"

	if !limiter.Allow(first) || !limiter.Allow(second) {
		t.Fatal("distinct callers must not share a budget")
	}
	if limiter.Allow(first) {
		t.Fatal("first caller exceeded its own budget")
	}
}

func TestClientIPIgnoresForwardedHeaderUntilTrusted(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		trusted    bool
		want       string
	}{
		{"direct ipv4", "203.0.113.9:4321", "", false, "203.0.113.9"},
		{"direct ipv6", "[2001:db8::1]:4321", "", false, "2001:db8::1"},
		// Without a proxy in front, the header is attacker-controlled: a
		// caller could mint a fresh rate-limit bucket on every request.
		{"forged header ignored when untrusted", "10.0.0.1:1", "1.2.3.4", false, "10.0.0.1"},
		// Behind a proxy, only the entry the proxy itself appended counts.
		{"rightmost entry used when trusted", "10.0.0.1:1", "1.2.3.4, 203.0.113.9", true, "203.0.113.9"},
		{"trusted but header absent", "10.0.0.1:1", "", true, "10.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if got := clientIP(req, tt.trusted); got != tt.want {
				t.Fatalf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLimiterCannotBeBypassedByForgedForwardedFor(t *testing.T) {
	// The attack this defends against: one caller sending a different
	// X-Forwarded-For each time to get a new bucket per request.
	limiter := NewLimiter(2, time.Minute, ClientIP)

	send := func(forwarded string) bool {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "198.51.100.7:5555"
		req.Header.Set("X-Forwarded-For", forwarded)
		return limiter.Allow(req)
	}

	if !send("1.1.1.1") || !send("2.2.2.2") {
		t.Fatal("requests inside the budget were refused")
	}
	if send("3.3.3.3") {
		t.Fatal("a forged X-Forwarded-For bought a fresh rate-limit bucket")
	}
}

func TestSecurityHeaders(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "development"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			handler := SecurityHeaders(SecurityOptions{Production: production, CSP: APICSP})(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
			)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			for header, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "strict-origin-when-cross-origin",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if rec.Header().Get("Content-Security-Policy") == "" {
				t.Error("no CSP set")
			}

			hsts := rec.Header().Get("Strict-Transport-Security")
			if production && hsts == "" {
				t.Error("production response has no HSTS")
			}
			// Over plain http HSTS would pin a scheme the service cannot serve.
			if !production && hsts != "" {
				t.Errorf("HSTS = %q outside production", hsts)
			}
		})
	}
}

func TestReadinessReportsUnreadyWhileDraining(t *testing.T) {
	srv := NewServer(ServerOptions{Addr: ":0"})
	handler := srv.ReadinessHandler(nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d before shutdown, want 200", rec.Code)
	}

	// Simulates SIGTERM arriving: the process is still healthy, but a load
	// balancer must stop sending it work before the listener closes.
	srv.once.Do(func() { close(srv.draining) })

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d while draining, want 503", rec.Code)
	}
}

func TestReadinessReportsDependencyFailure(t *testing.T) {
	srv := NewServer(ServerOptions{Addr: ":0"})
	handler := srv.ReadinessHandler(func() error { return errors.New("database unreachable") })

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d with a failing dependency, want 503", rec.Code)
	}
}
