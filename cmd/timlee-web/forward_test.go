package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rahulguptaio/timlee/internal/httpx"
)

// recordForwardedFor is an upstream that remembers the X-Forwarded-For of the
// last request it served.
func recordForwardedFor(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Forwarded-For")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(upstream.Close)
	return upstream, func() string { return got }
}

// trustProxy sets the process-wide proxy trust for one test.
func trustProxy(t *testing.T, trust bool) {
	t.Helper()
	before := httpx.TrustingProxy()
	httpx.SetTrustProxy(trust)
	t.Cleanup(func() { httpx.SetTrustProxy(before) })
}

// The control plane rate-limits per client address. Calls this site
// makes on a reader's behalf must carry that reader's address, or every
// reader shares the budget of this one process and the whole site degrades
// together once it runs out.
func TestCallsOnAReadersBehalfCarryTheirAddress(t *testing.T) {
	tests := []struct {
		name      string
		trust     bool
		forwarded string
		want      string
	}{
		{"direct connection", false, "", "203.0.113.7"},
		{"a header from the reader is ignored without a proxy", false, "198.51.100.9", "203.0.113.7"},
		// Behind our own proxy the rightmost entry is the one it appended;
		// anything to its left came from the reader and is not forwarded.
		{"behind the proxy", true, "198.51.100.9, 192.0.2.44", "192.0.2.44"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trustProxy(t, tc.trust)
			upstream, got := recordForwardedFor(t)
			client := newAuthClient(upstream.URL, "https://timlee.example", slog.New(slog.DiscardHandler))

			handler := withClientIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream.URL+"/auth/session", nil)
				if err != nil {
					t.Fatal(err)
				}
				// Whatever an outbound request was given is replaced.
				req.Header.Set("X-Forwarded-For", "6.6.6.6")
				resp, err := client.http.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			}))
			r := httptest.NewRequest(http.MethodGet, "/settings", nil)
			r.RemoteAddr = "203.0.113.7:51234"
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			handler.ServeHTTP(httptest.NewRecorder(), r)

			if got() != tc.want {
				t.Fatalf("X-Forwarded-For upstream = %q, want %q", got(), tc.want)
			}
		})
	}
}
