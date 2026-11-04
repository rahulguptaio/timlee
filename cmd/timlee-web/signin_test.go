package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/i18n"
)

// signInControl answers the control plane's sign-in routes with a chosen
// status, and hands everything else to the usual stub. calls counts what
// reached it, so a test can tell a form was answered before being sent.
type signInControl struct {
	fallback http.Handler
	status   int
	calls    int
}

func (c *signInControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || (r.URL.Path != "/auth/magic-link" && r.URL.Path != "/auth/dev") {
		c.fallback.ServeHTTP(w, r)
		return
	}
	c.calls++
	w.Header().Set("Content-Type", "application/json")
	if c.status != http.StatusOK {
		w.WriteHeader(c.status)
		// Written for an API caller; the page must never show it.
		_, _ = io.WriteString(w, `{"error":{"message":"upstream wording for status","type":"upstream"}}`)
		return
	}
	_, _ = io.WriteString(w, `{"sent":true}`)
}

func newSignInHarness(t *testing.T, status int) (*harness, *signInControl) {
	t.Helper()
	h := newHarness(t)
	control := &signInControl{fallback: h.control.handler(), status: status}
	upstream := httptest.NewServer(control)
	t.Cleanup(upstream.Close)
	h.srv.auth = newAuthClient(upstream.URL, h.srv.cfg.PublicOrigin, h.srv.log)
	return h, control
}

func linkForm(email string) url.Values {
	return url.Values{"intent": {"magic-link"}, "email": {email}, "returnTo": {"/settings"}}
}

func titleOf(body string) string {
	_, rest, _ := strings.Cut(body, "<title>")
	title, _, _ := strings.Cut(rest, "</title>")
	return title
}

// TestSignInLinkKeepsTheAddressOutOfTheURL follows the email form to the
// confirmation: the address reaches that page sealed in a cookie, never in
// the Location, a history entry or a log line.
func TestSignInLinkKeepsTheAddressOutOfTheURL(t *testing.T) {
	h, _ := newSignInHarness(t, http.StatusOK)
	rec := h.post("/sign-in", linkForm("ada@example.com"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	location := rec.Header().Get("Location")
	if location != "/sign-in/sent?returnTo=%2Fsettings" {
		t.Errorf("Location = %q, want the sent page with only the return target", location)
	}

	var sent *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sentCookie {
			sent = c
		}
	}
	if sent == nil {
		t.Fatal("no sent cookie")
	}
	if !sent.HttpOnly || sent.SameSite != http.SameSiteLaxMode || sent.Path != "/" || sent.MaxAge != int(sentLifetime.Seconds()) {
		t.Errorf("sent cookie = %+v, want HttpOnly, SameSite=Lax, Path=/, the link's 15 minutes", sent)
	}
	if strings.Contains(sent.Value, "ada") || strings.Contains(sent.Value, "example") {
		t.Errorf("sent cookie holds the address in the clear: %q", sent.Value)
	}

	page := h.get(location, sent)
	body := page.Body.String()
	if got := page.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on a page that names an address", got)
	}
	if got := titleOf(body); got != "Check your inbox — Timlee" {
		t.Errorf("title = %q", got)
	}
	wantContains(t, body,
		`We sent a sign-in link to <b><bdi>ada@example.com</bdi></b>. It expires in 15 minutes and works once.`,
		"It can take a minute to arrive.",
		`<input type="hidden" name="email" value="ada@example.com" />`,
		`<input type="hidden" name="returnTo" value="/settings" />`,
		"Use a different email",
	)
}

// TestSignInSentWithoutTheCookie is the same page once the address is gone:
// after its 15 minutes, with cookies off, or with a cookie that was tampered
// with or sealed by another server. It confirms without naming anyone and
// offers no resend, which would need the address.
func TestSignInSentWithoutTheCookie(t *testing.T) {
	h, _ := newSignInHarness(t, http.StatusOK)
	sealed := h.post("/sign-in", linkForm("ada@example.com")).Result().Cookies()[0]

	for name, cookies := range map[string][]*http.Cookie{
		"none":     nil,
		"tampered": {{Name: sentCookie, Value: sealed.Value[:len(sealed.Value)-2] + "AA"}},
		"garbage":  {{Name: sentCookie, Value: "not base64!"}},
	} {
		t.Run(name, func(t *testing.T) {
			body := h.get("/sign-in/sent", cookies...).Body.String()
			wantContains(t, body, "We sent a sign-in link to the address you entered.", "Use a different email")
			wantAbsent(t, body, "ada@example.com", "Resend link")
		})
	}

	// The address never comes from the URL, including the old one.
	body := h.get("/sign-in?sent=1&email=ada%40example.com").Body.String()
	wantAbsent(t, body, "ada@example.com", "Check your inbox")
}

// TestSignInEmailErrorsSitOnTheField: an address the control plane would
// refuse is answered here, beside the field, with a 422, before anything is
// sent.
func TestSignInEmailErrorsSitOnTheField(t *testing.T) {
	for email, message := range map[string]string{
		"":             "Enter your email address",
		"me@localhost": "Enter an email address like name@company.com",
	} {
		t.Run(email, func(t *testing.T) {
			h, control := newSignInHarness(t, http.StatusOK)
			rec := h.post("/sign-in", linkForm(email))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", rec.Code)
			}
			if control.calls != 0 {
				t.Errorf("%d requests reached the control plane, want none", control.calls)
			}
			body := rec.Body.String()
			if got := unisolate(titleOf(body)); got != "Error: Sign in — Timlee" {
				t.Errorf("title = %q", got)
			}
			wantContains(t, body,
				`<timlee-input invalid>`,
				`aria-describedby="auth-email-msg auth-email-error"`,
				`aria-invalid="true"`,
				`<p data-error id="auth-email-error"><span class="visually-hidden">Error: </span>`+message+`</p>`,
			)
			wantAbsent(t, body, `<timlee-alert tone="danger"`)
		})
	}
}

// TestSignInFailuresAreSentences maps every refusal to a status and a
// sentence a reader can act on. The control plane's own wording never
// reaches the page.
func TestSignInFailuresAreSentences(t *testing.T) {
	cases := []struct {
		name     string
		intent   string
		upstream int // 0: the control plane does not answer
		status   int
		message  string
	}{
		{"link unreachable", "magic-link", 0, 503, "Timlee could not reach its sign-in service. Nothing was sent. Try again in a moment."},
		{"link rate limited", "magic-link", 429, 429, "Too many sign-in links have been requested for this address. Wait a minute, then try again."},
		{"no mail", "magic-link", 503, 503, "Email sign-in is not available on this server."},
		{"link failed", "magic-link", 500, 502, "Timlee could not send the sign-in link. Try again in a moment."},
		{"link refused", "magic-link", 400, 400, "That sign-in could not start. Reload the page and try again."},
		{"dev unreachable", "dev", 0, 503, "Timlee could not reach its sign-in service. Try again in a moment."},
		{"dev rate limited", "dev", 429, 429, "Too many sign-in attempts. Wait a minute, then try again."},
		{"dev failed", "dev", 500, 502, "Timlee could not sign you in. Try again in a moment."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newSignInHarness(t, tc.upstream)
			if tc.upstream == 0 {
				gone := httptest.NewServer(http.NotFoundHandler())
				gone.Close()
				h.srv.auth = newAuthClient(gone.URL, h.srv.cfg.PublicOrigin, h.srv.log)
			}
			form := linkForm("ada@example.com")
			form.Set("intent", tc.intent)
			rec := h.post("/sign-in", form)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			body := rec.Body.String()
			if got := titleOf(body); !strings.HasPrefix(got, "Error: ") {
				t.Errorf("title = %q, want it to start with Error:", got)
			}
			wantContains(t, body, `<timlee-alert tone="danger" class="auth-alert" tabindex="-1" autofocus><p>`+tc.message+`</p></timlee-alert>`)
			wantAbsent(t, body, "upstream wording")
		})
	}
}

// The sentences are keys in the English catalog, so none may render as a
// missing message.
func TestSignInMessageKeysExist(t *testing.T) {
	h := newHarness(t)
	l := h.srv.localizer(httptest.NewRequest(http.MethodGet, "/sign-in", nil))
	keys := []string{string(emailProblem("")), string(emailProblem("me@localhost"))}
	for _, intent := range []string{"magic-link", "dev"} {
		for _, status := range []int{0, 400, 403, 429, 500, 502, 503} {
			_, key := signInFailure(intent, status)
			keys = append(keys, string(key))
		}
	}
	for code := range signInErrors {
		keys = append(keys, string(signInError(code)))
	}
	keys = append(keys, string(signInError("unknown")))
	for _, key := range keys {
		if got := l.T(i18n.Key(key)); strings.HasPrefix(got, "⟦") {
			t.Errorf("%s renders as %s: auth.arb does not hold it", key, got)
		}
	}
}

// unisolate strips the bidi isolates the localizer wraps arguments in.
func unisolate(s string) string { return strings.NewReplacer("\u2068", "", "\u2069", "").Replace(s) }
