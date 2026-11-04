package render_test

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/rahulguptaio/timlee/internal/render"
)

// A page with no caching policy of its own must still say how it may be
// cached: every response carries a fresh CSP nonce and may be personalised,
// so it is revalidated on every use and a signed-in page never lands in a
// shared cache. A handler that chose a stricter policy keeps it.
func TestRenderSetsACachingPolicy(t *testing.T) {
	files := fstest.MapFS{
		"layouts/base.gohtml": &fstest.MapFile{Data: []byte(`{{ define "base" }}{{ template "content" . }}{{ end }}`)},
		"pages/x.gohtml":      &fstest.MapFile{Data: []byte(`{{ define "content" }}x{{ end }}`)},
	}
	engine, err := render.New(render.Options{FS: files, Funcs: funcsFor(t, testManifest(t))})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	tests := []struct {
		name   string
		page   *render.Page
		preset string
		want   string
	}{
		{"visitor", &render.Page{}, "", "no-cache"},
		{"signed in", &render.Page{User: &render.User{ID: "u1"}}, "", "private, no-cache"},
		{"handler chose no-store", &render.Page{User: &render.User{ID: "u1"}}, "no-store", "no-store"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if tc.preset != "" {
				rec.Header().Set("Cache-Control", tc.preset)
			}
			if err := engine.Render(rec, 200, "x", tc.page); err != nil {
				t.Fatalf("render: %v", err)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}
}
