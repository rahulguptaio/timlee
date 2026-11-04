package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"

	"github.com/rahulguptaio/timlee/internal/assets"
)

// TestServedEncodingsEndToEnd runs the real encoder through the real handler.
//
// The sidecars only pay for themselves if they reach a browser as they were
// written. Anything that silently recompresses in between throws away the
// quality-11 pass this build spends, so the proof has to be the whole path:
// build the file, ask for it the way a client would, and decode what comes
// back to the bytes that went in.
func TestServedEncodingsEndToEnd(t *testing.T) {
	root, src, out := fixture(t)
	m := build(t, options{Root: root, Src: src, Out: out, Base: "/assets/"})

	handler := assets.NewHandler(assets.HandlerOptions{
		FS:       assets.Dir(out),
		Manifest: m,
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// DisableCompression stops the transport adding its own Accept-Encoding
	// and transparently decoding, which would hide exactly what is under test.
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	negotiations := []struct {
		name           string
		acceptEncoding string
		wantEncoding   string
	}{
		{
			name:           "brotli is preferred when offered",
			acceptEncoding: "br, gzip",
			wantEncoding:   "br",
		},
		{
			name:           "a gzip-only client still gets gzip",
			acceptEncoding: "gzip",
			wantEncoding:   "gzip",
		},
		{
			name: "br;q=0 is a refusal, not a preference",
			// A client that cannot decode brotli says so this way; reading
			// the token as consent would send it bytes it cannot read.
			acceptEncoding: "br;q=0, gzip",
			wantEncoding:   "gzip",
		},
		{
			name:           "no Accept-Encoding gets the original bytes",
			acceptEncoding: "",
			wantEncoding:   "",
		},
	}

	for _, logical := range []string{"app.css", "app.js"} {
		entry, ok := m.Lookup(logical)
		if !ok {
			t.Fatalf("manifest has no %q, only %v", logical, m.Names())
		}
		source, err := os.ReadFile(filepath.Join(out, entry.File))
		if err != nil {
			t.Fatal(err)
		}
		wantType := map[string]string{
			"app.css": "text/css; charset=utf-8",
			"app.js":  "text/javascript; charset=utf-8",
		}[logical]

		t.Run(logical, func(t *testing.T) {
			for _, tc := range negotiations {
				t.Run(tc.name, func(t *testing.T) {
					req, err := http.NewRequest(http.MethodGet, srv.URL+entry.Path, nil)
					if err != nil {
						t.Fatal(err)
					}
					if tc.acceptEncoding != "" {
						req.Header.Set("Accept-Encoding", tc.acceptEncoding)
					}
					res, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer res.Body.Close()

					if res.StatusCode != http.StatusOK {
						t.Fatalf("status = %s", res.Status)
					}
					if got := res.Header.Get("Content-Encoding"); got != tc.wantEncoding {
						t.Errorf("Content-Encoding = %q, want %q", got, tc.wantEncoding)
					}
					// The type must describe the asset, not the container it
					// travelled in: a .br served as application/brotli is a
					// download prompt instead of a stylesheet.
					if got := res.Header.Get("Content-Type"); got != wantType {
						t.Errorf("Content-Type = %q, want %q", got, wantType)
					}
					if got := res.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
						t.Errorf("Vary = %q, want it to include Accept-Encoding", got)
					}

					body, err := io.ReadAll(res.Body)
					if err != nil {
						t.Fatal(err)
					}
					if int64(len(body)) != res.ContentLength {
						t.Errorf("read %d bytes, Content-Length said %d", len(body), res.ContentLength)
					}

					decoded := decode(t, tc.wantEncoding, body)
					if !bytes.Equal(decoded, source) {
						t.Errorf("%s response does not decode back to %s", tc.wantEncoding, entry.File)
					}
					if tc.wantEncoding != "" && len(body) >= len(source) {
						t.Errorf("%s body is %d bytes, no smaller than the %d it encodes",
							tc.wantEncoding, len(body), len(source))
					}
				})
			}

			// Brotli is only written when it beats gzip, so the response a
			// browser actually receives must be the smaller of the two.
			br := size(t, filepath.Join(out, entry.File+".br"))
			gz := size(t, filepath.Join(out, entry.File+".gz"))
			if br >= gz {
				t.Errorf("%s: brotli %d bytes is not smaller than gzip %d", entry.File, br, gz)
			}
			t.Logf("%-8s raw %6d  gzip %6d (%.1f%%)  brotli %6d (%.1f%%)",
				logical, len(source),
				gz, 100*float64(gz)/float64(len(source)),
				br, 100*float64(br)/float64(len(source)))
		})
	}
}

// TestBrotliIsKeptOnlyWhenItWins guards the disk cost: a sidecar that loses to
// gzip is never served, so writing it would be waste.
func TestBrotliIsKeptOnlyWhenItWins(t *testing.T) {
	root, src, out := fixture(t)
	build(t, options{Root: root, Src: src, Out: out, Base: "/assets/"})

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".br") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".br")
		br := size(t, filepath.Join(out, e.Name()))
		switch {
		case br >= size(t, filepath.Join(out, base)):
			t.Errorf("%s is larger than the file it compresses", e.Name())
		case br >= size(t, filepath.Join(out, base+".gz")):
			t.Errorf("%s is larger than %s.gz, so it would never be the better answer", e.Name(), base)
		}
	}
}

func decode(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	var r io.Reader = bytes.NewReader(body)
	switch encoding {
	case "br":
		r = brotli.NewReader(bytes.NewReader(body))
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("response is not valid gzip: %v", err)
		}
		r = gz
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decode %s: %v", encoding, err)
	}
	return out
}

func size(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return int(info.Size())
}
