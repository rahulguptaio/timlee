package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"
)

// Cross-site request forgery, for the forms this site serves.
//
// Two independent checks, because each covers the other's blind spot:
//
//   - Origin. A browser names the page a POST came from, and Fetch Metadata
//     says whether it was same-origin. Every settings form must come from
//     this site's own public origin — not merely from one of the origins the
//     control plane trusts, which include the API and the desktop shell.
//   - A token bound to the session. It is derived from the session cookie,
//     which is HttpOnly and never leaves this site, so a page on another
//     origin cannot compute it; and because it is derived rather than
//     stored, it needs no state and survives a restart or a second instance.
//     It covers the request that carries neither Origin nor Sec-Fetch-Site.
//
// Deriving from the cookie is sound because the cookie is the credential: a
// forger who could read it would not need to forge anything. Someone who
// plants a cookie of their own can only produce a token for their own
// session, which acts on their own account.

// csrfField is the hidden input every settings form carries.
const csrfField = "_csrf"

// csrfToken is the token for the session r presents, or "" without one.
func csrfToken(r *http.Request) string {
	cookie, ok := sessionCookie(r)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte("timlee-web/csrf/v1\x00" + cookie.Value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// csrfInput renders the hidden field, so no template can misspell its name.
func csrfInput(token string) template.HTML {
	return template.HTML(`<input type="hidden" name="` + csrfField + `" value="` + //nolint:gosec // G203: constant markup; the one value is HTML-escaped, and is a base64url digest besides
		template.HTMLEscapeString(token) + `" />`)
}

// fromThisSite reports whether a browser says the request came from a page
// this site served. A request that says nothing either way is left to the
// token.
func (s *server) fromThisSite(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return strings.EqualFold(origin, trimSlash(s.cfg.PublicOrigin))
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin":
		return true
	}
	return false
}

// validToken compares in constant time; the presented value is attacker
// supplied.
func validToken(r *http.Request) bool {
	want := csrfToken(r)
	got := r.PostFormValue(csrfField)
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
