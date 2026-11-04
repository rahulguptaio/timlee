package assets_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/rahulguptaio/timlee/internal/assets"
)

// benchWriter keeps the status and headers and drops the body.
type benchWriter struct {
	header http.Header
	status int
}

func (w *benchWriter) Header() http.Header         { return w.header }
func (w *benchWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *benchWriter) WriteHeader(status int)      { w.status = status }

// ReadFrom copies through a pooled buffer, as net/http's own writer does;
// without it io.Copy would charge each response a fresh buffer.
func (w *benchWriter) ReadFrom(r io.Reader) (int64, error) { return io.Copy(io.Discard, r) }

// BenchmarkServeBrotli serves a stylesheet to a browser that accepts brotli,
// which the handler answers from the pre-compressed sidecar. The sizes match
// the real build: a 36 KB stylesheet with 5.6 KB and 7 KB sidecars.
func BenchmarkServeBrotli(b *testing.B) {
	css := bytes.Repeat([]byte(".card{display:grid;gap:var(--ts-space-3)}\n"), 860)
	fsys := fstest.MapFS{
		"app-EI56JATP.css":    {Data: css},
		"app-EI56JATP.css.br": {Data: bytes.Repeat([]byte{0x8b}, 5666)},
		"app-EI56JATP.css.gz": {Data: bytes.Repeat([]byte{0x1f}, 7038)},
	}
	manifest := assets.NewManifest("/assets/")
	manifest.Add("app.css", "app-EI56JATP.css", css)
	handler := assets.NewHandler(assets.HandlerOptions{FS: fsys, Manifest: manifest})

	r := httptest.NewRequest(http.MethodGet, "/assets/app-EI56JATP.css", nil)
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	w := &benchWriter{header: http.Header{}}

	b.ReportAllocs()
	for b.Loop() {
		clear(w.header)
		handler.ServeHTTP(w, r)
	}

	if w.status != http.StatusOK || w.header.Get("Content-Encoding") != "br" {
		b.Fatalf("status %d, Content-Encoding %q: the brotli sidecar was not served",
			w.status, w.header.Get("Content-Encoding"))
	}
}

// BenchmarkManifestLookup resolves a logical name the way every template
// does for each stylesheet and script tag.
func BenchmarkManifestLookup(b *testing.B) {
	manifest := assets.NewManifest("/assets/")
	for i := range 32 {
		manifest.Add(fmt.Sprintf("chunks/route-%02d.js", i), fmt.Sprintf("chunks/route-%02d-K4NQ7X2L.js", i), []byte("export{}"))
	}
	manifest.Add("app.css", "app-EI56JATP.css", []byte("body{}"))
	manifest.Add("app.js", "app-TVWDLM6W.js", []byte("export{}"))

	b.ReportAllocs()
	for b.Loop() {
		if manifest.URL("app.js") == "" {
			b.Fatal("app.js is missing from the manifest")
		}
	}
}
