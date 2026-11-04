package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/httpx"
)

// Signing in to the desktop app (RFC 8252).
//
// The app never shows a sign-in page in its own webview. It opens
// /sign-in/desktop/start in its own window with the port of a listener it
// holds on 127.0.0.1 and a PKCE challenge. That sets a flow cookie in the
// window and sends it on to this site's sign-in page with desktop=1, the
// handoff and the flow id, which the app opens in the system browser instead.
// Every way in on that page carries the handoff on, so whichever one the
// reader uses ends with the control plane sending the browser to the app's
// listener with a one-time code bound to the challenge and the flow. The app
// then opens /sign-in/desktop/complete in its own window with the code and
// the verifier, and this server trades them, with the window's flow cookie,
// for a session whose cookie lands in that window.

// desktopFlowCookie holds the id of the sign-in the app's window started.
const desktopFlowCookie = "timlee.desktop"

// desktopFlowLifetime outlives the longest a sign-in can take: the app waits
// fifteen minutes for its code.
const desktopFlowLifetime = 20 * time.Minute

// desktopSignIn is the handoff a sign-in page was opened with.
type desktopSignIn struct {
	Port      string
	Challenge string
	// Flow is the id desktopStart set as a cookie in the app's window.
	Flow string
}

// pkceChallenge is an S256 challenge: unpadded base64url of 32 bytes. A flow
// id is the same shape.
var pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// newDesktopSignIn validates a handoff the way the control plane will, so a
// malformed one is dropped here rather than failing after the reader has
// already signed in.
func newDesktopSignIn(port, challenge, method, flow string) *desktopSignIn {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 || method != "S256" ||
		!pkceChallenge.MatchString(challenge) || !pkceChallenge.MatchString(flow) {
		return nil
	}
	return &desktopSignIn{Port: strconv.Itoa(n), Challenge: challenge, Flow: flow}
}

func desktopFromQuery(q url.Values) *desktopSignIn {
	if q.Get("desktop") != "1" {
		return nil
	}
	return newDesktopSignIn(q.Get("port"), q.Get("code_challenge"), q.Get("code_challenge_method"), q.Get("flow"))
}

func desktopFromForm(r *http.Request) *desktopSignIn {
	return newDesktopSignIn(r.PostFormValue("desktop_port"), r.PostFormValue("code_challenge"), "S256", r.PostFormValue("desktop_flow"))
}

// query is the handoff as the sign-in page and the provider routes take it.
func (d *desktopSignIn) query() url.Values {
	return url.Values{
		"desktop":               {"1"},
		"port":                  {d.Port},
		"code_challenge":        {d.Challenge},
		"code_challenge_method": {"S256"},
		"flow":                  {d.Flow},
	}
}

// payload is the handoff as the control plane's JSON routes take it.
func (d *desktopSignIn) payload() map[string]string {
	return map[string]string{"port": d.Port, "codeChallenge": d.Challenge, "codeChallengeMethod": "S256", "flow": d.Flow}
}

// isLoopbackDelivery accepts only what the control plane is supposed to hand
// back: the app's own listener. Anything else is not followed.
func isLoopbackDelivery(target string, d *desktopSignIn) bool {
	return strings.HasPrefix(target, "http://127.0.0.1:"+d.Port+"/callback?code=")
}

// desktopStart is where the app's window starts a sign-in, with the port and
// challenge it would otherwise put in the sign-in page's URL. It sets a fresh
// flow id as a cookie in that window and sends the window on to the sign-in
// page with the same id, which the app opens in the system browser.
//
// The code the sign-in ends in is bound to the id, and desktopComplete
// redeems it only alongside this cookie, so only the window that started a
// sign-in can finish it. Without that, an attacker could start a sign-in for
// their own account, keep the code and verifier, and send anyone a link that
// completes it, signing that browser in as the attacker (login CSRF). The id
// is minted here and never taken from the request: a page that sends a
// browser here plants an id it cannot learn, and in production the __Host-
// prefix stops a sibling subdomain from setting the cookie itself.
//
// The sign-in page is the reader's language's, from the timlee.locale cookie
// (docs/I18N.md §2, "Desktop shell locale"); without one the app puts the
// operating system's language in the URL it hands the browser.
func (s *server) desktopStart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	l := s.localizer(r)
	desktop := newDesktopSignIn(q.Get("port"), q.Get("code_challenge"), q.Get("code_challenge_method"), newDesktopFlow())
	if desktop == nil {
		http.Redirect(w, r, l.Path("/sign-in?error=desktop_start"), http.StatusSeeOther)
		return
	}
	s.setShortCookie(w, s.cookieName(desktopFlowCookie), desktop.Flow, desktopFlowLifetime, http.SameSiteLaxMode)

	next := desktop.query()
	next.Set("returnTo", safePath(q.Get("returnTo"), "/apps"))
	// The Location carries the flow id: not to be cached, or passed on.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, l.Path("/sign-in")+"?"+next.Encode(), http.StatusSeeOther)
}

// newDesktopFlow returns 32 random bytes, base64url. crypto/rand.Read does
// not return an error: it crashes the process instead.
func newDesktopFlow() string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// desktopComplete trades the code the app received for a session. The app
// opens this in the window that started the sign-in, so the flow cookie
// desktopStart set is there to go with the code, and the session cookie is
// set there.
func (s *server) desktopComplete(w http.ResponseWriter, r *http.Request) {
	// The URL holds a one-time code and its verifier.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")

	// Only ever from the cookie, never the URL: a link can carry anything.
	// One sign-in has one completion, whatever its outcome.
	name := s.cookieName(desktopFlowCookie)
	flow := ""
	if c, err := r.Cookie(name); err == nil {
		flow = c.Value
	}
	s.clearCookie(w, name, http.SameSiteLaxMode)

	// The app navigates its window here itself, which a browser reports as
	// Sec-Fetch-Site: none, so a link followed from another site is refused
	// before its code is looked at. That is a backstop and no more: a link
	// opened from a mail or chat app says none as well, and a browser without
	// fetch metadata says nothing. The flow cookie is what decides.
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		http.Redirect(w, r, s.localizer(r).Path("/sign-in?error=desktop_flow"), http.StatusSeeOther)
		return
	}

	q := r.URL.Query()
	returnTo := safePath(q.Get("returnTo"), "/apps")
	// Forwarded without a flow too: the control plane spends the code either
	// way, and records the refusal.
	_, cookies, err := s.auth.forward(r.Context(), r, "/auth/desktop/session", map[string]string{
		"code":         q.Get("code"),
		"codeVerifier": q.Get("code_verifier"),
		"flow":         flow,
	}, nil)
	for _, c := range cookies {
		http.SetCookie(w, c)
	}
	if err != nil {
		s.log.Info("desktop sign-in not completed", "error", err, "flow_cookie", flow != "", "request_id", httpx.IDOf(r))
		// With no flow cookie this is not the window that started the
		// sign-in: say where it can be finished, not that a link expired.
		code := "link_expired"
		if flow == "" {
			code = "desktop_flow"
		}
		http.Redirect(w, r, s.localizer(r).Path("/sign-in?error="+code), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}
