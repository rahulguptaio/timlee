package main

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
)

// Accepting an invitation.
//
// The control plane mails {PublicOrigin}/invite.html?token=…, a path left
// over from the static site. That address is in inboxes now and must keep
// working, so it redirects here with its query intact.
//
// Opening the link never accepts anything by itself. Mail scanners and link
// previewers fetch URLs; an invitation accepted by a GET would be accepted by
// whatever fetched it first. Acceptance is a POST from a person who can see
// which account they are signed in as — and an invitation is bound to the
// address it was sent to, so the page says which one that must be.

type inviteData struct {
	Token string
	// State is one of: missing, signed-out, ready, mismatch, gone, failed.
	State string
	// Email is the signed-in account's address.
	Email    string
	SignIn   string
	Self     string
	DevLogin bool
	CSRF     template.HTML
}

// inviteLegacy keeps the mailed links working.
func (s *server) inviteLegacy(w http.ResponseWriter, r *http.Request) {
	target := "/invite"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *server) invitePage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	data := inviteData{Token: token, State: "ready"}

	up := s.auth.upstream(r)
	user, err := s.inviteReader(r, up)
	if err != nil {
		s.settingsUnavailable(w, r, err)
		return
	}
	switch {
	case token == "":
		data.State = "missing"
		s.renderInvite(w, r, up, user, data, http.StatusBadRequest)
		return
	case user == nil:
		data.State = "signed-out"
	}
	s.renderInvite(w, r, up, user, data, http.StatusOK)
}

// inviteReader resolves who is looking at an invitation, nil when nobody is
// signed in. Unlike the settings pages, a signed-out reader is expected here.
func (s *server) inviteReader(r *http.Request, up *upstream) (*sessionUser, error) {
	if _, ok := sessionCookie(r); !ok {
		return nil, nil
	}
	session, err := up.Session(r.Context())
	if err != nil {
		return nil, err
	}
	return session.User, nil
}

func (s *server) renderInvite(w http.ResponseWriter, r *http.Request, up *upstream, user *sessionUser, data inviteData, status int) {
	// The token is a credential: never cached, and its address never sent to
	// another origin. Not "no-referrer": under that policy a browser sends
	// "Origin: null" with this page's own forms, which the cross-site guard
	// rightly refuses.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")

	l := s.localizer(r)
	data.Self = l.Path("/invite")
	if data.Token != "" {
		data.Self += "?token=" + url.QueryEscape(data.Token)
	}
	data.SignIn = l.SignInPath(data.Self)

	p := &render.Page{
		Layout: "settings-solo",
		Robots: "noindex,nofollow,noarchive",
		Title:  l.T("auth.invite.documentTitle"),
		L:      l,
	}
	if user != nil {
		p.User = &render.User{ID: user.ID, Email: user.Email, Name: user.Name, AvatarURL: user.AvatarURL}
		data.Email = user.Email
		data.CSRF = csrfInput(csrfToken(r))
	} else if found, err := s.auth.Providers(r.Context()); err == nil {
		data.DevLogin = found.DevLogin
	}
	if data.State != "ready" && data.State != "signed-out" {
		p.Title = l.T("auth.invite.errorTitle", "title", p.Title)
	}
	up.relay(w)

	p.Data = data
	p.Defaults(r, httpx.NonceOf(r), httpx.IDOf(r), s.origins)
	p.Canonical = ""
	s.render(w, r, status, "invite", p)
}

// acceptInvite redeems the token for the signed-in account.
func (s *server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	l := s.localizer(r)
	if err := r.ParseForm(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, l.T("common.form.unreadable"), "bad_request")
		return
	}
	token := strings.TrimSpace(r.PostFormValue("token"))
	self := l.Path("/invite") + "?token=" + url.QueryEscape(token)

	if _, ok := sessionCookie(r); !ok {
		http.Redirect(w, r, l.SignInPath(self), http.StatusSeeOther)
		return
	}
	if !s.fromThisSite(r) {
		s.refuse(w, r, self, "origin")
		return
	}
	if !validToken(r) {
		s.refuse(w, r, self, "token")
		return
	}

	up := s.auth.upstream(r)
	data := inviteData{Token: token, State: "ready"}
	user, err := s.inviteReader(r, up)
	if err != nil {
		s.settingsUnavailable(w, r, err)
		return
	}
	if user == nil {
		up.relay(w)
		http.Redirect(w, r, l.SignInPath(self), http.StatusSeeOther)
		return
	}
	if token == "" {
		data.State = "missing"
		s.renderInvite(w, r, up, user, data, http.StatusBadRequest)
		return
	}

	accepted, err := up.AcceptInvitation(r.Context(), token)
	var refused *apiError
	switch {
	case err == nil:
	case errors.As(err, &refused) && refused.Status == http.StatusUnauthorized:
		up.relay(w)
		http.Redirect(w, r, l.SignInPath(self), http.StatusSeeOther)
		return
	case errors.As(err, &refused) && refused.Status == http.StatusForbidden && refused.Type != "forbidden_origin":
		data.State = "mismatch"
		s.renderInvite(w, r, up, user, data, http.StatusForbidden)
		return
	case errors.As(err, &refused) && refused.Status == http.StatusNotFound:
		data.State = "gone"
		s.renderInvite(w, r, up, user, data, http.StatusGone)
		return
	default:
		s.log.Warn("could not accept invitation", "error", err, "request_id", httpx.IDOf(r))
		data.State = "failed"
		s.renderInvite(w, r, up, user, data, http.StatusBadGateway)
		return
	}

	// Land in the organisation just joined, not whichever the session last
	// had selected. If this fails the membership still exists, and the
	// organisation switcher offers it.
	switched := up.SwitchOrg(r.Context(), accepted.Org.ID) == nil
	key := i18n.Key("auth.invite.accepted")
	if !switched {
		key = "auth.invite.acceptedNotSwitched"
	}
	message := l.T(key, "org", accepted.Org.Name, "role", string(accepted.Role))
	s.done(w, r, up, "/settings", flash{Tone: "success", Message: message})
}
