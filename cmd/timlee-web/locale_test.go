package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rahulguptaio/timlee/internal/assets"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
	"github.com/rahulguptaio/timlee/web/locales"
	templates "github.com/rahulguptaio/timlee/web/templates"
)

// Locale routing (docs/I18N.md §2), exercised with the locales
// TIMLEE_PSEUDO_LOCALES=1 ships next to English: en-xa (accented, left to
// right) and ar-xb (right to left). A translated locale is stood in for by a
// German catalog holding only the banner's messages, because Accept-Language
// can never ask for a pseudo-locale.

// pseudoLocales is what TIMLEE_PSEUDO_LOCALES=1 ships.
func pseudoLocales(t *testing.T) *i18n.Bundle {
	t.Helper()
	b, err := i18n.Load(locales.FS(), nil, true)
	if err != nil {
		t.Fatalf("load the pseudo-locales: %v", err)
	}
	return b
}

// withGerman is English and a German catalog, as TIMLEE_LOCALES=en,de will
// ship them once German is translated.
func withGerman(t *testing.T) *i18n.Bundle {
	t.Helper()
	fsys := fstest.MapFS{"de/common.arb": {Data: []byte(`{
  "@@locale": "de",
  "common.locale.heading": "Sprache",
  "common.locale.suggest": "Diese Seite gibt es auch auf Deutsch.",
  "common.locale.switch": "Auf Deutsch lesen",
  "common.locale.continue": "Weiter auf Deutsch",
  "common.locale.keep": "Bei {language} bleiben"
}`)}}
	err := fs.WalkDir(locales.FS(), "en", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(locales.FS(), path)
		fsys[path] = &fstest.MapFile{Data: raw}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := i18n.Load(fsys, []string{"de"}, false)
	if err != nil {
		t.Fatalf("load German: %v", err)
	}
	return b
}

func localeOf(t *testing.T, b *i18n.Bundle, id string) *i18n.Locale {
	t.Helper()
	loc, ok := b.ByID(id)
	if !ok {
		t.Fatalf("%s is not enabled", id)
	}
	return loc
}

// localeHarness is newHarness serving b's locales, with every page's assets
// so that any public page renders.
func localeHarness(t *testing.T, b *i18n.Bundle) *harness {
	t.Helper()
	h := newHarness(t)
	manifest := assets.NewManifest("/assets/")
	for _, name := range []string{"app.css", "app.js"} {
		manifest.Add(name, "TEST-"+name, []byte("x"))
	}
	funcs := render.FuncMap()
	for name, fn := range manifest.FuncMap() {
		funcs[name] = fn
	}
	engine, err := render.New(render.Options{FS: templates.FS(), Funcs: funcs})
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	h.srv.i18n, h.srv.engine = b, engine
	_, h.handler = h.srv.handler(http.NotFoundHandler())
	return h
}

// send serves a request with extra headers and cookies.
func (h *harness) send(method, path string, header http.Header, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for name, values := range header {
		r.Header[name] = values
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec
}

func htmlOpen(loc *i18n.Locale) string {
	return `<html lang="` + loc.Lang + `" dir="` + loc.Dir + `"`
}

func localeCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == localeCookie {
			return c
		}
	}
	return nil
}

var (
	switcherMarkup = regexp.MustCompile(`(?s)<div class="footer-group locale-switcher">.*?</div>`)
	bannerMarkup   = regexp.MustCompile(`(?s)<div class="locale-banner[^"]*">.*?</timlee-alert>`)
)

/* --------------------------------------------------------------- routes -- */

func TestLocalePrefixServesThePageInThatLocale(t *testing.T) {
	b := pseudoLocales(t)
	h := localeHarness(t, b)
	en, xa, xb := b.Default(), localeOf(t, b, "en-xa"), localeOf(t, b, "ar-xb")

	for _, tc := range []struct {
		path string
		want *i18n.Locale
	}{
		{"/", en}, {"/sign-in", en},
		{"/en-xa/", xa}, {"/en-xa/?q=qwen", xa}, {"/en-xa/sign-in", xa},
		{"/ar-xb/", xb}, {"/ar-xb/sign-in?returnTo=%2Fapps", xb},
	} {
		rec := h.get(tc.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (Location %q)", tc.path, rec.Code, rec.Header().Get("Location"))
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, htmlOpen(tc.want)) {
			t.Errorf("GET %s does not open with %s", tc.path, htmlOpen(tc.want))
		}
		if got := rec.Header().Get("Content-Language"); got != tc.want.Lang {
			t.Errorf("GET %s: Content-Language %q, want %q", tc.path, got, tc.want.Lang)
		}
	}

	// The page is written by its locale's catalog, not just labelled.
	body := h.get("/ar-xb/").Body.String()
	if heading := xb.Localizer().T("common.locale.heading"); !strings.Contains(body, "<h2>"+heading+"</h2>") {
		t.Errorf("/ar-xb/ does not carry the ar-xb switcher heading %q", heading)
	}
}

func TestLocaleRedirectsOnlyToCanonicalURLs(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	for _, tc := range []struct{ path, location string }{
		// The default locale has one URL.
		{"/en/sign-in?q=a", "/sign-in?q=a"},
		{"/en", "/"},
		{"/en/", "/"},
		{"/EN/sign-in", "/sign-in"},
		// A locale's root has its slash; its segment is lowercase.
		{"/en-xa", "/en-xa/"},
		{"/ar-xb?x=1", "/ar-xb/?x=1"},
		{"/EN-XA/sign-in", "/en-xa/sign-in"},
		{"/Ar-Xb/sign-in?x=1", "/ar-xb/sign-in?x=1"},
		// The app and everything a machine reads are never prefixed.
		{"/en-xa/apps", "/apps"},
		{"/en-xa/apps?view=grid", "/apps?view=grid"},
		{"/ar-xb/apps/demo", "/apps/demo"},
		{"/ar-xb/settings", "/settings"},
		{"/AR-XB/settings/keys", "/settings/keys"},
		{"/ar-xb/assets/TEST-app.css", "/assets/TEST-app.css"},
		{"/ar-xb/robots.txt", "/robots.txt"},
		{"/ar-xb/favicon.svg", "/favicon.svg"},
		{"/ar-xb/.well-known/security.txt", "/.well-known/security.txt"},
		{"/ar-xb/health", "/health"},
		{"/ar-xb/sign-in/desktop/start", "/sign-in/desktop/start"},
		// Never to another site.
		{"/en//evil.example", "/evil.example"},
		{"/en//evil", "/evil"},
		{"/EN-XA//evil", "/en-xa/evil"},
	} {
		rec := h.get(tc.path)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != tc.location {
			t.Errorf("GET %s = %d to %q, want 301 to %q", tc.path, rec.Code, rec.Header().Get("Location"), tc.location)
		}
	}

	// An unknown or unshipped locale is the 404 it always was, in English.
	for _, path := range []string{"/xx/sign-in", "/de/sign-in", "/fr/", "/en-xb/"} {
		rec := h.get(path)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), htmlOpen(h.srv.bundle().Default())) {
			t.Errorf("GET %s = %d, want the English 404", path, rec.Code)
		}
	}
}

// Nothing redirects on Accept-Language, an address or a cookie: a URL serves
// what it names, whoever asks.
func TestLocaleNeverRedirectsOnLanguageAddressOrCookie(t *testing.T) {
	b := withGerman(t)
	h := localeHarness(t, b)
	de := localeOf(t, b, "de")
	asks := []struct {
		name   string
		header http.Header
		cookie *http.Cookie
	}{
		{"Accept-Language", http.Header{"Accept-Language": {"de-DE,de;q=0.9"}}, nil},
		{"cookie", nil, &http.Cookie{Name: localeCookie, Value: "de"}},
		{"address", http.Header{"X-Forwarded-For": {"85.214.132.117"}, "Cf-Ipcountry": {"DE"}}, nil},
		{"all three", http.Header{"Accept-Language": {"de"}, "X-Forwarded-For": {"85.214.132.117"}}, &http.Cookie{Name: localeCookie, Value: "de"}},
	}
	for _, ask := range asks {
		for path, want := range map[string]*i18n.Locale{
			"/": b.Default(), "/?q=a": b.Default(), "/sign-in": b.Default(), "/de/": de, "/de/sign-in": de,
		} {
			var cookies []*http.Cookie
			if ask.cookie != nil {
				cookies = append(cookies, ask.cookie)
			}
			rec := h.send(http.MethodGet, path, ask.header, cookies...)
			if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
				t.Errorf("%s: GET %s = %d to %q, want 200 and no redirect", ask.name, path, rec.Code, rec.Header().Get("Location"))
				continue
			}
			if !strings.Contains(rec.Body.String(), htmlOpen(want)) {
				t.Errorf("%s: GET %s is not the %s page its URL names", ask.name, path, want.ID)
			}
		}
	}
}

// Every route class of the table in docs/I18N.md §2.
func TestLocalizedPathFollowsTheRouteClasses(t *testing.T) {
	public := []string{
		"/", "/sign-in", "/sign-in/sent", "/invite", "/no-such-page",
	}
	single := []string{
		"/apps", "/apps/demo", "/apps/demo/prompt",
		"/settings", "/settings/", "/settings/keys", "/settings/members/u1/role",
		"/sign-out", "/theme", "/locale", "/health", "/readyz", "/robots.txt",
		"/.well-known/security.txt", "/assets/app-X.css", "/favicon.svg", "/site.webmanifest", "/icon-512.png",
		"/sign-in/desktop/start", "/sign-in/desktop/complete", "/invite.html",
	}
	for _, p := range public {
		if !localizedPath(p) {
			t.Errorf("localizedPath(%q) = false, want a prefix allowed", p)
		}
	}
	for _, p := range single {
		if localizedPath(p) {
			t.Errorf("localizedPath(%q) = true, want one URL for every language", p)
		}
	}
	// Prefix-shaped names are not the app.
	for _, p := range []string{"/appsx", "/settings-guide", "/assetsx"} {
		if !localizedPath(p) {
			t.Errorf("localizedPath(%q) = false, want only whole segments to count", p)
		}
	}
}

/* ------------------------------------------------- whose language it is -- */

// signedInControl answers GET /auth/session for a reader who saved locale.
func signedInControl(t *testing.T, locale string) string {
	t.Helper()
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"user":{"id":"u1","email":"ada@example.com","name":"Ada Lovelace","avatarUrl":"","locale":"`+locale+`"},`+
			`"org":{"id":"o1","name":"Acme Corp","slug":"acme","role":"admin"}}`)
	}))
	t.Cleanup(control.Close)
	return control.URL
}

func TestAppPagesFollowTheReader(t *testing.T) {
	b := pseudoLocales(t)
	xa, xb := localeOf(t, b, "en-xa"), localeOf(t, b, "ar-xb")
	h := localeHarness(t, b)
	h.control.signedIn = true
	chose := func(id string) *http.Cookie { return &http.Cookie{Name: localeCookie, Value: id} }

	// The cookie, without an account preference. /apps is an app root; until
	// its pages exist the catch-all answers there, and follows the reader.
	rec := h.get("/apps", session(), chose("ar-xb"))
	if !strings.Contains(rec.Body.String(), htmlOpen(xb)) {
		t.Errorf("/apps with the ar-xb cookie = %d, want the ar-XB page", rec.Code)
	}
	// A cookie naming no shipped locale is no choice.
	if body := h.get("/apps", session(), chose("xx")).Body.String(); !strings.Contains(body, htmlOpen(b.Default())) {
		t.Error("/apps with an unknown locale cookie is not English")
	}
	// A page that does not exist follows the reader too, prefix first.
	for path, want := range map[string]*i18n.Locale{"/no-such-page": xb, "/xx/": xb, "/en-xa/no-such-page": xa} {
		rec := h.get(path, chose("ar-xb"))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), htmlOpen(want)) {
			t.Errorf("GET %s with the ar-xb cookie = %d, want the %s 404", path, rec.Code, want.ID)
		}
	}
	// A public page's language is its URL's, whatever the reader chose.
	if body := h.get("/", session(), chose("ar-xb")).Body.String(); !strings.Contains(body, htmlOpen(b.Default())) {
		t.Error("/ with the ar-xb cookie is not English")
	}

	// The account's preference comes before the cookie, and the cookie is
	// brought in step with it for the pages that only see the cookie.
	h.srv.auth = newAuthClient(signedInControl(t, "en-xa"), testPublic, h.srv.log)
	rec = h.get("/apps", session(), chose("ar-xb"))
	if !strings.Contains(rec.Body.String(), htmlOpen(xa)) {
		t.Error("/apps for a reader who saved en-xa is not en-XA")
	}
	if c := localeCookieOf(rec); c == nil || c.Value != "en-xa" {
		t.Errorf("the cookie was not brought in step with the account: %v", c)
	}
	if c := localeCookieOf(h.get("/apps", session(), chose("en-xa"))); c != nil {
		t.Errorf("a cookie already in step was set again: %v", c)
	}
}

// A page its handler builds itself, as the settings pages do, follows the
// account the same way once its User carries the saved language.
func TestPagesBuiltByTheirHandlerFollowTheAccount(t *testing.T) {
	b := pseudoLocales(t)
	h := localeHarness(t, b)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /settings/profile", func(w http.ResponseWriter, r *http.Request) {
		p := &render.Page{User: &render.User{ID: "u1", Email: "ada@example.com", Locale: "ar-xb"}}
		p.Defaults(r, "nonce", "id", h.srv.origins)
		p.Data = map[string]string{"Status": "200", "Title": "Profile", "Body": "Profile"}
		h.srv.render(w, r, http.StatusOK, "error", p)
	})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
	r.AddCookie(&http.Cookie{Name: localeCookie, Value: "en-xa"})
	h.srv.localeRoutes(mux).ServeHTTP(rec, r)
	if !strings.Contains(rec.Body.String(), htmlOpen(localeOf(t, b, "ar-xb"))) {
		t.Errorf("a page built by its handler for a reader who saved ar-xb is not ar-XB (%d)", rec.Code)
	}
	if c := localeCookieOf(rec); c == nil || c.Value != "ar-xb" {
		t.Errorf("the cookie was not brought in step with the account: %v", c)
	}
}

/* -------------------------------------------------------- the switcher -- */

// The switcher is plain links, so it needs no script, and following one is
// all it takes to read the page in that language.
func TestLanguageSwitcherIsPlainLinks(t *testing.T) {
	b := pseudoLocales(t)
	h := localeHarness(t, b)

	body := h.get("/en-xa/?q=qwen").Body.String()
	switcher := switcherMarkup.FindString(body)
	for _, want := range []string{
		`<a href="/?q=qwen" hreflang="en"><span lang="en" dir="ltr">English</span></a>`,
		`<a href="/en-xa/?q=qwen" hreflang="en-XA" aria-current="true"><span lang="en-XA" dir="ltr">Pseudo (accented)</span></a>`,
		`<a href="/ar-xb/?q=qwen" hreflang="ar-XB"><span lang="ar-XB" dir="rtl">Pseudo (RTL)</span></a>`,
	} {
		if !strings.Contains(switcher, want) {
			t.Errorf("the switcher on /en-xa/?q=qwen lacks %s\n%s", want, switcher)
		}
	}
	if strings.Contains(switcher, "<script") || strings.Contains(switcher, "<form") {
		t.Errorf("the switcher is not plain links:\n%s", switcher)
	}

	// Following a link, as a browser without script does.
	rec := h.get("/ar-xb/?q=qwen")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), htmlOpen(localeOf(t, b, "ar-xb"))) {
		t.Fatalf("following the ar-xb link = %d, want the ar-XB page", rec.Code)
	}
	if !strings.Contains(switcherMarkup.FindString(rec.Body.String()), `hreflang="ar-XB" aria-current="true"`) {
		t.Error("the ar-xb page does not mark ar-XB as current")
	}

	// The landing page and a 404 carry it too.
	for path, want := range map[string]string{
		"/":                   `<a href="/ar-xb/" hreflang="ar-XB">`,
		"/en-xa/no-such-page": `<a href="/ar-xb/no-such-page" hreflang="ar-XB">`,
	} {
		if s := switcherMarkup.FindString(h.get(path).Body.String()); !strings.Contains(s, want) {
			t.Errorf("the switcher on %s lacks %s", path, want)
		}
	}
	// An app page has one URL for every language: profile settings choose.
	h.control.signedIn = true
	if strings.Contains(h.get("/settings", session()).Body.String(), "locale-switcher") {
		t.Error("the console carries the link switcher")
	}
}

// POST /locale is the form behind the banner and profile settings: it works
// without script, remembers the choice and returns to the same page in the
// language chosen.
func TestLocaleFormRemembersTheChoice(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	choose := func(locale, returnTo string) *httptest.ResponseRecorder {
		return h.post("/locale", url.Values{"locale": {locale}, "returnTo": {returnTo}})
	}

	rec := choose("ar-xb", "/en-xa/?q=qwen")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ar-xb/?q=qwen" {
		t.Fatalf("POST /locale = %d to %q, want 303 to /ar-xb/?q=qwen", rec.Code, rec.Header().Get("Location"))
	}
	c := localeCookieOf(rec)
	if c == nil || c.Value != "ar-xb" || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
		c.MaxAge != int((365*24*time.Hour).Seconds()) {
		t.Errorf("cookie = %+v, want timlee.locale=ar-xb for a year, Path=/, HttpOnly, SameSite=Lax", c)
	}

	for _, tc := range []struct{ locale, returnTo, want string }{
		{"en", "/ar-xb/?x=1", "/?x=1"},
		{"en-xa", "/", "/en-xa/"},
		{"en-xa", "/ar-xb/", "/en-xa/"},
		{"ar-xb", "/?q=a+b", "/ar-xb/?q=a+b"},
		// An app page stays where it is: it follows the choice.
		{"ar-xb", "/settings?room=r1", "/settings?room=r1"},
		{"ar-xb", "/settings/profile", "/settings/profile"},
		// Never off the site.
		{"ar-xb", "//evil.example/x", "/ar-xb/"},
		{"ar-xb", "https://evil.example/x", "/ar-xb/"},
		{"ar-xb", "", "/ar-xb/"},
	} {
		if got := choose(tc.locale, tc.returnTo).Header().Get("Location"); got != tc.want {
			t.Errorf("choosing %s on %q goes to %q, want %q", tc.locale, tc.returnTo, got, tc.want)
		}
	}

	// No choice forgets the last one.
	rec = choose("", "/ar-xb/")
	if c := localeCookieOf(rec); c == nil || c.MaxAge >= 0 || rec.Header().Get("Location") != "/" {
		t.Errorf("clearing the choice = %q, cookie %+v; want / and the cookie deleted", rec.Header().Get("Location"), c)
	}
	// A locale this site does not ship is refused.
	rec = choose("de", "/")
	if rec.Code != http.StatusBadRequest || localeCookieOf(rec) != nil {
		t.Errorf("choosing an unshipped locale = %d, cookie %v; want 400 and none", rec.Code, localeCookieOf(rec))
	}
	// Like the theme form, only this site's pages may post it.
	r := httptest.NewRequest(http.MethodPost, "/locale", strings.NewReader("locale=ar-xb&returnTo=%2F"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	cross := httptest.NewRecorder()
	h.handler.ServeHTTP(cross, r)
	if cross.Code != http.StatusForbidden || localeCookieOf(cross) != nil {
		t.Errorf("a cross-site POST /locale = %d, cookie %v; want 403 and none", cross.Code, localeCookieOf(cross))
	}
}

/* ----------------------------------------------------------- the banner -- */

func TestSuggestionBannerOffersWithoutRedirecting(t *testing.T) {
	b := withGerman(t)
	h := localeHarness(t, b)
	german := http.Header{"Accept-Language": {"de-DE,de;q=0.9,en;q=0.5"}}

	rec := h.send(http.MethodGet, "/?q=qwen", german)
	banner := bannerMarkup.FindString(rec.Body.String())
	for _, want := range []string{
		`<timlee-alert tone="info" lang="de" dir="ltr">`,
		`<form method="post" action="/locale">`,
		`<input type="hidden" name="returnTo" value="/?q=qwen" />`,
		`<p>Diese Seite gibt es auch auf Deutsch.</p>`,
		`<button type="submit" name="locale" value="de">Auf Deutsch lesen</button>`,
		`<button type="submit" name="locale" value="en">Bei <bdi>English</bdi> bleiben</button>`,
	} {
		if !strings.Contains(banner, want) {
			t.Errorf("the banner lacks %s\n%s", want, banner)
		}
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), htmlOpen(b.Default())) {
		t.Errorf("GET / with German asked for = %d, want the English page", rec.Code)
	}
	if !slices.Contains(rec.Header().Values("Vary"), "Accept-Language") {
		t.Errorf("Vary = %q, want Accept-Language among them", rec.Header().Values("Vary"))
	}

	// The German page offers English the same way, in English.
	banner = bannerMarkup.FindString(h.send(http.MethodGet, "/de/", http.Header{"Accept-Language": {"en-GB"}}).Body.String())
	for _, want := range []string{`lang="en" dir="ltr"`, `This page is also available in English.`, `value="en">Switch to English<`, `value="de">Keep <bdi>Deutsch</bdi><`} {
		if !strings.Contains(banner, want) {
			t.Errorf("the English offer on /de/ lacks %s\n%s", want, banner)
		}
	}

	for _, tc := range []struct {
		name   string
		path   string
		header http.Header
		cookie *http.Cookie
	}{
		{"the page is already in that language", "/de/", german, nil},
		{"a language Timlee does not ship", "/", http.Header{"Accept-Language": {"fr-FR,fr"}}, nil},
		{"a language Timlee does not ship, on a German page", "/de/", http.Header{"Accept-Language": {"fr-FR,fr"}}, nil},
		{"no Accept-Language at all", "/", nil, nil},
		{"a reader who chose English", "/", german, &http.Cookie{Name: localeCookie, Value: "en"}},
		{"a reader who chose German, signed out", "/", german, &http.Cookie{Name: localeCookie, Value: "de"}},
		{"a page that does not exist", "/no-such-page", german, nil},
		{"an app page", "/settings", german, nil},
	} {
		var cookies []*http.Cookie
		if tc.cookie != nil {
			cookies = append(cookies, tc.cookie)
		}
		if banner := bannerMarkup.FindString(h.send(http.MethodGet, tc.path, tc.header, cookies...).Body.String()); banner != "" {
			t.Errorf("%s: %s offers a language:\n%s", tc.name, tc.path, banner)
		}
	}

	// A signed-in reader whose choice is not the page's is asked to continue
	// in it; until the account stores one, the cookie stands in.
	h.control.signedIn = true
	banner = bannerMarkup.FindString(h.get("/", session(), &http.Cookie{Name: localeCookie, Value: "de"}).Body.String())
	if !strings.Contains(banner, `value="de">Weiter auf Deutsch<`) {
		t.Errorf("a signed-in reader who chose German is not asked to continue in it:\n%s", banner)
	}
	h.srv.auth = newAuthClient(signedInControl(t, "de"), testPublic, h.srv.log)
	banner = bannerMarkup.FindString(h.get("/", session()).Body.String())
	if !strings.Contains(banner, `value="de">Weiter auf Deutsch<`) {
		t.Errorf("a reader whose account says German is not asked to continue in it:\n%s", banner)
	}
}

// The pseudo-locales are never suggested, so with them the only offer is
// English, written in English on a pseudo page.
func TestBannerOnAPseudoPageOffersEnglish(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	body := h.send(http.MethodGet, "/ar-xb/", http.Header{"Accept-Language": {"en-GB,en;q=0.8"}}).Body.String()
	banner := bannerMarkup.FindString(body)
	if !strings.Contains(banner, `<div class="locale-banner wrap">`) || !strings.Contains(banner, `lang="en" dir="ltr"`) ||
		!strings.Contains(banner, `value="ar-xb">Keep <bdi>Pseudo (RTL)</bdi><`) {
		t.Errorf("/ar-xb/ does not offer English in English:\n%s", banner)
	}
	if i := strings.Index(body, `<main id="main">`); i < 0 || !strings.HasPrefix(strings.TrimSpace(body[i+len(`<main id="main">`):]), `<div class="locale-banner`) {
		t.Error("the banner does not open main")
	}
	if banner := bannerMarkup.FindString(h.send(http.MethodGet, "/ar-xb/", http.Header{"Accept-Language": {"ar"}}).Body.String()); banner != "" {
		t.Errorf("Accept-Language: ar suggested a pseudo-locale:\n%s", banner)
	}
}

/* ------------------------------------------------- hreflang alternates -- */

// capturePage serves path through the locale middleware and a mux holding
// pattern, and returns the page s.page and s.localize built for it.
func capturePage(t *testing.T, h *harness, pattern, path string) (*render.Page, http.Header) {
	t.Helper()
	var p *render.Page
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		p = h.srv.page(r)
		h.srv.localize(w, r, p)
	})
	rec := httptest.NewRecorder()
	h.srv.localeRoutes(mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if p == nil {
		t.Fatalf("%s did not reach %q (%d)", path, pattern, rec.Code)
	}
	return p, rec.Header()
}

func TestAlternatesListEveryLanguageVersionOnceSeveralShip(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	want := []render.Alternate{
		{Lang: "en", OG: "en_GB", Href: testPublic + "/"},
		{Lang: "en-XA", Href: testPublic + "/en-xa/"},
		{Lang: "ar-XB", Href: testPublic + "/ar-xb/"},
		{Lang: "x-default", Href: testPublic + "/"},
	}
	// The same set on every version, and never the query.
	for _, path := range []string{"/", "/en-xa/?q=qwen", "/ar-xb/"} {
		p, header := capturePage(t, h, "GET /{$}", path)
		if !slices.Equal(p.Alternates, want) {
			t.Errorf("%s: alternates %v, want %v", path, p.Alternates, want)
		}
		if !slices.Contains(header.Values("Vary"), "Accept-Language") {
			t.Errorf("%s: Vary %q lacks Accept-Language", path, header.Values("Vary"))
		}
	}
	if p, _ := capturePage(t, h, "GET /{$}", "/ar-xb/"); !slices.Equal(p.Alternates, []render.Alternate{
		{Lang: "en", OG: "en_GB", Href: testPublic + "/"}, {Lang: "en-XA", Href: testPublic + "/en-xa/"},
		{Lang: "ar-XB", Href: testPublic + "/ar-xb/"}, {Lang: "x-default", Href: testPublic + "/"},
	}) {
		t.Errorf("the landing page's alternates are %v", p.Alternates)
	}

	// A page with one URL for every language, and one that does not exist,
	// have no language versions.
	if p, _ := capturePage(t, h, "GET /settings", "/settings"); p.Alternates != nil {
		t.Errorf("/settings has alternates %v", p.Alternates)
	}
	if p, _ := capturePage(t, h, "/", "/ar-xb/no-such-page"); p.Alternates != nil {
		t.Errorf("a 404 has alternates %v", p.Alternates)
	}

	// English alone has nothing to list, nothing to offer and nothing to vary
	// on.
	en := newHarness(t)
	p, header := capturePage(t, en, "GET /{$}", "/")
	if p.Alternates != nil || p.Languages != nil || slices.Contains(header.Values("Vary"), "Accept-Language") {
		t.Errorf("English only: alternates %v, languages %v, Vary %q", p.Alternates, p.Languages, header.Values("Vary"))
	}
}

func TestLocalizedPagesRenderCanonicalAndReciprocalAlternates(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	for _, tc := range []struct {
		path, canonical, lang string
	}{
		{path: "/", canonical: testPublic + "/", lang: "en"},
		{path: "/en-xa/", canonical: testPublic + "/en-xa/", lang: "en-XA"},
		{path: "/ar-xb/", canonical: testPublic + "/ar-xb/", lang: "ar-XB"},
	} {
		body := h.get(tc.path).Body.String()
		for _, want := range []string{
			`<link rel="canonical" href="` + tc.canonical + `" />`,
			`<link rel="alternate" hreflang="en" href="` + testPublic + `/" />`,
			`<link rel="alternate" hreflang="en-XA" href="` + testPublic + `/en-xa/" />`,
			`<link rel="alternate" hreflang="ar-XB" href="` + testPublic + `/ar-xb/" />`,
			`<link rel="alternate" hreflang="x-default" href="` + testPublic + `/" />`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s (%s) lacks %s", tc.path, tc.lang, want)
			}
		}
	}
}

func TestSharedPseudoLocalesAndBidiIsolation(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	accented := localeOf(t, h.srv.bundle(), "en-xa").Localizer()
	rtl := localeOf(t, h.srv.bundle(), "ar-xb").Localizer()

	landing := h.get("/en-xa/").Body.String()
	for _, want := range []string{
		accented.T("common.skip.content"),
		accented.T("common.nav.signIn"),
		accented.T("common.footer.product"),
		accented.T("common.footer.tagline"),
	} {
		if !strings.Contains(landing, want) {
			t.Errorf("en-XA landing page lacks %q", want)
		}
	}

	signIn := h.get("/en-xa/sign-in").Body.String()
	for _, want := range []string{
		accented.T("auth.signIn.heading"),
		string(accented.HTML("auth.signIn.continueWith", "provider", "GitHub")),
		`type="email" dir="ltr"`,
		`<code dir="ltr">`,
	} {
		if !strings.Contains(signIn, want) {
			t.Errorf("en-XA sign-in page lacks %q", want)
		}
	}

	notFound := h.get("/ar-xb/no-such-page").Body.String()
	for _, want := range []string{
		htmlOpen(rtl.Locale()),
		rtl.T("common.error.notFoundTitle"),
		rtl.T("common.error.notFoundBody"),
		`<bdi dir="ltr">404</bdi>`,
	} {
		if !strings.Contains(notFound, want) {
			t.Errorf("ar-XB 404 page lacks %q", want)
		}
	}

	h.control.signedIn = true
	account := h.get("/ar-xb/", session()).Body.String()
	for _, want := range []string{
		`<span class="account-name" dir="auto">Ada Lovelace</span>`,
		`<span dir="ltr">ada@example.com</span>`,
		`<span class="account-org" dir="auto">Acme Corp</span>`,
	} {
		if !strings.Contains(account, want) {
			t.Errorf("signed-in ar-XB chrome lacks %q", want)
		}
	}

	h.control.signedIn = false
	invite := h.get("/ar-xb/invite?token=t0k").Body.String()
	for _, want := range []string{
		rtl.T("auth.invite.signedOutTitle"),
		rtl.T("auth.invite.signIn"),
		`type="email" dir="ltr"`,
	} {
		if !strings.Contains(invite, want) {
			t.Errorf("ar-XB invitation page lacks %q", want)
		}
	}
}

// Direct GETs and POSTs exercise the native links and forms with no browser
// script involved: localization must not turn any shared auth control into a
// JavaScript-only action.
func TestSharedLocalizedAuthWorksWithoutJavaScript(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	stub := &signInStub{sent: map[string]map[string]any{}}
	control := httptest.NewServer(stub.handler())
	t.Cleanup(control.Close)
	h.srv.auth = newAuthClient(control.URL, testPublic, h.srv.log)
	h.srv.cfg.ControlOrigin = control.URL

	body := h.get("/ar-xb/sign-in?returnTo=%2Far-xb%2F").Body.String()
	for _, want := range []string{
		`<form class="auth-form" method="post" action="/ar-xb/sign-in">`,
		`<form method="post" action="/ar-xb/sign-in">`,
		`<button type="submit"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("localized sign-in form lacks %q", want)
		}
	}

	rec := h.post("/ar-xb/sign-in", url.Values{
		"intent":   {"dev"},
		"email":    {"ada@example.com"},
		"returnTo": {"/ar-xb/"},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ar-xb/" {
		t.Errorf("native dev sign-in = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := stub.sent["/auth/dev"]["locale"]; got != "ar-xb" {
		t.Errorf("native dev sign-in sent locale %v, want ar-xb", got)
	}

	rec = h.post("/theme", url.Values{"theme": {"dark"}, "returnTo": {"/ar-xb/sign-in"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ar-xb/sign-in" {
		t.Errorf("native theme form = %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	invite := h.get("/ar-xb/invite?token=t0k").Body.String()
	if !strings.Contains(invite, `<form class="st-dev" method="post" action="/ar-xb/sign-in">`) {
		t.Error("localized invitation has no native sign-in form")
	}
}

/* --------------------------------------------------------------- sign-in -- */

// signInStub is a control plane that records what the sign-in forms send it.
type signInStub struct {
	mu   sync.Mutex
	sent map[string]map[string]any
}

func (c *signInStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/providers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"providers":["github"],"magicLink":true,"devLogin":true}`)
	})
	mux.HandleFunc("GET /auth/session", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	record := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.sent[r.URL.Path] = body
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}
	mux.HandleFunc("POST /auth/magic-link", record)
	mux.HandleFunc("POST /auth/dev", record)
	return mux
}

func TestSignInKeepsTheLocaleAndItsReturnTarget(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	stub := &signInStub{sent: map[string]map[string]any{}}
	control := httptest.NewServer(stub.handler())
	t.Cleanup(control.Close)
	h.srv.auth = newAuthClient(control.URL, testPublic, h.srv.log)
	h.srv.cfg.ControlOrigin = control.URL

	body := h.get("/ar-xb/sign-in?returnTo=%2Far-xb%2F").Body.String()
	if n := strings.Count(body, `action="/ar-xb/sign-in"`); n != 2 {
		t.Errorf("%d sign-in forms post to /ar-xb/sign-in, want both", n)
	}
	for _, want := range []string{
		`<input type="hidden" name="returnTo" value="/ar-xb/" />`,
		`href="` + control.URL + `/auth/github/start?locale=ar-xb&amp;returnTo=%2Far-xb%2F"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/ar-xb/sign-in lacks %s", want)
		}
	}

	// The email link: the control plane learns the language, and the
	// confirmation is in it.
	rec := h.post("/ar-xb/sign-in", url.Values{"intent": {"magic-link"}, "email": {"ada@example.com"}, "returnTo": {"/ar-xb/"}})
	want := "/ar-xb/sign-in/sent?returnTo=%2Far-xb%2F"
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Errorf("POST /ar-xb/sign-in = %d to %q, want 303 to %q", rec.Code, rec.Header().Get("Location"), want)
	}
	if got := stub.sent["/auth/magic-link"]; got["locale"] != "ar-xb" || got["returnTo"] != "/ar-xb/" {
		t.Errorf("the control plane was sent %v, want locale ar-xb and returnTo /ar-xb/", got)
	}
	confirm := h.get(rec.Header().Get("Location"), rec.Result().Cookies()...).Body.String()
	if !strings.Contains(confirm, htmlOpen(localeOf(t, h.srv.bundle(), "ar-xb"))) || !strings.Contains(confirm, `action="/ar-xb/sign-in"`) {
		t.Error("the confirmation page, and its form to send the link again, are not in ar-XB")
	}

	// Development sign-in returns straight to the page, in its language.
	rec = h.post("/ar-xb/sign-in", url.Values{"intent": {"dev"}, "email": {"ada@example.com"}, "returnTo": {"/ar-xb/"}})
	if rec.Header().Get("Location") != "/ar-xb/" || stub.sent["/auth/dev"]["locale"] != "ar-xb" {
		t.Errorf("dev sign-in = %q, sent %v; want /ar-xb/ with locale ar-xb", rec.Header().Get("Location"), stub.sent["/auth/dev"])
	}

	// English is what it was: no prefix, no locale sent.
	body = h.get("/sign-in?returnTo=%2Fmodels").Body.String()
	if strings.Contains(body, "locale=") || strings.Count(body, `action="/sign-in"`) != 2 {
		t.Error("the English sign-in page changed")
	}
	h.post("/sign-in", url.Values{"intent": {"magic-link"}, "email": {"ada@example.com"}, "returnTo": {"/"}})
	if _, sent := stub.sent["/auth/magic-link"]["locale"]; sent {
		t.Error("an English sign-in sent a locale")
	}
}

/* --------------------------------------------------------- desktop app -- */

// The desktop app starts a sign-in in its window at /sign-in/desktop/start,
// one URL for every language like the app's other routes, and the site sends
// the window on to the sign-in page in the reader's language, which the app
// opens in the system browser (docs/I18N.md §2, "Desktop shell locale").
func TestDesktopSignInOpensInTheReadersLanguage(t *testing.T) {
	h, _ := newDesktopHarness(t)
	h.srv.i18n = pseudoLocales(t)
	_, h.handler = h.srv.handler(http.NotFoundHandler())
	const handoff = "port=49152&code_challenge=" + rfcChallenge + "&code_challenge_method=S256&returnTo=%2Fsettings"
	chose := func(id string) *http.Cookie { return &http.Cookie{Name: localeCookie, Value: id} }

	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		want   string
	}{
		{"the reader chose ar-xb", chose("ar-xb"), "/ar-xb/sign-in"},
		{"the reader chose en-xa", chose("en-xa"), "/en-xa/sign-in"},
		// The app adds the system's language itself when the site names none.
		{"no choice", nil, "/sign-in"},
		{"the reader chose English", chose("en"), "/sign-in"},
		{"a language this site does not ship", chose("de"), "/sign-in"},
	} {
		var cookies []*http.Cookie
		if tc.cookie != nil {
			cookies = append(cookies, tc.cookie)
		}
		rec := h.get("/sign-in/desktop/start?"+handoff, cookies...)
		next, err := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusSeeOther || err != nil || next.Path != tc.want {
			t.Errorf("%s: status %d to %q, want 303 to %s", tc.name, rec.Code, rec.Header().Get("Location"), tc.want)
			continue
		}
		var flow string
		for _, c := range rec.Result().Cookies() {
			if c.Name == desktopFlowCookie {
				flow = c.Value
			}
		}
		if d := desktopFromQuery(next.Query()); d == nil || d.Flow != flow || flow == "" || next.Query().Get("returnTo") != "/settings" {
			t.Errorf("%s: %q does not carry the handoff and the window's flow", tc.name, next)
		}
	}

	// Its refusals are in that language too.
	if rec := h.get("/sign-in/desktop/start?port=80&code_challenge="+rfcChallenge, chose("ar-xb")); rec.Header().Get("Location") != "/ar-xb/sign-in?error=desktop_start" {
		t.Errorf("a refused start goes to %q, want /ar-xb/sign-in?error=desktop_start", rec.Header().Get("Location"))
	}
	if rec := h.get("/sign-in/desktop/complete?code=c&code_verifier=v", chose("ar-xb")); rec.Header().Get("Location") != "/ar-xb/sign-in?error=desktop_flow" {
		t.Errorf("a completion without the window's flow goes to %q, want /ar-xb/sign-in?error=desktop_flow", rec.Header().Get("Location"))
	}

	// Under a prefix the relay is the one URL, query and all, and sets nothing.
	rec := h.get("/ar-xb/sign-in/desktop/start?"+handoff, chose("en-xa"))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/sign-in/desktop/start?"+handoff || len(rec.Result().Cookies()) != 0 {
		t.Errorf("/ar-xb/sign-in/desktop/start = %d to %q, %d cookies; want 301 to the unprefixed start",
			rec.Code, rec.Header().Get("Location"), len(rec.Result().Cookies()))
	}
}

/* ------------------------------------------------------ profile settings -- */

// localeStack is newStack, the settings pages against the real control
// plane, serving b's locales. It reports the bodies of PATCH /api/v1/me.
func localeStack(t *testing.T, b *i18n.Bundle) (*stack, func() []string) {
	t.Helper()
	st := newStack(t, func(cfg *config.Config) { cfg.PseudoLocales = true })
	var (
		mu      sync.Mutex
		patched []string
	)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st.intercept != nil && st.intercept(w, r) {
			return
		}
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/me" {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			patched = append(patched, string(raw))
			mu.Unlock()
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
		}
		st.auth.ServeHTTP(w, r)
	}))
	t.Cleanup(control.Close)

	srv := *localeHarness(t, b).srv
	srv.auth = newAuthClient(control.URL, testPublic, srv.log)
	srv.origins.Control = control.URL
	_, st.web = srv.handler(http.NotFoundHandler())
	return st, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(patched)
	}
}

func TestLocalizedInvitationFormSendsItsLocaleWithoutJavaScript(t *testing.T) {
	b := pseudoLocales(t)
	st, _ := localeStack(t, b)
	owner := st.signIn(t, "owner@example.com")
	if rec := owner.submit("/locale", url.Values{
		"locale":   {"ar-xb"},
		"returnTo": {"/settings/members"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save locale = %d, want 303", rec.Code)
	}

	var payload map[string]string
	st.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/org/invitations" {
			return false
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode invitation request: %v", err)
		}
		httpx.WriteJSON(w, r, http.StatusCreated, map[string]any{"invitation": map[string]any{}})
		return true
	}

	rec := owner.submit("/settings/members/invite", url.Values{
		"email": {"new@example.com"},
		"role":  {"member"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("localized invitation form = %d, want 303", rec.Code)
	}
	if payload["locale"] != "ar-xb" {
		t.Errorf(
			"control-plane payload = %v, want locale ar-xb; redirect %q body %q",
			payload,
			rec.Header().Get("Location"),
			rec.Body.String(),
		)
	}
}

func TestProfileSettingsChooseTheLanguage(t *testing.T) {
	b := pseudoLocales(t)
	st, patched := localeStack(t, b)
	ada := st.signIn(t, "ada@example.com")

	body := ada.page("/settings/profile")
	form := regexp.MustCompile(`(?s)<form class="st-inline-form" method="post" action="/locale">.*?</form>`).FindString(body)
	for _, want := range []string{
		`<input type="hidden" name="` + csrfField + `"`,
		`<input type="hidden" name="returnTo" value="/settings/profile" />`,
		`<select id="pf-language-select" name="locale" aria-describedby="pf-language-hint">`,
		`<option value="">Not set</option>`,
		`<option value="en" lang="en">English</option>`,
		`<option value="ar-xb" lang="ar-XB">Pseudo (RTL)</option>`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("the Language form lacks %s\n%s", want, form)
		}
	}
	// A new language is a new <html lang dir>, which settings.ts's in-place
	// swap would not bring: the form is left to the browser.
	if strings.Contains(form, "data-enhance") {
		t.Error("the Language form is enhanced by settings.ts")
	}

	rec := ada.submit("/locale", url.Values{"locale": {"ar-xb"}, "returnTo": {"/settings/profile"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/profile" {
		t.Fatalf("choosing ar-xb = %d to %q, want 303 to /settings/profile", rec.Code, rec.Header().Get("Location"))
	}
	if got := patched(); len(got) != 1 || got[0] != `{"locale":"ar-xb"}` {
		t.Errorf("PATCH /api/v1/me bodies = %q, want one with the locale", got)
	}

	xb := localeOf(t, b, "ar-xb")
	body = ada.page("/settings/profile")
	for _, want := range []string{
		htmlOpen(xb),
		`<option value="ar-xb" lang="ar-XB" selected>Pseudo (RTL)</option>`,
		xb.Localizer().T("common.locale.saved"),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("after choosing ar-xb, /settings/profile lacks %q", want)
		}
	}
	// Every settings page follows the choice.
	if body := ada.page("/settings/keys"); !strings.Contains(body, htmlOpen(xb)) {
		t.Error("/settings/keys does not follow the choice")
	}

	// Without the session's token the choice is this browser's only.
	ada.post("/locale", url.Values{"locale": {"en-xa"}, "returnTo": {"/settings/profile"}}, map[string]string{"Origin": testPublic})
	if got := patched(); len(got) != 1 {
		t.Errorf("a form without the token saved to the account: %q", got)
	}
	// "Not set" forgets it.
	ada.submit("/locale", url.Values{"locale": {""}, "returnTo": {"/settings/profile"}})
	body = ada.page("/settings/profile")
	if !strings.Contains(body, htmlOpen(b.Default())) || regexp.MustCompile(`<option value="[a-z-]+" lang="[^"]+" selected>`).MatchString(body) {
		t.Error("after Not set, profile settings are not English with no language selected")
	}
}

/* ---------------------------------------------------------- English only -- */

// With English alone, as production ships today, nothing of this is shown.
func TestEnglishOnlyShowsNoLanguageChoice(t *testing.T) {
	english, err := englishOnly()
	if err != nil {
		t.Fatal(err)
	}
	h := localeHarness(t, english)
	for _, path := range []string{"/", "/sign-in", "/no-such-page"} {
		rec := h.send(http.MethodGet, path, http.Header{"Accept-Language": {"de"}}, &http.Cookie{Name: localeCookie, Value: "de"})
		body := rec.Body.String()
		for _, absent := range []string{"locale-switcher", "locale-banner", localeCookie} {
			if strings.Contains(body, absent) {
				t.Errorf("%s shows %s with English alone", path, absent)
			}
		}
		if slices.Contains(rec.Header().Values("Vary"), "Accept-Language") || localeCookieOf(rec) != nil {
			t.Errorf("%s varies on Accept-Language or sets the language cookie with English alone", path)
		}
	}
	if rec := h.get("/en/"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/" {
		t.Errorf("/en/ = %d to %q, want 301 to /", rec.Code, rec.Header().Get("Location"))
	}
	if rec := h.get("/en-xa/"); rec.Code != http.StatusNotFound {
		t.Errorf("/en-xa/ = %d without the pseudo-locales, want 404", rec.Code)
	}
}

// The cookies page lists the language cookie, as POST /locale sets it, once a
// reader can choose.
