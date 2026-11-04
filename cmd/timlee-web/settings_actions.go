package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
)

// The settings forms.
//
// Each one is a plain POST that this process checks, relays to the control
// plane as JSON, and answers with a redirect carrying a one-time message —
// so a refresh never repeats an action, and none of it needs scripting. Input
// a person can fix is answered in place instead (422, the form re-rendered
// with its error); everything else becomes a message on the page they were
// on, worded for the person rather than the protocol.

// maxFormBytes bounds a settings form. The largest is a name and a token.
const maxFormBytes = 64 << 10

// emailShape matches the control plane's own deliberately loose check, so a
// form never passes here and fails there on shape alone.
var emailShape = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// settingsAction guards a settings form before its handler runs: a session,
// a request from this site, and the token bound to that session. back is the
// page the form lives on, where every outcome is reported.
func (s *server) settingsAction(back string, h func(http.ResponseWriter, *http.Request, *upstream)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		if _, ok := sessionCookie(r); !ok {
			http.Redirect(w, r, render.SignInPath(back), http.StatusSeeOther)
			return
		}
		if !s.fromThisSite(r) {
			s.refuse(w, r, back, "origin")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, s.localizer(r).T("common.form.unreadable"), "bad_request")
			return
		}
		if !validToken(r) {
			s.refuse(w, r, back, "token")
			return
		}
		h(w, r, s.auth.upstream(r))
	}
}

// refuse answers a form this site cannot vouch for. Nothing is relayed and
// nothing changes; the page says so and offers the way back.
func (s *server) refuse(w http.ResponseWriter, r *http.Request, back, reason string) {
	s.log.Warn("settings form refused", "reason", reason, "path", r.URL.Path,
		"origin", r.Header.Get("Origin"), "request_id", httpx.IDOf(r))
	s.notice(w, r, http.StatusForbidden, notice{
		Title: s.localizer(r).T("errors.settings.formUnverifiedTitle"),
		Body:  s.localizer(r).T("errors.settings.formUnverifiedBody"),
		Href:  back,
		Label: s.localizer(r).T("errors.action.reload"),
	})
}

// done reports a completed action on the page it came from.
func (s *server) done(w http.ResponseWriter, r *http.Request, up *upstream, to string, f flash) {
	up.relay(w)
	s.setFlash(w, f)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// failed reports a refused or failed action on the page it came from.
// explain words the control plane's refusal for this particular action; it
// is not consulted for an expired session or an unreachable control plane,
// which read the same whatever was attempted.
func (s *server) failed(w http.ResponseWriter, r *http.Request, up *upstream, back string, err error, explain func(*apiError) string) {
	var refused *apiError
	switch {
	case errors.As(err, &refused) && refused.Status == http.StatusUnauthorized:
		up.relay(w)
		http.Redirect(w, r, render.SignInPath(back), http.StatusSeeOther)
		return
	case errors.As(err, &refused):
		message := explain(refused)
		if message == "" {
			message = generic(s.localizer(r), refused)
		}
		s.done(w, r, up, back, flash{Tone: "danger", Message: message})
	default:
		s.log.Warn("settings action failed", "error", err, "path", r.URL.Path, "request_id", httpx.IDOf(r))
		s.done(w, r, up, back, flash{Tone: "danger",
			Message: s.localizer(r).T("errors.settings.actionUnreachable")})
	}
}

// generic words a refusal no action-specific rule claimed.
func generic(l *i18n.Localizer, e *apiError) string {
	switch {
	case e.Status == http.StatusForbidden && e.Type == "forbidden_origin":
		return l.T("errors.settings.forbiddenOrigin")
	case e.Status == http.StatusForbidden:
		// A settings page only offers actions the loaded role allowed. A
		// later 403 therefore means the role changed or the action was no
		// longer allowed; never inspect the control plane's English message.
		return l.T("errors.settings.roleChanged")
	case e.Status == http.StatusTooManyRequests:
		return l.T("errors.settings.rateLimited")
	case e.Status >= 500:
		return l.T("errors.settings.server")
	}
	return l.T("errors.settings.generic")
}

// rerender re-runs a settings page for a form that needs correcting.
func (s *server) rerender(w http.ResponseWriter, r *http.Request, section, back string) (*settingsReq, bool) {
	sr, ok := s.beginSettings(w, r, section)
	if !ok {
		return nil, false
	}
	// The organisation switcher returns to the page, not to the form's URL,
	// which answers only POST.
	sr.shell.Path = back
	return sr, true
}

// field reads a form value with surrounding space removed.
func field(r *http.Request, name string) string { return strings.TrimSpace(r.PostFormValue(name)) }

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

/* ------------------------------------------------------------- profile -- */

func (s *server) updateProfile(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/profile"
	l := s.localizer(r)
	name := field(r, "name")

	problem := ""
	switch {
	case hasControl(name):
		problem = l.T("settings.profile.nameControl")
	// The control plane keeps 120 bytes and cuts the rest, mid-character if
	// need be; refusing here is the only way to never store half a letter.
	case len(name) > 120:
		problem = l.T("settings.profile.nameLong", "max", 120)
	}
	if problem != "" {
		if sr, ok := s.rerender(w, r, "profile", back); ok {
			s.renderProfile(w, r, sr, profileForm{Name: name, Error: problem}, http.StatusUnprocessableEntity)
		}
		return
	}

	if err := up.UpdateName(r.Context(), name); err != nil {
		s.failed(w, r, up, back, err, func(*apiError) string { return "" })
		return
	}
	message := l.T("settings.profile.saved")
	if name == "" {
		message = l.T("settings.profile.removed")
	}
	s.done(w, r, up, back, flash{Tone: "success", Message: message})
}

func (s *server) unlinkIdentity(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/security"
	l := s.localizer(r)
	provider := field(r, "provider")
	if provider == "" {
		provider = l.T("settings.security.thatMethod")
	}

	if err := up.UnlinkIdentity(r.Context(), r.PathValue("id")); err != nil {
		s.failed(w, r, up, back, err, func(e *apiError) string {
			switch e.Status {
			case http.StatusConflict:
				return l.T("settings.security.onlyMethodError")
			case http.StatusNotFound:
				return l.T("settings.security.alreadyUnlinked", "provider", provider)
			}
			return ""
		})
		return
	}
	s.done(w, r, up, back, flash{Tone: "success", Message: l.T("settings.security.unlinked", "provider", provider)})
}

/* ---------------------------------------------------------------- keys -- */

func (s *server) createKey(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/keys"
	l := s.localizer(r)
	form := keyForm{Label: field(r, "label"), Expires: field(r, "expires")}

	switch {
	case form.Label == "":
		form.Error = l.T("settings.keys.labelMissing")
	case hasControl(form.Label):
		form.Error = l.T("settings.keys.labelControl")
	case len(form.Label) > 64:
		// As with names, the control plane truncates by bytes.
		form.Error = l.T("settings.keys.labelLong", "max", 64)
	}
	days, ok := keyLifetime(form.Expires)
	if !ok {
		form.ExpiresError = l.T("settings.keys.expiresError")
	}
	if form.Error != "" || form.ExpiresError != "" {
		if sr, ok := s.rerender(w, r, "keys", back); ok {
			s.renderKeys(w, r, sr, form, http.StatusUnprocessableEntity)
		}
		return
	}

	created, err := up.CreateKey(r.Context(), form.Label, days)
	if err != nil {
		s.failed(w, r, up, back, err, func(e *apiError) string {
			if e.Status == http.StatusForbidden {
				return l.T("settings.keys.createForbidden")
			}
			return ""
		})
		return
	}

	if err := s.sealReveal(w, r, revealedKey{ID: created.ID, Label: created.Label, Prefix: created.Prefix, Secret: created.Secret}); err != nil {
		// A key nobody ever saw is a live credential nobody can use or
		// rotate. Withdraw it rather than leave it behind.
		s.log.Error("could not hand a new key to the page; revoking it", "error", err, "request_id", httpx.IDOf(r))
		_ = up.RevokeKey(r.Context(), created.ID)
		s.done(w, r, up, back, flash{Tone: "danger",
			Message: l.T("settings.keys.createUnshown")})
		return
	}
	message := l.T("settings.keys.createdNoExpiry", "label", created.Label)
	if created.ExpiresAt != nil {
		message = l.T("settings.keys.created", "label", created.Label, "date", stampOf(l, *created.ExpiresAt, time.Now()).Full)
	}
	s.done(w, r, up, back, flash{Tone: "success", Message: message})
}

// rotateKey replaces a key with a new one under the same label, and shows the
// new secret the way a created key's is shown: once.
func (s *server) rotateKey(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/keys"
	id := r.PathValue("id")
	label := field(r, "label")
	l := s.localizer(r)
	hours, err := strconv.Atoi(field(r, "overlap"))
	valid := err == nil
	if valid {
		valid = false
		for _, o := range keyOverlaps {
			valid = valid || o.Hours == hours
		}
	}
	if !valid {
		s.done(w, r, up, "/settings/keys?rotate="+url.QueryEscape(id)+"#rotate-"+id, flash{Tone: "danger",
			Message: l.T("settings.keys.overlapMissing", "label", label)})
		return
	}

	rotated, err := up.RotateKey(r.Context(), id, hours)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			s.done(w, r, up, back, flash{Tone: "info", Message: l.T("settings.keys.rotateGone", "label", label)})
			return
		}
		s.failed(w, r, up, back, err, func(e *apiError) string {
			switch {
			case e.Status == http.StatusForbidden:
				return l.T("settings.keys.rotateForbidden")
			case e.Status == http.StatusConflict:
				return l.T("settings.keys.rotateRetired")
			}
			return ""
		})
		return
	}

	if err := s.sealReveal(w, r, revealedKey{ID: rotated.ID, Label: rotated.Label, Prefix: rotated.Prefix, Secret: rotated.Secret}); err != nil {
		s.log.Error("could not hand a replacement key to the page; revoking it", "error", err, "request_id", httpx.IDOf(r))
		_ = up.RevokeKey(r.Context(), rotated.ID)
		s.done(w, r, up, back, flash{Tone: "danger", Message: l.T("settings.keys.rotateUnshown", "label", rotated.Label)})
		return
	}
	message := l.T("settings.keys.rotatedRevoked", "label", rotated.Label)
	if hours > 0 {
		message = l.T("settings.keys.rotated", "label", rotated.Label, "date", stampOf(l, rotated.ReplacedKeyStopsAt, time.Now()).Full)
	}
	s.done(w, r, up, back, flash{Tone: "success", Message: message})
}

func (s *server) revokeKey(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/keys"
	l := s.localizer(r)
	label := field(r, "label")

	if err := up.RevokeKey(r.Context(), r.PathValue("id")); err != nil {
		if statusOf(err) == http.StatusNotFound {
			s.done(w, r, up, back, flash{Tone: "info", Message: l.T("settings.keys.alreadyRevoked", "label", label)})
			return
		}
		s.failed(w, r, up, back, err, func(e *apiError) string {
			if e.Status == http.StatusForbidden {
				return l.T("settings.keys.revokeForbidden")
			}
			return ""
		})
		return
	}
	s.done(w, r, up, back, flash{Tone: "success",
		Message: l.T("settings.keys.revoked", "label", label)})
}

/* ------------------------------------------------------------- members -- */

func (s *server) invite(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/members"
	l := s.localizer(r)
	email := strings.ToLower(field(r, "email"))
	granted := role(strings.ToLower(field(r, "role")))
	resend := r.PostFormValue("resend") == "1"

	form := inviteForm{Email: email, Role: granted}
	switch {
	case email == "":
		form.EmailError = l.T("settings.members.emailMissing")
	case !emailShape.MatchString(email):
		form.EmailError = l.T("settings.members.emailInvalid")
	}
	if !granted.valid() {
		form.RoleError = l.T("settings.members.roleMissing")
	}
	if form.EmailError != "" || form.RoleError != "" {
		if sr, ok := s.rerender(w, r, "members", back); ok {
			s.renderMembers(w, r, sr, form, http.StatusUnprocessableEntity)
		}
		return
	}

	ctx := r.Context()
	if err := up.Invite(ctx, email, granted, l.Locale().ID); err != nil {
		refused := &apiError{}
		if errors.As(err, &refused) && refused.Status == http.StatusBadRequest && !resend {
			// The control plane's own validation, about input the reader
			// can fix: answered on the form, like ours.
			form.EmailError = l.T("settings.members.emailInvalid")
			if sr, ok := s.rerender(w, r, "members", back); ok {
				s.renderMembers(w, r, sr, form, http.StatusUnprocessableEntity)
			}
			return
		}
		s.failed(w, r, up, back, err, func(e *apiError) string {
			if e.Status == http.StatusServiceUnavailable {
				return mailFailure(l, ctx, up, email)
			}
			return ""
		})
		return
	}

	message := l.T("settings.members.invited", "email", email)
	if resend {
		message = l.T("settings.members.resent", "email", email)
	}
	s.done(w, r, up, back, flash{Tone: "success", Message: message})
}

// mailFailure words a 503 from the invitation endpoint. "No mail is
// configured" and "this one message failed" need different next steps, and
// the providers endpoint is what tells them apart. Either way the control
// plane recorded nothing: it sends before it records.
func mailFailure(l *i18n.Localizer, ctx context.Context, up *upstream, email string) string {
	if available, known := up.MailAvailable(ctx); known && !available {
		return l.T("settings.members.mailUnavailable", "email", email)
	}
	return l.T("settings.members.mailFailed", "email", email)
}

func (s *server) revokeInvitation(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/members"
	l := s.localizer(r)
	email := field(r, "email")
	if email == "" {
		email = l.T("settings.members.thatAddress")
	}

	if err := up.RevokeInvitation(r.Context(), r.PathValue("id")); err != nil {
		if statusOf(err) == http.StatusNotFound {
			s.done(w, r, up, back, flash{Tone: "info",
				Message: l.T("settings.members.invitationGone", "email", email)})
			return
		}
		s.failed(w, r, up, back, err, func(*apiError) string { return "" })
		return
	}
	s.done(w, r, up, back, flash{Tone: "success",
		Message: l.T("settings.members.invitationRevoked", "email", email)})
}

func (s *server) setRole(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/members"
	l := s.localizer(r)
	name := field(r, "name")
	if name == "" {
		name = l.T("settings.members.thatMember")
	}
	self := r.PostFormValue("self") == "1"
	granted := role(strings.ToLower(field(r, "role")))
	if !granted.valid() {
		s.done(w, r, up, back, flash{Tone: "danger", Message: l.T("settings.members.roleForInvalid", "name", name)})
		return
	}

	// A change its maker cannot undo alone asks first: the members page
	// renders the question, and its answer posts again with confirm=1.
	// A change the control plane would refuse anyway (one the reader may not
	// make, or demoting the only owner) goes straight there, to be refused
	// in its own words.
	id := r.PathValue("userId")
	if r.PostFormValue("confirm") != "1" {
		if org, err := up.Org(r.Context()); err == nil {
			owners := 0
			for _, m := range org.Members {
				if m.Role == roleOwner {
					owners++
				}
			}
			for _, m := range org.Members {
				allowed := canGrant(org.Role, granted) && canActOn(org.Role, m.Role)
				doomed := m.Role == roleOwner && granted != roleOwner && owners == 1
				if m.ID == id && allowed && !doomed && needsConfirm(self, m.Role, granted) {
					up.relay(w)
					q := url.Values{"role-confirm": {id}, "role": {string(granted)}}
					http.Redirect(w, r, back+"?"+q.Encode()+"#confirm-role-"+id, http.StatusSeeOther)
					return
				}
			}
		}
	}

	if err := up.SetRole(r.Context(), id, granted); err != nil {
		s.failed(w, r, up, back, err, func(e *apiError) string {
			switch e.Status {
			case http.StatusConflict:
				return l.T("settings.members.lastOwner")
			case http.StatusNotFound:
				return l.T("settings.members.noLongerMember", "name", name)
			case http.StatusForbidden:
				return l.T("settings.members.changeHigherRole", "name", name)
			}
			return ""
		})
		return
	}

	// The confirmation repeats the button's verb, "Change role".
	key := i18n.Key("settings.members.roleChangedOther")
	if self {
		key = "settings.members.roleChangedSelf"
	}
	message := l.T(key, "name", name, "role", i18n.Formatted(granted.Label(l)))
	s.done(w, r, up, back, flash{Tone: "success", Message: message})
}

func (s *server) removeMember(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/members"
	l := s.localizer(r)
	name := field(r, "name")
	if name == "" {
		name = l.T("settings.members.thatMember")
	}

	revoked, err := up.RemoveMember(r.Context(), r.PathValue("userId"))
	if err != nil {
		s.failed(w, r, up, back, err, func(e *apiError) string {
			switch e.Status {
			case http.StatusConflict:
				return l.T("settings.members.lastOwner")
			case http.StatusNotFound:
				return l.T("settings.members.noLongerMember", "name", name)
			case http.StatusForbidden:
				return l.T("settings.members.removeHigherRole", "name", name)
			}
			return ""
		})
		return
	}

	message := l.T("settings.members.removed", "name", name, "keys", revoked)
	s.done(w, r, up, back, flash{Tone: "success", Message: message})
}

/* --------------------------------------------------------- organisation -- */

// switchOrg changes which organisation the session acts on. It is the one
// settings form that is not enhanced in place: everything on the page belongs
// to the organisation, so the whole page is what should change.
func (s *server) switchOrg(w http.ResponseWriter, r *http.Request, up *upstream) {
	back := safePath(r.PostFormValue("returnTo"), "/settings")
	if !strings.HasPrefix(back, "/settings") {
		back = "/settings"
	}
	// A confirmation in the query string belongs to the organisation being
	// left; it must not reappear in the next one.
	back, _, _ = strings.Cut(back, "?")

	orgID := field(r, "orgId")
	if err := up.SwitchOrg(r.Context(), orgID); err != nil {
		s.failed(w, r, up, back, err, func(e *apiError) string {
			if e.Status == http.StatusForbidden {
				return s.localizer(r).T("settings.organisation.switchForbidden")
			}
			return ""
		})
		return
	}

	// The picker posts only an id; the session says what it is called.
	l := s.localizer(r)
	message := l.T("settings.organisation.switched")
	if session, err := up.Session(r.Context()); err == nil && session.Org != nil && session.Org.ID == orgID {
		message = l.T("settings.organisation.switchedTo", "name", session.Org.Name)
	}
	s.done(w, r, up, back, flash{Tone: "info", Message: message})
}

// settingsLeftover answers a GET for a URL that only takes a form, which is
// what a reload of a corrected form or an old bookmark produces: the section
// it belongs to, rather than a dead end.
func (s *server) settingsLeftover(w http.ResponseWriter, r *http.Request) {
	section := r.PathValue("section")
	for _, sec := range settingsSections {
		if sec.ID != "overview" && strings.TrimPrefix(sec.Href, "/settings/") == section {
			http.Redirect(w, r, sec.Href, http.StatusSeeOther)
			return
		}
	}
	s.notFound(w, r)
}

// endOtherSessions signs the reader out everywhere but here.
func (s *server) endOtherSessions(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/security"
	l := s.localizer(r)
	ended, err := up.EndOtherSessions(r.Context())
	if err != nil {
		s.failed(w, r, up, back, err, func(*apiError) string { return "" })
		return
	}
	s.done(w, r, up, back, flash{Tone: "success", Message: l.T("settings.security.ended", "count", ended)})
}

// renameOrg renames the active organisation.
func (s *server) renameOrg(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/organisation"
	l := s.localizer(r)
	name := field(r, "name")
	problem := ""
	switch {
	case name == "":
		problem = l.T("settings.organisation.nameMissing")
	case utf8.RuneCountInString(name) > maxOrgName:
		problem = l.T("settings.organisation.nameLong", "max", maxOrgName)
	case hasControl(name):
		problem = l.T("settings.organisation.nameControl")
	}
	if problem != "" {
		if sr, ok := s.rerender(w, r, "organisation", back); ok {
			s.renderOrganisation(w, r, sr, orgForm{Name: name, NameError: problem}, http.StatusUnprocessableEntity)
		}
		return
	}
	if err := up.RenameOrg(r.Context(), name); err != nil {
		s.failed(w, r, up, back, err, func(*apiError) string { return "" })
		return
	}
	s.done(w, r, up, back, flash{Tone: "success", Message: l.T("settings.organisation.renamed", "name", name)})
}

// leaveOrg takes the reader out of the active organisation, then shows them
// the overview of what they still belong to.
func (s *server) leaveOrg(w http.ResponseWriter, r *http.Request, up *upstream) {
	l := s.localizer(r)
	name := field(r, "org")
	revoked, err := up.LeaveOrg(r.Context())
	if err != nil {
		s.failed(w, r, up, "/settings/organisation", err, func(e *apiError) string {
			if e.Status == http.StatusConflict {
				return l.T("settings.organisation.soleOwner")
			}
			return ""
		})
		return
	}
	s.done(w, r, up, "/settings", flash{Tone: "success", Message: l.T("settings.organisation.left", "name", name, "keys", revoked)})
}

// deleteOrg deletes the active organisation once its name has been typed.
func (s *server) deleteOrg(w http.ResponseWriter, r *http.Request, up *upstream) {
	const back = "/settings/organisation"
	l := s.localizer(r)
	confirm := field(r, "confirm")
	name := field(r, "org")
	if confirm == "" || confirm != name {
		if sr, ok := s.rerender(w, r, "organisation", back); ok {
			current := name
			if sr.shell.Org != nil {
				current = sr.shell.Org.Name
			}
			s.renderOrganisation(w, r, sr, orgForm{Confirm: confirm,
				ConfirmError: l.T("settings.organisation.confirmMismatch", "name", current)}, http.StatusUnprocessableEntity)
		}
		return
	}
	_, keys, err := up.DeleteOrg(r.Context(), confirm)
	if err != nil {
		s.failed(w, r, up, back, err, func(e *apiError) string {
			if e.Status == http.StatusBadRequest {
				// The name changed since the page was loaded.
				return l.T("settings.organisation.confirmStale")
			}
			return ""
		})
		return
	}
	s.done(w, r, up, "/settings", flash{Tone: "success", Message: l.T("settings.organisation.deleted", "name", name, "keys", keys)})
}
