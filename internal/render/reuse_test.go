package render_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/rahulguptaio/timlee/internal/render"
)

// The layouts and partials are parsed once and shared by every page, and the
// buffers pages render into are reused across requests. These tests pin what
// that reuse must never change.

func reuseFixture() fstest.MapFS {
	return fstest.MapFS{
		"layouts/base.gohtml": &fstest.MapFile{Data: []byte(
			`{{ define "base" }}<main>{{ template "content" . }}</main>{{ block "extra" . }}{{ end }}{{ end }}`)},
		// A layout named like a page, as the console's is.
		"layouts/console.gohtml": &fstest.MapFile{Data: []byte(`{{ define "console" }}<app>{{ template "content" . }}</app>{{ end }}`)},
		"partials/nav.gohtml":    &fstest.MapFile{Data: []byte(`{{ define "nav" }}<nav>{{ .Title }}</nav>{{ end }}`)},
		"pages/a.gohtml":         &fstest.MapFile{Data: []byte(`{{ define "content" }}{{ template "nav" . }}page a{{ end }}`)},
		"pages/b.gohtml": &fstest.MapFile{Data: []byte(
			`{{ define "content" }}page b {{ .Title }}{{ end }}{{ define "extra" }}<aside>b only</aside>{{ end }}`)},
		"pages/console.gohtml": &fstest.MapFile{Data: []byte(`{{ define "content" }}console page{{ end }}`)},
		"pages/broken.gohtml":  &fstest.MapFile{Data: []byte(`{{ define "content" }}partial output{{ .Missing.Field }}{{ end }}`)},
	}
}

// Every page defines "content"; sharing the layouts must not let one page's
// definitions reach another's set, nor a page's name displace a layout's.
func TestPagesKeepTheirOwnDefinitions(t *testing.T) {
	engine, err := render.New(render.Options{FS: reuseFixture()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	tests := []struct {
		page, layout, want string
	}{
		{"a", "", "<main><nav>T</nav>page a</main>"},
		{"b", "", "<main>page b T</main><aside>b only</aside>"},
		{"console", "console", "<app>console page</app>"},
		{"a", "console", "<app><nav>T</nav>page a</app>"},
	}
	for _, tc := range tests {
		body, err := engine.HTML(tc.page, &render.Page{Title: "T", Layout: tc.layout})
		if err != nil {
			t.Fatalf("%s in %q: %v", tc.page, tc.layout, err)
		}
		if string(body) != tc.want {
			t.Errorf("%s in %q = %q, want %q", tc.page, tc.layout, body, tc.want)
		}
	}
}

// A template that fails part-way has already written into its buffer. That
// output must not survive into the next page rendered from the same buffer.
func TestFailedRenderLeavesNothingBehind(t *testing.T) {
	engine, err := render.New(render.Options{FS: reuseFixture()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	for range 50 {
		rec := httptest.NewRecorder()
		if err := engine.Render(rec, http.StatusOK, "broken", &render.Page{}); err == nil {
			t.Fatal("want an execution error from the broken page")
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("a failed render wrote %d bytes", rec.Body.Len())
		}

		rec = httptest.NewRecorder()
		if err := engine.Render(rec, http.StatusOK, "a", &render.Page{Title: "T"}); err != nil {
			t.Fatalf("render a: %v", err)
		}
		if got := rec.Body.String(); got != "<main><nav>T</nav>page a</main>" {
			t.Fatalf("body after a failed render = %q", got)
		}
		if got := rec.Header().Get("Content-Length"); got != fmt.Sprint(rec.Body.Len()) {
			t.Fatalf("Content-Length = %q for a %d-byte body", got, rec.Body.Len())
		}
	}
}

// Concurrent renders must each get a buffer of their own: a buffer handed
// back while its page is still being written would interleave two readers'
// documents. Run with -race.
func TestConcurrentRendersAreIsolated(t *testing.T) {
	engine, err := render.New(render.Options{FS: reuseFixture()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for worker := range 16 {
		wg.Go(func() {
			for i := range 200 {
				title := fmt.Sprintf("w%d-%d-%s", worker, i, strings.Repeat("x", i))
				rec := httptest.NewRecorder()
				if err := engine.Render(rec, http.StatusOK, "b", &render.Page{Title: title}); err != nil {
					errs <- err
					return
				}
				if want := "<main>page b " + title + "</main><aside>b only</aside>"; rec.Body.String() != want {
					errs <- fmt.Errorf("got %q, want %q", rec.Body.String(), want)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// HTML hands its bytes to the caller, so they must stay intact after later
// renders reuse whatever buffers the engine keeps.
func TestHTMLResultOutlivesLaterRenders(t *testing.T) {
	engine, err := render.New(render.Options{FS: reuseFixture()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	first, err := engine.HTML("b", &render.Page{Title: "first"})
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	want := string(first)
	for range 20 {
		rec := httptest.NewRecorder()
		if err := engine.Render(rec, http.StatusOK, "b", &render.Page{Title: "later"}); err != nil {
			t.Fatalf("render: %v", err)
		}
	}
	if string(first) != want {
		t.Fatalf("HTML result changed to %q after later renders", first)
	}
}
