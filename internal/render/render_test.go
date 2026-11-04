package render_test

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rahulguptaio/timlee/internal/assets"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
	"github.com/rahulguptaio/timlee/web/locales"
	templates "github.com/rahulguptaio/timlee/web/templates"
)

// testManifest is a manifest with the two entries every layout asks for.
func testManifest(t *testing.T) *assets.Manifest {
	t.Helper()
	m := assets.NewManifest("/assets/")
	m.Add("app.css", "app-TEST.css", []byte("body{}"))
	m.Add("app.js", "app-TEST.js", []byte("export{}"))
	return m
}

func funcsFor(t *testing.T, m *assets.Manifest) template.FuncMap {
	t.Helper()
	funcs := render.FuncMap()
	for name, fn := range m.FuncMap() {
		funcs[name] = fn
	}
	return funcs
}

// TestShippedTemplatesParse is the gate the server relies on: every template
// that ships must parse, because a template that fails at request time is a
// 500 in front of a reader.
func TestShippedTemplatesParse(t *testing.T) {
	engine, err := render.New(render.Options{
		FS:    templates.FS(),
		Funcs: funcsFor(t, testManifest(t)),
	})
	if err != nil {
		t.Fatalf("parse shipped templates: %v", err)
	}

	want := []string{
		"app", "apps", "error", "invite", "landing",
		"settings", "settings-audit", "settings-keys", "settings-members", "settings-notice", "settings-organisation", "settings-profile", "settings-security",
		"sign-in",
	}
	got := engine.Pages()
	if len(got) != len(want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("pages[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestNewReportsBrokenTemplates(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{
			name: "malformed action",
			files: fstest.MapFS{
				"layouts/base.gohtml": &fstest.MapFile{Data: []byte(`{{ define "base" }}{{ template "content" . }}{{ end }}`)},
				"pages/x.gohtml":      &fstest.MapFile{Data: []byte(`{{ define "content" }}{{ .Title `)},
			},
			want: "parse pages/x.gohtml",
		},
		{
			name: "unknown function",
			files: fstest.MapFS{
				"layouts/base.gohtml": &fstest.MapFile{Data: []byte(`{{ define "base" }}{{ template "content" . }}{{ end }}`)},
				"pages/x.gohtml":      &fstest.MapFile{Data: []byte(`{{ define "content" }}{{ nosuchfunc . }}{{ end }}`)},
			},
			want: "parse pages/x.gohtml",
		},
		{
			name: "no pages",
			files: fstest.MapFS{
				"layouts/base.gohtml": &fstest.MapFile{Data: []byte(`{{ define "base" }}{{ end }}`)},
			},
			want: "no pages found",
		},
		{
			name:  "no layouts",
			files: fstest.MapFS{"pages/x.gohtml": &fstest.MapFile{Data: []byte(`{{ define "content" }}{{ end }}`)}},
			want:  "no layouts or partials found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := render.New(render.Options{FS: tc.files})
			if err == nil {
				t.Fatal("want a startup error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestUnknownAssetFailsTheRender is why the manifest's FuncMap is wired in:
// a mistyped asset name must stop the page, not ship src="".
func TestUnknownAssetFailsTheRender(t *testing.T) {
	files := fstest.MapFS{
		"layouts/base.gohtml": &fstest.MapFile{
			Data: []byte(`{{ define "base" }}<link href="{{ asset "typo.css" }}">{{ template "content" . }}{{ end }}`),
		},
		"pages/x.gohtml": &fstest.MapFile{Data: []byte(`{{ define "content" }}ok{{ end }}`)},
	}

	engine, err := render.New(render.Options{FS: files, Funcs: funcsFor(t, testManifest(t))})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	rec := httptest.NewRecorder()
	err = engine.Render(rec, http.StatusOK, "x", &render.Page{})
	if err == nil {
		t.Fatal("want a render error for an unknown asset, got nil")
	}
	if !strings.Contains(err.Error(), "typo.css") {
		t.Errorf("error = %q, want it to name the missing entry", err)
	}
	// Buffered, so the caller can still send a 500 instead of trailing a
	// broken document behind a 200.
	if rec.Body.Len() != 0 {
		t.Errorf("wrote %d bytes before failing; want none", rec.Body.Len())
	}
}

func TestRenderWritesCompleteDocument(t *testing.T) {
	files := fstest.MapFS{
		"layouts/base.gohtml": &fstest.MapFile{
			Data: []byte(`{{ define "base" }}<html data-theme="{{ .Theme }}"><body>{{ template "content" . }}</body></html>{{ end }}`),
		},
		"layouts/bare.gohtml": &fstest.MapFile{Data: []byte(`{{ define "bare" }}bare:{{ template "content" . }}{{ end }}`)},
		"pages/x.gohtml":      &fstest.MapFile{Data: []byte(`{{ define "content" }}<h1>{{ .Title }}</h1>{{ end }}`)},
	}

	engine, err := render.New(render.Options{FS: files, Funcs: funcsFor(t, testManifest(t))})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	tests := []struct {
		name   string
		page   *render.Page
		want   string
		header string
	}{
		{
			name: "default layout",
			page: &render.Page{Title: "Hello", Theme: render.Dark},
			want: `<html data-theme="dark"><body><h1>Hello</h1></body></html>`,
		},
		{
			name: "named layout",
			page: &render.Page{Layout: "bare", Title: "Hi"},
			want: `bare:<h1>Hi</h1>`,
		},
		{
			name: "escapes page data",
			page: &render.Page{Title: `<script>alert(1)</script>`},
			want: `&lt;script&gt;`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := engine.Render(rec, http.StatusOK, "x", tc.page); err != nil {
				t.Fatalf("render: %v", err)
			}
			if got := rec.Body.String(); !strings.Contains(got, tc.want) {
				t.Errorf("body = %q, want it to contain %q", got, tc.want)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			if vary := rec.Header().Values("Vary"); len(vary) != 1 || vary[0] != "Cookie" {
				t.Errorf("Vary = %v, want Cookie: the theme is a cookie, never a client hint", vary)
			}
		})
	}
}

func TestRenderRejectsUnknownPageAndLayout(t *testing.T) {
	files := fstest.MapFS{
		"layouts/base.gohtml": &fstest.MapFile{Data: []byte(`{{ define "base" }}{{ template "content" . }}{{ end }}`)},
		"pages/x.gohtml":      &fstest.MapFile{Data: []byte(`{{ define "content" }}ok{{ end }}`)},
	}
	engine, err := render.New(render.Options{FS: files})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if _, err := engine.HTML("nope", &render.Page{}); err == nil ||
		!strings.Contains(err.Error(), `no page "nope"`) {
		t.Errorf("unknown page error = %v", err)
	}
	if _, err := engine.HTML("x", &render.Page{Layout: "missing"}); err == nil ||
		!strings.Contains(err.Error(), "which no template defines") {
		t.Errorf("unknown layout error = %v", err)
	}
}

// TestDevModeRereadsFromDisk covers the development path: an edit must show
// up on the next request, not the next build.
func TestDevModeRereadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"layouts", "partials", "pages"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("layouts/base.gohtml", `{{ define "base" }}{{ template "content" . }}{{ end }}`)
	write("pages/x.gohtml", `{{ define "content" }}first{{ end }}`)

	engine, err := render.New(render.Options{Dir: dir, Dev: true})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	body, err := engine.HTML("x", &render.Page{})
	if err != nil || string(body) != "first" {
		t.Fatalf("body = %q, err = %v", body, err)
	}

	write("pages/x.gohtml", `{{ define "content" }}second{{ end }}`)
	body, err = engine.HTML("x", &render.Page{})
	if err != nil || string(body) != "second" {
		t.Fatalf("after edit: body = %q, err = %v", body, err)
	}
}

func TestThemeOf(t *testing.T) {
	tests := []struct {
		name   string
		cookie string
		hint   string
		want   render.Theme
	}{
		{name: "cookie is the choice", cookie: "dark", hint: "light", want: render.Dark},
		{name: "light is a choice too", cookie: "light", want: render.Light},
		// A hint is the system's scheme, which CSS follows without being
		// told; writing it into the document would pin it.
		{name: "hint is not a choice", hint: "dark", want: render.System},
		{name: "no cookie follows the system", want: render.System},
		{name: "junk cookie ignored", cookie: "neon", want: render.System},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: render.ThemeCookie, Value: tc.cookie})
			}
			if tc.hint != "" {
				r.Header.Set("Sec-CH-Prefers-Color-Scheme", tc.hint)
			}
			if got := render.ThemeOf(r); got != tc.want {
				t.Errorf("ThemeOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// The toggle offers the opposite of what the reader sees; going back to the
// system's own scheme forgets the choice rather than pinning it.
func TestThemeToggleAndColor(t *testing.T) {
	tests := []struct {
		chosen           render.Theme
		system           string
		wantNext, wantBG string
	}{
		{chosen: render.System, system: "light", wantNext: "dark", wantBG: "#ffffff"},
		{chosen: render.System, system: "dark", wantNext: "light", wantBG: "#0b1424"},
		{chosen: render.Dark, system: "light", wantNext: "system", wantBG: "#0b1424"},
		{chosen: render.Dark, system: "dark", wantNext: "light", wantBG: "#0b1424"},
		{chosen: render.Light, system: "light", wantNext: "dark", wantBG: "#ffffff"},
		{chosen: render.Light, system: "dark", wantNext: "system", wantBG: "#ffffff"},
	}
	for _, tc := range tests {
		p := &render.Page{Theme: tc.chosen}
		if got := p.ThemeToggle(tc.system); got != tc.wantNext {
			t.Errorf("chosen %q, system %s: ThemeToggle = %q, want %q", tc.chosen, tc.system, got, tc.wantNext)
		}
		if got := p.ThemeColor(tc.system); got != tc.wantBG {
			t.Errorf("chosen %q, system %s: ThemeColor = %q, want %q", tc.chosen, tc.system, got, tc.wantBG)
		}
	}
}

func TestThemeLabelIsLocalized(t *testing.T) {
	b, err := i18n.Load(locales.FS(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	accented, _ := b.ByID("en-xa")
	tests := []struct {
		name  string
		theme render.Theme
		key   i18n.Key
	}{
		{name: "system offers dark", theme: render.System, key: "common.theme.switchDark"},
		{name: "light offers dark", theme: render.Light, key: "common.theme.switchDark"},
		{name: "dark offers light", theme: render.Dark, key: "common.theme.switchLight"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &render.Page{Theme: tc.theme, L: b.For(accented)}
			if got, want := p.ThemeLabel(), p.L.T(tc.key); got != want {
				t.Errorf("ThemeLabel = %q, want %q", got, want)
			}
		})
	}
}

func TestUserDisplayAndInitials(t *testing.T) {
	tests := []struct {
		name     string
		user     *render.User
		display  string
		initials string
	}{
		{name: "nil", user: nil, display: "", initials: ""},
		{name: "full name", user: &render.User{Name: "Ada Lovelace", Email: "ada@x.io"}, display: "Ada Lovelace", initials: "AL"},
		{name: "falls back to local part", user: &render.User{Email: "ada@x.io"}, display: "ada", initials: "A"},
		{name: "three words take two", user: &render.User{Name: "Jean Luc Picard"}, display: "Jean Luc Picard", initials: "JL"},
		{name: "punctuation skipped", user: &render.User{Name: "(dev) user"}, display: "(dev) user", initials: "DU"},
		{name: "nothing usable", user: &render.User{}, display: "", initials: "?"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.Display(); got != tc.display {
				t.Errorf("Display = %q, want %q", got, tc.display)
			}
			if got := tc.user.Initials(); got != tc.initials {
				t.Errorf("Initials = %q, want %q", got, tc.initials)
			}
		})
	}
}

func TestFillClass(t *testing.T) {
	tests := []struct {
		value, total float64
		want         string
	}{
		{1240, 1240, "pct-100"},
		{860, 1240, "pct-70"},
		{190, 1240, "pct-15"},
		{0, 1240, "pct-0"},
		{1, 0, "pct-0"},
		{99, 1, "pct-100"},
	}
	for _, tc := range tests {
		got := render.FillClass(tc.value, tc.total)
		if got != tc.want {
			t.Errorf("FillClass(%v, %v) = %q, want %q", tc.value, tc.total, got, tc.want)
		}
		// Every class the templates can emit must exist in the stylesheet
		// shipped with the response, or the bar renders at zero width.
		if !strings.Contains(string(render.FillCSS), "."+got+"{") {
			t.Errorf("FillCSS has no rule for %q", got)
		}
	}
}

func TestSignInPath(t *testing.T) {
	tests := []struct{ returnTo, want string }{
		{"", "/sign-in"},
		{"/", "/sign-in"},
		{"/console", "/sign-in?returnTo=%2Fconsole"},
		{"/console?section=keys", "/sign-in?returnTo=%2Fconsole%3Fsection%3Dkeys"},
	}
	for _, tc := range tests {
		if got := render.SignInPath(tc.returnTo); got != tc.want {
			t.Errorf("SignInPath(%q) = %q, want %q", tc.returnTo, got, tc.want)
		}
	}
}

func TestJSONEscapesScriptClose(t *testing.T) {
	got, err := render.JSON(map[string]string{"name": "</script><img src=x>"})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(got), "</script>") {
		t.Errorf("structured data can close its own script element: %s", got)
	}
}

func TestThousands(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{{0, "0"}, {999, "999"}, {4096, "4,096"}, {1234567, "1,234,567"}, {-4096, "-4,096"}}
	for _, tc := range tests {
		if got := render.Thousands(tc.in); got != tc.want {
			t.Errorf("Thousands(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
