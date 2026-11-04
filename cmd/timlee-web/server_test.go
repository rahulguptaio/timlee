package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rahulguptaio/timlee/internal/assets"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/render"
	templates "github.com/rahulguptaio/timlee/web/templates"
)

// stubControl stands in for the control plane. The session it reports is what
// the server must put in the markup, which is the whole point of this layer.
type stubControl struct {
	signedIn bool
	// denied reports the signed-in user as not on the prompt allowlist.
	denied bool
	calls  int
}

func (s *stubControl) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/session", func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		w.Header().Set("Content-Type", "application/json")
		if !s.signedIn {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		allowed := "true"
		if s.denied {
			allowed = "false"
		}
		_, _ = io.WriteString(w, `{"user":{"id":"u1","email":"ada@example.com","name":"Ada Lovelace","avatarUrl":"","allowed":`+allowed+`},`+
			`"org":{"id":"o1","name":"Acme Corp","slug":"acme","role":"admin"}}`)
	})
	mux.HandleFunc("GET /auth/providers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"providers":["github","google"],"magicLink":true,"devLogin":true,"mailOutbox":true}`)
	})
	return mux
}

type harness struct {
	handler http.Handler
	control *stubControl
	srv     *server
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	control := &stubControl{}
	upstream := httptest.NewServer(control.handler())
	t.Cleanup(upstream.Close)

	manifest := assets.NewManifest("/assets/")
	manifest.Add("app.css", "app-TEST.css", []byte("body{}"))
	manifest.Add("app.js", "app-TEST.js", []byte("export{}"))

	funcs := render.FuncMap()
	for name, fn := range manifest.FuncMap() {
		funcs[name] = fn
	}

	engine, err := render.New(render.Options{FS: templates.FS(), Funcs: funcs})
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	cfg := config.Config{
		Env:           "test",
		PublicOrigin:  "https://timlee.example",
		ControlOrigin: upstream.URL,
		SessionTTL:    14 * 24 * time.Hour,
	}

	logger := slog.New(slog.DiscardHandler)
	srv := &server{
		cfg:    cfg,
		log:    logger,
		engine: engine,
		auth:   newAuthClient(upstream.URL, cfg.PublicOrigin, logger),
		origins: render.Origins{
			Public:  cfg.PublicOrigin,
			Control: upstream.URL,
		},
	}

	assetFS := fstest.MapFS{"app-TEST.css": &fstest.MapFile{Data: []byte("body{}")}}
	_, handler := srv.handler(assets.NewHandler(assets.HandlerOptions{FS: assetFS, Manifest: manifest}))

	return &harness{handler: handler, control: control, srv: srv}
}

// get issues a request, optionally carrying a session cookie so the server
// has something to resolve.
func (h *harness) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec
}

func (h *harness) post(path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://timlee.example")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec
}

func session() *http.Cookie { return &http.Cookie{Name: "timlee.sid", Value: "sid.signature"} }

// TestLandingIsCompleteInTheFirstResponse is the point of the whole package:
// the body a browser receives already holds the page, not a root div.
func TestLandingIsCompleteInTheFirstResponse(t *testing.T) {
	h := newHarness(t)
	rec := h.get("/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	want := []string{
		`<h1 id="hero-title" class="hero-title">Describe an app. Timlee builds it.</h1>`,
		"Svelte app built on a shared design system",
		`href="/sign-in?returnTo=%2Fapps"`,
		`<a class="skip-link" href="#main">Skip to content</a>`,
		`<main id="main">`,
		`<link rel="canonical" href="https://timlee.example/"`,
	}
	for _, fragment := range want {
		if !strings.Contains(body, fragment) {
			t.Errorf("landing page is missing %q", fragment)
		}
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("a URL was rejected by the escaper; a template is interpolating into an attribute unsafely")
	}
}

func TestNavReflectsTheSessionTheServerResolved(t *testing.T) {
	tests := []struct {
		name     string
		signedIn bool
		cookie   bool
		want     []string
		absent   []string
	}{
		{
			name:   "signed out",
			want:   []string{`href="/sign-in">Sign in`, `href="/sign-in?returnTo=%2Fapps">Sign in to start building`},
			absent: []string{"account-name", "Your apps"},
		},
		{
			name:     "signed in",
			signedIn: true,
			cookie:   true,
			want: []string{
				`href="/apps">Your apps`,
				`href="/apps">Open your apps`,
				`<span class="account-name" dir="auto">Ada Lovelace</span>`,
				"ada@example.com",
				"Acme Corp",
			},
			absent: []string{`href="/sign-in">Sign in`},
		},
		{
			// A session cookie the control plane does not recognise must
			// render exactly like no cookie at all.
			name:     "cookie but no session",
			signedIn: false,
			cookie:   true,
			want:     []string{`href="/sign-in">Sign in`},
			absent:   []string{"account-name"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.control.signedIn = tc.signedIn

			var cookies []*http.Cookie
			if tc.cookie {
				cookies = append(cookies, session())
			}
			body := h.get("/", cookies...).Body.String()

			for _, fragment := range tc.want {
				if !strings.Contains(body, fragment) {
					t.Errorf("missing %q", fragment)
				}
			}
			for _, fragment := range tc.absent {
				if strings.Contains(body, fragment) {
					t.Errorf("unexpectedly present: %q", fragment)
				}
			}

			// No cookie, no call: most readers are signed out and the
			// landing page must not wait on the control plane for them.
			if !tc.cookie && h.control.calls != 0 {
				t.Errorf("called the control plane %d times without a session cookie", h.control.calls)
			}
		})
	}
}

func TestThemeIsResolvedServerSide(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		cookie string
		hint   string
		want   []string
	}{
		// No choice, no attribute: color-scheme follows the system in every
		// engine, WebKit included, which never sends a client hint.
		{name: "landing follows the system", path: "/", want: []string{
			`<html lang="en" dir="ltr">`, `<meta name="color-scheme" content="light dark" />`,
		}},
		{name: "a client hint is not a choice", path: "/", hint: "dark", want: []string{`<html lang="en" dir="ltr">`}},
		{name: "cookie switches the first response", path: "/", cookie: "dark", want: []string{
			`<html lang="en" dir="ltr" data-theme="dark">`, `<meta name="color-scheme" content="dark" />`,
		}},
		{name: "sign-in follows the cookie", path: "/sign-in", cookie: "dark", want: []string{`data-theme="dark"`}},
		// Toggling back to the system's own scheme forgets the choice.
		{name: "the toggle can return to the system", path: "/", cookie: "dark", want: []string{
			`name="theme" value="system"`, `name="theme" value="light"`,
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: render.ThemeCookie, Value: tc.cookie})
			}
			if tc.hint != "" {
				r.Header.Set("Sec-CH-Prefers-Color-Scheme", tc.hint)
			}
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, r)

			for _, want := range tc.want {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("response does not contain %q", want)
				}
			}
			if got := rec.Header().Get("Accept-CH"); got != "" {
				t.Errorf("Accept-CH = %q: nothing reads the hint", got)
			}
			// Nothing is left for a script to correct after paint.
			if strings.Contains(rec.Body.String(), "localStorage") {
				t.Error("the document still fixes the theme up in the browser")
			}
		})
	}
}

func TestThemeFormSetsTheCookieAndReturns(t *testing.T) {
	h := newHarness(t)

	rec := h.post("/theme", url.Values{"theme": {"dark"}, "returnTo": {"/sign-in"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/sign-in" {
		t.Errorf("Location = %q", got)
	}

	themeCookie := func(rec *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == render.ThemeCookie {
				return c
			}
		}
		return nil
	}
	found := themeCookie(rec)
	if found == nil || found.Value != "dark" {
		t.Fatalf("theme cookie = %v", found)
	}
	if found.HttpOnly {
		t.Error("the theme cookie must stay readable so an enhancement can keep in step")
	}

	// "system" forgets the choice: the cookie is deleted, not set.
	if gone := themeCookie(h.post("/theme", url.Values{"theme": {"system"}})); gone == nil || gone.MaxAge >= 0 {
		t.Errorf("theme cookie after system = %v, want it expired", gone)
	}

	if bad := h.post("/theme", url.Values{"theme": {"neon"}}); bad.Code != http.StatusBadRequest {
		t.Errorf("unknown theme status = %d, want 400", bad.Code)
	}
}

func TestSecurityHeadersAndNonce(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/", "/sign-in", "/nope"} {
		t.Run(path, func(t *testing.T) {
			rec := h.get(path)
			csp := rec.Header().Get("Content-Security-Policy")

			if csp == "" {
				t.Fatal("no Content-Security-Policy")
			}
			if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
				t.Errorf("policy relaxes inline execution: %s", csp)
			}
			for _, directive := range []string{"default-src 'none'", "base-uri 'none'", "frame-ancestors 'none'", "object-src 'none'"} {
				if !strings.Contains(csp, directive) {
					t.Errorf("policy is missing %q", directive)
				}
			}

			nonce := nonceFrom(csp)
			if nonce == "" {
				t.Fatalf("no nonce in %q", csp)
			}
			// The nonce in the header must be the one the document carries,
			// or the inline block it authorises is blocked.
			if !strings.Contains(rec.Body.String(), `nonce="`+nonce+`"`) {
				t.Error("the document does not carry the nonce the policy names")
			}

			for header, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "strict-origin-when-cross-origin",
				"Content-Type":           "text/html; charset=utf-8",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

func nonceFrom(csp string) string {
	_, rest, found := strings.Cut(csp, "script-src 'self' 'nonce-")
	if !found {
		return ""
	}
	nonce, _, _ := strings.Cut(rest, "'")
	return nonce
}

// TestEveryResponseCarriesADistinctNonce guards the mistake that makes a
// nonce worthless: reusing one across responses.
func TestEveryResponseCarriesADistinctNonce(t *testing.T) {
	h := newHarness(t)
	first := nonceFrom(h.get("/").Header().Get("Content-Security-Policy"))
	second := nonceFrom(h.get("/").Header().Get("Content-Security-Policy"))
	if first == "" || first == second {
		t.Errorf("nonces %q and %q are not distinct", first, second)
	}
}

func TestSignInRendersEveryWayInThatIsConfigured(t *testing.T) {
	h := newHarness(t)
	body := h.get("/sign-in?returnTo=/apps").Body.String()

	for _, fragment := range []string{
		"Sign in or create an account",
		"Continue with <bdi>GitHub</bdi>",
		"Continue with <bdi>Google</bdi>",
		`method="post" action="/sign-in"`,
		`name="intent" value="magic-link"`,
		`name="intent" value="dev"`,
		`name="returnTo" value="/apps"`,
		"noindex,nofollow",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("sign-in page is missing %q", fragment)
		}
	}

	// A reader who already has a session has nothing to do here.
	h.control.signedIn = true
	rec := h.get("/sign-in?returnTo=/apps", session())
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/apps" {
		t.Errorf("signed-in sign-in = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSafePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "/fallback"},
		{"/apps", "/apps"},
		{"/apps?view=grid", "/apps?view=grid"},
		{"//evil.example", "/fallback"},
		{"/\\evil.example", "/fallback"},
		{"https://evil.example", "/fallback"},
		{"javascript:alert(1)", "/fallback"},
		{"apps", "/fallback"},
	}
	for _, tc := range tests {
		if got := safePath(tc.in, "/fallback"); got != tc.want {
			t.Errorf("safePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHealthReportsTheRenderablePages(t *testing.T) {
	h := newHarness(t)
	rec := h.get("/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Status string   `json:"status"`
		Pages  []string `json:"pages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" || len(body.Pages) == 0 {
		t.Errorf("health = %+v", body)
	}
}

// TestMenusOpenWithoutScript checks the header's menus are popovers their
// buttons open, which is what makes Escape, light dismiss and focus return
// work before, and without, any script.
func TestMenusOpenWithoutScript(t *testing.T) {
	h := newHarness(t)
	h.control.signedIn = true
	body := h.get("/", session()).Body.String()

	for _, fragment := range []string{
		`popovertarget="site-menu"`,
		`<nav id="site-menu" class="nav-menu" popover aria-label="Menu">`,
		`popovertarget="account-menu"`,
		`<div id="account-menu" class="account-panel" popover>`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("header is missing %q", fragment)
		}
	}
	if strings.Contains(body, "<details") {
		t.Error("a menu is still a <details>, which needs script to close on Escape")
	}
}

// TestSentViewNamesTheAddress covers the page a reader lands on after asking
// for a link: it names the address, and its resend form posts that address.
// The address travels from the form in a sealed cookie (signin.go).
func TestSentViewNamesTheAddress(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	if err := h.srv.sealSent(rec, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	body := h.get("/sign-in/sent", rec.Result().Cookies()...).Body.String()

	for _, fragment := range []string{
		"We sent a sign-in link to <b><bdi>ada@example.com</bdi></b>",
		`<input type="hidden" name="email" value="ada@example.com" />`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("sent view is missing %q", fragment)
		}
	}
}

// TestPageCarriesTheAllowlistDecision: the control plane decides whether the
// signed-in account may submit prompts, and every page knows the answer.
func TestPageCarriesTheAllowlistDecision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		signedIn bool
		denied   bool
		want     bool
	}{
		{name: "signed out", want: false},
		{name: "allowlisted", signedIn: true, want: true},
		{name: "not allowlisted", signedIn: true, denied: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.control.signedIn, h.control.denied = tc.signedIn, tc.denied
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(session())
			if got := h.srv.page(r).Allowed(); got != tc.want {
				t.Errorf("Allowed() = %v, want %v", got, tc.want)
			}
		})
	}
}
