package authsvc

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/store"
)

// Account, organisation and API-key management.
//
// Every handler resolves the caller's role in the target organisation before
// acting, and the resolution lives in scoped() rather than in the handlers,
// so a new route cannot accidentally ship without one.

// orgScope is the organisation a request acts on and the role the caller
// holds in it. The org comes from the session, never from the request body —
// otherwise any caller could act on any organisation by changing a field.
type orgScope struct {
	orgID string
	role  store.Role
}

// authed refuses an unauthenticated caller, and names the caller on the
// request's events.
func (s *Service) authed(h func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		if user == nil {
			httpx.WriteError(w, http.StatusUnauthorized, "Sign in to continue.", "unauthorized")
			return
		}
		logx.Identify(r.Context(), logx.IDs{UserID: user.ID})
		h(w, r)
	})
}

// scoped refuses a caller who is not a member of the active organisation at
// the required level.
func (s *Service) scoped(minimum store.Role, h func(http.ResponseWriter, *http.Request, orgScope)) http.Handler {
	return s.authed(func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)

		orgID := ""
		if current := sessionOf(r); current != nil {
			orgID = current.data.OrgID
		}
		if orgID == "" {
			memberships, err := s.store.OrgsForUser(r.Context(), user.ID)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if len(memberships) == 0 {
				httpx.WriteError(w, http.StatusForbidden, "No organisation selected.", "forbidden")
				return
			}
			orgID = memberships[0].ID
		}
		logx.Identify(r.Context(), logx.IDs{OrgID: orgID})

		role, err := s.store.RoleOf(r.Context(), orgID, user.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !role.AtLeast(minimum) {
			httpx.WriteError(w, http.StatusForbidden, "Requires "+string(minimum)+" access.", "forbidden")
			return
		}
		h(w, r, orgScope{orgID: orgID, role: role})
	})
}

/* ------------------------------------------------------------- profile -- */

func (s *Service) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   *string `json:"name"`
		Locale *string `json:"locale"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	if body.Name != nil {
		trimmed := *body.Name
		if len(trimmed) > 120 {
			trimmed = trimmed[:120]
		}
		body.Name = &trimmed
	}
	if body.Locale != nil {
		locale := strings.ToLower(strings.TrimSpace(*body.Locale))
		if locale != "" && !i18n.KnownLocale(locale) {
			httpx.WriteError(w, http.StatusBadRequest, "Unknown locale.", "bad_request")
			return
		}
		body.Locale = &locale
	}

	user, err := s.store.UpdateUser(r.Context(), userOf(r).ID, body.Name, nil, body.Locale)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Which settings changed, never their values.
	if body.Name != nil {
		logx.Event(r.Context(), "audit.settings.changed", slog.String("settings", "name"))
	}
	if body.Locale != nil {
		logx.Event(r.Context(), "audit.settings.changed", slog.String("settings", "locale"))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"user": map[string]string{"id": user.ID, "email": user.Email, "name": user.Name, "locale": user.Locale},
	})
}

func (s *Service) handleListIdentities(w http.ResponseWriter, r *http.Request) {
	identities, err := s.store.IdentitiesForUser(r.Context(), userOf(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"identities": identities})
}

func (s *Service) handleUnlinkIdentity(w http.ResponseWriter, r *http.Request) {
	user := userOf(r)
	id := r.PathValue("id")

	switch err := s.store.UnlinkIdentity(r.Context(), user.ID, id); {
	case errors.Is(err, store.ErrLastIdentity):
		httpx.WriteError(w, http.StatusConflict, "Cannot unlink your only sign-in method.", "conflict")
		return
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "No such identity.", "not_found")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}

	if err := s.store.RecordAudit(r.Context(), store.AuditEvent{
		ActorID: user.ID, Action: "identity.unlinked", Target: id, IP: httpx.ClientIP(r),
	}); err != nil {
		s.logger.Error("could not record audit event", "action", "identity.unlinked", "error", err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

/* ------------------------------------------------------------- sessions -- */

// handleSessions reports how many other sessions the caller's account has:
// other browsers, devices and the desktop app.
func (s *Service) handleSessions(w http.ResponseWriter, r *http.Request) {
	others, err := s.store.OtherSessions(r.Context(), userOf(r).ID, sessionOf(r).id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"others": others})
}

// handleEndOtherSessions signs the account out everywhere but here. The
// sessions are deleted, so the next request that presents one is signed
// out; the data plane's verdict cache can accept one for up to 30 seconds.
func (s *Service) handleEndOtherSessions(w http.ResponseWriter, r *http.Request) {
	user := userOf(r)
	ended, err := s.store.EndOtherSessions(r.Context(), user.ID, sessionOf(r).id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.RecordAudit(r.Context(), store.AuditEvent{
		ActorID: user.ID, Action: "auth.sessions_ended", Metadata: fmt.Sprintf(`{"ended":%d}`, ended), IP: httpx.ClientIP(r),
	}); err != nil {
		s.logger.Error("could not record audit event", "action", "auth.sessions_ended", "error", err)
	}
	logx.Event(r.Context(), "audit.sessions.ended", slog.Int64("ended", ended))
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ended": ended})
}

/* --------------------------------------------------------- organisation -- */

// maxOrgName bounds an organisation's name, in characters.
const maxOrgName = 80

// orgName checks a proposed organisation name: trimmed, not empty, no
// control characters, at most maxOrgName characters. It returns the name to
// store, or the reason it cannot be one.
func orgName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", "Give the organisation a name."
	case utf8.RuneCountInString(name) > maxOrgName:
		return "", fmt.Sprintf("Use at most %d characters.", maxOrgName)
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", "Use letters, numbers, spaces and punctuation only."
	}
	return name, ""
}

// handleRenameOrg renames the active organisation. Owners and admins may.
func (s *Service) handleRenameOrg(w http.ResponseWriter, r *http.Request, scope orgScope) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	name, problem := orgName(body.Name)
	if problem != "" {
		httpx.WriteError(w, http.StatusBadRequest, problem, "invalid_request")
		return
	}
	previous, err := s.store.RenameOrg(r.Context(), scope.orgID, userOf(r).ID, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if previous != name {
		logx.Event(r.Context(), "audit.org.renamed", slog.String("org_id", scope.orgID))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true, "name": name})
}

// handleLeaveOrg takes the caller out of the active organisation. Leaving
// is removing yourself: the API keys you created for it are revoked, and
// the last owner cannot leave. The session moves to another organisation
// the caller belongs to, if any.
func (s *Service) handleLeaveOrg(w http.ResponseWriter, r *http.Request, scope orgScope) {
	user := userOf(r)
	revokedKeys, err := s.store.RemoveMember(r.Context(), scope.orgID, user.ID)
	switch {
	case errors.Is(err, store.ErrLastOwner):
		httpx.WriteError(w, http.StatusConflict,
			"You are this organisation's only owner. Make someone else an owner before you leave, or delete the organisation.", "conflict")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	s.audit(r, scope.orgID, "member.left", user.ID, map[string]any{"role": scope.role, "revokedKeys": revokedKeys})
	logx.Event(r.Context(), "audit.member.left", slog.String("org_id", scope.orgID), slog.Int64("revoked_keys", revokedKeys))

	next, err := s.moveSession(w, r, scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true, "revokedKeys": revokedKeys, "orgId": next})
}

// handleDeleteOrg deletes the active organisation, with its members, API
// keys, invitations and audit log. Only an owner may, and only by sending
// the organisation's name: deleting removes other people's access and
// cannot be undone.
func (s *Service) handleDeleteOrg(w http.ResponseWriter, r *http.Request, scope orgScope) {
	var body struct {
		ConfirmName string `json:"confirmName"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	org, err := s.store.OrgByID(r.Context(), scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(body.ConfirmName) != org.Name {
		httpx.WriteError(w, http.StatusBadRequest,
			"Type the organisation's name exactly as it is shown to delete it.", "confirmation_required")
		return
	}
	members, liveKeys, err := s.store.DeleteOrg(r.Context(), scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The organisation's own audit log went with it; this is the record.
	logx.Event(r.Context(), "audit.org.deleted", slog.String("org_id", scope.orgID),
		slog.Int64("members", members), slog.Int64("live_keys", liveKeys))

	next, err := s.moveSession(w, r, scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true, "members": members, "revokedKeys": liveKeys, "orgId": next})
}

// moveSession points the caller's session away from an organisation it can
// no longer act on, to the first one it still belongs to, or to none.
func (s *Service) moveSession(w http.ResponseWriter, r *http.Request, from string) (string, error) {
	current := sessionOf(r)
	if current == nil || current.data.OrgID != from {
		return "", nil
	}
	memberships, err := s.store.OrgsForUser(r.Context(), userOf(r).ID)
	if err != nil {
		return "", err
	}
	current.data.OrgID = ""
	if len(memberships) > 0 {
		current.data.OrgID = memberships[0].ID
	}
	return current.data.OrgID, s.sessions.save(w, r, current)
}

func (s *Service) handleOrg(w http.ResponseWriter, r *http.Request, scope orgScope) {
	org, err := s.store.OrgByID(r.Context(), scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	members, err := s.store.Members(r.Context(), scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"org":     org,
		"role":    scope.role,
		"members": members,
	})
}

/* ------------------------------------------------------------- api keys -- */

// maxKeyDays is the longest lifetime a key can be given in days; a longer
// one is a key that never expires, and is asked for as such.
const maxKeyDays = 365

// maxOverlapHours is the longest a rotated key keeps working beside its
// replacement: enough to deploy the new key everywhere, not so long that a
// rotation leaves a second key in service.
const maxOverlapHours = 7 * 24

func (s *Service) handleListKeys(w http.ResponseWriter, r *http.Request, scope orgScope) {
	keys, err := s.store.ListAPIKeys(r.Context(), scope.orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Service) handleCreateKey(w http.ResponseWriter, r *http.Request, scope orgScope) {
	var body struct {
		Label string `json:"label"`
		// ExpiresInDays is the key's lifetime. Absent or null makes a key
		// that works until it is revoked.
		ExpiresInDays *int `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}

	label := strings.TrimSpace(body.Label)
	if len(label) > 64 {
		label = label[:64]
	}
	if label == "" {
		label = "Untitled key"
	}

	var expiresAt *int64
	if days := body.ExpiresInDays; days != nil {
		if *days < 1 || *days > maxKeyDays {
			httpx.WriteError(w, http.StatusBadRequest,
				"expiresInDays must be a whole number of days from 1 to 365, or null for a key that never expires.", "bad_request")
			return
		}
		at := time.Now().Add(time.Duration(*days) * 24 * time.Hour).UnixMilli()
		expiresAt = &at
	}

	created, err := s.store.CreateAPIKey(r.Context(), scope.orgID, userOf(r).ID, label, expiresAt)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	logx.Event(r.Context(), "audit.api_key.created",
		slog.String("key_id", created.ID), slog.String("label", created.Label))

	// The only time the secret is ever returned. It is not stored in a
	// recoverable form, so it cannot be shown again, and nothing on the way
	// may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, r, http.StatusCreated, map[string]any{
		"id":        created.ID,
		"label":     created.Label,
		"prefix":    created.Prefix,
		"secret":    created.Secret,
		"expiresAt": created.ExpiresAt,
		"warning":   "Copy this key now. It will not be shown again.",
	})
}

// handleRotateKey replaces a key with a new one under the same label. The old
// key keeps working for overlapHours (24 unless asked otherwise) so the new
// one can be deployed first; 0 revokes it at once.
func (s *Service) handleRotateKey(w http.ResponseWriter, r *http.Request, scope orgScope) {
	var body struct {
		OverlapHours *int `json:"overlapHours"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	hours := 24
	if body.OverlapHours != nil {
		hours = *body.OverlapHours
	}
	if hours < 0 || hours > maxOverlapHours {
		httpx.WriteError(w, http.StatusBadRequest,
			"overlapHours must be a whole number of hours from 0 to 168.", "bad_request")
		return
	}

	keyID := r.PathValue("id")
	rotated, err := s.store.RotateAPIKey(r.Context(), scope.orgID, userOf(r).ID, keyID, time.Duration(hours)*time.Hour)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "No such key.", "not_found")
		return
	case errors.Is(err, store.ErrKeyRetired):
		httpx.WriteError(w, http.StatusConflict,
			"This key is revoked or expired, so it cannot be rotated. Create a new key instead.", "conflict")
		return
	case errors.Is(err, store.ErrKeyReplaced):
		httpx.WriteError(w, http.StatusConflict,
			"This key has already been replaced. Rotate its replacement instead.", "conflict")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	logx.Event(r.Context(), "audit.api_key.rotated",
		slog.String("key_id", keyID), slog.String("replacement_id", rotated.ID))

	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, r, http.StatusCreated, map[string]any{
		"id":                 rotated.ID,
		"label":              rotated.Label,
		"prefix":             rotated.Prefix,
		"secret":             rotated.Secret,
		"expiresAt":          rotated.ExpiresAt,
		"replaces":           rotated.Replaces,
		"replacedKeyStopsAt": rotated.ReplacedStopsAt,
		"warning":            "Copy this key now. It will not be shown again.",
	})
}

func (s *Service) handleRevokeKey(w http.ResponseWriter, r *http.Request, scope orgScope) {
	keyID := r.PathValue("id")
	revoked, err := s.store.RevokeAPIKey(r.Context(), scope.orgID, userOf(r).ID, keyID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !revoked {
		httpx.WriteError(w, http.StatusNotFound, "No such key.", "not_found")
		return
	}
	logx.Event(r.Context(), "audit.api_key.revoked", slog.String("key_id", keyID))
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

/* ---------------------------------------------------------------- audit -- */

// maxAuditOffset bounds how deep a page may start, so one request cannot
// make the database skip an unbounded number of rows.
const maxAuditOffset = 1_000_000

// handleAudit answers one page of the organisation's audit log, newest
// first. Every parameter is optional:
//
//   - limit (1-500, default 50) and offset page through the matches;
//   - category (keys, members, invitations or organisation), actor (a user
//     id) and target (an id, or part of a label) narrow them;
//   - since and until bound when, in epoch milliseconds: since inclusive,
//     until exclusive;
//   - actors=1 adds everyone who appears in the log, for a filter to offer.
//
// total counts every match, and earliest is when the oldest kept event was
// recorded, whatever the filters.
func (s *Service) handleAudit(w http.ResponseWriter, r *http.Request, scope orgScope) {
	query := r.URL.Query()
	number := func(name string, low, high int64) (int64, bool) {
		raw := query.Get(name)
		if raw == "" {
			return 0, true
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < low || n > high {
			httpx.WriteError(w, http.StatusBadRequest,
				fmt.Sprintf("%s must be a whole number from %d to %d.", name, low, high), "invalid_request")
			return 0, false
		}
		return n, true
	}
	limit, ok := number("limit", 1, store.MaxAuditPage)
	if !ok {
		return
	}
	offset, ok := number("offset", 0, maxAuditOffset)
	if !ok {
		return
	}
	since, ok := number("since", 0, math.MaxInt64)
	if !ok {
		return
	}
	until, ok := number("until", 0, math.MaxInt64)
	if !ok {
		return
	}
	filter := store.AuditFilter{
		Category: query.Get("category"),
		ActorID:  query.Get("actor"),
		Target:   query.Get("target"),
		Since:    since,
		Until:    until,
	}
	if filter.Category != "" && !slices.Contains(store.AuditCategories, filter.Category) {
		httpx.WriteError(w, http.StatusBadRequest,
			"category must be one of "+strings.Join(store.AuditCategories, ", ")+".", "invalid_request")
		return
	}
	if len(filter.Target) > 200 {
		httpx.WriteError(w, http.StatusBadRequest, "target must be at most 200 characters.", "invalid_request")
		return
	}

	page, err := s.store.QueryAudit(r.Context(), scope.orgID, filter, int(offset), int(limit))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := map[string]any{"events": page.Events, "total": page.Total, "earliest": nil}
	if page.Earliest > 0 {
		out["earliest"] = page.Earliest
	}
	if query.Get("actors") == "1" {
		actors, err := s.store.AuditActors(r.Context(), scope.orgID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out["actors"] = actors
	}
	httpx.WriteJSON(w, r, http.StatusOK, out)
}
