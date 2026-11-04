package authsvc

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/store"
)

// member signs a user in, installs them in an organisation at a role, and
// makes that organisation the active one for their session — which is where
// every /api/v1/org route reads it from.
func (h *harness) member(t *testing.T, email, orgID string, role store.Role) (userID, cookie string) {
	t.Helper()

	cookie = h.signIn(t, email)
	user, err := h.store.UserByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("find %s: %v", email, err)
	}
	if err := h.store.AddMember(t.Context(), orgID, user.ID, role); err != nil {
		t.Fatalf("add %s as %s: %v", email, role, err)
	}
	if res := h.call(t, http.MethodPost, "/auth/org", `{"orgId":"`+orgID+`"}`,
		map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("switch org: status %d, body %s", res.Code, res.Body)
	}
	return user.ID, cookie
}

// founder signs the first user in and returns the organisation they own.
func (h *harness) founder(t *testing.T, email string) (userID, orgID, cookie string) {
	t.Helper()

	cookie = h.signIn(t, email)
	user, err := h.store.UserByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("find %s: %v", email, err)
	}
	orgs, err := h.store.OrgsForUser(t.Context(), user.ID)
	if err != nil || len(orgs) == 0 {
		t.Fatalf("orgs = %v, err = %v", orgs, err)
	}
	return user.ID, orgs[0].ID, cookie
}

// inviteToken pulls an organisation's live invitation token out of the
// outbox, which is the only place it is ever published.
//
// Candidates are matched against the hash the database is holding, which both
// proves the mailed token is the one that was stored and picks the right
// message when a re-invite wrote two in the same millisecond.
func (h *harness) inviteToken(t *testing.T, orgID string) string {
	t.Helper()

	rows, err := h.store.DB().Query(
		`SELECT hash FROM invitation WHERE org_id = ? AND accepted_at IS NULL AND revoked_at IS NULL`, orgID)
	if err != nil {
		t.Fatalf("read invitations: %v", err)
	}
	defer rows.Close()

	live := map[string]bool{}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		live[hash] = true
	}
	if len(live) == 0 {
		t.Fatal("no live invitation to find a token for")
	}

	for _, link := range h.outboxLinks(t) {
		parsed, err := url.Parse(link)
		if err != nil {
			t.Fatalf("parse %q: %v", link, err)
		}
		token := parsed.Query().Get("token")
		if token == "" || !live[store.HashSecret(token)] {
			continue
		}
		if parsed.Path != "/invite" {
			t.Errorf("invitation link path = %q, want /invite", parsed.Path)
		}
		return token
	}

	t.Fatal("no outbox message carries a live invitation token")
	return ""
}

func (h *harness) auditActions(t *testing.T, orgID string) []string {
	t.Helper()

	events, err := h.store.RecentAudit(t.Context(), orgID, 50)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	return actions
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

/* ------------------------------------------------- who may manage whom -- */

func TestMemberManagementRequiresAdmin(t *testing.T) {
	routes := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/v1/org/invitations", `{"email":"ada@example.com"}`},
		{http.MethodGet, "/api/v1/org/invitations", ""},
		{http.MethodDelete, "/api/v1/org/invitations/inv_whatever", ""},
		{http.MethodPatch, "/api/v1/org/members/usr_whatever", `{"role":"member"}`},
		{http.MethodDelete, "/api/v1/org/members/usr_whatever", ""},
	}

	for _, role := range []store.Role{store.RoleViewer, store.RoleMember} {
		for _, route := range routes {
			t.Run(string(role)+" "+route.method+" "+route.path, func(t *testing.T) {
				h := newHarness(t)
				_, orgID, _ := h.founder(t, "owner@example.com")
				_, cookie := h.member(t, "weak@example.com", orgID, role)

				res := h.call(t, route.method, route.path, route.body, map[string]string{"Cookie": cookie})
				if res.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403 (body %s)", res.Code, res.Body)
				}
			})
		}
	}
}

func TestNobodyGrantsAboveTheirOwnRole(t *testing.T) {
	tests := []struct {
		name   string
		actor  store.Role
		method string
		path   string
		body   string
		status int
	}{
		{
			name: "admin cannot invite an owner", actor: store.RoleAdmin,
			method: http.MethodPost, path: "/api/v1/org/invitations",
			body: `{"email":"new@example.com","role":"owner"}`, status: http.StatusForbidden,
		},
		{
			name: "admin may invite an admin", actor: store.RoleAdmin,
			method: http.MethodPost, path: "/api/v1/org/invitations",
			body: `{"email":"new@example.com","role":"admin"}`, status: http.StatusCreated,
		},
		{
			name: "owner may invite an owner", actor: store.RoleOwner,
			method: http.MethodPost, path: "/api/v1/org/invitations",
			body: `{"email":"new@example.com","role":"owner"}`, status: http.StatusCreated,
		},
		{
			name: "an unknown role is refused", actor: store.RoleOwner,
			method: http.MethodPost, path: "/api/v1/org/invitations",
			body: `{"email":"new@example.com","role":"superuser"}`, status: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, orgID, _ := h.founder(t, "owner@example.com")
			_, cookie := h.member(t, "actor@example.com", orgID, tc.actor)

			res := h.call(t, tc.method, tc.path, tc.body, map[string]string{"Cookie": cookie})
			if res.Code != tc.status {
				t.Errorf("status = %d, want %d (body %s)", res.Code, tc.status, res.Body)
			}
		})
	}
}

func TestAdminCannotEscalateThroughARoleChange(t *testing.T) {
	tests := []struct {
		name   string
		target string // "self", "owner" or "peer"
		method string
		body   string
		status int
	}{
		{name: "promoting self to owner", target: "self", method: http.MethodPatch, body: `{"role":"owner"}`, status: http.StatusForbidden},
		{name: "promoting a peer to owner", target: "peer", method: http.MethodPatch, body: `{"role":"owner"}`, status: http.StatusForbidden},
		{name: "demoting the owner", target: "owner", method: http.MethodPatch, body: `{"role":"member"}`, status: http.StatusForbidden},
		{name: "removing the owner", target: "owner", method: http.MethodDelete, status: http.StatusForbidden},
		{name: "demoting a peer admin", target: "peer", method: http.MethodPatch, body: `{"role":"member"}`, status: http.StatusOK},
		{name: "stepping down to member", target: "self", method: http.MethodPatch, body: `{"role":"member"}`, status: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ownerID, orgID, _ := h.founder(t, "owner@example.com")
			adminID, cookie := h.member(t, "admin@example.com", orgID, store.RoleAdmin)
			peerID, _ := h.member(t, "peer@example.com", orgID, store.RoleAdmin)

			target := map[string]string{"self": adminID, "owner": ownerID, "peer": peerID}[tc.target]
			res := h.call(t, tc.method, "/api/v1/org/members/"+target, tc.body, map[string]string{"Cookie": cookie})
			if res.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", res.Code, tc.status, res.Body)
			}

			// A refusal must leave the organisation exactly as it was.
			if tc.status == http.StatusForbidden {
				role, err := h.store.RoleOf(t.Context(), orgID, target)
				if err != nil {
					t.Fatalf("role: %v", err)
				}
				want := map[string]store.Role{"self": store.RoleAdmin, "owner": store.RoleOwner, "peer": store.RoleAdmin}[tc.target]
				if role != want {
					t.Errorf("role = %q, want %q unchanged", role, want)
				}
			}
		})
	}
}

func TestActingOnAStrangerIsANotFound(t *testing.T) {
	h := newHarness(t)
	_, _, cookie := h.founder(t, "owner@example.com")
	outsider, _, _ := h.founder(t, "outsider@example.com")

	for _, step := range []struct{ method, body string }{
		{http.MethodPatch, `{"role":"member"}`},
		{http.MethodDelete, ""},
	} {
		res := h.call(t, step.method, "/api/v1/org/members/"+outsider, step.body, map[string]string{"Cookie": cookie})
		if res.Code != http.StatusNotFound {
			t.Errorf("%s a non-member: status %d, want 404 (body %s)", step.method, res.Code, res.Body)
		}
	}
}

/* --------------------------------------------------------- last owner -- */

func TestOrganisationNeverLosesItsLastOwner(t *testing.T) {
	tests := []struct {
		name   string
		method string
		body   string
	}{
		{name: "demotion", method: http.MethodPatch, body: `{"role":"admin"}`},
		{name: "removal", method: http.MethodDelete},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ownerID, orgID, cookie := h.founder(t, "owner@example.com")

			refused := h.call(t, tc.method, "/api/v1/org/members/"+ownerID, tc.body, map[string]string{"Cookie": cookie})
			if refused.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (body %s)", refused.Code, refused.Body)
			}
			if role, err := h.store.RoleOf(t.Context(), orgID, ownerID); err != nil || role != store.RoleOwner {
				t.Fatalf("role = %q (err %v), want the owner untouched", role, err)
			}

			// Once a second owner exists the same call succeeds, which proves
			// the refusal is a guard on the invariant rather than a blanket
			// prohibition on owners changing.
			h.member(t, "second@example.com", orgID, store.RoleOwner)
			allowed := h.call(t, tc.method, "/api/v1/org/members/"+ownerID, tc.body, map[string]string{"Cookie": cookie})
			if allowed.Code != http.StatusOK {
				t.Fatalf("with a second owner: status %d, want 200 (body %s)", allowed.Code, allowed.Body)
			}
			if owners, err := h.store.CountOwners(t.Context(), orgID); err != nil || owners != 1 {
				t.Errorf("owners = %d (err %v), want 1", owners, err)
			}
		})
	}
}

/* -------------------------------------------------------- invitations -- */

func TestInvitationRoundTrip(t *testing.T) {
	h := newHarness(t)
	_, orgID, ownerCookie := h.founder(t, "owner@example.com")

	created := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"Ada@Example.com","role":"admin"}`, map[string]string{"Cookie": ownerCookie})
	if created.Code != http.StatusCreated {
		t.Fatalf("invite: status %d, body %s", created.Code, created.Body)
	}
	invitation, _ := decode(t, created)["invitation"].(map[string]any)
	if invitation == nil || invitation["email"] != "ada@example.com" || invitation["role"] != "admin" {
		t.Fatalf("invitation = %v, want the lower-cased address at the invited role", invitation)
	}
	// The token is a live credential; it goes out by mail and nowhere else.
	if strings.Contains(created.Body.String(), h.inviteToken(t, orgID)) {
		t.Error("the invitation response leaked the token")
	}

	listed := h.call(t, http.MethodGet, "/api/v1/org/invitations", "", map[string]string{"Cookie": ownerCookie})
	pending, _ := decode(t, listed)["invitations"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending = %v, want exactly one", pending)
	}

	token := h.inviteToken(t, orgID)
	adaCookie := h.signIn(t, "ada@example.com")
	accepted := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+token+"/accept", "",
		map[string]string{"Cookie": adaCookie})
	if accepted.Code != http.StatusOK {
		t.Fatalf("accept: status %d, body %s", accepted.Code, accepted.Body)
	}
	if role := decode(t, accepted)["role"]; role != "admin" {
		t.Errorf("role = %v, want admin", role)
	}

	ada, err := h.store.UserByEmail(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatalf("find ada: %v", err)
	}
	if role, err := h.store.RoleOf(t.Context(), orgID, ada.ID); err != nil || role != store.RoleAdmin {
		t.Errorf("membership = %q (err %v), want admin", role, err)
	}

	// Accepted invitations leave the pending list, and the token is spent.
	after := h.call(t, http.MethodGet, "/api/v1/org/invitations", "", map[string]string{"Cookie": ownerCookie})
	if remaining, _ := decode(t, after)["invitations"].([]any); len(remaining) != 0 {
		t.Errorf("pending = %v, want none after acceptance", remaining)
	}
	replay := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+token+"/accept", "",
		map[string]string{"Cookie": adaCookie})
	if replay.Code != http.StatusNotFound {
		t.Errorf("replay: status %d, want 404", replay.Code)
	}
}

func TestInvitationIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, h *harness, orgID, token string)
		as      string
		status  int
	}{
		{
			name:   "to a different address",
			as:     "mallory@example.com",
			status: http.StatusForbidden,
		},
		{
			name: "once expired",
			prepare: func(t *testing.T, h *harness, _, _ string) {
				if _, err := h.store.DB().Exec(`UPDATE invitation SET expires_at = ?`,
					time.Now().Add(-time.Second).UnixMilli()); err != nil {
					t.Fatalf("expire: %v", err)
				}
			},
			as:     "ada@example.com",
			status: http.StatusNotFound,
		},
		{
			name: "once revoked",
			prepare: func(t *testing.T, h *harness, orgID, _ string) {
				pending, err := h.store.PendingInvitations(t.Context(), orgID)
				if err != nil || len(pending) != 1 {
					t.Fatalf("pending = %v, err = %v", pending, err)
				}
				if revoked, err := h.store.RevokeInvitation(t.Context(), orgID, pending[0].ID); err != nil || !revoked {
					t.Fatalf("revoke: revoked=%v err=%v", revoked, err)
				}
			},
			as:     "ada@example.com",
			status: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, orgID, ownerCookie := h.founder(t, "owner@example.com")

			if res := h.call(t, http.MethodPost, "/api/v1/org/invitations",
				`{"email":"ada@example.com","role":"admin"}`, map[string]string{"Cookie": ownerCookie}); res.Code != http.StatusCreated {
				t.Fatalf("invite: status %d, body %s", res.Code, res.Body)
			}
			token := h.inviteToken(t, orgID)
			if tc.prepare != nil {
				tc.prepare(t, h, orgID, token)
			}

			cookie := h.signIn(t, tc.as)
			res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+token+"/accept", "",
				map[string]string{"Cookie": cookie})
			if res.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", res.Code, tc.status, res.Body)
			}

			user, err := h.store.UserByEmail(t.Context(), tc.as)
			if err != nil {
				t.Fatalf("find %s: %v", tc.as, err)
			}
			if role, err := h.store.RoleOf(t.Context(), orgID, user.ID); err != nil || role != "" {
				t.Errorf("membership = %q (err %v), want none", role, err)
			}
		})
	}
}

func TestAcceptingRequiresASession(t *testing.T) {
	h := newHarness(t)
	_, orgID, ownerCookie := h.founder(t, "owner@example.com")
	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ada@example.com"}`, map[string]string{"Cookie": ownerCookie}); res.Code != http.StatusCreated {
		t.Fatalf("invite: status %d", res.Code)
	}

	res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+h.inviteToken(t, orgID)+"/accept", "", nil)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
}

func TestReInvitingIsIdempotent(t *testing.T) {
	h := newHarness(t)
	_, orgID, cookie := h.founder(t, "owner@example.com")

	first := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ada@example.com","role":"viewer"}`, map[string]string{"Cookie": cookie})
	if first.Code != http.StatusCreated {
		t.Fatalf("first invite: status %d, body %s", first.Code, first.Body)
	}
	stale := h.inviteToken(t, orgID)

	second := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ADA@example.com","role":"admin"}`, map[string]string{"Cookie": cookie})
	if second.Code != http.StatusCreated {
		t.Fatalf("second invite: status %d, body %s", second.Code, second.Body)
	}
	fresh := h.inviteToken(t, orgID)

	firstID, _ := decode(t, first)["invitation"].(map[string]any)
	secondID, _ := decode(t, second)["invitation"].(map[string]any)
	if firstID["id"] != secondID["id"] {
		t.Errorf("re-invite created a second invitation (%v then %v)", firstID["id"], secondID["id"])
	}

	listed := h.call(t, http.MethodGet, "/api/v1/org/invitations", "", map[string]string{"Cookie": cookie})
	pending, _ := decode(t, listed)["invitations"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}

	// Rotating the token is the point of re-inviting: the superseded link
	// must be dead, or a lost mail stays usable forever.
	adaCookie := h.signIn(t, "ada@example.com")
	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+stale+"/accept", "",
		map[string]string{"Cookie": adaCookie}); res.Code != http.StatusNotFound {
		t.Errorf("superseded token: status %d, want 404", res.Code)
	}
	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+fresh+"/accept", "",
		map[string]string{"Cookie": adaCookie}); res.Code != http.StatusOK {
		t.Errorf("current token: status %d, want 200", res.Code)
	}
	ada, _ := h.store.UserByEmail(t.Context(), "ada@example.com")
	if role, _ := h.store.RoleOf(t.Context(), orgID, ada.ID); role != store.RoleAdmin {
		t.Errorf("role = %q, want the re-invite's role", role)
	}
}

func TestRevokingAnInvitation(t *testing.T) {
	h := newHarness(t)
	_, orgID, cookie := h.founder(t, "owner@example.com")

	created := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ada@example.com"}`, map[string]string{"Cookie": cookie})
	if created.Code != http.StatusCreated {
		t.Fatalf("invite: status %d", created.Code)
	}
	invitation, _ := decode(t, created)["invitation"].(map[string]any)
	id, _ := invitation["id"].(string)
	token := h.inviteToken(t, orgID)

	if res := h.call(t, http.MethodDelete, "/api/v1/org/invitations/"+id, "",
		map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", res.Code, res.Body)
	}
	// Revoking twice is a 404, not a second revocation.
	if res := h.call(t, http.MethodDelete, "/api/v1/org/invitations/"+id, "",
		map[string]string{"Cookie": cookie}); res.Code != http.StatusNotFound {
		t.Errorf("second revoke: status %d, want 404", res.Code)
	}

	adaCookie := h.signIn(t, "ada@example.com")
	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+token+"/accept", "",
		map[string]string{"Cookie": adaCookie}); res.Code != http.StatusNotFound {
		t.Errorf("revoked token: status %d, want 404", res.Code)
	}
}

func TestInvitationsAreScopedToOneOrganisation(t *testing.T) {
	h := newHarness(t)
	_, mine, mineCookie := h.founder(t, "mine@example.com")
	_, theirs, theirsCookie := h.founder(t, "theirs@example.com")

	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ada@example.com"}`, map[string]string{"Cookie": mineCookie}); res.Code != http.StatusCreated {
		t.Fatalf("invite: status %d", res.Code)
	}
	pending, err := h.store.PendingInvitations(t.Context(), mine)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, err = %v", pending, err)
	}

	// The other tenant can neither see it nor revoke it.
	listed := h.call(t, http.MethodGet, "/api/v1/org/invitations", "", map[string]string{"Cookie": theirsCookie})
	if got, _ := decode(t, listed)["invitations"].([]any); len(got) != 0 {
		t.Errorf("another tenant saw %d invitations, want 0", len(got))
	}
	if res := h.call(t, http.MethodDelete, "/api/v1/org/invitations/"+pending[0].ID, "",
		map[string]string{"Cookie": theirsCookie}); res.Code != http.StatusNotFound {
		t.Errorf("another tenant revoked it: status %d, want 404", res.Code)
	}

	// Accepting joins the inviting organisation, not the accepter's own.
	adaCookie := h.signIn(t, "ada@example.com")
	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+h.inviteToken(t, mine)+"/accept", "",
		map[string]string{"Cookie": adaCookie}); res.Code != http.StatusOK {
		t.Fatalf("accept: status %d, body %s", res.Code, res.Body)
	}
	ada, _ := h.store.UserByEmail(t.Context(), "ada@example.com")
	if role, _ := h.store.RoleOf(t.Context(), mine, ada.ID); role != store.RoleMember {
		t.Errorf("role in the inviting org = %q, want member", role)
	}
	if role, _ := h.store.RoleOf(t.Context(), theirs, ada.ID); role != "" {
		t.Errorf("role in an unrelated org = %q, want none", role)
	}
}

func TestInvitationEmailLandsInTheOutbox(t *testing.T) {
	h := newHarness(t)
	_, _, cookie := h.founder(t, "owner@example.com")

	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ada@example.com","role":"member"}`, map[string]string{"Cookie": cookie}); res.Code != http.StatusCreated {
		t.Fatalf("invite: status %d, body %s", res.Code, res.Body)
	}

	// lastLink asserts the outbox shape other tooling depends on; this adds
	// the invitation-specific part of it.
	link := h.lastLink(t)
	if !strings.HasPrefix(link, testOrigin+"/invite?token=") {
		t.Errorf("link = %q, want one on the site with a token", link)
	}
}

func TestInvitationEmailUsesRecipientPreference(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.PseudoLocales = true })

	if res := h.call(
		t,
		http.MethodPost,
		"/auth/dev",
		`{"email":"ada@example.com","locale":"ar-xb"}`,
		nil,
	); res.Code != http.StatusOK {
		t.Fatalf("create recipient: status %d, body %s", res.Code, res.Body)
	}
	_, _, cookie := h.founder(t, "owner@example.com")

	res := h.call(
		t,
		http.MethodPost,
		"/api/v1/org/invitations",
		`{"email":"ada@example.com","role":"member","locale":"en-xa"}`,
		map[string]string{"Cookie": cookie},
	)
	if res.Code != http.StatusCreated {
		t.Fatalf("invite: status %d, body %s", res.Code, res.Body)
	}
	msg := h.lastMessage(t)
	if !strings.Contains(msg.HTML, `<html lang="ar-XB" dir="rtl">`) {
		t.Errorf("email did not use the recipient preference: %s", msg.HTML)
	}
	if !strings.HasPrefix(msg.Links[0], testOrigin+"/ar-xb/invite?token=") {
		t.Errorf("link = %q, want the recipient locale", msg.Links[0])
	}
}

func TestInvitationEmailUsesInviterLocaleForANewRecipient(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.PseudoLocales = true })
	_, _, cookie := h.founder(t, "owner@example.com")

	res := h.call(
		t,
		http.MethodPost,
		"/api/v1/org/invitations",
		`{"email":"new@example.com","role":"member","locale":"ar-xb"}`,
		map[string]string{"Cookie": cookie},
	)
	if res.Code != http.StatusCreated {
		t.Fatalf("invite: status %d, body %s", res.Code, res.Body)
	}
	msg := h.lastMessage(t)
	if !strings.Contains(msg.HTML, `<html lang="ar-XB" dir="rtl">`) {
		t.Errorf("email did not use the inviter locale: %s", msg.HTML)
	}
	if !strings.HasPrefix(msg.Links[0], testOrigin+"/ar-xb/invite?token=") {
		t.Errorf("link = %q, want the inviter locale", msg.Links[0])
	}
}

func TestInvitingNeedsAValidAddress(t *testing.T) {
	h := newHarness(t)
	_, _, cookie := h.founder(t, "owner@example.com")

	for _, body := range []string{`{}`, `{"email":"   "}`, `{"email":"ada"}`, `{"email":"ada@example"}`} {
		res := h.call(t, http.MethodPost, "/api/v1/org/invitations", body, map[string]string{"Cookie": cookie})
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, res.Code)
		}
	}

	var minted int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM invitation`).Scan(&minted); err != nil {
		t.Fatalf("count: %v", err)
	}
	if minted != 0 {
		t.Errorf("%d invitations were minted for invalid addresses, want 0", minted)
	}
}

/* ------------------------------------------------------------ removal -- */

func TestRemovingAMemberKeepsTheAccountAndKillsItsOrgKeys(t *testing.T) {
	h := newHarness(t)
	_, orgID, ownerCookie := h.founder(t, "owner@example.com")
	leaverID, leaverCookie := h.member(t, "leaver@example.com", orgID, store.RoleAdmin)

	created := h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"leaver"}`, map[string]string{"Cookie": leaverCookie})
	if created.Code != http.StatusCreated {
		t.Fatalf("create key: status %d, body %s", created.Code, created.Body)
	}
	secret, _ := decode(t, created)["secret"].(string)

	removed := h.call(t, http.MethodDelete, "/api/v1/org/members/"+leaverID, "", map[string]string{"Cookie": ownerCookie})
	if removed.Code != http.StatusOK {
		t.Fatalf("remove: status %d, body %s", removed.Code, removed.Body)
	}
	if revoked := decode(t, removed)["revokedKeys"]; revoked != float64(1) {
		t.Errorf("revokedKeys = %v, want 1", revoked)
	}

	// The account survives: they keep their identity and their own workspace.
	if _, err := h.store.UserByID(t.Context(), leaverID); err != nil {
		t.Errorf("the account was deleted along with the membership: %v", err)
	}
	session := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": leaverCookie})
	if user, _ := decode(t, session)["user"].(map[string]any); user == nil {
		t.Error("removing a member invalidated their sign-in")
	}

	// Their credential for that organisation does not.
	verified := h.call(t, http.MethodPost, "/internal/verify-key", `{"key":"`+secret+`"}`,
		map[string]string{"X-Internal-Token": "internal-token-for-tests-0123456789abcdef"})
	if verified.Code != http.StatusUnauthorized {
		t.Errorf("verify after removal: status %d, want 401", verified.Code)
	}
	if res := h.call(t, http.MethodGet, "/api/v1/org", "", map[string]string{"Cookie": leaverCookie}); res.Code == http.StatusOK {
		t.Error("a removed member can still read the organisation")
	}
}

/* -------------------------------------------------------------- audit -- */

func TestMemberManagementIsAudited(t *testing.T) {
	h := newHarness(t)
	_, orgID, cookie := h.founder(t, "owner@example.com")

	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"ada@example.com","role":"member"}`, map[string]string{"Cookie": cookie}); res.Code != http.StatusCreated {
		t.Fatalf("invite: status %d", res.Code)
	}
	adaCookie := h.signIn(t, "ada@example.com")
	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations/"+h.inviteToken(t, orgID)+"/accept", "",
		map[string]string{"Cookie": adaCookie}); res.Code != http.StatusOK {
		t.Fatalf("accept: status %d, body %s", res.Code, res.Body)
	}

	ada, _ := h.store.UserByEmail(t.Context(), "ada@example.com")
	if res := h.call(t, http.MethodPatch, "/api/v1/org/members/"+ada.ID, `{"role":"admin"}`,
		map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("promote: status %d, body %s", res.Code, res.Body)
	}
	if res := h.call(t, http.MethodDelete, "/api/v1/org/members/"+ada.ID, "",
		map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("remove: status %d, body %s", res.Code, res.Body)
	}

	if res := h.call(t, http.MethodPost, "/api/v1/org/invitations",
		`{"email":"bob@example.com"}`, map[string]string{"Cookie": cookie}); res.Code != http.StatusCreated {
		t.Fatalf("second invite: status %d", res.Code)
	}
	pending, err := h.store.PendingInvitations(t.Context(), orgID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, err = %v", pending, err)
	}
	if res := h.call(t, http.MethodDelete, "/api/v1/org/invitations/"+pending[0].ID, "",
		map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("revoke: status %d", res.Code)
	}

	actions := h.auditActions(t, orgID)
	for _, want := range []string{
		"member.invited", "invitation.accepted", "member.role_changed", "member.removed", "invitation.revoked",
	} {
		if !contains(actions, want) {
			t.Errorf("no %s event; recorded %v", want, actions)
		}
	}

	// Each event names who did it and what it touched.
	events, err := h.store.RecentAudit(t.Context(), orgID, 50)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	for _, e := range events {
		if e.ActorID == "" || e.Target == "" || e.CreatedAt == 0 {
			t.Errorf("event %+v is missing an actor, target or timestamp", e)
		}
	}
}

// TestAuditAPIFiltersAndPages: GET /api/v1/audit narrows and pages on the
// server, counts every match, names people and targets as they were, and
// refuses parameters it cannot use rather than guessing.
func TestAuditAPIFiltersAndPages(t *testing.T) {
	h := newHarness(t)
	_, orgID, cookie := h.founder(t, "ada@example.com")
	as := map[string]string{"Cookie": cookie}
	// Only owners and admins read the log, whatever they ask for.
	_, member := h.member(t, "bea@example.com", orgID, store.RoleMember)
	if res := h.call(t, http.MethodGet, "/api/v1/audit?category=keys", "", map[string]string{"Cookie": member}); res.Code != http.StatusForbidden {
		t.Fatalf("a member read the audit log: %d", res.Code)
	}
	for _, label := range []string{"one", "two", "three"} {
		// Events are ordered by the millisecond; keep each clear of the last.
		time.Sleep(2 * time.Millisecond)
		if res := h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"`+label+`"}`, as); res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", label, res.Code, res.Body)
		}
	}

	type page struct {
		Events   []store.AuditEvent `json:"events"`
		Total    int                `json:"total"`
		Earliest *int64             `json:"earliest"`
		Actors   []store.AuditActor `json:"actors"`
	}
	get := func(query string) page {
		t.Helper()
		res := h.call(t, http.MethodGet, "/api/v1/audit"+query, "", as)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", query, res.Code, res.Body)
		}
		var out page
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	all := get("")
	if all.Total != 4 || len(all.Events) != 4 || all.Earliest == nil || all.Actors != nil {
		t.Errorf("unfiltered = %d of %d, earliest %v, actors %v; want all 4, an earliest time and no actors unasked", len(all.Events), all.Total, all.Earliest, all.Actors)
	}
	first := get("?category=keys&limit=2")
	rest := get("?category=keys&limit=2&offset=2")
	if first.Total != 3 || len(first.Events) != 2 || len(rest.Events) != 1 ||
		first.Events[0].TargetLabel != "three" || rest.Events[0].TargetLabel != "one" {
		t.Errorf("key pages = %+v then %+v; want three, two, then one, of 3", first.Events, rest.Events)
	}
	if e := first.Events[0]; e.ActorEmail != "ada@example.com" || e.ActorName == "" {
		t.Errorf("event names its actor as %q <%s>, want who did it", e.ActorName, e.ActorEmail)
	}
	if got := get("?target=TW"); got.Total != 1 || got.Events[0].TargetLabel != "two" {
		t.Errorf("target filter = %+v, want the key labelled two", got.Events)
	}
	if got := get("?since=" + strconv.FormatInt(first.Events[0].CreatedAt, 10)); got.Total != 1 {
		t.Errorf("since the newest event = %d events, want 1", got.Total)
	}
	if got := get("?actors=1"); len(got.Actors) != 1 || got.Actors[0].Email != "ada@example.com" {
		t.Errorf("actors = %+v, want Ada", got.Actors)
	}

	for _, bad := range []string{
		"?limit=0", "?limit=501", "?offset=-1", "?since=yesterday", "?until=1.5",
		"?category=billing", "?target=" + strings.Repeat("x", 201),
	} {
		if res := h.call(t, http.MethodGet, "/api/v1/audit"+bad, "", as); res.Code != http.StatusBadRequest {
			t.Errorf("GET /api/v1/audit%s = %d, want 400", bad, res.Code)
		}
	}
}

// TestOrganisationLifecycle: owners and admins rename, any member leaves
// (except the last owner), only an owner deletes, and only by naming it.
func TestOrganisationLifecycle(t *testing.T) {
	h := newHarness(t)
	ownerID, orgID, owner := h.founder(t, "ada@example.com")
	adminID, admin := h.member(t, "grace@example.com", orgID, store.RoleAdmin)
	_, member := h.member(t, "bea@example.com", orgID, store.RoleMember)
	as := func(cookie string) map[string]string { return map[string]string{"Cookie": cookie} }

	t.Run("rename", func(t *testing.T) {
		if res := h.call(t, http.MethodPatch, "/api/v1/org", `{"name":"Mine"}`, as(member)); res.Code != http.StatusForbidden {
			t.Errorf("a member renamed: %d", res.Code)
		}
		for _, bad := range []string{`{"name":"  "}`, `{"name":"` + strings.Repeat("x", 81) + `"}`, `{"name":"a\u0007b"}`} {
			if res := h.call(t, http.MethodPatch, "/api/v1/org", bad, as(admin)); res.Code != http.StatusBadRequest {
				t.Errorf("%s: %d, want 400", bad, res.Code)
			}
		}
		if res := h.call(t, http.MethodPatch, "/api/v1/org", `{"name":"  Analytical Engines "}`, as(admin)); res.Code != http.StatusOK {
			t.Fatalf("rename: %d %s", res.Code, res.Body)
		}
		if org, _ := h.store.OrgByID(t.Context(), orgID); org.Name != "Analytical Engines" {
			t.Errorf("name = %q", org.Name)
		}
		events, _ := h.store.RecentAudit(t.Context(), orgID, 1)
		if len(events) != 1 || events[0].Action != "org.renamed" || !strings.Contains(events[0].Metadata, `"to":"Analytical Engines"`) {
			t.Errorf("newest event = %+v, want org.renamed to Analytical Engines", events)
		}
	})

	t.Run("leave", func(t *testing.T) {
		if res := h.call(t, http.MethodPost, "/api/v1/org/leave", `{}`, as(owner)); res.Code != http.StatusConflict {
			t.Errorf("the only owner left: %d", res.Code)
		}
		key, err := h.store.CreateAPIKey(t.Context(), orgID, adminID, "grace's", nil)
		if err != nil {
			t.Fatal(err)
		}
		// Events are ordered by the millisecond; keep this one clear of the key's.
		time.Sleep(2 * time.Millisecond)
		res := h.call(t, http.MethodPost, "/api/v1/org/leave", `{}`, as(admin))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"revokedKeys":1`) {
			t.Fatalf("leave: %d %s", res.Code, res.Body)
		}
		if role, _ := h.store.RoleOf(t.Context(), orgID, adminID); role != "" {
			t.Errorf("still %s after leaving", role)
		}
		if h.verifies(t, key.Secret) {
			t.Error("the leaver's key still verifies")
		}
		if actions := h.auditActions(t, orgID); actions[0] != "member.left" {
			t.Errorf("newest event = %s, want member.left", actions[0])
		}
		// Grace's own workspace is where her session acts now.
		if res := h.call(t, http.MethodGet, "/api/v1/org", "", as(admin)); res.Code != http.StatusOK || strings.Contains(res.Body.String(), orgID) {
			t.Errorf("after leaving, the session still acts on the organisation: %d %s", res.Code, res.Body)
		}
	})

	t.Run("delete", func(t *testing.T) {
		key, err := h.store.CreateAPIKey(t.Context(), orgID, ownerID, "service", nil)
		if err != nil {
			t.Fatal(err)
		}
		if res := h.call(t, http.MethodDelete, "/api/v1/org", `{"confirmName":"Analytical Engines"}`, as(member)); res.Code != http.StatusForbidden {
			t.Errorf("a member deleted: %d", res.Code)
		}
		for _, wrong := range []string{`{}`, `{"confirmName":"analytical engines"}`, `{"confirmName":"Analytical"}`} {
			if res := h.call(t, http.MethodDelete, "/api/v1/org", wrong, as(owner)); res.Code != http.StatusBadRequest {
				t.Errorf("%s: %d, want 400", wrong, res.Code)
			}
		}
		if _, err := h.store.OrgByID(t.Context(), orgID); err != nil {
			t.Fatalf("a refused delete deleted: %v", err)
		}
		res := h.call(t, http.MethodDelete, "/api/v1/org", `{"confirmName":"Analytical Engines"}`, as(owner))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"members":2`) {
			t.Fatalf("delete: %d %s", res.Code, res.Body)
		}
		if _, err := h.store.OrgByID(t.Context(), orgID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("organisation still there: %v", err)
		}
		if h.verifies(t, key.Secret) {
			t.Error("a key of the deleted organisation still verifies")
		}
		if role, _ := h.store.RoleOf(t.Context(), orgID, adminID); role != "" {
			t.Errorf("membership outlived the organisation: %s", role)
		}
	})
}

// TestEndOtherSessions: an account signs itself out everywhere else, and
// nowhere else, and no one else.
func TestEndOtherSessions(t *testing.T) {
	h := newHarness(t)
	_, _, laptop := h.founder(t, "ada@example.com")
	phone := h.signIn(t, "ada@example.com")
	desk := h.signIn(t, "ada@example.com")
	bea := h.signIn(t, "bea@example.com")
	as := func(cookie string) map[string]string { return map[string]string{"Cookie": cookie} }
	signedIn := func(cookie string) bool {
		res := h.call(t, http.MethodGet, "/auth/session", "", as(cookie))
		return strings.Contains(res.Body.String(), `"user":{`)
	}

	res := h.call(t, http.MethodGet, "/auth/sessions", "", as(laptop))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"others":2`) {
		t.Fatalf("sessions: %d %s", res.Code, res.Body)
	}
	if res := h.call(t, http.MethodPost, "/auth/sessions/end-others", `{}`, nil); res.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", res.Code)
	}
	res = h.call(t, http.MethodPost, "/auth/sessions/end-others", `{}`, as(laptop))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"ended":2`) {
		t.Fatalf("end others: %d %s", res.Code, res.Body)
	}
	if signedIn(phone) || signedIn(desk) {
		t.Error("another session survived")
	}
	if !signedIn(laptop) || !signedIn(bea) {
		t.Error("the session that asked, or someone else's, was ended")
	}
}
