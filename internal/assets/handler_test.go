package assets_test

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/assets"
)

const (
	immutable  = "public, max-age=31536000, immutable"
	revalidate = "no-cache"
)

func newHandler(t *testing.T, fsys fs.FS, expose bool) http.Handler {
	t.Helper()
	return assets.NewHandler(assets.HandlerOptions{
		FS:             fsys,
		Manifest:       testManifest(t, fsys),
		ExposeManifest: expose,
	})
}

func get(t *testing.T, h http.Handler, path, acceptEncoding string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		r.Header.Set("Accept-Encoding", acceptEncoding)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result()
}

// TestCachingDecision is the whole reason content hashes exist: a hashed name
// may be cached forever because changed bytes produce a different URL, and
// anything else may not, because the same URL will one day mean something new.
func TestCachingDecision(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		expose bool
		want   string
	}{
		{
			name: "a manifest entry is immutable",
			path: "/assets/app-TVWDLM6W.js",
			want: immutable,
		},
		{
			name: "a hashed stylesheet is immutable",
			path: "/assets/app-FEPGMDJK.css",
			want: immutable,
		},
		{
			name: "a code-split chunk is immutable on its name alone",
			path: "/assets/chunk-K4NQ7X2L.js",
			want: immutable,
		},
		{
			name: "a sourcemap inherits its bundle's hash",
			path: "/assets/app-TVWDLM6W.js.map",
			want: immutable,
		},
		{
			name: "an unhashed file must be revalidated",
			path: "/assets/robots.txt",
			want: revalidate,
		},
		{
			name:   "the manifest itself must never be cached as immutable",
			path:   "/assets/manifest.json",
			expose: true,
			want:   revalidate,
		},
	}

	for sourceName, fsys := range sources(t) {
		t.Run(sourceName, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					res := get(t, newHandler(t, fsys, tc.expose), tc.path, "")
					if res.StatusCode != http.StatusOK {
						t.Fatalf("status = %d, want 200", res.StatusCode)
					}
					if got := res.Header.Get("Cache-Control"); got != tc.want {
						t.Errorf("Cache-Control = %q, want %q", got, tc.want)
					}
				})
			}
		})
	}
}

// TestContentNegotiation covers the classic bug: a pre-compressed sibling must
// be served with the *original* file's Content-Type, or the browser downloads
// an application/gzip file instead of running a script.
func TestContentNegotiation(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		acceptEncoding string
		wantEncoding   string
		wantBody       string
		wantType       string
	}{
		{
			name:         "no Accept-Encoding gets the original bytes",
			path:         "/assets/app-TVWDLM6W.js",
			wantBody:     `console.log("app");`,
			wantType:     "text/javascript; charset=utf-8",
			wantEncoding: "",
		},
		{
			name:           "gzip is served from the .gz sibling, still typed as javascript",
			path:           "/assets/app-TVWDLM6W.js",
			acceptEncoding: "gzip, deflate",
			wantBody:       "GZIP-BYTES-FOR-APP-JS",
			wantType:       "text/javascript; charset=utf-8",
			wantEncoding:   "gzip",
		},
		{
			name:           "brotli wins when both siblings exist and both are accepted",
			path:           "/assets/app-TVWDLM6W.js",
			acceptEncoding: "gzip, deflate, br",
			wantBody:       "BROTLI-BYTES-FOR-APP-JS",
			wantType:       "text/javascript; charset=utf-8",
			wantEncoding:   "br",
		},
		{
			name:           "css keeps its own type when gzipped",
			path:           "/assets/app-FEPGMDJK.css",
			acceptEncoding: "gzip",
			wantBody:       "GZIP-BYTES-FOR-APP-CSS",
			wantType:       "text/css; charset=utf-8",
			wantEncoding:   "gzip",
		},
		{
			name:           "brotli accepted but absent falls back to gzip",
			path:           "/assets/app-FEPGMDJK.css",
			acceptEncoding: "br, gzip",
			wantBody:       "GZIP-BYTES-FOR-APP-CSS",
			wantType:       "text/css; charset=utf-8",
			wantEncoding:   "gzip",
		},
		{
			name:           "no sibling at all is served as-is",
			path:           "/assets/chunk-K4NQ7X2L.js",
			acceptEncoding: "gzip, br",
			wantBody:       "export const shared = 1;",
			wantType:       "text/javascript; charset=utf-8",
			wantEncoding:   "",
		},
		{
			name:           "q=0 is a refusal, not a request",
			path:           "/assets/app-TVWDLM6W.js",
			acceptEncoding: "gzip;q=0, br;q=0",
			wantBody:       `console.log("app");`,
			wantType:       "text/javascript; charset=utf-8",
			wantEncoding:   "",
		},
		{
			name:           "a wildcard accepts the best encoding on offer",
			path:           "/assets/app-TVWDLM6W.js",
			acceptEncoding: "*",
			wantBody:       "BROTLI-BYTES-FOR-APP-JS",
			wantType:       "text/javascript; charset=utf-8",
			wantEncoding:   "br",
		},
		{
			name:           "an explicit refusal overrides the wildcard",
			path:           "/assets/app-TVWDLM6W.js",
			acceptEncoding: "*, br;q=0",
			wantBody:       "GZIP-BYTES-FOR-APP-JS",
			wantType:       "text/javascript; charset=utf-8",
			wantEncoding:   "gzip",
		},
		{
			name:           "an unhashed text file negotiates too",
			path:           "/assets/robots.txt",
			acceptEncoding: "gzip",
			wantBody:       "GZIP-BYTES-FOR-ROBOTS",
			wantType:       "text/plain; charset=utf-8",
			wantEncoding:   "gzip",
		},
	}

	for sourceName, fsys := range sources(t) {
		t.Run(sourceName, func(t *testing.T) {
			h := newHandler(t, fsys, false)
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					res := get(t, h, tc.path, tc.acceptEncoding)
					if res.StatusCode != http.StatusOK {
						t.Fatalf("status = %d, want 200", res.StatusCode)
					}
					if got := res.Header.Get("Content-Encoding"); got != tc.wantEncoding {
						t.Errorf("Content-Encoding = %q, want %q", got, tc.wantEncoding)
					}
					if got := res.Header.Get("Content-Type"); got != tc.wantType {
						t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
					}
					if got := res.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
						t.Errorf("Vary = %q, want it to include Accept-Encoding", got)
					}
					body := readBody(t, res)
					if strings.TrimSpace(body) != tc.wantBody {
						t.Errorf("body = %q, want %q", strings.TrimSpace(body), tc.wantBody)
					}
					// A compressed response declares the compressed length;
					// sending the original's would truncate or hang the read.
					if got := res.ContentLength; got != int64(len(body)) {
						t.Errorf("Content-Length = %d, want %d", got, len(body))
					}
				})
			}
		})
	}
}

// TestETagVariesByEncoding guards shared caches: one URL now has three
// representations, and reusing a validator across them serves a gzip body to a
// client that asked for none.
func TestETagVariesByEncoding(t *testing.T) {
	h := newHandler(t, embeddedFS(t), false)

	seen := map[string]string{}
	for _, enc := range []string{"", "gzip", "br"} {
		res := get(t, h, "/assets/app-TVWDLM6W.js", enc)
		tag := res.Header.Get("ETag")
		if tag == "" {
			t.Fatalf("Accept-Encoding %q: no ETag", enc)
		}
		if prev, dup := seen[tag]; dup {
			t.Errorf("Accept-Encoding %q reuses the ETag of %q: %s", enc, prev, tag)
		}
		seen[tag] = enc
	}
}

func TestHandlerRefusals(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		expose     bool
		wantStatus int
	}{
		{
			name:       "an unknown asset is a 404",
			path:       "/assets/missing-DEADBEEF.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "the manifest is hidden unless opted in",
			path:       "/assets/manifest.json",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "traversal cannot climb out of the tree",
			path:       "/assets/../../etc/passwd",
			wantStatus: http.StatusNotFound,
		},
		{
			// Cleaning turns this into /app-….js, which the StripPrefix
			// tolerance would otherwise serve from the root of the tree.
			name:       "a path that climbs out of the mount is refused, not re-rooted",
			path:       "/assets/../app-TVWDLM6W.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "the mount point itself is not a listing",
			path:       "/assets/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "writes are refused",
			method:     http.MethodPost,
			path:       "/assets/app-TVWDLM6W.js",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "a HEAD is allowed",
			method:     http.MethodHead,
			path:       "/assets/app-TVWDLM6W.js",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			r := httptest.NewRequest(method, tc.path, nil)
			w := httptest.NewRecorder()
			newHandler(t, embeddedFS(t), tc.expose).ServeHTTP(w, r)

			res := w.Result()
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			// A 404 that inherited an immutable Cache-Control would outlive
			// the deploy that fixes it.
			if res.StatusCode == http.StatusNotFound {
				if got := res.Header.Get("Cache-Control"); got == immutable {
					t.Errorf("a 404 was marked %q", got)
				}
			}
		})
	}
}

// TestHandlerMountedUnderStripPrefix keeps the handler usable either way, so a
// caller that already reaches for http.StripPrefix is not silently serving
// 404s.
func TestHandlerMountedUnderStripPrefix(t *testing.T) {
	fsys := embeddedFS(t)
	inner := assets.NewHandler(assets.HandlerOptions{FS: fsys, Manifest: testManifest(t, fsys)})

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", inner))

	res := get(t, mux, "/static/app-TVWDLM6W.js", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Cache-Control"); got != immutable {
		t.Errorf("Cache-Control = %q, want %q", got, immutable)
	}
}

// TestHandlerWithoutManifest proves the name pattern alone is enough, which is
// what lets a tree be served before its manifest is wired up.
func TestHandlerWithoutManifest(t *testing.T) {
	h := assets.NewHandler(assets.HandlerOptions{FS: embeddedFS(t)})

	if got := get(t, h, "/assets/app-TVWDLM6W.js", "").Header.Get("Cache-Control"); got != immutable {
		t.Errorf("hashed asset Cache-Control = %q, want %q", got, immutable)
	}
	if got := get(t, h, "/assets/robots.txt", "").Header.Get("Cache-Control"); got != revalidate {
		t.Errorf("unhashed asset Cache-Control = %q, want %q", got, revalidate)
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
