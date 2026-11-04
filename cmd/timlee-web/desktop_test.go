package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The worked example from RFC 7636 appendix B, and a flow id as desktopStart
// mints one.
const (
	rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	appFlow      = "Vb5r0sQ2bS3vBqkq5mH5u2P1yWm7wN0gVf4c9b8a7d6"
)

const desktopQuery = "desktop=1&port=49152&code_challenge=" + rfcChallenge + "&code_challenge_method=S256&flow=" + appFlow

// flowCookie is what desktopStart left in the app's window.
func flowCookie(value string) *http.Cookie {
	return &http.Cookie{Name: desktopFlowCookie, Value: value}
}

// desktopControl answers the control plane's desktop routes and hands every
// other request to the usual stub. It records what the site forwarded.
type desktopControl struct {
	t        *testing.T
	fallback http.Handler
	// redirect is what /auth/dev answers for a desktop sign-in.
	redirect string
	// redeem decides whether /auth/desktop/session issues a session, which,
	// as on the control plane, it does only for the flow the code was
	// minted for: appFlow.
	redeem bool
	bodies map[string]map[string]any
}

func (c *desktopControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "POST /auth/dev", "POST /auth/magic-link", "POST /auth/desktop/session":
	default:
		c.fallback.ServeHTTP(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		c.t.Errorf("%s: undecodable body: %v", r.URL.Path, err)
	}
	c.bodies[r.URL.Path] = body
	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/auth/dev":
		_, _ = io.WriteString(w, `{"ok":true,"desktopRedirect":"`+c.redirect+`"}`)
	case "/auth/magic-link":
		_, _ = io.WriteString(w, `{"sent":true}`)
	case "/auth/desktop/session":
		if !c.redeem || body["flow"] != appFlow {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"That sign-in link has expired or was already used.","code":"link_expired"}}`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "timlee.sid", Value: "desktop.session", Path: "/", HttpOnly: true})
		_, _ = io.WriteString(w, `{"ok":true}`)
	}
}

func newDesktopHarness(t *testing.T) (*harness, *desktopControl) {
	t.Helper()
	h := newHarness(t)
	control := &desktopControl{
		t:        t,
		fallback: h.control.handler(),
		redirect: "http://127.0.0.1:49152/callback?code=one-time",
		redeem:   true,
		bodies:   map[string]map[string]any{},
	}
	upstream := httptest.NewServer(control)
	t.Cleanup(upstream.Close)
	h.srv.auth = newAuthClient(upstream.URL, h.srv.cfg.PublicOrigin, h.srv.log)
	h.srv.cfg.ControlOrigin = upstream.URL
	return h, control
}

func TestDesktopSignInCarriesTheHandoffThroughEveryWayIn(t *testing.T) {
	h, _ := newDesktopHarness(t)
	// A browser that is already signed in still signs the app in.
	h.control.signedIn = true
	rec := h.get("/sign-in?returnTo=%2Fsettings&"+desktopQuery, session())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the page rather than a redirect", rec.Code)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		"Signing in to the Timlee app",
		`<input type="hidden" name="desktop_port" value="49152" />`,
		`<input type="hidden" name="code_challenge" value="` + rfcChallenge + `" />`,
		`<input type="hidden" name="desktop_flow" value="` + appFlow + `" />`,
		"/auth/github/start?code_challenge=" + rfcChallenge + "&amp;code_challenge_method=S256&amp;desktop=1&amp;flow=" + appFlow + "&amp;port=49152&amp;returnTo=%2Fsettings",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("desktop sign-in page is missing %q", fragment)
		}
	}
	if n := strings.Count(body, `name="desktop_port"`); n != 2 {
		t.Errorf("%d forms carry the handoff, want both the email and the development form", n)
	}

	// A handoff the control plane would refuse is not passed on at all.
	for _, bad := range []string{
		strings.Replace(desktopQuery, "49152", "80", 1),
		strings.Replace(desktopQuery, "S256", "plain", 1),
		strings.Replace(desktopQuery, rfcChallenge, "short", 1),
		strings.Replace(desktopQuery, "&flow="+appFlow, "", 1),
	} {
		h.control.signedIn = false
		if body := h.get("/sign-in?" + bad).Body.String(); strings.Contains(body, "desktop_port") ||
			strings.Contains(body, "Signing in to the Timlee app") {
			t.Errorf("%q was treated as a desktop sign-in", bad)
		}
	}
}

func TestDesktopDevSignInSendsTheBrowserToTheApp(t *testing.T) {
	h, control := newDesktopHarness(t)
	form := url.Values{
		"intent":         {"dev"},
		"returnTo":       {"/console"},
		"desktop_port":   {"49152"},
		"code_challenge": {rfcChallenge},
		"desktop_flow":   {appFlow},
	}

	rec := h.post("/sign-in", form)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != control.redirect {
		t.Fatalf("status %d to %q, want 302 to the app's listener", rec.Code, rec.Header().Get("Location"))
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("the browser got %d cookies; the session belongs to the app", len(cookies))
	}
	desktop, _ := control.bodies["/auth/dev"]["desktop"].(map[string]any)
	if desktop["port"] != "49152" || desktop["codeChallenge"] != rfcChallenge || desktop["codeChallengeMethod"] != "S256" ||
		desktop["flow"] != appFlow {
		t.Errorf("forwarded handoff = %v", desktop)
	}

	// Only the app's own listener is ever followed.
	control.redirect = "https://evil.example/callback?code=one-time"
	if rec := h.post("/sign-in", form); rec.Code != http.StatusBadGateway {
		t.Errorf("status %d to %q, want the answer refused", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDesktopMagicLinkKeepsTheHandoffOnTheSentPage(t *testing.T) {
	h, control := newDesktopHarness(t)
	rec := h.post("/sign-in", url.Values{
		"intent":         {"magic-link"},
		"email":          {"ada@example.com"},
		"desktop_port":   {"49152"},
		"code_challenge": {rfcChallenge},
		"desktop_flow":   {appFlow},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 to the sent page", rec.Code)
	}
	if desktop, _ := control.bodies["/auth/magic-link"]["desktop"].(map[string]any); desktop["flow"] != appFlow {
		t.Errorf("magic-link request = %v, want the handoff", control.bodies["/auth/magic-link"])
	}
	sent := h.get(rec.Header().Get("Location"), rec.Result().Cookies()...).Body.String()
	for _, fragment := range []string{
		"Open it on this computer",
		`<input type="hidden" name="desktop_port" value="49152" />`,
		`<input type="hidden" name="desktop_flow" value="` + appFlow + `" />`,
	} {
		if !strings.Contains(sent, fragment) {
			t.Errorf("sent page is missing %q", fragment)
		}
	}
}

func TestDesktopStartSetsTheFlowInTheAppWindow(t *testing.T) {
	h, _ := newDesktopHarness(t)
	// A flow in the request is ignored: the id is always minted here.
	const start = "/sign-in/desktop/start?port=49152&code_challenge=" + rfcChallenge +
		"&code_challenge_method=S256&returnTo=%2Fsettings&flow=" + appFlow

	rec := h.get(start)
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatalf("status %d with %d cookies, want 303 setting the flow cookie", rec.Code, len(cookies))
	}
	c := cookies[0]
	if c.Name != desktopFlowCookie || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.MaxAge != int(desktopFlowLifetime.Seconds()) || c.Value == appFlow || !pkceChallenge.MatchString(c.Value) {
		t.Errorf("flow cookie = %+v, want a fresh id, HttpOnly, SameSite=Lax, Path=/, for the sign-in's lifetime", c)
	}
	// The sign-in page the app opens in the system browser carries the same
	// id, so the code it ends in is bound to this window.
	next, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || next.Path != "/sign-in" {
		t.Fatalf("Location = %q, want the sign-in page", rec.Header().Get("Location"))
	}
	if d := desktopFromQuery(next.Query()); d == nil || d.Flow != c.Value || d.Port != "49152" ||
		next.Query().Get("returnTo") != "/settings" {
		t.Errorf("sign-in page %q does not carry the handoff and the window's flow", next)
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer"} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if again := h.get(start).Result().Cookies(); len(again) != 1 || again[0].Value == c.Value {
		t.Errorf("a second start reused the flow id")
	}

	// A handoff the control plane would refuse starts nothing.
	bad := h.get(strings.Replace(start, "49152", "80", 1))
	if bad.Code != http.StatusSeeOther || len(bad.Result().Cookies()) != 0 ||
		!strings.HasPrefix(bad.Header().Get("Location"), "/sign-in?error=") {
		t.Errorf("bad handoff: status %d to %q with %d cookies", bad.Code, bad.Header().Get("Location"), len(bad.Result().Cookies()))
	}

	// Production binds the cookie to this exact origin over HTTPS.
	h.srv.cfg.Env = "production"
	if prod := h.get(start).Result().Cookies(); len(prod) != 1 || prod[0].Name != "__Host-"+desktopFlowCookie || !prod[0].Secure {
		t.Errorf("production flow cookie = %+v, want __Host- and Secure", prod)
	}
}

func TestDesktopCompleteSetsTheSessionInTheAppWindow(t *testing.T) {
	h, control := newDesktopHarness(t)
	target := "/sign-in/desktop/complete?code=one-time&code_verifier=the-verifier&returnTo=%2Fsettings"

	rec := h.get(target, flowCookie(appFlow))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
		t.Fatalf("status %d to %q, want 303 to /settings", rec.Code, rec.Header().Get("Location"))
	}
	cookies := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		cookies[c.Name] = c
	}
	if sid := cookies["timlee.sid"]; sid == nil || sid.Value != "desktop.session" {
		t.Errorf("cookies = %v, want the control plane's session relayed", cookies)
	}
	if flow := cookies[desktopFlowCookie]; flow == nil || flow.MaxAge >= 0 {
		t.Errorf("flow cookie = %v, want it cleared: one sign-in, one completion", flow)
	}
	if got := control.bodies["/auth/desktop/session"]; got["code"] != "one-time" || got["codeVerifier"] != "the-verifier" || got["flow"] != appFlow {
		t.Errorf("forwarded %v, want the code, its verifier and the window's flow", got)
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer"} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	control.redeem = false
	rec = h.get(target, flowCookie(appFlow))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/sign-in?error=link_expired" {
		t.Errorf("refused code: status %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "timlee.sid" {
			t.Errorf("refused code: a session was set")
		}
	}
}

func TestDesktopCompleteRefusesAWindowThatDidNotStartTheSignIn(t *testing.T) {
	// The login CSRF: an attacker signs in to their own account for a desktop
	// client of their own and sends somebody the link that completes it, with
	// their code and verifier. Opened in another browser, from a mail or chat
	// app, with or without fetch metadata, there is no flow cookie, or there
	// is another sign-in's; either way the session is not set, and the reader
	// gets a page that says why.
	const attack = "/sign-in/desktop/complete?code=attackers-code&code_verifier=attackers-verifier&flow=" + appFlow

	tests := []struct {
		name    string
		cookie  *http.Cookie
		site    string
		forward string // the flow the control plane is sent; "-" for no call
		page    string
		message string
	}{
		{"no flow cookie, opened from a mail app", nil, "none", "", "/sign-in?error=desktop_flow", "only be finished in the Timlee app that started it"},
		{"no flow cookie, no fetch metadata", nil, "", "", "/sign-in?error=desktop_flow", "only be finished in the Timlee app that started it"},
		{"another sign-in's flow cookie", flowCookie(strings.Repeat("x", 43)), "none", strings.Repeat("x", 43), "/sign-in?error=link_expired", "has expired or was already used"},
		{"a link on another site", flowCookie(appFlow), "cross-site", "-", "/sign-in?error=desktop_flow", "only be finished in the Timlee app that started it"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, control := newDesktopHarness(t)
			r := httptest.NewRequest(http.MethodGet, attack, nil)
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tc.page {
				t.Fatalf("status %d to %q, want 303 to %q", rec.Code, rec.Header().Get("Location"), tc.page)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == "timlee.sid" {
					t.Errorf("a session was set")
				}
			}
			forwarded, called := control.bodies["/auth/desktop/session"]
			switch {
			case tc.forward == "-" && called:
				t.Errorf("the control plane was asked to redeem the code")
			case tc.forward != "-" && (!called || forwarded["flow"] != tc.forward):
				// Never the flow in the URL: only the window's own cookie counts.
				t.Errorf("forwarded %v, want flow %q", forwarded, tc.forward)
			}

			// The page the reader lands on explains it.
			page := h.get(tc.page)
			if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), tc.message) {
				t.Errorf("%s: status %d, want a sign-in page saying %q", tc.page, page.Code, tc.message)
			}
		})
	}
}
