package authsvc

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/store"
)

// Member and invitation management: the half of the admin console that
// changes who belongs to an organisation and what they may do.
//
// Three rules carry almost all of the risk here, so they live in named
// helpers rather than inline in each handler:
//
//   - Nobody may hand out, or take, authority they do not already hold
//     (canGrant, canActOn). Without this an admin promotes themselves to
//     owner, or demotes the owner and takes the tenant.
//   - An organisation must never lose its last owner (enforced in the store,
//     inside the same transaction as the change). This is the classic way a
//     tenant locks itself out for good.
//   - An invitation may only be accepted by the address it was sent to.
//     Otherwise a forwarded link is a way into somebody else's organisation.

// invitationTTL is how long an invitation link stays usable. Long enough to
// survive a weekend and a forgotten inbox, short enough that a link found in
// an old mailbox is dead.
const invitationTTL = 7 * 24 * time.Hour

/* --------------------------------------------------------- authority -- */

// canGrant reports whether an actor may hand out a role. Nobody may grant
// above their own level, which is what stops an admin promoting anyone —
// themselves included — to owner.
func canGrant(actor, granted store.Role) bool {
	return granted.Valid() && actor.AtLeast(granted)
}

// canActOn reports whether an actor may change or remove a member. Acting on
// somebody stronger than you is privilege escalation by another route: an
// admin who can demote the owner has taken the organisation.
func canActOn(actor, target store.Role) bool {
	return !target.Outranks(actor)
}

/* ------------------------------------------------------- invitations -- */

func (s *Service) handleInviteMember(w http.ResponseWriter, r *http.Request, scope orgScope) {
	var body struct {
		Email  string `json:"email"`
		Role   string `json:"role"`
		Locale string `json:"locale"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !emailPattern.MatchString(email) {
		httpx.WriteError(w, http.StatusBadRequest, "Enter a valid email address.", "bad_request")
		return
	}

	role := store.Role(strings.ToLower(strings.TrimSpace(body.Role)))
	if role == "" {
		role = store.RoleMember
	}
	if !role.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "Unknown role.", "bad_request")
		return
	}
	if !canGrant(scope.role, role) {
		httpx.WriteError(w, http.StatusForbidden,
			"You cannot invite someone at a higher role than your own.", "forbidden")
		return
	}

	// The token is published by mail and nowhere else — returning it in this
	// response would put a live credential in the inviter's browser history,
	// logs and any proxy in between. So with no transport an invitation
	// cannot be delivered, and none is created: a pending row that nobody was
	// told about would show an admin an invitation that never went out. This
	// is the same 503 magic-link sign-in gives, for the same reason.
	if !s.mail.available() {
		httpx.WriteError(w, http.StatusServiceUnavailable,
			"Invitations cannot be sent: this server has no mail transport configured.",
			"mail_unavailable")
		return
	}

	actor := userOf(r)
	org, err := s.store.OrgByID(r.Context(), scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Sent before it is recorded, so the pending list only shows invitations
	// that went out. Recording first gets a failed send wrong: a re-invite
	// has already rotated the token, so the invitee's earlier link is dead
	// while the list claims a fresh invitation was delivered. Sending first
	// leaves nothing behind, and any earlier invitation keeps working. The
	// reverse failure (mailed, then not recorded) reaches the admin as an
	// error, so they invite again.
	secret, hash := store.NewInvitationToken()
	locale := s.mailLocale(body.Locale)
	recipient, err := s.store.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	if recipient != nil && recipient.Locale != "" {
		locale = s.mailLocale(recipient.Locale)
	}
	link := s.localizedPublicPath(locale.ID, "/invite") + "?token=" + url.QueryEscape(secret)
	now := time.Now()
	if _, err := s.mail.send(invitationMessage(
		locale.Localizer(),
		email,
		org.Name,
		actor.Email,
		link,
		now,
		now.Add(invitationTTL),
	)); err != nil {
		s.logger.Error("could not send an invitation", "org", scope.orgID, "error", err, "request_id", httpx.IDOf(r))
		httpx.WriteError(w, http.StatusServiceUnavailable,
			"The invitation could not be sent. Try again.", "mail_unavailable")
		return
	}

	invitation, err := s.store.CreateInvitation(r.Context(), scope.orgID, email, role, actor.ID, hash, invitationTTL.Milliseconds())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.audit(r, scope.orgID, "member.invited", invitation.ID, map[string]any{"email": email, "role": role})

	httpx.WriteJSON(w, r, http.StatusCreated, map[string]any{"invitation": invitation})
}

func (s *Service) handleListInvitations(w http.ResponseWriter, r *http.Request, scope orgScope) {
	invitations, err := s.store.PendingInvitations(r.Context(), scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"invitations": invitations})
}

func (s *Service) handleRevokeInvitation(w http.ResponseWriter, r *http.Request, scope orgScope) {
	id := r.PathValue("id")

	revoked, err := s.store.RevokeInvitation(r.Context(), scope.orgID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !revoked {
		httpx.WriteError(w, http.StatusNotFound, "No such invitation.", "not_found")
		return
	}

	s.audit(r, scope.orgID, "invitation.revoked", id, nil)
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

// handleAcceptInvitation is the one route here that is not org-scoped: the
// person accepting is, by definition, not yet a member of the organisation
// they are joining. It takes the org from the invitation instead, and is
// gated only on being signed in.
func (s *Service) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	user := userOf(r)

	invitation, err := s.store.AcceptInvitation(r.Context(), r.PathValue("token"), user.ID, user.Email)
	switch {
	case errors.Is(err, store.ErrInvitationEmailMismatch):
		httpx.WriteError(w, http.StatusForbidden,
			"This invitation was sent to a different address. Sign in as that address to accept it.",
			"forbidden")
		return
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound,
			"This invitation has expired or has already been used.", "not_found")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}

	org, err := s.store.OrgByID(r.Context(), invitation.OrgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.audit(r, invitation.OrgID, "invitation.accepted", invitation.ID, map[string]any{"role": invitation.Role})

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"ok":   true,
		"org":  org,
		"role": invitation.Role,
	})
}

/* ----------------------------------------------------------- members -- */

func (s *Service) handleSetMemberRole(w http.ResponseWriter, r *http.Request, scope orgScope) {
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}

	role := store.Role(strings.ToLower(strings.TrimSpace(body.Role)))
	if !role.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "Unknown role.", "bad_request")
		return
	}
	if !canGrant(scope.role, role) {
		httpx.WriteError(w, http.StatusForbidden,
			"You cannot grant a role above your own.", "forbidden")
		return
	}

	target := r.PathValue("userId")
	targetRole, ok := s.resolveTarget(w, r, scope, target)
	if !ok {
		return
	}

	switch err := s.store.SetMemberRole(r.Context(), scope.orgID, target, role); {
	case errors.Is(err, store.ErrLastOwner):
		httpx.WriteError(w, http.StatusConflict,
			"This organisation would be left without an owner. Promote another owner first.", "conflict")
		return
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "No such member.", "not_found")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}

	s.audit(r, scope.orgID, "member.role_changed", target, map[string]any{"from": targetRole, "to": role})

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"ok":     true,
		"member": map[string]any{"id": target, "role": role},
	})
}

func (s *Service) handleRemoveMember(w http.ResponseWriter, r *http.Request, scope orgScope) {
	target := r.PathValue("userId")
	targetRole, ok := s.resolveTarget(w, r, scope, target)
	if !ok {
		return
	}

	revokedKeys, err := s.store.RemoveMember(r.Context(), scope.orgID, target)
	switch {
	case errors.Is(err, store.ErrLastOwner):
		httpx.WriteError(w, http.StatusConflict,
			"This organisation would be left without an owner. Promote another owner first.", "conflict")
		return
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "No such member.", "not_found")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}

	s.audit(r, scope.orgID, "member.removed", target, map[string]any{"role": targetRole, "revokedKeys": revokedKeys})

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true, "revokedKeys": revokedKeys})
}

// resolveTarget looks up the member a request acts on and refuses the two
// cases no handler should have to remember: a stranger, and somebody who
// outranks the caller.
func (s *Service) resolveTarget(w http.ResponseWriter, r *http.Request, scope orgScope, userID string) (store.Role, bool) {
	if userID == "" {
		httpx.WriteError(w, http.StatusNotFound, "No such member.", "not_found")
		return "", false
	}

	role, err := s.store.RoleOf(r.Context(), scope.orgID, userID)
	if err != nil {
		s.fail(w, r, err)
		return "", false
	}
	if role == "" {
		httpx.WriteError(w, http.StatusNotFound, "No such member.", "not_found")
		return "", false
	}
	if !canActOn(scope.role, role) {
		httpx.WriteError(w, http.StatusForbidden,
			"You cannot change a member who holds a higher role than your own.", "forbidden")
		return "", false
	}
	return role, true
}

/* ------------------------------------------------------------ helpers -- */

// audit records one administrative action against the acting user.
func (s *Service) audit(r *http.Request, orgID, action, target string, fields map[string]any) {
	actor := ""
	if user := userOf(r); user != nil {
		actor = user.ID
	}

	metadata := ""
	if len(fields) > 0 {
		if encoded, err := json.Marshal(fields); err == nil {
			metadata = string(encoded)
		}
	}

	if err := s.store.RecordAudit(r.Context(), store.AuditEvent{
		OrgID:    orgID,
		ActorID:  actor,
		Action:   action,
		Target:   target,
		Metadata: metadata,
		IP:       httpx.ClientIP(r),
	}); err != nil {
		// An unrecorded action is worth a log line, but refusing a change
		// that already happened would be worse than a gap in the trail.
		s.logger.Error("could not record audit event", "action", action, "error", err)
	}
}
