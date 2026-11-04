package i18n

import (
	"html/template"
	"strings"
	"testing"
)

func TestPathPrefixesPublicPagesOutsideTheDefaultLocale(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, true)
	b.LocalizePaths(func(path string) bool {
		return path != "/console" && !strings.HasPrefix(path, "/settings/")
	})
	de, _ := b.ByID("de")
	xa, _ := b.ByID("en-xa")

	tests := []struct{ in, en, de, xa string }{
		{"/", "/", "/de/", "/en-xa/"},
		{"/models", "/models", "/de/models", "/en-xa/models"},
		{"/models?q=qwen#top", "/models?q=qwen#top", "/de/models?q=qwen#top", "/en-xa/models?q=qwen#top"},
		{"/#deployment", "/#deployment", "/de/#deployment", "/en-xa/#deployment"},
		{"/sign-in?returnTo=%2F", "/sign-in?returnTo=%2F", "/de/sign-in?returnTo=%2F", "/en-xa/sign-in?returnTo=%2F"},
		// Already in a language: left alone, whichever it is.
		{"/de/models", "/de/models", "/de/models", "/de/models"},
		{"/de", "/de", "/de", "/de"},
		{"/en-xa/", "/en-xa/", "/en-xa/", "/en-xa/"},
		// One URL for every language.
		{"/console", "/console", "/console", "/console"},
		{"/settings/keys?x=1", "/settings/keys?x=1", "/settings/keys?x=1", "/settings/keys?x=1"},
		// Not a path on this site.
		{"", "", "", ""},
		{"#top", "#top", "#top", "#top"},
		{"mailto:hello@example.com", "mailto:hello@example.com", "mailto:hello@example.com", "mailto:hello@example.com"},
		{"https://example.com/models", "https://example.com/models", "https://example.com/models", "https://example.com/models"},
		{"//example.com/models", "//example.com/models", "//example.com/models", "//example.com/models"},
		{`/\example.com`, `/\example.com`, `/\example.com`, `/\example.com`},
	}
	for _, tc := range tests {
		for _, c := range []struct {
			l    *Localizer
			want string
		}{{b.For(nil), tc.en}, {de.Localizer(), tc.de}, {xa.Localizer(), tc.xa}} {
			if got := c.l.Path(tc.in); got != c.want {
				t.Errorf("%s Path(%q) = %q, want %q", c.l.Locale().ID, tc.in, got, c.want)
			}
		}
	}
}

func TestPathTreatsEveryPathAsAPageUntilTold(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, false)
	de, _ := b.ByID("de")
	if got := de.Localizer().Path("/console"); got != "/de/console" {
		t.Errorf("Path(/console) before LocalizePaths = %q, want /de/console", got)
	}
}

func TestSignInPathKeepsTheLocaleAndTheReturnTarget(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, false)
	de, _ := b.ByID("de")
	tests := []struct {
		l              *Localizer
		returnTo, want string
	}{
		// English is what render.SignInPath has always written.
		{b.For(nil), "", "/sign-in"},
		{b.For(nil), "/", "/sign-in"},
		{b.For(nil), "/docs/quickstart", "/sign-in?returnTo=%2Fdocs%2Fquickstart"},
		{de.Localizer(), "", "/de/sign-in"},
		{de.Localizer(), "/de/models?q=a b", "/de/sign-in?returnTo=%2Fde%2Fmodels%3Fq%3Da+b"},
		{de.Localizer(), "/console", "/de/sign-in?returnTo=%2Fconsole"},
	}
	for _, tc := range tests {
		if got := tc.l.SignInPath(tc.returnTo); got != tc.want {
			t.Errorf("%s SignInPath(%q) = %q, want %q", tc.l.Locale().ID, tc.returnTo, got, tc.want)
		}
	}
}

func TestLocalesListsTheEnabledOnesDefaultFirst(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, true)
	var ids []string
	for _, loc := range b.Locales() {
		ids = append(ids, loc.ID)
	}
	if got := strings.Join(ids, " "); got != "en de en-xa ar-xb" {
		t.Errorf("Locales() = %s, want en de en-xa ar-xb", got)
	}
}

func TestHrefTemplateFunc(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, false)
	de, _ := b.ByID("de")
	tmpl := template.Must(template.New("x").Funcs(FuncMap()).Parse(`<a href="{{ href .L "/models" }}">x</a>`))
	var out strings.Builder
	if err := tmpl.Execute(&out, map[string]any{"L": de.Localizer()}); err != nil {
		t.Fatal(err)
	}
	if want := `<a href="/de/models">x</a>`; out.String() != want {
		t.Errorf("href = %s, want %s", out.String(), want)
	}
	if err := tmpl.Execute(&out, map[string]any{"L": (*Localizer)(nil)}); err == nil {
		t.Error("href with a nil localizer did not fail")
	}
}
