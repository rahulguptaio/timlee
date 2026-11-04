package authsvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/store"
)

/* ------------------------------------------------- forwarded scheme -- */

func TestForwardedSchemeIsOnlyBelievedBehindATrustedProxy(t *testing.T) {
	tests := []struct {
		name       string
		trustProxy bool
		forwarded  string
		wantCookie bool
	}{
		// What any caller can send to pass plain http off as secure.
		{name: "forged header without a declared proxy", trustProxy: false, forwarded: "https"},
		{name: "the same header from a declared proxy", trustProxy: true, forwarded: "https", wantCookie: true},
		{name: "a declared proxy reporting plain http", trustProxy: true, forwarded: "http"},
		// The rightmost entry is the one our own proxy appended.
		{name: "only the rightmost hop counts", trustProxy: true, forwarded: "https, http"},
		{name: "a client prepending its own claim changes nothing", trustProxy: true, forwarded: "http, https", wantCookie: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) {
				c.Env = "production"
				c.TrustProxy = tc.trustProxy
			})

			res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"ada@example.com"}`,
				map[string]string{"X-Forwarded-Proto": tc.forwarded})
			cookies := res.Result().Cookies()

			if tc.wantCookie {
				if len(cookies) != 1 || res.Code != http.StatusOK {
					t.Fatalf("status %d with %d cookies, want 200 and a session", res.Code, len(cookies))
				}
				if !cookies[0].Secure {
					t.Error("the issued cookie is not Secure")
				}
				return
			}
			if len(cookies) != 0 || res.Code != http.StatusInternalServerError {
				t.Fatalf("status %d with %d cookies, want an explicit 500 and no session", res.Code, len(cookies))
			}
		})
	}
}

/* -------------------------------------------------------- readiness -- */

func TestReadinessReportsDatabaseReachability(t *testing.T) {
	h := newHarness(t)

	ready := h.call(t, http.MethodGet, "/readyz", "", nil)
	if ready.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", ready.Code, ready.Body)
	}

	// Readiness answers "should traffic come here", and without the database
	// this instance cannot resolve a single session.
	if err := h.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if unready := h.call(t, http.MethodGet, "/readyz", "", nil); unready.Code != http.StatusServiceUnavailable {
		t.Errorf("status with an unreachable database = %d, want 503", unready.Code)
	}

	// Liveness deliberately keeps answering: a database outage should take
	// instances out of rotation, not have every one of them restarted.
	if live := h.call(t, http.MethodGet, "/health", "", nil); live.Code != http.StatusOK {
		t.Errorf("/health = %d, want 200 while the database is down", live.Code)
	}
}

func TestReadinessPingsUnderTheProbesContext(t *testing.T) {
	// The ping belongs to the probe: one that has already given up must not
	// leave it running, so the only honest answer it can get is "not ready".
	for _, behindServer := range []bool{false, true} {
		h := newHarness(t)
		if behindServer {
			h.SetReadiness(httpx.NewServer(httpx.ServerOptions{}).ReadinessHandler)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("behind the server's handler = %v: status %d for a probe that had given up, want 503",
				behindServer, w.Code)
		}
	}
}

/* --------------------------------------- invitations need a transport -- */

func TestInvitingWithoutAMailTransportIsRefused(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.MailOutbox = "" })
	_, orgID, cookie := h.founder(t, "owner@example.com")

	res := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ada@example.com","role":"member"}`, map[string]string{"Cookie": cookie})
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 rather than a 201 for an invitation nobody received (body %s)",
			res.Code, res.Body)
	}
	detail := decode(t, res)["error"].(map[string]any)
	if detail["type"] != "mail_unavailable" {
		t.Errorf("error type = %v, want mail_unavailable", detail["type"])
	}
	// "Try again" would send the admin round a loop that can never succeed.
	if message, _ := detail["message"].(string); !strings.Contains(message, "no mail transport") {
		t.Errorf("message = %q, want it to say the server has no mail transport", message)
	}

	// No row either: a pending invitation an admin can see but nobody was
	// told about is the same lie in a different place.
	pending, err := h.store.PendingInvitations(t.Context(), orgID)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0", len(pending))
	}
}

func TestAFailedSendKeepsTheEarlierInvitationWorking(t *testing.T) {
	h := newHarness(t)
	_, orgID, cookie := h.founder(t, "owner@example.com")
	invite := func() *httptest.ResponseRecorder {
		return h.call(t, http.MethodPost, "/api/v1/org/invitations",
			`{"email":"ada@example.com","role":"member"}`, map[string]string{"Cookie": cookie})
	}

	if res := invite(); res.Code != http.StatusCreated {
		t.Fatalf("first invitation: status %d (body %s)", res.Code, res.Body)
	}
	delivered := h.inviteToken(t, orgID)

	// The transport breaks: the outbox is now a file, so nothing can be
	// written into it.
	broken := filepath.Join(filepath.Dir(h.outbox), "not-a-directory")
	if err := os.WriteFile(broken, nil, 0o600); err != nil {
		t.Fatalf("break the outbox: %v", err)
	}
	h.mail.outbox = broken

	if res := invite(); res.Code != http.StatusServiceUnavailable {
		t.Fatalf("re-invite over a failing transport: status %d, want 503 (body %s)", res.Code, res.Body)
	}

	// Recording the re-invite before sending it would have rotated the token
	// ada already holds, for a message that never arrived.
	adaCookie := h.signIn(t, "ada@example.com")
	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+delivered+"/accept", "",
		map[string]string{"Cookie": adaCookie}); res.Code != http.StatusOK {
		t.Errorf("the invitation ada received: status %d, want 200 (body %s)", res.Code, res.Body)
	}
}

/* ----------------------------------------------------- desktop PKCE -- */

// The worked example from RFC 7636 appendix B, and the flow id the site set
// in the app window that started the sign-in.
const (
	rfcVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	appFlow      = "Vb5r0sQ2bS3vBqkq5mH5u2P1yWm7wN0gVf4c9b8a7d6"
)

func TestDesktopSignInValidatesItsHandoff(t *testing.T) {
	const pkce = "&code_challenge_method=S256&code_challenge=" + rfcChallenge
	const flow = "&flow=" + appFlow

	tests := []struct {
		name  string
		query string
		ok    bool
	}{
		{name: "a valid native client", query: "port=49152" + pkce + flow, ok: true},
		{name: "no port", query: strings.TrimPrefix(pkce, "&") + flow},
		{name: "privileged port", query: "port=80" + pkce + flow},
		{name: "port out of range", query: "port=70000" + pkce + flow},
		{name: "port smuggling a host", query: "port=4915@evil.example" + pkce + flow},
		{name: "no challenge", query: "port=49152&code_challenge_method=S256" + flow},
		{name: "plain challenge method", query: "port=49152&code_challenge_method=plain&code_challenge=" + rfcChallenge + flow},
		{name: "no challenge method", query: "port=49152&code_challenge=" + rfcChallenge + flow},
		{name: "challenge is not base64url", query: "port=49152&code_challenge_method=S256&code_challenge=" + strings.Repeat("+", 43) + flow},
		{name: "no flow", query: "port=49152" + pkce},
		{name: "flow is not base64url", query: "port=49152" + pkce + "&flow=" + strings.Repeat("~", 43)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) {
				c.Providers = map[string]config.Provider{"github": {ClientID: "id", ClientSecret: "secret"}}
			})

			res := h.call(t, http.MethodGet, "/auth/github/start?desktop=1&"+tc.query, "", nil)
			if !tc.ok {
				if res.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 before the provider is involved", res.Code)
				}
				return
			}
			if res.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302 to the provider (body %s)", res.Code, res.Body)
			}

			// The sign-in in progress remembers where to deliver the code, and
			// the challenge and flow that code will be bound to: the flow by
			// its hash, as nothing that redeems a code is kept as it is.
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, c := range res.Result().Cookies() {
				r.AddCookie(c)
			}
			current, _, err := h.sessions.resolve(r, 0)
			if err != nil || current == nil || current.data.Desktop == nil ||
				*current.data.Desktop != (desktopHandoff{Port: "49152", Challenge: rfcChallenge, FlowHash: store.HashSecret(appFlow)}) {
				t.Errorf("sign-in in progress = %+v, want port 49152 bound to the challenge and the flow's hash", current)
			}
		})
	}
}

func TestDesktopSignInLeavesTheBrowserAsItWas(t *testing.T) {
	h := newHarness(t)
	user, _, err := h.store.UpsertFromProvider(t.Context(), store.Profile{
		Provider: "email", Subject: "ada@example.com", Email: "ada@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	handoff := &desktopHandoff{Port: "49152", Challenge: rfcChallenge, FlowHash: store.HashSecret(appFlow)}

	// What the OAuth callback does once the provider has vouched for the
	// user. A signed-out browser stays signed out: the session is the app's.
	w := httptest.NewRecorder()
	h.completeSignIn(w, httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil), user, "github", "", handoff)
	codeFrom(t, w.Header().Get("Location"))
	if n := len(w.Result().Cookies()); n != 0 {
		t.Errorf("a signed-out browser got %d cookies, want none", n)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("delivery headers = %q / %q, want no-store and no-referrer",
			w.Header().Get("Cache-Control"), w.Header().Get("Referrer-Policy"))
	}

	// A browser that was already signed in keeps that session, as the same
	// user, with only the finished round trip cleared from it.
	before, err := h.sessions.issue(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
		sessionData{UserID: user.ID, Flow: &oauthFlow{Provider: "github", State: "s"}, Desktop: handoff})
	if err != nil {
		t.Fatalf("pre-auth session: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	r = r.WithContext(context.WithValue(r.Context(), sessionKey, before))
	w = httptest.NewRecorder()
	h.completeSignIn(w, r, user, "github", "", handoff)
	codeFrom(t, w.Header().Get("Location"))
	for _, c := range w.Result().Cookies() {
		if c.Value != h.sessions.sign(before.id) {
			t.Errorf("the browser was issued a different session (%s)", c.Name)
		}
	}
	after := httptest.NewRequest(http.MethodGet, "/", nil)
	after.AddCookie(&http.Cookie{Name: h.sessions.name, Value: h.sessions.sign(before.id)})
	if kept, _, err := h.sessions.resolve(after, 0); err != nil || kept == nil || kept.data.UserID != user.ID || kept.data.Flow != nil || kept.data.Desktop != nil {
		t.Errorf("browser session afterwards = %+v (error %v), want the same user with the flow cleared", kept, err)
	}
}

/* ---------------------------------------------- desktop handoff routes -- */

const rfcDesktop = `"desktop":{"port":"49152","codeChallenge":"` + rfcChallenge + `","codeChallengeMethod":"S256","flow":"` + appFlow + `"}`

// redeemDesktop is what the site does when the desktop app's window opens
// /sign-in/desktop/complete: flow is what the window's flow cookie held.
func (h *harness) redeemDesktop(t *testing.T, code, verifier, flow string) *httptest.ResponseRecorder {
	t.Helper()
	return h.call(t, http.MethodPost, "/auth/desktop/session",
		`{"code":"`+code+`","codeVerifier":"`+verifier+`","flow":"`+flow+`"}`, nil)
}

// codeFrom takes the code out of a loopback delivery URL, checking that it
// goes nowhere but the client's own listener.
func codeFrom(t *testing.T, delivery string) string {
	t.Helper()
	u, err := url.Parse(delivery)
	if err != nil || u.Scheme != "http" || u.Host != "127.0.0.1:49152" || u.Path != "/callback" {
		t.Fatalf("delivered to %q, want http://127.0.0.1:49152/callback", delivery)
	}
	return u.Query().Get("code")
}

// desktopCode signs in for the desktop app through the development route and
// returns the code delivered to the app's listener. The browser gets nothing.
func (h *harness) desktopCode(t *testing.T) string {
	t.Helper()
	signedIn := h.call(t, http.MethodPost, "/auth/dev", `{"email":"ada@example.com",`+rfcDesktop+`}`, nil)
	if signedIn.Code != http.StatusOK {
		t.Fatalf("dev sign-in for the desktop: status %d, body %s", signedIn.Code, signedIn.Body)
	}
	if n := len(signedIn.Result().Cookies()); n != 0 {
		t.Fatalf("the browser got %d cookies, want none: the session is the app's", n)
	}
	return codeFrom(t, decode(t, signedIn)["desktopRedirect"].(string))
}

func TestDesktopCodeIsSpentByItsFirstExchangeAttempt(t *testing.T) {
	// Missing, malformed, or well formed and wrong: whichever verifier comes
	// first is the only one the code ever sees.
	for _, first := range []string{"", "short", strings.Repeat("a", 43)} {
		h := newHarness(t)
		code := h.desktopCode(t)

		if res := h.redeemDesktop(t, code, first, appFlow); res.Code != http.StatusBadRequest || len(res.Result().Cookies()) != 0 {
			t.Fatalf("verifier %q: status %d and %d cookies, want a refusal", first, res.Code, len(res.Result().Cookies()))
		}
		// Whoever tried first had the code's only try. The app, arriving
		// second with the right verifier, is refused, so an interception
		// shows as a failed sign-in instead of going unnoticed.
		if res := h.redeemDesktop(t, code, rfcVerifier, appFlow); res.Code != http.StatusBadRequest || len(res.Result().Cookies()) != 0 {
			t.Errorf("verifier %q, then the right one: status %d and %d cookies, want the code already spent",
				first, res.Code, len(res.Result().Cookies()))
		}
	}
}

func TestDesktopSessionIsIssuedOnceForTheRightVerifier(t *testing.T) {
	h := newHarness(t)
	code := h.desktopCode(t)

	redeemed := h.redeemDesktop(t, code, rfcVerifier, appFlow)
	cookies := redeemed.Result().Cookies()
	if redeemed.Code != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("redeemed with the verifier: status %d and %d cookies, want 200 and a session (body %s)",
			redeemed.Code, len(cookies), redeemed.Body)
	}
	session := h.call(t, http.MethodGet, "/auth/session", "",
		map[string]string{"Cookie": cookies[0].Name + "=" + cookies[0].Value})
	if user, _ := decode(t, session)["user"].(map[string]any); user == nil || user["email"] != "ada@example.com" {
		t.Errorf("session = %v, want ada@example.com", user)
	}

	if replay := h.redeemDesktop(t, code, rfcVerifier, appFlow); replay.Code != http.StatusBadRequest {
		t.Errorf("replay: status %d, want 400", replay.Code)
	}
}

func TestDevSignInRefusesABadDesktopHandoff(t *testing.T) {
	for name, bad := range map[string]string{
		"privileged port": strings.Replace(rfcDesktop, "49152", "80", 1),
		"no flow":         strings.Replace(rfcDesktop, `,"flow":"`+appFlow+`"`, "", 1),
	} {
		if bad == rfcDesktop {
			t.Fatalf("%s: the hand-off was not changed", name)
		}
		h := newHarness(t)
		res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"ada@example.com",`+bad+`}`, nil)
		if res.Code != http.StatusBadRequest || len(res.Result().Cookies()) != 0 {
			t.Errorf("%s: status %d and %d cookies, want 400 and nothing issued", name, res.Code, len(res.Result().Cookies()))
		}
	}
}

func TestDesktopMagicLinkIsHandedToTheApp(t *testing.T) {
	h := newHarness(t)
	sent := h.call(t, http.MethodPost, "/auth/magic-link", `{"email":"ada@example.com",`+rfcDesktop+`}`, nil)
	if sent.Code != http.StatusOK {
		t.Fatalf("request link: status %d, body %s", sent.Code, sent.Body)
	}
	link := strings.TrimPrefix(h.lastLink(t), "http://127.0.0.1:8788")
	if !strings.Contains(link, "&desktop_port=49152") {
		t.Fatalf("link = %q, want it to name the app's port", link)
	}

	// Opened in a browser, the link signs nobody in there: it goes on to the
	// app's listener, still unspent.
	opened := h.call(t, http.MethodGet, link, "", nil)
	if opened.Code != http.StatusFound || len(opened.Result().Cookies()) != 0 {
		t.Fatalf("opened: status %d and %d cookies, want 302 and no session", opened.Code, len(opened.Result().Cookies()))
	}
	// The Location carries the code: not to be cached, or passed on in a Referer.
	if opened.Header().Get("Cache-Control") != "no-store" || opened.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("hand-off headers = %q / %q, want no-store and no-referrer",
			opened.Header().Get("Cache-Control"), opened.Header().Get("Referrer-Policy"))
	}
	code := codeFrom(t, opened.Header().Get("Location"))

	// Without the app's port the link is no use to a browser at all.
	stripped := strings.Replace(link, "&desktop_port=49152", "", 1)
	if res := h.call(t, http.MethodGet, stripped, "", nil); res.Code != http.StatusSeeOther ||
		!strings.Contains(res.Header().Get("Location"), "error=link_expired") {
		t.Errorf("opened without the port: status %d to %q, want the expired-link page", res.Code, res.Header().Get("Location"))
	}

	if res := h.redeemDesktop(t, code, rfcVerifier, appFlow); res.Code != http.StatusOK || len(res.Result().Cookies()) != 1 {
		t.Fatalf("the app's redemption: status %d, body %s", res.Code, res.Body)
	}
}

func TestDesktopCodeIsRedeemedOnlyInTheWindowThatStartedIt(t *testing.T) {
	// The login CSRF this closes: an attacker signs in to their own account
	// for a desktop client of their own, keeps the code and the verifier, and
	// sends somebody a link that completes the sign-in. That browser holds no
	// flow cookie, or one from a sign-in it started itself. Either way the
	// code is refused, and spent.
	for name, flow := range map[string]string{
		"no flow cookie":        "",
		"another window's flow": "Xb5r0sQ2bS3vBqkq5mH5u2P1yWm7wN0gVf4c9b8a7d6",
	} {
		h := newHarness(t)
		events := logx.Capture(t)
		code := h.desktopCode(t)

		if res := h.redeemDesktop(t, code, rfcVerifier, flow); res.Code != http.StatusBadRequest || len(res.Result().Cookies()) != 0 {
			t.Fatalf("%s: status %d and %d cookies, want a refusal", name, res.Code, len(res.Result().Cookies()))
		}
		attrs(t, only(t, events, "audit.auth.sign_in_failed"), map[string]string{"provider": "desktop", "reason": "flow_mismatch"})
		if res := h.redeemDesktop(t, code, rfcVerifier, appFlow); res.Code != http.StatusBadRequest {
			t.Errorf("%s, then the right window: status %d, want the code already spent", name, res.Code)
		}
	}
}

func TestDesktopSignInThroughAProviderIsRedeemedByTheApp(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Providers = map[string]config.Provider{"github": {ClientID: "id", ClientSecret: "secret"}}
	})
	start := h.call(t, http.MethodGet, "/auth/github/start?desktop=1&port=49152&code_challenge_method=S256&code_challenge="+
		rfcChallenge+"&flow="+appFlow, "", nil)
	if start.Code != http.StatusFound {
		t.Fatalf("start: status %d, body %s", start.Code, start.Body)
	}
	user, _, err := h.store.UpsertFromProvider(t.Context(), store.Profile{
		Provider: "github", Subject: "1", Email: "ada@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// What the callback does once the provider has vouched for ada, with the
	// sign-in in progress this browser started.
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	for _, c := range start.Result().Cookies() {
		r.AddCookie(c)
	}
	current, _, err := h.sessions.resolve(r, 0)
	if err != nil || current == nil || current.data.Desktop == nil {
		t.Fatalf("no desktop sign-in in progress: %+v (error %v)", current, err)
	}
	w := httptest.NewRecorder()
	h.completeSignIn(w, r.WithContext(context.WithValue(r.Context(), sessionKey, current)), user, "github", "", current.data.Desktop)

	if res := h.redeemDesktop(t, codeFrom(t, w.Header().Get("Location")), rfcVerifier, appFlow); res.Code != http.StatusOK || len(res.Result().Cookies()) != 1 {
		t.Fatalf("the app's redemption: status %d, body %s", res.Code, res.Body)
	}
}
