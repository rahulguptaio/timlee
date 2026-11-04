package httpx_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/httpx"
)

// benchWriter keeps the status and headers and drops the body, so a
// benchmark measures the code producing a response rather than a recorder
// growing a buffer.
type benchWriter struct {
	header http.Header
	status int
}

func newBenchWriter() *benchWriter { return &benchWriter{header: http.Header{}} }

func (w *benchWriter) Header() http.Header         { return w.header }
func (w *benchWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *benchWriter) WriteHeader(status int)      { w.status = status }

// reset readies the writer for the next iteration. Without it a header that is
// added rather than set, like Vary, would grow on every iteration.
func (w *benchWriter) reset() {
	clear(w.header)
	w.status = 0
}

type benchModel struct {
	ID            string   `json:"id"`
	Object        string   `json:"object"`
	Created       int64    `json:"created"`
	OwnedBy       string   `json:"owned_by"`
	ContextLength int      `json:"context_length"`
	InputPerM     float64  `json:"input_per_million"`
	OutputPerM    float64  `json:"output_per_million"`
	Capabilities  []string `json:"capabilities"`
}

// Bodies on either side of the 512-byte compression threshold: an 18-byte
// probe answer and a 14.5 KB model catalogue.
var (
	benchSmall = map[string]string{"status": "ready"}
	benchLarge = func() map[string]any {
		models := make([]benchModel, 64)
		for i := range models {
			models[i] = benchModel{
				ID:            fmt.Sprintf("org-%02d/model-%02d-instruct", i%7, i),
				Object:        "model",
				Created:       1767225600 + int64(i)*86400,
				OwnedBy:       "timlee",
				ContextLength: 4096 << (i % 4),
				InputPerM:     0.05 * float64(i%9+1),
				OutputPerM:    0.2 * float64(i%9+1),
				Capabilities:  []string{"chat", "completions", "prefix-cache"},
			}
		}
		return map[string]any{"object": "list", "data": models}
	}()
)

func BenchmarkWriteJSON(b *testing.B) {
	bodies := []struct {
		name  string
		value any
	}{{"small", benchSmall}, {"large", benchLarge}}
	encodings := []struct{ name, accept string }{{"identity", ""}, {"gzip", "gzip, deflate, br, zstd"}}

	for _, body := range bodies {
		for _, enc := range encodings {
			b.Run(body.name+"/"+enc.name, func(b *testing.B) {
				r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				if enc.accept != "" {
					r.Header.Set("Accept-Encoding", enc.accept)
				}
				w := newBenchWriter()

				b.ReportAllocs()
				for b.Loop() {
					w.reset()
					httpx.WriteJSON(w, r, http.StatusOK, body.value)
				}

				// Guard the path under measurement: only a large body that
				// the caller accepts compressed is worth compressing.
				wantGzip := body.name == "large" && enc.name == "gzip"
				if w.status != http.StatusOK || (w.header.Get("Content-Encoding") == "gzip") != wantGzip {
					b.Fatalf("status %d, Content-Encoding %q: not the path this benchmark measures",
						w.status, w.header.Get("Content-Encoding"))
				}
			})
		}
	}
}

// BenchmarkWriteJSONNotModified is a revalidation: the body is still encoded
// and hashed to compare validators, but nothing is compressed or sent.
func BenchmarkWriteJSONNotModified(b *testing.B) {
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := newBenchWriter()
	httpx.WriteJSON(w, r, http.StatusOK, benchLarge)
	r.Header.Set("If-None-Match", w.header.Get("ETag"))

	b.ReportAllocs()
	for b.Loop() {
		w.reset()
		httpx.WriteJSON(w, r, http.StatusOK, benchLarge)
	}

	if w.status != http.StatusNotModified {
		b.Fatalf("status %d, want 304", w.status)
	}
}

// BenchmarkOriginGuard measures the check every cookie-authenticated write
// passes through: a same-site JSON POST that is let through.
func BenchmarkOriginGuard(b *testing.B) {
	v1 := func(path string) bool { return strings.HasPrefix(path, "/v1/") }
	guard := httpx.OriginGuard(httpx.OriginGuardOptions{
		Allowed:     []string{"https://timlee.example", "https://api.timlee.example"},
		Protect:     v1,
		RequireJSON: v1,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	r := httptest.NewRequest(http.MethodPost, "/v1/rooms", strings.NewReader(`{"title":"Capacity plan"}`))
	r.Header.Set("Origin", "https://timlee.example")
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := newBenchWriter()

	b.ReportAllocs()
	for b.Loop() {
		w.reset()
		guard.ServeHTTP(w, r)
	}

	if w.status != http.StatusNoContent {
		b.Fatalf("status %d: the guard refused a same-site request", w.status)
	}
}

// BenchmarkLimiterAllow measures the limiter under contention from every
// processor. The larger caller count is past the 4096 entries at which Allow
// starts sweeping its map on each call.
func BenchmarkLimiterAllow(b *testing.B) {
	for _, callers := range []int{256, 8192} {
		b.Run(fmt.Sprintf("callers=%d", callers), func(b *testing.B) {
			limiter := httpx.NewLimiter(1<<30, time.Minute, nil)
			requests := make([]*http.Request, callers)
			for i := range requests {
				r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				r.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:52100", i>>16&255, i>>8&255, i&255)
				requests[i] = r
				// Every caller already holds a live window, as under
				// sustained traffic.
				limiter.Allow(r)
			}

			var offset atomic.Uint64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				// Goroutines start far apart so they contend on the lock,
				// not on the same window.
				i := offset.Add(1) * 7919
				for pb.Next() {
					if !limiter.Allow(requests[i%uint64(callers)]) {
						b.Error("refused under the limit")
						return
					}
					i++
				}
			})
		})
	}
}
