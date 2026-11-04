package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/web/public"
)

func rootFileServer(t *testing.T) http.Handler {
	t.Helper()
	files, err := loadRootFiles(public.FS(), "https://timlee.example/")
	if err != nil {
		t.Fatalf("load root files: %v", err)
	}
	mux := http.NewServeMux()
	handleRootFiles(mux, files)
	mux.HandleFunc("/", http.NotFound)
	return mux
}

func TestRootFilesAreServedWithARevalidatingCachePolicy(t *testing.T) {
	srv := rootFileServer(t)

	for path, wantType := range map[string]string{
		"/favicon.svg":          "image/svg+xml",
		"/icon-32.png":          "image/png",
		"/icon-192.png":         "image/png",
		"/icon-512.png":         "image/png",
		"/apple-touch-icon.png": "image/png",
		"/site.webmanifest":     "application/manifest+json",
		"/robots.txt":           "text/plain; charset=utf-8",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != wantType {
				t.Errorf("Content-Type = %q, want %q", got, wantType)
			}
			// Unhashed names must never be promised immutable: the next
			// deploy may change the bytes behind the same URL.
			if got := rec.Header().Get("Cache-Control"); got != rootFileCacheControl {
				t.Errorf("Cache-Control = %q", got)
			}
			etag := rec.Header().Get("ETag")
			if etag == "" {
				t.Fatal("no ETag")
			}

			revalidate := httptest.NewRequest(http.MethodGet, path, nil)
			revalidate.Header.Set("If-None-Match", etag)
			again := httptest.NewRecorder()
			srv.ServeHTTP(again, revalidate)
			if again.Code != http.StatusNotModified {
				t.Errorf("revalidation status = %d, want 304", again.Code)
			}
		})
	}

	if got := rootFileCacheControl; strings.Contains(got, "immutable") {
		t.Errorf("root files are marked immutable: %q", got)
	}
}

func TestRobotsNamesTheSitemapOnThisOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	rootFileServer(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "Sitemap: https://timlee.example/sitemap.xml\n") {
		t.Errorf("robots.txt = %q", body)
	}
	if strings.Contains(body, "Disallow") {
		t.Errorf("robots.txt blocks crawling, which would hide noindex from crawlers: %q", body)
	}
}

func TestDocumentHeadLinksTheRootFilesAndSpeculationRules(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/", "/sign-in"} {
		t.Run(path, func(t *testing.T) {
			rec := h.get(path)
			body := rec.Body.String()
			for _, fragment := range []string{
				`<link rel="icon" type="image/svg+xml" href="/favicon.svg" />`,
				`<link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png" />`,
				`<link rel="manifest" href="/site.webmanifest" />`,
				`<script type="speculationrules" nonce="` + nonceFrom(rec.Header().Get("Content-Security-Policy")) + `">`,
				`"eagerness": "moderate"`,
			} {
				if !strings.Contains(body, fragment) {
					t.Errorf("missing %q", fragment)
				}
			}

			// A rule set the browser cannot parse is silently ignored, so
			// prove the block is JSON after html/template has seen it.
			_, rest, _ := strings.Cut(body, `<script type="speculationrules"`)
			_, rules, _ := strings.Cut(rest, ">")
			rules, _, _ = strings.Cut(rules, "</script>")
			if !json.Valid([]byte(rules)) {
				t.Errorf("speculation rules are not valid JSON: %s", rules)
			}
		})
	}
}
