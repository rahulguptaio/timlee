package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rahulguptaio/timlee/internal/assets"
	"github.com/rahulguptaio/timlee/internal/authsvc"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
	"github.com/rahulguptaio/timlee/internal/store"
	templates "github.com/rahulguptaio/timlee/web/templates"
)

// These tests run the settings pages against the real control plane: authsvc
// on an in-memory database, reached over HTTP exactly as in production. Who
// may grant what, the last owner and single-use invitations are therefore the
// control plane's own rules, not a stub's idea of them — and a page that
// offered something those rules refuse would show up here.

const testPublic = "https://timlee.example"

type stack struct {
	web  http.Handler
	auth *authsvc.Service
	db   *store.Store
	srv  *server
	// intercept answers a control-plane request before authsvc sees it,
	// reporting whether it did. It stands in for a newer control plane.
	intercept func(w http.ResponseWriter, r *http.Request) bool
}

func newStack(t *testing.T, adjust ...func(*config.Config)) *stack {
	t.Helper()

	st := &stack{}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st.intercept != nil && st.intercept(w, r) {
			return
		}
		st.auth.ServeHTTP(w, r)
	}))
	t.Cleanup(control.Close)

	cfg := config.Config{
		Env:           "development",
		PublicOrigin:  testPublic,
		ControlOrigin: control.URL,
		SessionSecret: "session-secret-for-tests-0123456789abcdef",
		InternalToken: "internal-token-for-tests-0123456789abcdef",
		SessionTTL:    time.Hour,
		AllowDevLogin: true,
		MailOutbox:    scratchDir(t),
		Providers:     map[string]config.Provider{},
	}
	for _, fn := range adjust {
		fn(&cfg)
	}

	db, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	quiet := slog.New(slog.DiscardHandler)
	st.db = db
	st.auth = authsvc.New(context.Background(), cfg, db, quiet)

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

	srv := &server{
		cfg:     cfg,
		log:     quiet,
		engine:  engine,
		auth:    newAuthClient(control.URL, cfg.PublicOrigin, quiet),
		origins: render.Origins{Public: cfg.PublicOrigin, Control: control.URL},
	}
	st.srv = srv
	assetFS := fstest.MapFS{"app-TEST.css": &fstest.MapFile{Data: []byte("body{}")}}
	_, st.web = srv.handler(assets.NewHandler(assets.HandlerOptions{FS: assetFS, Manifest: manifest}))
	return st
}

func TestSettingsPagesUsePseudoLocalesAndBidiSafeValues(t *testing.T) {
	st := newStack(t)
	st.srv.i18n = pseudoLocales(t)
	owner, _ := st.founder(t, "ada@example.com")

	xa := localeOf(t, st.srv.i18n, "en-xa").Localizer()
	owner.jar[localeCookie] = "en-xa"
	accented := owner.page("/settings/profile")
	wantContains(t, accented,
		htmlOpen(xa.Locale()),
		xa.T("settings.profile.title"),
		`<bdi>ada@example.com</bdi>`,
		`<script type="application/json" id="timlee-i18n"`,
		`"errors.settings.network"`,
		`"components.toast.dismiss"`,
	)

	xb := localeOf(t, st.srv.i18n, "ar-xb").Localizer()
	owner.jar[localeCookie] = "ar-xb"
	rtl := owner.page("/settings/keys")
	wantContains(t, rtl,
		htmlOpen(xb.Locale()),
		xb.T("settings.keys.title"),
		`method="post" action="/settings/keys"`,
		`<bdi dir="auto">`,
		`"lang":"ar-XB"`,
		`"dir":"rtl"`,
	)
}

func TestControlErrorCopyUsesStableCodesNotProtocolMessages(t *testing.T) {
	t.Parallel()

	l := english(t)
	first := generic(l, &apiError{
		Status:  http.StatusForbidden,
		Type:    "forbidden",
		Message: "Requires admin access.",
	})
	second := generic(l, &apiError{
		Status:  http.StatusForbidden,
		Type:    "forbidden",
		Message: "هذا النص من عقد البروتوكول ولا يظهر للمستخدم",
	})
	if first != second || first != l.T("errors.settings.roleChanged") {
		t.Errorf("forbidden mappings = %q and %q, want stable localized copy", first, second)
	}

	origin := generic(l, &apiError{
		Status:  http.StatusForbidden,
		Type:    "forbidden_origin",
		Message: "arbitrary protocol detail",
	})
	if origin != l.T("errors.settings.forbiddenOrigin") {
		t.Errorf("forbidden_origin mapping = %q", origin)
	}
}

// scratchDir is a mail outbox inside the package, because tests here may not
// write to the system temporary directory.
func scratchDir(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(".scratch-probe", 0o700); err != nil {
		t.Fatalf("scratch root: %v", err)
	}
	dir, err := os.MkdirTemp(".scratch-probe", "web-")
	if err != nil {
		t.Fatalf("scratch dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir)
		_ = os.Remove(".scratch-probe")
	})
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("scratch path: %v", err)
	}
	return abs
}

/* -------------------------------------------------------------- people -- */

// signIn starts a session through the control plane's development route,
// which creates the account — and an organisation it owns — on first use.
func (st *stack) signIn(t *testing.T, email string) *browser {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/dev", strings.NewReader(`{"email":"`+email+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testPublic)
	rec := httptest.NewRecorder()
	st.auth.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dev sign-in for %s: %d %s", email, rec.Code, rec.Body)
	}
	b := &browser{t: t, st: st, jar: map[string]string{}}
	b.keep(rec.Result().Cookies())
	user, err := st.db.UserByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("find %s: %v", email, err)
	}
	b.userID = user.ID
	return b
}

// founder is a new account and the organisation it owns.
func (st *stack) founder(t *testing.T, email string) (*browser, string) {
	t.Helper()
	b := st.signIn(t, email)
	orgs, err := st.db.OrgsForUser(t.Context(), b.userID)
	if err != nil || len(orgs) == 0 {
		t.Fatalf("orgs for %s = %v, %v", email, orgs, err)
	}
	return b, orgs[0].ID
}

// join adds a new account to orgID at a role and makes that organisation the
// one its session acts on.
func (st *stack) join(t *testing.T, email, orgID string, role store.Role) *browser {
	t.Helper()
	b := st.signIn(t, email)
	if err := st.db.AddMember(t.Context(), orgID, b.userID, role); err != nil {
		t.Fatalf("add %s as %s: %v", email, role, err)
	}
	b.switchTo(orgID)
	return b
}

func (st *stack) roleOf(t *testing.T, orgID, userID string) store.Role {
	t.Helper()
	role, err := st.db.RoleOf(t.Context(), orgID, userID)
	if err != nil {
		t.Fatalf("role of %s: %v", userID, err)
	}
	return role
}

/* ------------------------------------------------------------- browser -- */

// browser is one person's cookie jar, sending requests the way a page this
// site served would send them.
type browser struct {
	t      *testing.T
	st     *stack
	jar    map[string]string
	userID string
}

func (b *browser) keep(cookies []*http.Cookie) {
	for _, c := range cookies {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.jar, c.Name)
			continue
		}
		b.jar[c.Name] = c.Value
	}
}

func (b *browser) send(r *http.Request) *httptest.ResponseRecorder {
	for name, value := range b.jar {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	b.st.web.ServeHTTP(rec, r)
	b.keep(rec.Result().Cookies())
	return rec
}

func (b *browser) get(path string) *httptest.ResponseRecorder {
	// A browser keeps the fragment to itself.
	path, _, _ = strings.Cut(path, "#")
	return b.send(httptest.NewRequest(http.MethodGet, path, nil))
}

func (b *browser) page(path string) string {
	b.t.Helper()
	rec := b.get(path)
	if rec.Code != http.StatusOK {
		b.t.Fatalf("GET %s = %d, want 200; Location %q", path, rec.Code, rec.Header().Get("Location"))
	}
	return rec.Body.String()
}

func (b *browser) post(path string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return b.send(r)
}

// submit posts a form as this site's own page would: from its origin, with
// the token bound to this session.
func (b *browser) submit(path string, form url.Values) *httptest.ResponseRecorder {
	if form == nil {
		form = url.Values{}
	}
	if !form.Has(csrfField) {
		form.Set(csrfField, b.token())
	}
	return b.post(path, form, map[string]string{"Origin": testPublic})
}

func (b *browser) token() string {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, value := range b.jar {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	return csrfToken(r)
}

// outcome submits a form, expects the redirect every settings form answers
// with, and returns the message the next page carries.
func (b *browser) outcome(path string, form url.Values) (tone, message, body string) {
	b.t.Helper()
	rec := b.submit(path, form)
	if rec.Code != http.StatusSeeOther {
		b.t.Fatalf("POST %s = %d, want 303; body %s", path, rec.Code, rec.Body)
	}
	next := b.get(rec.Header().Get("Location"))
	if next.Code != http.StatusOK {
		b.t.Fatalf("GET %s after POST = %d", rec.Header().Get("Location"), next.Code)
	}
	body = next.Body.String()
	tone, message = flashIn(body)
	return tone, message, body
}

func (b *browser) switchTo(orgID string) {
	b.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/org", strings.NewReader(`{"orgId":"`+orgID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testPublic)
	for name, value := range b.jar {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	b.st.auth.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.t.Fatalf("switch org: %d %s", rec.Code, rec.Body)
	}
}

var flashPattern = regexp.MustCompile(`(?s)<timlee-alert tone="(\w+)" class="st-flash" data-flash="\w+"[^>]*>.*?<p>(.*?)</p>`)

func flashIn(body string) (tone, message string) {
	m := flashPattern.FindStringSubmatch(body)
	if m == nil {
		return "", ""
	}
	return m[1], html.UnescapeString(m[2])
}

func wantFlash(t *testing.T, tone, message, wantTone, wantMessage string) {
	t.Helper()
	plain := isolates.Replace(message)
	if tone != wantTone || plain != wantMessage {
		t.Errorf("flash = %s %q\n want %s %q", tone, message, wantTone, wantMessage)
	}
}

func wantContains(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(body, f) {
			t.Errorf("page is missing %q", f)
		}
	}
}

func wantAbsent(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if strings.Contains(body, f) {
			t.Errorf("page unexpectedly contains %q", f)
		}
	}
}

// between is the part of body from the first from up to the next to, failing
// the test when either is missing.
func between(t *testing.T, body, from, to string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, from)
	part, _, closed := strings.Cut(after, to)
	if !ok || !closed {
		t.Fatalf("page has no %q … %q", from, to)
	}
	return from + part
}

// wantRefused checks that a form was turned away before it reached the
// control plane.
func wantRefused(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

/* ------------------------------------------------------------- shapes -- */

// TestRolesMatchTheControlPlane holds the restated ranking to authsvc's.
func TestRolesMatchTheControlPlane(t *testing.T) {
	all := []string{"viewer", "member", "admin", "owner", "", "root"}
	for _, a := range all {
		for _, b := range all {
			if got, want := role(a).atLeast(role(b)), store.Role(a).AtLeast(store.Role(b)); got != want {
				t.Errorf("%q atLeast %q = %v, store says %v", a, b, got, want)
			}
			if got, want := role(a).outranks(role(b)), store.Role(a).Outranks(store.Role(b)); got != want {
				t.Errorf("%q outranks %q = %v, store says %v", a, b, got, want)
			}
		}
		if got, want := role(a).valid(), store.Role(a).Valid(); got != want {
			t.Errorf("%q valid = %v, store says %v", a, got, want)
		}
	}
}

/* -------------------------------------------------------------- access -- */

func TestSettingsNeedASession(t *testing.T) {
	st := newStack(t)
	anon := &browser{t: t, st: st, jar: map[string]string{}}

	for _, path := range []string{"/settings", "/settings/profile", "/settings/members", "/settings/keys", "/settings/audit"} {
		rec := anon.get(path)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != render.SignInPath(path) {
			t.Errorf("GET %s = %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s rendered markup before redirecting", path)
		}
	}

	rec := anon.post("/settings/keys", url.Values{"label": {"x"}}, map[string]string{"Origin": testPublic})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/sign-in") {
		t.Errorf("signed-out POST = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestAnOutageIsNotASignInPage: a reader who may well be signed in is told
// what is wrong instead of being sent to a sign-in page that cannot work.
func TestAnOutageIsNotASignInPage(t *testing.T) {
	st := newStack(t)
	ada, _ := st.founder(t, "ada@example.com")

	for status, want := range map[int]string{
		http.StatusBadGateway:      "control plane is not answering",
		http.StatusTooManyRequests: "more requests than it can answer",
	} {
		st.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/auth/session" {
				return false
			}
			http.Error(w, `{"error":{"message":"No.","type":"x"}}`, status)
			return true
		}
		rec := ada.get("/settings/members")
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("control plane %d: page = %d, Retry-After %q", status, rec.Code, rec.Header().Get("Retry-After"))
		}
		wantContains(t, rec.Body.String(), "Settings are unavailable right now", want, `href="/settings/members">Try again`)
	}
}

// TestEveryPageRendersForAnOwner renders each surface complete and under the
// page policy, with the section marked in the navigation.
func TestEveryPageRendersForAnOwner(t *testing.T) {
	st := newStack(t)
	owner, _ := st.founder(t, "ada@example.com")

	for path, heading := range map[string]string{
		"/settings":              "<h1>Overview</h1>",
		"/settings/profile":      "<h1>Profile</h1>",
		"/settings/security":     "<h1>Security</h1>",
		"/settings/organisation": "<h1>Organisation</h1>",
		"/settings/members":      "<h1>Members</h1>",
		"/settings/keys":         "<h1>API keys</h1>",
		"/settings/audit":        "<h1>Audit log</h1>",
	} {
		rec := owner.get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		body := rec.Body.String()
		wantContains(t, body, heading, `href="`+path+`" aria-current="page"`, "noindex", `<main id="main"`)
		wantAbsent(t, body, "ZgotmplZ")
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q", path, got)
		}
		if strings.Contains(body, ` style="`) {
			t.Errorf("%s carries a style attribute, which the policy forbids", path)
		}
	}
}

// TestRoleGatingIsDecidedOnTheServer renders the same organisation for each
// role. Every control present must be one the control plane would accept
// from that reader; a regression that offered a forbidden action fails here.
func TestRoleGatingIsDecidedOnTheServer(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	admin := st.join(t, "grace@example.com", orgID, store.RoleAdmin)
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	viewer := st.join(t, "vic@example.com", orgID, store.RoleViewer)

	roleForm := func(b *browser) string { return `action="/settings/members/` + b.userID + `/role"` }
	removeLink := func(b *browser) string { return `href="/settings/members?remove=` + b.userID }

	t.Run("owner", func(t *testing.T) {
		members := owner.page("/settings/members")
		wantContains(t, members, `action="/settings/members/invite"`, `<option value="owner"`, `value="owner"`,
			roleForm(admin), roleForm(member), roleForm(viewer), roleForm(owner),
			removeLink(admin), removeLink(member), removeLink(viewer),
			"You are the only owner.")
		// Nobody is offered a way to remove themselves from here.
		wantAbsent(t, members, removeLink(owner))
		wantContains(t, owner.page("/settings/keys"), `action="/settings/keys"`)
		wantContains(t, owner.page("/settings/audit"), `<timlee-table label="Audit log"`)
	})

	t.Run("admin", func(t *testing.T) {
		members := admin.page("/settings/members")
		wantContains(t, members, `action="/settings/members/invite"`,
			roleForm(admin), roleForm(member), roleForm(viewer), removeLink(member), removeLink(viewer),
			"Only an owner can change an owner.")
		// The owner outranks an admin, and owner is above what an admin may
		// grant: neither may appear anywhere on the page.
		wantAbsent(t, members, roleForm(owner), removeLink(owner), `value="owner"`, removeLink(admin))
		wantContains(t, admin.page("/settings/keys"), `action="/settings/keys"`)
		wantContains(t, admin.page("/settings/audit"), `<timlee-table label="Audit log"`)
	})

	for name, b := range map[string]*browser{"member": member, "viewer": viewer} {
		t.Run(name, func(t *testing.T) {
			members := b.page("/settings/members")
			wantContains(t, members, "Only owners and admins can invite people", "ada@example.com", "mailto:grace@example.com")
			wantAbsent(t, members, `action="/settings/members/invite"`, `/role"`, `?remove=`, `?revoke=`, "Pending invitations")

			keys := b.page("/settings/keys")
			wantContains(t, keys, "Only owners and admins can see, create and revoke")
			wantAbsent(t, keys, `action="/settings/keys"`, `?revoke=`)

			audit := b.page("/settings/audit")
			wantContains(t, audit, "Only owners and admins can read")
			wantAbsent(t, audit, "<table", `action="/settings/audit"`)

			wantContains(t, b.page("/settings"), "managed by owners and admins")
		})
	}
}

/* ------------------------------------------------------------- profile -- */

func TestProfileName(t *testing.T) {
	st := newStack(t)
	ada, _ := st.founder(t, "ada@example.com")

	t.Run("success", func(t *testing.T) {
		tone, message, body := ada.outcome("/settings/profile", url.Values{"name": {"  Ada Lovelace  "}})
		wantFlash(t, tone, message, "success", "Saved your name.")
		wantContains(t, body, `value="Ada Lovelace"`)
		if user, _ := st.db.UserByID(t.Context(), ada.userID); user.Name != "Ada Lovelace" {
			t.Errorf("stored name = %q", user.Name)
		}
	})

	t.Run("validation", func(t *testing.T) {
		for _, bad := range []string{"Ada\x07", strings.Repeat("é", 61)} {
			rec := ada.submit("/settings/profile", url.Values{"name": {bad}})
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			wantContains(t, rec.Body.String(), `aria-invalid="true"`, "<title>Error: Profile", `<a href="#pf-name-input">`)
		}
		if user, _ := st.db.UserByID(t.Context(), ada.userID); user.Name != "Ada Lovelace" {
			t.Errorf("a refused name was stored: %q", user.Name)
		}
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, ada.post("/settings/profile", url.Values{"name": {"Mallory"}}, map[string]string{"Origin": testPublic}))
		wantRefused(t, ada.submit("/settings/profile", url.Values{"name": {"Mallory"}, csrfField: {"forged"}}))
		if user, _ := st.db.UserByID(t.Context(), ada.userID); user.Name == "Mallory" {
			t.Error("a forged form changed the name")
		}
	})
}

func TestUnlinkIdentity(t *testing.T) {
	st := newStack(t)
	ada, _ := st.founder(t, "ada@example.com")

	identities := func() []store.Identity {
		out, err := st.db.IdentitiesForUser(t.Context(), ada.userID)
		if err != nil {
			t.Fatalf("identities: %v", err)
		}
		return out
	}

	t.Run("only one is refused, and not offered", func(t *testing.T) {
		wantAbsent(t, ada.page("/settings/profile"), "Sign-in methods")
		body := ada.page("/settings/security")
		wantContains(t, body, "Your only sign-in method", `<bdi dir="ltr">ada@example.com</bdi>`)
		wantAbsent(t, body, "?unlink=")

		only := identities()[0].ID
		tone, message, _ := ada.outcome("/settings/security/identities/"+only+"/unlink", url.Values{"provider": {"Email"}})
		wantFlash(t, tone, message, "danger", "That is your only way to sign in, so it cannot be unlinked. Link another sign-in method first.")
	})

	if _, _, err := st.db.UpsertFromProvider(t.Context(), store.Profile{
		Provider: "github", Subject: "gh-1", Email: "ada@example.com", EmailVerified: true,
	}); err != nil {
		t.Fatalf("link github: %v", err)
	}
	var github string
	for _, id := range identities() {
		if id.Provider == "github" {
			github = id.ID
		}
	}

	t.Run("csrf", func(t *testing.T) {
		other := st.signIn(t, "mallory@example.com")
		wantRefused(t, ada.submit("/settings/security/identities/"+github+"/unlink", url.Values{csrfField: {other.token()}}))
		if len(identities()) != 2 {
			t.Error("a forged form unlinked an identity")
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		// Someone else's identity is not theirs to unlink, even with a
		// valid form of their own.
		other := st.signIn(t, "mallory@example.com")
		tone, _, _ := other.outcome("/settings/security/identities/"+github+"/unlink", url.Values{"provider": {"GitHub"}})
		if tone != "danger" || len(identities()) != 2 {
			t.Errorf("unlinking another account's identity: tone %q, identities %d", tone, len(identities()))
		}
	})

	t.Run("success", func(t *testing.T) {
		wantContains(t, ada.page("/settings/security?unlink="+github), `id="confirm-unlink-`+github+`" aria-labelledby="unlink-`+github+`-title" open`)
		// The form's old address, from a page loaded before the move, still
		// works and reports on the page the methods moved to.
		rec := ada.submit("/settings/profile/identities/"+github+"/unlink", url.Values{"provider": {"GitHub"}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/security" {
			t.Fatalf("old unlink address: %d to %q", rec.Code, rec.Header().Get("Location"))
		}
		tone, message := flashIn(ada.page("/settings/security"))
		wantFlash(t, tone, message, "success", "Unlinked GitHub. It can no longer be used to sign in to this account.")
		if len(identities()) != 1 {
			t.Error("identity still linked")
		}
	})
}

/* ---------------------------------------------------------------- keys -- */

var secretPattern = regexp.MustCompile(`mk_live_[A-Za-z0-9_-]{20,}`)

func TestCreateKeyRevealsTheSecretExactlyOnce(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")

	rec := owner.submit("/settings/keys", url.Values{"label": {"billing worker"}, "expires": {"30"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	// The secret crosses the redirect sealed: never in the URL, never in a
	// cookie anyone could read.
	if secretPattern.MatchString(rec.Header().Get("Location")) || secretPattern.MatchString(strings.Join(rec.Header().Values("Set-Cookie"), "\n")) {
		t.Fatal("the secret left the server outside the page that shows it")
	}

	sealed := owner.jar["timlee.reveal"]
	first := owner.page(rec.Header().Get("Location"))
	secret := secretPattern.FindString(first)
	if secret == "" {
		t.Fatal("the new key's secret is not on the page after creating it")
	}
	wantContains(t, first, "Copy your new key now", "I&#39;ve stored it", `data-flash="success"`)
	// Without script, autofocus is what brings a reader to the secret.
	wantContains(t, first, `tabindex="-1" autofocus data-vault>`, `<bdi>TIMLEE_API_KEY</bdi></code>`)

	keys, _ := st.db.ListAPIKeys(t.Context(), orgID)
	if len(keys) != 1 || !strings.HasPrefix(secret, keys[0].Prefix) {
		t.Fatalf("keys = %+v", keys)
	}

	if strings.Contains(owner.page("/settings/keys"), secret) {
		t.Error("the secret was shown a second time")
	}

	// Replaying the sealed secret into another session opens nothing.
	other, _ := st.founder(t, "mallory@example.com")
	other.jar["timlee.reveal"] = sealed
	if secretPattern.MatchString(other.page("/settings/keys")) {
		t.Error("another session could read the secret")
	}
}

// TestFormErrorsAreSummarised: a form answered with errors says so in the
// title, and opens on a summary that links to each field at fault, so a
// reader without script or sight starts where the problems are listed.
func TestFormErrorsAreSummarised(t *testing.T) {
	st := newStack(t)
	owner, _ := st.founder(t, "ada@example.com")

	for _, tc := range []struct {
		name, path string
		form       url.Values
		title      string
		problems   map[string]string
	}{
		{"keys", "/settings/keys", url.Values{"label": {""}}, "API keys", map[string]string{
			"key-label":           "Give the key a label so you can recognise it later.",
			"key-expires-input-7": "Choose when the key expires.",
		}},
		{"invite", "/settings/members/invite", url.Values{"email": {"nope"}, "role": {"root"}}, "Members", map[string]string{
			"invite-email":             "Enter an email address like name@company.com.",
			"invite-role-input-viewer": "Choose the role they will join with.",
		}},
		{"profile", "/settings/profile", url.Values{"name": {"Ada\x07"}}, "Profile", map[string]string{
			"pf-name-input": "",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := owner.submit(tc.path, tc.form)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			body := isolates.Replace(rec.Body.String())
			wantContains(t, body, "<title>Error: "+tc.title+" — Settings — Timlee</title>",
				`class="st-error-summary" tabindex="-1" autofocus`, `<h2 id="error-summary-title">There is a problem</h2>`)
			summary := between(t, body, "st-error-summary", "</timlee-alert>")
			if got := strings.Count(summary, "<li>"); got != len(tc.problems) {
				t.Errorf("summary lists %d problems, want %d:\n%s", got, len(tc.problems), summary)
			}
			for field, message := range tc.problems {
				wantContains(t, summary, `<a href="#`+field+`">`+html.EscapeString(message))
				// The link lands on a real control.
				wantContains(t, body, `id="`+field+`"`)
				if message != "" {
					wantContains(t, body, `<span class="visually-hidden">Error: </span>`+html.EscapeString(message))
				}
			}
		})
	}

	// A page that was not answering a form has neither.
	body := owner.page("/settings/keys")
	wantAbsent(t, body, "st-error-summary", "<title>Error:")
}

func TestCreateKeyForm(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	count := func() int {
		keys, _ := st.db.ListAPIKeys(t.Context(), orgID)
		return len(keys)
	}

	t.Run("validation", func(t *testing.T) {
		rec := owner.submit("/settings/keys", url.Values{"label": {"   "}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		wantContains(t, rec.Body.String(), "Give the key a label so you can recognise it later.", `aria-invalid="true"`)
		if count() != 0 {
			t.Error("a key was created from an invalid form")
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		tone, message, _ := member.outcome("/settings/keys", url.Values{"label": {"sneaky"}, "expires": {"30"}})
		wantFlash(t, tone, message, "danger", "Only owners and admins can create API keys, and your role no longer allows it.")
		if count() != 0 {
			t.Error("a member created a key")
		}
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, owner.post("/settings/keys", url.Values{"label": {"x"}}, map[string]string{"Origin": testPublic}))
		// An origin the control plane trusts is still not this site.
		wantRefused(t, owner.post("/settings/keys", url.Values{"label": {"x"}, csrfField: {owner.token()}},
			map[string]string{"Origin": "https://api.timlee.example"}))
		wantRefused(t, owner.post("/settings/keys", url.Values{"label": {"x"}, csrfField: {owner.token()}},
			map[string]string{"Origin": "https://evil.example"}))
		wantRefused(t, owner.post("/settings/keys", url.Values{"label": {"x"}, csrfField: {owner.token()}},
			map[string]string{"Sec-Fetch-Site": "same-site"}))
		if count() != 0 {
			t.Error("a refused form created a key")
		}
		// A browser that says nothing about where the form came from is
		// left to the token, which this one carries.
		if rec := owner.post("/settings/keys", url.Values{"label": {"x"}, "expires": {"30"}, csrfField: {owner.token()}}, nil); rec.Code != http.StatusSeeOther {
			t.Errorf("token-only form = %d, want 303", rec.Code)
		}
	})
}

func TestRevokeKey(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	created, err := st.db.CreateAPIKey(t.Context(), orgID, owner.userID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/settings/keys/" + created.ID + "/revoke"
	revoked := func() bool {
		keys, _ := st.db.ListAPIKeys(t.Context(), orgID)
		return len(keys) == 1 && keys[0].RevokedAt != nil
	}

	t.Run("confirmation without scripting", func(t *testing.T) {
		wantContains(t, owner.page("/settings/keys"), `id="revoke-`+created.ID+`" hidden`)
		body := owner.page("/settings/keys?revoke=" + created.ID)
		wantContains(t, body, `class="st-confirm" id="revoke-`+created.ID+`">`, "Revoke key", `aria-expanded="true"`)
	})

	t.Run("forbidden", func(t *testing.T) {
		tone, message, _ := member.outcome(path, url.Values{"label": {"ci"}})
		wantFlash(t, tone, message, "danger", "Only owners and admins can revoke API keys, and your role no longer allows it.")
		if revoked() {
			t.Error("a member revoked a key")
		}
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, owner.post(path, url.Values{"label": {"ci"}}, map[string]string{"Origin": testPublic}))
		if revoked() {
			t.Error("a forged form revoked a key")
		}
	})

	t.Run("success", func(t *testing.T) {
		tone, message, body := owner.outcome(path, url.Values{"label": {"ci"}})
		wantFlash(t, tone, message, "success", "Revoked “ci”. Requests that use it fail within 30 seconds.")
		if !revoked() {
			t.Error("key is still live")
		}
		wantContains(t, body, "<timlee-badge>Revoked</timlee-badge>")
	})

	t.Run("already revoked", func(t *testing.T) {
		tone, message, _ := owner.outcome(path, url.Values{"label": {"ci"}})
		wantFlash(t, tone, message, "info", "“ci” was already revoked.")
	})
}

// isolates are the marks a catalog message puts around each argument, so a
// name written right to left cannot reorder the sentence around it.
var isolates = strings.NewReplacer("\u2068", "", "\u2069", "")

func TestCreateKeyAsksWhenItExpires(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	expiryOf := func(label string) *int64 {
		t.Helper()
		keys, _ := st.db.ListAPIKeys(t.Context(), orgID)
		for _, k := range keys {
			if k.Label == label {
				return k.ExpiresAt
			}
		}
		t.Fatalf("no key labelled %q", label)
		return nil
	}

	form := isolates.Replace(owner.page("/settings/keys"))
	wantContains(t, form, `name="expires" value="30" checked`, `name="expires" value="never"`,
		"Until revoked", ">"+time.Now().Add(7*24*time.Hour).UTC().Format("2 Jan 2006")+"<")

	tone, message, _ := owner.outcome("/settings/keys", url.Values{"label": {"weekly"}, "expires": {"7"}})
	expires := expiryOf("weekly")
	if expires == nil || time.Until(time.UnixMilli(*expires)) < 7*24*time.Hour-time.Minute || time.Until(time.UnixMilli(*expires)) > 7*24*time.Hour {
		t.Fatalf("expiry = %v, want 7 days from now", expires)
	}
	wantFlash(t, tone, isolates.Replace(message), "success",
		"Created “weekly”. It expires on "+string(stampOf(english(t), *expires, time.Now()).Full)+". Copy it now — it is shown only once.")

	tone, message, _ = owner.outcome("/settings/keys", url.Values{"label": {"forever"}, "expires": {"never"}})
	if expiryOf("forever") != nil {
		t.Error("a key made to never expire has an expiry")
	}
	wantFlash(t, tone, isolates.Replace(message), "success", "Created “forever”. It does not expire. Copy it now — it is shown only once.")

	for _, expires := range []string{"", "12", "-1"} {
		rec := owner.submit("/settings/keys", url.Values{"label": {"unsure"}, "expires": {expires}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expires %q: status %d, want 422", expires, rec.Code)
		}
		wantContains(t, rec.Body.String(), "Choose when the key expires.", `value="unsure"`)
	}
	if keys, _ := st.db.ListAPIKeys(t.Context(), orgID); len(keys) != 2 {
		t.Errorf("%d keys, want only the two valid forms' keys", len(keys))
	}
}

func TestKeysTableShowsWhereEachKeyIs(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	owner.outcome("/settings/profile", url.Values{"name": {"Ada Lovelace"}})
	key := func(label string, expiresIn time.Duration) string {
		t.Helper()
		var expires *int64
		if expiresIn != 0 {
			at := time.Now().Add(expiresIn).UnixMilli()
			expires = &at
		}
		created, err := st.db.CreateAPIKey(t.Context(), orgID, owner.userID, label, expires)
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	never := key("forever", 0)
	soon := key("soon", 3*24*time.Hour+time.Hour)
	key("gone", -time.Hour)
	revoked := key("revoked", 0)
	if _, err := st.db.RevokeAPIKey(t.Context(), orgID, owner.userID, revoked); err != nil {
		t.Fatal(err)
	}
	old := key("worker", 90*24*time.Hour)
	rotated, err := st.db.RotateAPIKey(t.Context(), orgID, owner.userID, old, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	body := isolates.Replace(owner.page("/settings/keys"))
	wantContains(t, body,
		"<caption>4 active, 1 expired, 1 revoked</caption>",
		`>Expires<span class="visually-hidden"> (sort ascending)</span></a></th>`,
		`<span class="st-nowrap"><bdi dir="auto">Ada Lovelace</bdi></span>`,
		`<timlee-badge tone="warning">Expires in 3 days</timlee-badge>`,
		"<timlee-badge>Expired</timlee-badge>",
		"<timlee-badge>Revoked</timlee-badge>",
		`<timlee-badge tone="info">Replaced</timlee-badge>`,
		"Works until "+string(stampOf(english(t), rotated.ReplacedStopsAt, time.Now()).Full),
		"Replaces <code",
		`href="/settings/keys?rotate=`+never+`#rotate-`+never+`"`,
		`href="/settings/keys?rotate=`+soon+`#rotate-`+soon+`"`,
		`href="/settings/keys?status=expired"`,
	)
	// A replaced key is handed over already, and a revoked or expired one
	// has nothing left to hand over.
	wantAbsent(t, body, `?rotate=`+old+`#`, `?rotate=`+revoked+`#`, "Scopes", "inference:read")

	expired := isolates.Replace(owner.page("/settings/keys?status=expired"))
	wantContains(t, expired, "<caption>1 of 6 keys</caption>", ">gone<", `href="/settings/keys?status=expired" aria-current="page"`)
	wantAbsent(t, expired, ">forever<", ">soon<")

	if !strings.Contains(owner.page("/settings/keys?status=revoked&createdBy="+owner.userID), "Showing keys created by") {
		t.Error("a filter by creator does not say whose keys it shows")
	}

	overview := isolates.Replace(owner.page("/settings"))
	wantContains(t, overview, "4 active keys", `href="/settings/keys?status=expiring">1 key expires in the next 7 days</a>`)
}

func TestRotateKey(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	old, err := st.db.CreateAPIKey(t.Context(), orgID, owner.userID, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/settings/keys/" + old.ID + "/rotate"

	t.Run("confirmation without scripting", func(t *testing.T) {
		wantContains(t, owner.page("/settings/keys"), `id="rotate-`+old.ID+`" hidden`)
		body := owner.page("/settings/keys?rotate=" + old.ID)
		wantContains(t, body, `class="st-confirm st-rotate" id="rotate-`+old.ID+`">`, "Keep the old key working for",
			`name="overlap" value="24" checked`, `name="overlap" value="0"`, "Create replacement key")
	})

	t.Run("forbidden", func(t *testing.T) {
		tone, message, _ := member.outcome(path, url.Values{"label": {"worker"}, "overlap": {"24"}})
		wantFlash(t, tone, message, "danger", "Only owners and admins can rotate API keys, and your role no longer allows it.")
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, owner.post(path, url.Values{"label": {"worker"}, "overlap": {"24"}}, map[string]string{"Origin": testPublic}))
	})

	t.Run("no overlap chosen", func(t *testing.T) {
		rec := owner.submit(path, url.Values{"label": {"worker"}, "overlap": {"5"}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/keys?rotate="+old.ID+"#rotate-"+old.ID {
			t.Errorf("status %d to %q, want the confirmation again", rec.Code, rec.Header().Get("Location"))
		}
	})

	if keys, _ := st.db.ListAPIKeys(t.Context(), orgID); len(keys) != 1 || keys[0].ReplacedBy != "" {
		t.Fatalf("keys = %+v: a refused rotation changed something", keys)
	}

	t.Run("success", func(t *testing.T) {
		rec := owner.submit(path, url.Values{"label": {"worker"}, "overlap": {"24"}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d", rec.Code)
		}
		if secretPattern.MatchString(rec.Header().Get("Location")) || secretPattern.MatchString(strings.Join(rec.Header().Values("Set-Cookie"), "\n")) {
			t.Fatal("the secret left the server outside the page that shows it")
		}
		next := owner.get(rec.Header().Get("Location"))
		if got := next.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q on the page showing the secret", got)
		}
		body := next.Body.String()
		secret := secretPattern.FindString(body)
		if secret == "" || secret == old.Secret {
			t.Fatal("the replacement's secret is not on the page")
		}
		if strings.Contains(owner.page("/settings/keys"), secret) {
			t.Error("the replacement's secret was shown a second time")
		}

		replacement, err := st.db.VerifyAPIKey(t.Context(), secret)
		if err != nil || replacement.Label != "worker" {
			t.Fatalf("replacement = %+v, %v", replacement, err)
		}
		before, err := st.db.VerifyAPIKey(t.Context(), old.Secret)
		if err != nil {
			t.Fatalf("the old key stopped at once: %v", err)
		}
		tone, message := flashIn(body)
		wantFlash(t, tone, isolates.Replace(message), "success",
			"Created a replacement for “worker”. The old key keeps working until "+string(stampOf(english(t), *before.ExpiresAt, time.Now()).Full)+". Copy the new key now — it is shown only once.")
	})

	t.Run("already replaced", func(t *testing.T) {
		tone, message, _ := owner.outcome(path, url.Values{"label": {"worker"}, "overlap": {"24"}})
		wantFlash(t, tone, message, "danger", "This key is revoked or expired, so it cannot be rotated. Create a new key instead.")
	})

	t.Run("stop it now", func(t *testing.T) {
		leaked, err := st.db.CreateAPIKey(t.Context(), orgID, owner.userID, "leaked", nil)
		if err != nil {
			t.Fatal(err)
		}
		tone, message, _ := owner.outcome("/settings/keys/"+leaked.ID+"/rotate", url.Values{"label": {"leaked"}, "overlap": {"0"}})
		wantFlash(t, tone, isolates.Replace(message), "success",
			"Created a replacement for “leaked”. Requests that use the old key fail within 30 seconds. Copy the new key now — it is shown only once.")
		if _, err := st.db.VerifyAPIKey(t.Context(), leaked.Secret); err == nil {
			t.Error("the old key still works after rotating with no overlap")
		}
	})
}

/* ------------------------------------------------------------- members -- */

func TestInvite(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	admin := st.join(t, "grace@example.com", orgID, store.RoleAdmin)
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	pending := func() []store.Invitation {
		out, _ := st.db.PendingInvitations(t.Context(), orgID)
		return out
	}

	t.Run("success", func(t *testing.T) {
		tone, message, body := owner.outcome("/settings/members/invite", url.Values{"email": {" New@Example.com "}, "role": {"member"}})
		wantFlash(t, tone, message, "success", "Invitation sent to new@example.com. The link works for 7 days.")
		wantContains(t, body, "<h2 id=\"mb-pending\"", "new@example.com", `href="/settings/members?revoke=`)
		if got := pending(); len(got) != 1 || got[0].Role != store.RoleMember {
			t.Errorf("pending = %+v", got)
		}
	})

	t.Run("validation", func(t *testing.T) {
		for message, form := range map[string]url.Values{
			"Enter an email address like name@company.com.":      {"email": {"not-an-email"}, "role": {"member"}},
			"Enter the email address to send the invitation to.": {"email": {""}, "role": {"member"}},
			"Choose the role they will join with.":               {"email": {"x@example.com"}, "role": {"root"}},
		} {
			rec := owner.submit("/settings/members/invite", form)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%v: status = %d, want 422", form, rec.Code)
			}
			wantContains(t, rec.Body.String(), message)
		}
		if len(pending()) != 1 {
			t.Error("an invalid form created an invitation")
		}
	})

	t.Run("escalation", func(t *testing.T) {
		tone, message, body := admin.outcome("/settings/members/invite", url.Values{"email": {"boss@example.com"}, "role": {"owner"}})
		wantFlash(t, tone, message, "danger", "Your role in this organisation no longer allows that. The page now shows what you can do.")
		// A failure takes focus on load, with no script to move it there.
		wantContains(t, body, `data-flash="danger" role="region" aria-labelledby="flash-title" tabindex="-1" autofocus>`)
	})

	t.Run("forbidden", func(t *testing.T) {
		tone, message, _ := member.outcome("/settings/members/invite", url.Values{"email": {"pal@example.com"}, "role": {"viewer"}})
		wantFlash(t, tone, message, "danger", "Your role in this organisation no longer allows that. The page now shows what you can do.")
		if len(pending()) != 1 {
			t.Error("a forbidden invitation was created")
		}
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, owner.post("/settings/members/invite", url.Values{"email": {"z@example.com"}, "role": {"member"}},
			map[string]string{"Origin": testPublic}))
		if len(pending()) != 1 {
			t.Error("a forged form created an invitation")
		}
	})

	t.Run("send again", func(t *testing.T) {
		body := isolates.Replace(owner.page("/settings/members"))
		wantContains(t, body, "Sending an invitation again emails a new link, and the earlier link stops working.",
			`Send again<span class="visually-hidden"> (a new link to new@example.com)</span>`)

		tone, message, body := owner.outcome("/settings/members/invite", url.Values{"email": {"new@example.com"}, "role": {"member"}, "resend": {"1"}})
		wantFlash(t, tone, message, "success", "Sent new@example.com a new invitation link. The previous link no longer works.")
		// Success is announced in its region, but leaves focus alone.
		wantContains(t, body, `data-flash="success" role="region" aria-labelledby="flash-title">`, `<h2 id="flash-title" class="visually-hidden">Outcome</h2>`)
	})

	t.Run("expired", func(t *testing.T) {
		if _, err := st.db.CreateInvitation(t.Context(), orgID, "late@example.com", store.RoleViewer, owner.userID, "hash-late", 1); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		body := isolates.Replace(owner.page("/settings/members"))
		row := between(t, body, `<td class="st-primary-cell"><bdi dir="ltr">late@example.com</bdi></td>`, "</tr>")
		wantContains(t, row, `<timlee-badge tone="warning">Expired on `+time.Now().UTC().Format("2 Jan 2006")+`</timlee-badge>`,
			`Send a new invitation<span class="visually-hidden"> (a new link to late@example.com)</span>`)
		wantAbsent(t, row, ">Send again<")
	})
}

// TestInviteWithoutMail: an invitation reaches its recipient by mail and no
// other way, so a control plane that cannot send it refuses with 503 and
// records nothing.
func TestInviteWithoutMail(t *testing.T) {
	t.Run("no transport", func(t *testing.T) {
		st := newStack(t, func(c *config.Config) { c.MailOutbox = "" })
		owner, orgID := st.founder(t, "ada@example.com")

		// The form is shown, disabled, with the reason, rather than offered.
		wantContains(t, owner.page("/settings/members"), "Invitations cannot be sent from this server.", `class="st-plain" disabled`)
		wantAbsent(t, owner.page("/settings/members"), `href="#invite-email"`)

		// Posted anyway, as from a page loaded before mail went away.
		tone, message, body := owner.outcome("/settings/members/invite", url.Values{"email": {"new@example.com"}, "role": {"member"}})
		wantFlash(t, tone, message, "danger", "Invitations are sent by email, and this server has no mail configured, so new@example.com was not invited. "+
			"Ask whoever runs Timlee to set up mail, then try again.")
		wantContains(t, body, "No invitations are waiting.")

		// One sent before mail went away cannot be sent again either.
		if _, err := st.db.CreateInvitation(t.Context(), orgID, "early@example.com", store.RoleMember, owner.userID, "hash-early", int64(time.Hour/time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		body = owner.page("/settings/members")
		wantContains(t, body, "early@example.com", `href="/settings/members?revoke=`)
		wantAbsent(t, body, `name="resend"`)
	})

	t.Run("sending failed", func(t *testing.T) {
		st := newStack(t)
		owner, _ := st.founder(t, "ada@example.com")
		st.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/org/invitations" {
				return false
			}
			http.Error(w, `{"error":{"message":"The invitation could not be sent. Try again.","type":"mail_unavailable"}}`,
				http.StatusServiceUnavailable)
			return true
		}
		tone, message, _ := owner.outcome("/settings/members/invite", url.Values{"email": {"new@example.com"}, "role": {"member"}})
		wantFlash(t, tone, message, "danger", "The invitation email to new@example.com could not be sent, so nothing changed. Try again in a moment.")
	})
}

func TestRevokeInvitation(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	_, hash := store.NewInvitationToken()
	invitation, err := st.db.CreateInvitation(t.Context(), orgID, "new@example.com", store.RoleMember, owner.userID, hash, time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	path := "/settings/members/invitations/" + invitation.ID + "/revoke"
	live := func() bool {
		out, _ := st.db.PendingInvitations(t.Context(), orgID)
		return len(out) == 1
	}

	t.Run("confirmation without scripting", func(t *testing.T) {
		wantContains(t, owner.page("/settings/members?revoke="+invitation.ID), `id="confirm-revoke-`+invitation.ID+`" aria-labelledby="revoke-`+invitation.ID+`-title" open`)
	})

	t.Run("forbidden", func(t *testing.T) {
		tone, _, _ := member.outcome(path, url.Values{"email": {"new@example.com"}})
		if tone != "danger" || !live() {
			t.Errorf("member revoke: tone %q, live %v", tone, live())
		}
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, owner.post(path, url.Values{csrfField: {owner.token()}}, map[string]string{"Origin": "https://evil.example"}))
		if !live() {
			t.Error("a forged form revoked the invitation")
		}
	})

	t.Run("success", func(t *testing.T) {
		tone, message, _ := owner.outcome(path, url.Values{"email": {"new@example.com"}})
		wantFlash(t, tone, message, "success", "Revoked the invitation for new@example.com. Its link no longer works.")
		if live() {
			t.Error("invitation still live")
		}
	})

	t.Run("already gone", func(t *testing.T) {
		tone, message, _ := owner.outcome(path, url.Values{"email": {"new@example.com"}})
		wantFlash(t, tone, message, "info", "The invitation for new@example.com had already been accepted, revoked or replaced.")
	})
}

func TestChangeRole(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	admin := st.join(t, "grace@example.com", orgID, store.RoleAdmin)
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)

	t.Run("success", func(t *testing.T) {
		tone, message, _ := owner.outcome("/settings/members/"+member.userID+"/role", url.Values{"role": {"admin"}, "name": {"Bea"}})
		wantFlash(t, tone, message, "success", "Changed Bea’s role to Admin.")
		if got := st.roleOf(t, orgID, member.userID); got != store.RoleAdmin {
			t.Errorf("role = %s", got)
		}
		if err := st.db.AddMember(t.Context(), orgID, member.userID, store.RoleMember); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("last owner", func(t *testing.T) {
		tone, message, _ := owner.outcome("/settings/members/"+owner.userID+"/role", url.Values{"role": {"admin"}, "name": {"Ada"}, "self": {"1"}})
		wantFlash(t, tone, message, "danger", "An organisation needs at least one owner — promote someone else first.")
		if got := st.roleOf(t, orgID, owner.userID); got != store.RoleOwner {
			t.Errorf("the last owner was demoted to %s", got)
		}
	})

	t.Run("escalation", func(t *testing.T) {
		tone, message, _ := admin.outcome("/settings/members/"+member.userID+"/role", url.Values{"role": {"owner"}, "name": {"Bea"}})
		wantFlash(t, tone, isolates.Replace(message), "danger", "Could not change Bea’s role. You cannot change a member who holds a higher role than your own.")
		tone, message, _ = admin.outcome("/settings/members/"+owner.userID+"/role", url.Values{"role": {"viewer"}, "name": {"Ada"}})
		wantFlash(t, tone, message, "danger", "Could not change Ada’s role. You cannot change a member who holds a higher role than your own.")
		if st.roleOf(t, orgID, member.userID) != store.RoleMember || st.roleOf(t, orgID, owner.userID) != store.RoleOwner {
			t.Error("an admin changed a role it may not grant")
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		tone, message, _ := member.outcome("/settings/members/"+admin.userID+"/role", url.Values{"role": {"viewer"}, "name": {"Grace"}})
		wantFlash(t, tone, isolates.Replace(message), "danger", "Could not change Grace’s role. You cannot change a member who holds a higher role than your own.")
	})

	t.Run("validation", func(t *testing.T) {
		tone, message, _ := owner.outcome("/settings/members/"+member.userID+"/role", url.Values{"role": {"emperor"}, "name": {"Bea"}})
		wantFlash(t, tone, message, "danger", "Choose one of the listed roles for Bea.")
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, owner.submit("/settings/members/"+member.userID+"/role", url.Values{"role": {"admin"}, csrfField: {admin.token()}}))
		if got := st.roleOf(t, orgID, member.userID); got != store.RoleMember {
			t.Errorf("a forged form changed the role to %s", got)
		}
	})
}

func TestRemoveMember(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	admin := st.join(t, "grace@example.com", orgID, store.RoleAdmin)
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	if _, err := st.db.CreateAPIKey(t.Context(), orgID, member.userID, "bea's key", nil); err != nil {
		t.Fatal(err)
	}

	t.Run("confirmation without scripting", func(t *testing.T) {
		wantContains(t, owner.page("/settings/members?remove="+member.userID), `id="confirm-remove-`+member.userID+`" aria-labelledby="remove-`+member.userID+`-title" open`)
	})

	t.Run("forbidden", func(t *testing.T) {
		tone, message, _ := admin.outcome("/settings/members/"+owner.userID+"/remove", url.Values{"name": {"Ada"}})
		wantFlash(t, tone, message, "danger", "Could not remove Ada. You cannot change a member who holds a higher role than your own.")
	})

	t.Run("last owner", func(t *testing.T) {
		tone, message, _ := owner.outcome("/settings/members/"+owner.userID+"/remove", url.Values{"name": {"Ada"}})
		wantFlash(t, tone, message, "danger", "An organisation needs at least one owner — promote someone else first.")
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, owner.post("/settings/members/"+member.userID+"/remove", url.Values{"name": {"Bea"}}, nil))
		if st.roleOf(t, orgID, member.userID) == "" {
			t.Error("a forged form removed a member")
		}
	})

	t.Run("success", func(t *testing.T) {
		tone, message, _ := owner.outcome("/settings/members/"+member.userID+"/remove", url.Values{"name": {"Bea"}})
		wantFlash(t, tone, message, "success", "Removed Bea from the organisation. The 1 API key they created was revoked and stops working within 30 seconds.")
		if st.roleOf(t, orgID, member.userID) != "" {
			t.Error("still a member")
		}
	})
}

func TestRemovalNamesTheKeysItRevokes(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	bea := st.join(t, "bea@example.com", orgID, store.RoleAdmin)
	bea.outcome("/settings/profile", url.Values{"name": {"Bea"}})
	grace := st.join(t, "grace@example.com", orgID, store.RoleMember)
	key := func(by *browser, label string, expiresIn time.Duration) store.CreatedKey {
		t.Helper()
		var expires *int64
		if expiresIn != 0 {
			at := time.Now().Add(expiresIn).UnixMilli()
			expires = &at
		}
		created, err := st.db.CreateAPIKey(t.Context(), orgID, by.userID, label, expires)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	used := key(bea, "deploy bot", 0)
	if _, err := st.db.VerifyAPIKey(t.Context(), used.Secret); err != nil {
		t.Fatal(err)
	}
	unused := key(bea, "notebook", 30*24*time.Hour)
	key(bea, "stale", -time.Hour)
	revoked := key(bea, "retired", 0)
	if _, err := st.db.RevokeAPIKey(t.Context(), orgID, owner.userID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	key(owner, "owner's own", 0)

	body := isolates.Replace(owner.page("/settings/members?remove=" + bea.userID))
	start := strings.Index(body, `id="confirm-remove-`+bea.userID+`"`)
	if start < 0 {
		t.Fatal("no removal confirmation for Bea")
	}
	dialog := body[start : start+strings.Index(body[start:], "</dialog>")]
	wantContains(t, dialog,
		"Bea loses access to ",
		"and the 2 API keys they created are revoked. Both take effect within 30 seconds.",
		"deploy bot</bdi></b> <code",
		used.Prefix+"</bdi>…</code>, last used 0 seconds ago",
		"notebook</bdi></b> <code",
		unused.Prefix+"</bdi>…</code>, never used",
		"1 of these keys was used in the last 24 hours.",
		`href="/settings/keys?createdBy=`+bea.userID+`&amp;status=active"`,
		"Remove Bea and revoke 2 keys",
	)
	// Only working keys of hers: not an expired or revoked one, and not
	// anyone else's.
	wantAbsent(t, dialog, "stale", "retired", "owner&#39;s own", "Remove member<")

	// Someone with no keys is removed with nothing else said about keys.
	plain := isolates.Replace(owner.page("/settings/members?remove=" + grace.userID))
	start = strings.Index(plain, `id="confirm-remove-`+grace.userID+`"`)
	dialog = plain[start : start+strings.Index(plain[start:], "</dialog>")]
	wantContains(t, dialog, "loses access to ", "within 30 seconds. To bring them back, invite them again.", ">Remove member</button>")
	wantAbsent(t, dialog, "API key", "Rotate these keys first")

	tone, message, _ := owner.outcome("/settings/members/"+bea.userID+"/remove", url.Values{"name": {"Bea"}})
	wantFlash(t, tone, message, "success", "Removed Bea from the organisation. The 2 API keys they created were revoked and stop working within 30 seconds.")
}

func TestSwitchOrganisation(t *testing.T) {
	st := newStack(t)
	ada, acme := st.founder(t, "ada@example.com")
	grace, _ := st.founder(t, "grace@example.com")
	if err := st.db.AddMember(t.Context(), acme, grace.userID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	acmeName := ""
	if orgs, _ := st.db.OrgsForUser(t.Context(), ada.userID); len(orgs) > 0 {
		acmeName = orgs[0].Name
	}

	body := grace.page("/settings/keys")
	wantContains(t, body, `action="/settings/org"`, `<option value="`+acme+`"`)
	// One organisation, nothing to switch between.
	wantAbsent(t, ada.page("/settings"), `action="/settings/org"`)

	t.Run("success", func(t *testing.T) {
		rec := grace.submit("/settings/org", url.Values{"orgId": {acme}, "returnTo": {"/settings/keys?revoke=key_x"}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/keys" {
			t.Fatalf("switch = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		tone, message := flashIn(grace.page("/settings/keys"))
		wantFlash(t, tone, message, "info", "Switched to "+acmeName+".")
	})

	t.Run("forbidden", func(t *testing.T) {
		stranger, _ := st.founder(t, "mallory@example.com")
		tone, message, _ := stranger.outcome("/settings/org", url.Values{"orgId": {acme}})
		wantFlash(t, tone, message, "danger", "You are no longer a member of that organisation, so it could not be opened.")
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, grace.post("/settings/org", url.Values{"orgId": {acme}}, map[string]string{"Origin": testPublic}))
	})
}

/* --------------------------------------------------------- invitations -- */

func TestInvitationAcceptance(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	token, hash := store.NewInvitationToken()
	if _, err := st.db.CreateInvitation(t.Context(), orgID, "bea@example.com", store.RoleMember, owner.userID, hash, time.Hour.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	self := "/invite?token=" + url.QueryEscape(token)

	t.Run("the mailed link keeps working", func(t *testing.T) {
		anon := &browser{t: t, st: st, jar: map[string]string{}}
		rec := anon.get("/invite.html?token=" + url.QueryEscape(token))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != self {
			t.Fatalf("legacy link = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		body := anon.page(self)
		wantContains(t, body, "Sign in to accept", `href="`+html.EscapeString(render.SignInPath(self))+`"`, `name="intent" value="dev"`)
		// The page's forms must still carry this site's Origin, which a
		// no-referrer policy would replace with "null".
		if got := anon.get(self).Header().Get("Referrer-Policy"); got != "same-origin" {
			t.Errorf("Referrer-Policy = %q, want same-origin", got)
		}
		if anon.get("/invite").Code != http.StatusBadRequest {
			t.Error("a link without a token is not reported as incomplete")
		}
	})

	bea := st.signIn(t, "bea@example.com")
	mallory := st.signIn(t, "mallory@example.com")
	member := func() store.Role { return st.roleOf(t, orgID, bea.userID) }

	t.Run("opening the link accepts nothing", func(t *testing.T) {
		wantContains(t, bea.page(self), "Accept invitation", "bea@example.com")
		if member() != "" {
			t.Fatal("a GET accepted the invitation")
		}
	})

	t.Run("csrf", func(t *testing.T) {
		wantRefused(t, bea.post("/invite", url.Values{"token": {token}}, map[string]string{"Origin": testPublic}))
		if member() != "" {
			t.Fatal("a forged form accepted the invitation")
		}
	})

	t.Run("another address", func(t *testing.T) {
		rec := mallory.submit("/invite", url.Values{"token": {token}})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		wantContains(t, rec.Body.String(), "This invitation is for a different email address", "mallory@example.com", `action="/sign-out"`)
	})

	t.Run("success", func(t *testing.T) {
		rec := bea.submit("/invite", url.Values{"token": {token}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
			t.Fatalf("accept = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		if member() != store.RoleMember {
			t.Fatalf("role after accepting = %q", member())
		}
		body := bea.page("/settings")
		tone, message := flashIn(body)
		if tone != "success" || !strings.HasPrefix(message, "Accepted your invitation to ") || !strings.HasSuffix(message, "You joined as a Member.") {
			t.Errorf("flash = %s %q", tone, message)
		}
	})

	t.Run("single use", func(t *testing.T) {
		rec := bea.submit("/invite", url.Values{"token": {token}})
		if rec.Code != http.StatusGone {
			t.Fatalf("second accept = %d, want 410", rec.Code)
		}
		wantContains(t, rec.Body.String(), "This invitation can no longer be used")
	})
}

/* --------------------------------------------------------------- audit -- */

func TestAuditLog(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	owner.outcome("/settings/profile", url.Values{"name": {"Ada Lovelace"}})
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	member.outcome("/settings/profile", url.Values{"name": {"Bea Ng"}})
	owner.outcome("/settings/members/"+member.userID+"/role", url.Values{"role": {"admin"}, "name": {"Bea"}})
	// Events are ordered by the millisecond; keep this one clear of the next.
	time.Sleep(2 * time.Millisecond)
	owner.outcome("/settings/keys", url.Values{"label": {"billing worker"}, "expires": {"never"}})
	keys, _ := st.db.ListAPIKeys(t.Context(), orgID)
	for range 52 {
		if err := st.db.RecordAudit(t.Context(), store.AuditEvent{OrgID: orgID, ActorID: owner.userID, Action: "api_key.created", Target: keys[0].ID}); err != nil {
			t.Fatal(err)
		}
	}
	// Bea leaves; the log still names her as she was.
	if _, err := st.db.RemoveMember(t.Context(), orgID, member.userID); err != nil {
		t.Fatal(err)
	}

	body := isolates.Replace(owner.page("/settings/audit"))
	wantContains(t, body, "Created an API key", "Ada Lovelace", "Showing 1–50 of 55 events", `density="compact"`,
		`rel="next"`, `href="/settings/audit?page=2"`, `<a href="/settings/audit" aria-current="page">All kept</a>`,
		"Timlee keeps this organisation’s audit events for as long as it exists. This page and its exports cover",
		`href="/settings/audit/export.csv" download>Download CSV</a>`, "Export all 55 matching events, from every page:")

	page2 := isolates.Replace(owner.page("/settings/audit?page=2"))
	wantContains(t, page2, "Showing 51–55 of 55 events", "Changed a role", "Member → Admin", "Bea Ng", "Created the organisation")
	wantAbsent(t, page2, "Former member")

	filtered := isolates.Replace(owner.page("/settings/audit?category=members&actor=" + owner.userID))
	wantContains(t, filtered, "Showing 1–1 of 1 event matching the filters", "Changed a role", `href="/settings/audit">Clear filters`,
		`href="/settings/audit/export.csv?actor=`+owner.userID+`&amp;category=members"`)
	wantAbsent(t, filtered, "Created an API key")
	wantContains(t, owner.page("/settings/audit?category=invitations"), "No events match these filters")
	wantContains(t, isolates.Replace(owner.page("/settings/audit?target=BILLING")), "of 53 events matching the filters")

	t.Run("period", func(t *testing.T) {
		day := isolates.Replace(owner.page("/settings/audit?period=24h&category=members"))
		wantContains(t, day, `<a href="/settings/audit?category=members&amp;period=24h" aria-current="page">Last 24 hours</a>`,
			`<input type="hidden" name="period" value="24h" />`, "Showing 1–1 of 1 event matching the filters")
		today := time.Now().UTC().Format("2006-01-02")
		tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
		wantContains(t, isolates.Replace(owner.page("/settings/audit?from="+today+"&to="+today)), "of 55 events matching the filters")
		wantContains(t, owner.page("/settings/audit?from="+tomorrow), "No events match these filters")
	})

	t.Run("bad range", func(t *testing.T) {
		for query, problem := range map[string]string{
			"from=2026-13-01":               `<a href="#audit-from">The start date must be a real date, like 2026-10-01</a>`,
			"to=yesterday":                  `<a href="#audit-to">The end date must be a real date, like 2026-10-31</a>`,
			"from=2026-10-30&to=2026-10-01": `<a href="#audit-from">The start date must be on or before the end date</a>`,
		} {
			rec := owner.get("/settings/audit?" + query)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status %d, want 422", query, rec.Code)
			}
			wantContains(t, rec.Body.String(), "<title>Error: Audit log", problem, `aria-invalid="true"`)
			// An export of a range that cannot be read goes back to say why.
			if rec := owner.get("/settings/audit/export.csv?" + query); rec.Code != http.StatusSeeOther {
				t.Errorf("export %s: status %d, want 303", query, rec.Code)
			}
		}
	})

	t.Run("export", func(t *testing.T) {
		rec := owner.get("/settings/audit/export.csv?category=members")
		if rec.Code != http.StatusOK {
			t.Fatalf("csv: status %d", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
			t.Errorf("csv content type = %q", got)
		}
		if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="timlee-audit-`+time.Now().UTC().Format("2006-01-02")+`.csv"` {
			t.Errorf("csv disposition = %q", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("csv cache control = %q", got)
		}
		lines, err := csv.NewReader(rec.Body).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != 2 || strings.Join(lines[0], ",") != "time_utc,actor_id,actor_name,actor_email,action,description,target_type,target_id,target_label,details" {
			t.Fatalf("csv = %q", lines)
		}
		if row := lines[1]; row[2] != "Ada Lovelace" || row[4] != "member.role_changed" || row[5] != "Changed a role (Member → Admin)" ||
			row[6] != "user" || row[7] != member.userID || row[8] != "Bea Ng" || row[9] != `{"from":"member","to":"admin"}` {
			t.Errorf("csv row = %q", row)
		}

		rec = owner.get("/settings/audit/export.json")
		var doc struct {
			Total     int  `json:"total"`
			Truncated bool `json:"truncated"`
			Events    []struct {
				TimeUTC     string          `json:"time_utc"`
				Action      string          `json:"action"`
				TargetLabel string          `json:"target_label"`
				Details     json.RawMessage `json:"details"`
			} `json:"events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("json: %v\n%s", err, rec.Body)
		}
		if doc.Total != 55 || len(doc.Events) != 55 || doc.Truncated || doc.Events[54].Action != "org.created" {
			t.Errorf("json = %d of %d events (truncated %v), last %+v", len(doc.Events), doc.Total, doc.Truncated, doc.Events[len(doc.Events)-1])
		}
		if _, err := time.Parse(time.RFC3339, doc.Events[0].TimeUTC); err != nil {
			t.Errorf("time_utc %q is not ISO 8601: %v", doc.Events[0].TimeUTC, err)
		}
	})

	t.Run("formula", func(t *testing.T) {
		// A label is someone's text; a spreadsheet must not run it.
		owner.outcome("/settings/keys", url.Values{"label": {`=HYPERLINK("http://evil.example","x")`}, "expires": {"never"}})
		rec := owner.get("/settings/audit/export.csv?target=HYPERLINK")
		lines, err := csv.NewReader(rec.Body).ReadAll()
		if err != nil || len(lines) != 2 {
			t.Fatalf("csv = %q, %v", lines, err)
		}
		if got := lines[1][8]; got != `'=HYPERLINK("http://evil.example","x")` {
			t.Errorf("target_label = %q, want it defused with a leading apostrophe", got)
		}
	})

	t.Run("readers without access", func(t *testing.T) {
		viewer := st.join(t, "vic@example.com", orgID, store.RoleViewer)
		wantAbsent(t, viewer.page("/settings/audit"), "Created an API key")
		rec := viewer.get("/settings/audit/export.csv")
		if rec.Code != http.StatusSeeOther || strings.Contains(rec.Body.String(), "api_key") {
			t.Errorf("viewer export: status %d, body %.200s; want a redirect and no events", rec.Code, rec.Body)
		}
	})
}

// TestSignOutOfOtherSessions: the Security page counts the account's other
// sessions and ends them, and only them.
func TestSignOutOfOtherSessions(t *testing.T) {
	st := newStack(t)
	laptop, _ := st.founder(t, "ada@example.com")
	phone := st.signIn(t, "ada@example.com")
	someoneElse := st.signIn(t, "bea@example.com")

	body := isolates.Replace(laptop.page("/settings/security"))
	wantContains(t, body, "You are signed in here and in 1 other browser, device or desktop app.",
		`action="/settings/security/sessions/end-others"`, "a service that caches session checks may accept one of them for up to 30 seconds more")

	tone, message, body := laptop.outcome("/settings/security/sessions/end-others", nil)
	wantFlash(t, tone, message, "success", "Signed out of 1 other session.")
	wantContains(t, isolates.Replace(body), "You are signed in here and nowhere else.")

	if rec := phone.get("/settings"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/sign-in") {
		t.Errorf("the other session still opens settings: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	someoneElse.page("/settings")
	laptop.page("/settings")

	t.Run("csrf", func(t *testing.T) {
		again := st.signIn(t, "ada@example.com")
		wantRefused(t, laptop.post("/settings/security/sessions/end-others", url.Values{}, map[string]string{"Origin": testPublic}))
		again.page("/settings")
	})
}

// TestRenameOrganisation: owners and admins rename; a bad name is answered
// on the form; everyone else is told who can.
func TestRenameOrganisation(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)

	tone, message, body := owner.outcome("/settings/organisation", url.Values{"name": {"  Analytical Engines  "}})
	wantFlash(t, isolates.Replace(tone), isolates.Replace(message), "success", "Renamed the organisation to Analytical Engines.")
	wantContains(t, body, `value="Analytical Engines"`)
	if org, _ := st.db.OrgByID(t.Context(), orgID); org.Name != "Analytical Engines" {
		t.Errorf("stored name = %q", org.Name)
	}

	for _, bad := range []string{"", strings.Repeat("x", 81), "Engines\x07"} {
		rec := owner.submit("/settings/organisation", url.Values{"name": {bad}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%q: status %d, want 422", bad, rec.Code)
		}
		wantContains(t, rec.Body.String(), "<title>Error: Organisation", `<a href="#org-name">`, `aria-invalid="true"`)
	}

	body = isolates.Replace(member.page("/settings/organisation"))
	wantContains(t, body, "Only owners and admins can rename the organisation.")
	wantAbsent(t, body, `action="/settings/organisation" `, "Danger zone")
	tone, _, _ = member.outcome("/settings/organisation", url.Values{"name": {"Mine now"}})
	if org, _ := st.db.OrgByID(t.Context(), orgID); tone != "danger" || org.Name != "Analytical Engines" {
		t.Errorf("a member renamed the organisation: tone %q, name %q", tone, org.Name)
	}
}

// TestLeaveOrganisation: leaving names the keys it revokes, revokes them,
// and is refused to the only owner.
func TestLeaveOrganisation(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	admin := st.join(t, "grace@example.com", orgID, store.RoleAdmin)
	admin.outcome("/settings/keys", url.Values{"label": {"grace's worker"}, "expires": {"30"}})
	org, _ := st.db.OrgByID(t.Context(), orgID)

	body := isolates.Replace(admin.page("/settings/organisation?leave=1"))
	wantContains(t, body, `id="confirm-leave" aria-labelledby="leave-title" open`,
		"the 1 API key you created for it is revoked. It stops working within 30 seconds.", "<b><bdi>grace&#39;s worker</bdi></b>")

	tone, message, _ := admin.outcome("/settings/organisation/leave", url.Values{"org": {org.Name}})
	wantFlash(t, tone, isolates.Replace(message), "success", "You left "+org.Name+". The 1 API key you created for it was revoked.")
	if role := st.roleOf(t, orgID, admin.userID); role != "" {
		t.Errorf("still a member, as %s", role)
	}
	keys, _ := st.db.ListAPIKeys(t.Context(), orgID)
	if len(keys) != 1 || keys[0].RevokedAt == nil {
		t.Errorf("keys = %+v, want grace's revoked", keys)
	}

	// The only owner is told why not, and offered no button.
	body = isolates.Replace(owner.page("/settings/organisation"))
	wantContains(t, body, "You are this organisation’s only owner, so you cannot leave it.")
	wantAbsent(t, body, `href="/settings/organisation?leave=1`)
	tone, message, _ = owner.outcome("/settings/organisation/leave", url.Values{"org": {"x"}})
	wantFlash(t, tone, isolates.Replace(message), "danger", "You are this organisation’s only owner, so you cannot leave it. Make someone else an owner first, or delete the organisation.")
	if role := st.roleOf(t, orgID, owner.userID); role != store.RoleOwner {
		t.Errorf("the only owner left: role %q", role)
	}
}

// TestDeleteOrganisation: only an owner, only with the name typed, and it
// takes the keys with it.
func TestDeleteOrganisation(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	admin := st.join(t, "grace@example.com", orgID, store.RoleAdmin)
	owner.switchTo(orgID)
	owner.outcome("/settings/keys", url.Values{"label": {"worker"}, "expires": {"30"}})
	keys, _ := st.db.ListAPIKeys(t.Context(), orgID)
	org, _ := st.db.OrgByID(t.Context(), orgID)
	name := org.Name

	body := isolates.Replace(owner.page("/settings/organisation"))
	wantContains(t, body, "Danger zone", "all 2 members’ access, yours included", "its 1 working API key, which stops working within 30 seconds",
		"Type <b><bdi>"+html.EscapeString(name)+"</bdi></b> to confirm")
	wantAbsent(t, isolates.Replace(admin.page("/settings/organisation")), "Danger zone")

	rec := owner.submit("/settings/organisation/delete", url.Values{"org": {name}, "confirm": {strings.ToLower(name)}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong name: status %d, want 422", rec.Code)
	}
	wantContains(t, isolates.Replace(rec.Body.String()), `<a href="#org-delete-confirm">Type the organisation’s name exactly as shown: `+html.EscapeString(name)+`</a>`)

	// An admin who posts the form anyway is refused by the control plane.
	tone, _, _ := admin.outcome("/settings/organisation/delete", url.Values{"org": {name}, "confirm": {name}})
	if _, err := st.db.OrgByID(t.Context(), orgID); tone != "danger" || err != nil {
		t.Fatalf("an admin deleted the organisation: tone %q, err %v", tone, err)
	}

	tone, message, _ := owner.outcome("/settings/organisation/delete", url.Values{"org": {name}, "confirm": {name}})
	wantFlash(t, tone, isolates.Replace(message), "success", "Deleted "+name+". Its 1 working API key stops working within 30 seconds.")
	if _, err := st.db.OrgByID(t.Context(), orgID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("organisation still there: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v", keys)
	}
	if left, _ := st.db.ListAPIKeys(t.Context(), orgID); len(left) != 0 {
		t.Errorf("keys outlived their organisation: %+v", left)
	}
	// Everyone who was in it is somewhere else now, not stuck on a page
	// that refuses them.
	admin.page("/settings")
	owner.page("/settings/organisation")
}

// TestTablesSortAndPageOnTheServer: column headers are links the server
// answers sorted, only the sorted column says so, filters and sort survive
// each other's links, and a long table pages.
func TestTablesSortAndPageOnTheServer(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	for _, label := range []string{"bravo", "alpha", "charlie"} {
		if _, err := st.db.CreateAPIKey(t.Context(), orgID, owner.userID, label, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// A revoked key, so the table offers its status filters too.
	gone, err := st.db.CreateAPIKey(t.Context(), orgID, owner.userID, "delta", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.RevokeAPIKey(t.Context(), orgID, owner.userID, gone.ID); err != nil {
		t.Fatal(err)
	}
	order := func(body string, labels ...string) {
		t.Helper()
		last := -1
		for _, label := range labels {
			at := strings.Index(body, `<span>`+label)
			if at < last {
				t.Errorf("%s is out of order in %v", label, labels)
			}
			last = at
		}
	}

	body := isolates.Replace(owner.page("/settings/keys"))
	wantAbsent(t, body, "aria-sort")
	wantContains(t, body, `<a class="st-sort" href="/settings/keys?dir=asc&amp;sort=label">Label<span class="visually-hidden"> (sort ascending)</span></a>`)
	order(body, "charlie", "alpha", "bravo")

	body = isolates.Replace(owner.page("/settings/keys?sort=label&dir=asc"))
	order(body, "alpha", "bravo", "charlie")
	wantContains(t, body, `<th scope="col" aria-sort="ascending"><a class="st-sort" href="/settings/keys?dir=desc&amp;sort=label">Label<span class="visually-hidden"> (sort descending)</span></a></th>`,
		`href="/settings/keys?dir=asc&amp;sort=label&amp;status=active"`)
	if n := strings.Count(body, "aria-sort="); n != 1 {
		t.Errorf("%d headers carry aria-sort, want only the sorted one", n)
	}
	order(isolates.Replace(owner.page("/settings/keys?sort=label&dir=desc&status=active")), "charlie", "bravo", "alpha")
	order(isolates.Replace(owner.page("/settings/keys?sort=created&dir=asc")), "bravo", "alpha", "charlie")
	wantAbsent(t, owner.page("/settings/keys?sort=prefix"), "aria-sort")

	st.join(t, "zed@example.com", orgID, store.RoleViewer)
	st.join(t, "bea@example.com", orgID, store.RoleAdmin)
	members := isolates.Replace(owner.page("/settings/members?sort=role&dir=desc"))
	wantContains(t, members, `<th scope="col" aria-sort="descending"><a class="st-sort" href="/settings/members?dir=asc&amp;sort=role">Role`)
	if a, b, z := strings.Index(members, `id="member-`+owner.userID), strings.Index(members, "bea@example.com"), strings.Index(members, "zed@example.com"); !(a < b && b < z) {
		t.Errorf("by role, strongest first: owner %d, admin %d, viewer %d", a, b, z)
	}

	t.Run("paging", func(t *testing.T) {
		for i := range 52 {
			if _, err := st.db.CreateAPIKey(t.Context(), orgID, owner.userID, fmt.Sprintf("bulk %02d", i), nil); err != nil {
				t.Fatal(err)
			}
		}
		first := isolates.Replace(owner.page("/settings/keys?sort=label&dir=asc"))
		wantContains(t, first, " · showing 1–50</caption>", `href="/settings/keys?dir=asc&amp;page=2&amp;sort=label" rel="next"`)
		if n := strings.Count(first, `<tr id="key-`); n != 50 {
			t.Errorf("first page lists %d keys, want 50", n)
		}
		second := isolates.Replace(owner.page("/settings/keys?sort=label&dir=asc&page=2"))
		wantContains(t, second, " · showing 51–56</caption>", "charlie")
		if n := strings.Count(second, `<tr id="key-`); n != 6 {
			t.Errorf("second page lists %d keys, want 6", n)
		}
	})
}

// TestRoleChangesThatCannotBeUndoneAskFirst: giving up your own management,
// making an owner and demoting one ask first; anything else is one step.
func TestRoleChangesThatCannotBeUndoneAskFirst(t *testing.T) {
	st := newStack(t)
	owner, orgID := st.founder(t, "ada@example.com")
	admin := st.join(t, "grace@example.com", orgID, store.RoleAdmin)
	member := st.join(t, "bea@example.com", orgID, store.RoleMember)
	member.outcome("/settings/profile", url.Values{"name": {"Bea"}})

	asked := func(b *browser, id string, form url.Values) string {
		t.Helper()
		rec := b.submit("/settings/members/"+id+"/role", form)
		want := "/settings/members?" + url.Values{"role": {form.Get("role")}, "role-confirm": {id}}.Encode() + "#confirm-role-" + id
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Fatalf("POST %v: %d to %q, want the question at %q", form, rec.Code, rec.Header().Get("Location"), want)
		}
		return isolates.Replace(b.page(want))
	}

	// One step: an admin made of a member.
	tone, _, _ := owner.outcome("/settings/members/"+member.userID+"/role", url.Values{"role": {"admin"}, "name": {"Bea"}})
	if tone != "success" || st.roleOf(t, orgID, member.userID) != store.RoleAdmin {
		t.Fatalf("a routine change asked or failed: tone %q", tone)
	}

	// Giving up your own management.
	body := asked(admin, admin.userID, url.Values{"role": {"member"}, "self": {"1"}})
	if st.roleOf(t, orgID, admin.userID) != store.RoleAdmin {
		t.Fatal("the role changed before it was confirmed")
	}
	wantContains(t, body, `id="confirm-role-`+admin.userID+`" aria-labelledby="confirm-role-`+admin.userID+`-title" open`,
		"Change your own role to Member?", "Only an owner or admin can change this back.", "Change my role to Member", "Keep my role",
		`<input type="hidden" name="confirm" value="1" />`)
	tone, _, _ = admin.outcome("/settings/members/"+admin.userID+"/role", url.Values{"role": {"member"}, "self": {"1"}, "confirm": {"1"}})
	if tone != "success" || st.roleOf(t, orgID, admin.userID) != store.RoleMember {
		t.Errorf("the confirmed change did not apply: tone %q", tone)
	}

	// Making an owner, then demoting one.
	body = asked(owner, member.userID, url.Values{"role": {"owner"}, "name": {"Bea"}})
	wantContains(t, body, "Make Bea an owner?", "Bea will be able to change your role or remove you.")
	owner.outcome("/settings/members/"+member.userID+"/role", url.Values{"role": {"owner"}, "name": {"Bea"}, "confirm": {"1"}})
	if st.roleOf(t, orgID, member.userID) != store.RoleOwner {
		t.Fatal("Bea was not made an owner")
	}
	body = asked(owner, member.userID, url.Values{"role": {"admin"}, "name": {"Bea"}})
	wantContains(t, body, "Change Bea’s role to Admin?", "Bea will no longer be able to change owners.")

	// A question for a change that needs none is not asked.
	wantAbsent(t, owner.page("/settings/members?role-confirm="+admin.userID+"&role=viewer"), `id="confirm-role-`)
}

// english is the default locale's localizer, as a request without a locale
// gets it.
func english(t *testing.T) *i18n.Localizer {
	t.Helper()
	b, err := englishOnly()
	if err != nil {
		t.Fatal(err)
	}
	return b.For(nil)
}
