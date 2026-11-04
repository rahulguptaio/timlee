package store

import (
	"errors"
	"testing"
	"time"
)

/* ----------------------------------------------------------- authority -- */

func TestRoleOutranks(t *testing.T) {
	tests := []struct {
		have, other Role
		want        bool
	}{
		{RoleOwner, RoleAdmin, true},
		{RoleAdmin, RoleOwner, false},
		{RoleAdmin, RoleAdmin, false},
		{RoleMember, RoleViewer, true},
		{RoleViewer, RoleMember, false},
		// An unknown role is outranked by everything and outranks nothing, so
		// a corrupt row can neither act nor be protected.
		{RoleViewer, "superuser", true},
		{"superuser", RoleViewer, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.have)+"/"+string(tc.other), func(t *testing.T) {
			if got := tc.have.Outranks(tc.other); got != tc.want {
				t.Errorf("Role(%q).Outranks(%q) = %v, want %v", tc.have, tc.other, got, tc.want)
			}
		})
	}
}

func TestRoleValid(t *testing.T) {
	for _, role := range []Role{RoleOwner, RoleAdmin, RoleMember, RoleViewer} {
		if !role.Valid() {
			t.Errorf("Role(%q).Valid() = false", role)
		}
	}
	for _, role := range []Role{"", "root", "Owner", "superuser"} {
		if role.Valid() {
			t.Errorf("Role(%q).Valid() = true, want false", role)
		}
	}
}

/* ---------------------------------------------------------- last owner -- */

func TestLastOwnerIsProtected(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *Store, orgID, ownerID string) error
	}{
		{
			name: "demotion",
			change: func(s *Store, orgID, ownerID string) error {
				return s.SetMemberRole(t.Context(), orgID, ownerID, RoleAdmin)
			},
		},
		{
			name: "removal",
			change: func(s *Store, orgID, ownerID string) error {
				_, err := s.RemoveMember(t.Context(), orgID, ownerID)
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			owner, orgID := seedUser(t, s, "owner@example.com")

			if err := tc.change(s, orgID, owner.ID); !errors.Is(err, ErrLastOwner) {
				t.Fatalf("err = %v, want ErrLastOwner", err)
			}
			role, err := s.RoleOf(t.Context(), orgID, owner.ID)
			if err != nil || role != RoleOwner {
				t.Fatalf("role = %q (err %v), want the owner untouched", role, err)
			}

			// With a second owner the same change is allowed, which is what
			// makes the refusal a guard rather than a prohibition.
			second, _ := seedUser(t, s, "second@example.com")
			if err := s.AddMember(t.Context(), orgID, second.ID, RoleOwner); err != nil {
				t.Fatalf("add second owner: %v", err)
			}
			if err := tc.change(s, orgID, owner.ID); err != nil {
				t.Fatalf("%s with a second owner: %v", tc.name, err)
			}
			if owners, err := s.CountOwners(t.Context(), orgID); err != nil || owners != 1 {
				t.Errorf("owners = %d (err %v), want 1", owners, err)
			}
		})
	}
}

func TestSetMemberRoleRejectsStrangers(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "owner@example.com")
	outsider, _ := seedUser(t, s, "outsider@example.com")

	if err := s.SetMemberRole(t.Context(), orgID, outsider.ID, RoleAdmin); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMemberRole: err = %v, want ErrNotFound", err)
	}
	if _, err := s.RemoveMember(t.Context(), orgID, outsider.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveMember: err = %v, want ErrNotFound", err)
	}
}

func TestRemoveMemberKeepsTheAccountAndKillsItsKeys(t *testing.T) {
	s := newStore(t)
	owner, orgID := seedUser(t, s, "owner@example.com")
	leaver, otherOrg := seedUser(t, s, "leaver@example.com")
	if err := s.AddMember(t.Context(), orgID, leaver.ID, RoleAdmin); err != nil {
		t.Fatalf("add member: %v", err)
	}

	here, err := s.CreateAPIKey(t.Context(), orgID, leaver.ID, "shared org", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	elsewhere, err := s.CreateAPIKey(t.Context(), otherOrg, leaver.ID, "own org", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	theirs, err := s.CreateAPIKey(t.Context(), orgID, owner.ID, "owner's", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	// Already dead: it is not one of the keys the removal stops.
	past := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := s.CreateAPIKey(t.Context(), orgID, leaver.ID, "expired", &past); err != nil {
		t.Fatalf("create key: %v", err)
	}

	revoked, err := s.RemoveMember(t.Context(), orgID, leaver.ID)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if revoked != 1 {
		t.Errorf("revoked %d keys, want 1: the one that still worked", revoked)
	}

	// The membership is gone; the person is not.
	if role, err := s.RoleOf(t.Context(), orgID, leaver.ID); err != nil || role != "" {
		t.Errorf("role = %q (err %v), want none", role, err)
	}
	if _, err := s.UserByID(t.Context(), leaver.ID); err != nil {
		t.Errorf("the account was deleted with the membership: %v", err)
	}

	// Their key for this org is dead; everything else still works.
	if _, err := s.VerifyAPIKey(t.Context(), here.Secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("key for the org they left: err = %v, want ErrNotFound", err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), elsewhere.Secret); err != nil {
		t.Errorf("key for an org they are still in was revoked: %v", err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), theirs.Secret); err != nil {
		t.Errorf("another member's key was revoked: %v", err)
	}
}

/* --------------------------------------------------------- invitations -- */

func invite(t *testing.T, s *Store, orgID, email string, role Role, ttl time.Duration) (string, *Invitation) {
	t.Helper()
	secret, hash := NewInvitationToken()
	invitation, err := s.CreateInvitation(t.Context(), orgID, email, role, "", hash, ttl.Milliseconds())
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	return secret, invitation
}

func TestInvitationStoresOnlyAHash(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "owner@example.com")
	secret, invitation := invite(t, s, orgID, "ada@example.com", RoleMember, time.Hour)

	var stored string
	if err := s.DB().QueryRow(`SELECT hash FROM invitation WHERE id = ?`, invitation.ID).Scan(&stored); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if stored == secret {
		t.Fatal("the raw token was stored; a copy of the file would be replayable as an invitation")
	}
	if stored != HashSecret(secret) {
		t.Errorf("stored = %q, want the hash of the token", stored)
	}
}

func TestInvitationAcceptance(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "owner@example.com")
	ada, _ := seedUser(t, s, "ada@example.com")

	secret, _ := invite(t, s, orgID, "Ada@Example.com", RoleAdmin, time.Hour)

	accepted, err := s.AcceptInvitation(t.Context(), secret, ada.ID, ada.Email)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.OrgID != orgID || accepted.Role != RoleAdmin {
		t.Errorf("accepted = %+v, want org %s as admin", accepted, orgID)
	}
	if role, err := s.RoleOf(t.Context(), orgID, ada.ID); err != nil || role != RoleAdmin {
		t.Errorf("role = %q (err %v), want admin", role, err)
	}
}

func TestInvitationRejection(t *testing.T) {
	tests := []struct {
		name    string
		ttl     time.Duration
		setUp   func(t *testing.T, s *Store, orgID, secret string, invitationID string, accepterID string)
		useWith func(secret string) string
		wantErr error
	}{
		{
			name:    "expired",
			ttl:     -time.Second,
			wantErr: ErrNotFound,
		},
		{
			name:    "unknown token",
			ttl:     time.Hour,
			useWith: func(string) string { return "never-minted" },
			wantErr: ErrNotFound,
		},
		{
			name:    "near miss",
			ttl:     time.Hour,
			useWith: func(secret string) string { return secret + "x" },
			wantErr: ErrNotFound,
		},
		{
			name: "revoked",
			ttl:  time.Hour,
			setUp: func(t *testing.T, s *Store, orgID, _, invitationID, _ string) {
				if revoked, err := s.RevokeInvitation(t.Context(), orgID, invitationID); err != nil || !revoked {
					t.Fatalf("revoke: revoked=%v err=%v", revoked, err)
				}
			},
			wantErr: ErrNotFound,
		},
		{
			name: "already used",
			ttl:  time.Hour,
			setUp: func(t *testing.T, s *Store, _, secret, _, accepterID string) {
				if _, err := s.AcceptInvitation(t.Context(), secret, accepterID, "ada@example.com"); err != nil {
					t.Fatalf("first accept: %v", err)
				}
			},
			wantErr: ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			_, orgID := seedUser(t, s, "owner@example.com")
			ada, _ := seedUser(t, s, "ada@example.com")

			secret, invitation := invite(t, s, orgID, "ada@example.com", RoleMember, tc.ttl)
			if tc.setUp != nil {
				tc.setUp(t, s, orgID, secret, invitation.ID, ada.ID)
			}

			presented := secret
			if tc.useWith != nil {
				presented = tc.useWith(secret)
			}
			if _, err := s.AcceptInvitation(t.Context(), presented, ada.ID, ada.Email); !errors.Is(err, tc.wantErr) {
				t.Fatalf("accept: err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestInvitationRefusesAnotherAddress(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "owner@example.com")
	mallory, _ := seedUser(t, s, "mallory@example.com")

	secret, _ := invite(t, s, orgID, "ada@example.com", RoleAdmin, time.Hour)

	if _, err := s.AcceptInvitation(t.Context(), secret, mallory.ID, mallory.Email); !errors.Is(err, ErrInvitationEmailMismatch) {
		t.Fatalf("accept: err = %v, want ErrInvitationEmailMismatch", err)
	}
	if role, err := s.RoleOf(t.Context(), orgID, mallory.ID); err != nil || role != "" {
		t.Errorf("role = %q (err %v), want no membership for an address that was not invited", role, err)
	}

	// A rejected attempt must not burn the invitation for its real recipient.
	ada, _, err := s.UpsertFromProvider(t.Context(), Profile{
		Provider: "email", Subject: "ada@example.com", Email: "ada@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("seed ada: %v", err)
	}
	if _, err := s.AcceptInvitation(t.Context(), secret, ada.ID, ada.Email); err != nil {
		t.Errorf("the real recipient could not accept afterwards: %v", err)
	}
}

func TestInvitationNeverDemotesAnExistingMember(t *testing.T) {
	s := newStore(t)
	owner, orgID := seedUser(t, s, "owner@example.com")

	// The only owner is invited back as a viewer. Honouring that literally
	// would strip the organisation of its last owner through a path that has
	// no guard of its own.
	secret, _ := invite(t, s, orgID, owner.Email, RoleViewer, time.Hour)
	accepted, err := s.AcceptInvitation(t.Context(), secret, owner.ID, owner.Email)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.Role != RoleOwner {
		t.Errorf("granted %q, want the stronger existing role to stand", accepted.Role)
	}
	if owners, err := s.CountOwners(t.Context(), orgID); err != nil || owners != 1 {
		t.Fatalf("owners = %d (err %v), want 1", owners, err)
	}
}

func TestReInvitingRotatesInsteadOfDuplicating(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "owner@example.com")
	ada, _ := seedUser(t, s, "ada@example.com")

	first, original := invite(t, s, orgID, "ada@example.com", RoleViewer, time.Hour)
	second, reissued := invite(t, s, orgID, "ada@example.com", RoleAdmin, time.Hour)

	if reissued.ID != original.ID {
		t.Errorf("re-invite created %s alongside %s, want one invitation per address", reissued.ID, original.ID)
	}
	pending, err := s.PendingInvitations(t.Context(), orgID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %d (err %v), want 1", len(pending), err)
	}
	if pending[0].Role != RoleAdmin {
		t.Errorf("role = %q, want the re-invite's role", pending[0].Role)
	}

	// Rotating means the superseded token is dead, not merely duplicated.
	if _, err := s.AcceptInvitation(t.Context(), first, ada.ID, ada.Email); !errors.Is(err, ErrNotFound) {
		t.Errorf("the superseded token still works: err = %v, want ErrNotFound", err)
	}
	if _, err := s.AcceptInvitation(t.Context(), second, ada.ID, ada.Email); err != nil {
		t.Errorf("the current token does not work: %v", err)
	}
}

func TestInvitationsAreTenantScoped(t *testing.T) {
	s := newStore(t)
	_, mine := seedUser(t, s, "mine@example.com")
	_, theirs := seedUser(t, s, "theirs@example.com")

	_, invitation := invite(t, s, mine, "ada@example.com", RoleMember, time.Hour)

	if revoked, err := s.RevokeInvitation(t.Context(), theirs, invitation.ID); err != nil || revoked {
		t.Errorf("another tenant revoked an invitation: revoked=%v err=%v", revoked, err)
	}
	if _, err := s.InvitationByID(t.Context(), theirs, invitation.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another tenant read an invitation: err = %v, want ErrNotFound", err)
	}
	if pending, err := s.PendingInvitations(t.Context(), theirs); err != nil || len(pending) != 0 {
		t.Errorf("their pending list = %d (err %v), want 0", len(pending), err)
	}
}

func TestRevokedAndAcceptedInvitationsLeaveThePendingList(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "owner@example.com")
	ada, _ := seedUser(t, s, "ada@example.com")

	_, revokable := invite(t, s, orgID, "bob@example.com", RoleMember, time.Hour)
	acceptable, _ := invite(t, s, orgID, "ada@example.com", RoleMember, time.Hour)

	if pending, err := s.PendingInvitations(t.Context(), orgID); err != nil || len(pending) != 2 {
		t.Fatalf("pending = %d (err %v), want 2", len(pending), err)
	}
	if _, err := s.RevokeInvitation(t.Context(), orgID, revokable.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.AcceptInvitation(t.Context(), acceptable, ada.ID, ada.Email); err != nil {
		t.Fatalf("accept: %v", err)
	}

	pending, err := s.PendingInvitations(t.Context(), orgID)
	if err != nil || len(pending) != 0 {
		t.Errorf("pending = %d (err %v), want 0 once both are settled", len(pending), err)
	}

	// A settled address can be invited again: the uniqueness guard is on live
	// invitations only.
	if _, err := s.CreateInvitation(t.Context(), orgID, "bob@example.com", RoleMember, "", HashSecret("fresh"), time.Hour.Milliseconds()); err != nil {
		t.Errorf("re-invite after revoke: %v", err)
	}
}

func TestPruneInvitationsKeepsLiveOnes(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "owner@example.com")

	invite(t, s, orgID, "live@example.com", RoleMember, time.Hour)
	invite(t, s, orgID, "stale@example.com", RoleMember, -time.Hour)

	removed, err := s.PruneInvitations(t.Context())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d, want 1", removed)
	}
	if pending, err := s.PendingInvitations(t.Context(), orgID); err != nil || len(pending) != 1 {
		t.Errorf("pending = %d (err %v), want the live one", len(pending), err)
	}
}
