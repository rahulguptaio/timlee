package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"time"

	"github.com/rahulguptaio/timlee/internal/i18n"
)

// What sign-in says when it cannot go on, and how the address a link went to
// reaches the page that confirms it without ever being in a URL.

// sentCookie carries that address from the form's redirect to the
// confirmation page. A URL would put it in the history, the access logs and
// any Referer; the cookie is sealed, so not even the cookie jar holds it in
// the clear.
const sentCookie = "timlee.sent"

// sentLifetime is the link's own: once it has expired, the page that names
// the address has nothing left to confirm.
const sentLifetime = 15 * time.Minute

// emailProblem names what is wrong with an address, or "" when nothing is.
// emailShape is the control plane's own check, applied here first so a typo
// is reported beside the field, before anything is sent.
func emailProblem(email string) i18n.Key {
	switch {
	case email == "":
		return "auth.email.missing"
	case !emailShape.MatchString(email):
		return "auth.email.invalid"
	}
	return ""
}

// signInFailure is the status and the sentence a refused sign-in answers
// with. The control plane's own message is never shown: it is written for an
// API caller, and a status 0 means it never answered at all.
func signInFailure(intent string, status int) (int, i18n.Key) {
	link := intent == "magic-link"
	switch {
	case status == 0 && link:
		return http.StatusServiceUnavailable, "auth.failure.unreachableNothingSent"
	case status == 0:
		return http.StatusServiceUnavailable, "auth.failure.unreachable"
	case status == http.StatusTooManyRequests && link:
		return http.StatusTooManyRequests, "auth.failure.linkRateLimited"
	case status == http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "auth.failure.rateLimited"
	case status == http.StatusServiceUnavailable && link:
		return http.StatusServiceUnavailable, "auth.failure.mailUnavailable"
	case status >= 500 && link:
		return http.StatusBadGateway, "auth.failure.linkNotSent"
	case status >= 500:
		return http.StatusBadGateway, "auth.failure.notSignedIn"
	default:
		return http.StatusBadRequest, "auth.failure.refused"
	}
}

// sentCipher seals the address under a key derived from the server's secret
// and labelled for this one use.
func (s *server) sentCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("timlee-web/sent/v1\x00" + s.cfg.SessionSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealSent remembers where a link went, for the confirmation page.
func (s *server) sealSent(w http.ResponseWriter, email string) error {
	aead, err := s.sentCipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nonce, nonce, []byte(email), []byte(sentCookie))
	s.setShortCookie(w, s.cookieName(sentCookie), base64.RawURLEncoding.EncodeToString(sealed),
		sentLifetime, http.SameSiteLaxMode)
	return nil
}

// sentAddress opens the cookie. A missing, expired or tampered one is "", and
// the page confirms the link without naming the address. It is not cleared:
// a reload within the link's lifetime still names it.
func (s *server) sentAddress(r *http.Request) string {
	c, err := r.Cookie(s.cookieName(sentCookie))
	if err != nil {
		return ""
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return ""
	}
	aead, err := s.sentCipher()
	if err != nil || len(sealed) < aead.NonceSize() {
		return ""
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(sentCookie))
	if err != nil || emailProblem(string(plain)) != "" {
		return ""
	}
	return string(plain)
}

// sentPath is the confirmation page for a link, keeping the return target
// and the desktop app's handoff, neither of which is personal.
func sentPath(returnTo string, desktop *desktopSignIn) string {
	query := url.Values{}
	if returnTo != "" && returnTo != "/" {
		query.Set("returnTo", returnTo)
	}
	if desktop != nil {
		for key, values := range desktop.query() {
			query[key] = values
		}
	}
	if len(query) == 0 {
		return "/sign-in/sent"
	}
	return "/sign-in/sent?" + query.Encode()
}

// signInSent confirms that a link was emailed. The form redirects here
// rather than rendering it, so a reload does not send a second email.
func (s *server) signInSent(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	data := signInData{
		ReturnTo:  safePath(query.Get("returnTo"), "/apps"),
		Sent:      true,
		SentEmail: s.sentAddress(r),
		Desktop:   desktopFromQuery(query),
	}
	// The page can name an address: no cache may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	s.renderSignIn(w, r, s.page(r), data, http.StatusOK)
}
