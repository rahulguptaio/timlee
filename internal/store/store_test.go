package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// scratchDir is a per-test directory inside the package, removed afterwards.
// The OS temp directory is avoided so a run leaves nothing behind outside the
// checkout, even when it fails.
func scratchDir(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(".scratch-probe", 0o700); err != nil {
		t.Fatalf("scratch root: %v", err)
	}
	dir, err := os.MkdirTemp(".scratch-probe", "store-")
	if err != nil {
		t.Fatalf("scratch dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// seedUser creates an account with its own organisation and returns both.
func seedUser(t *testing.T, s *Store, email string) (*User, string) {
	t.Helper()
	user, created, err := s.UpsertFromProvider(t.Context(), Profile{
		Provider: "email", Subject: email, Email: email, EmailVerified: true, Name: "Ada Lovelace",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if !created {
		t.Fatalf("seed user: %s already existed", email)
	}
	orgs, err := s.OrgsForUser(t.Context(), user.ID)
	if err != nil || len(orgs) != 1 {
		t.Fatalf("seed user: orgs=%v err=%v", orgs, err)
	}
	return user, orgs[0].ID
}

/* ------------------------------------------------------------ api keys -- */

func TestAPIKeyLifecycle(t *testing.T) {
	s := newStore(t)
	user, orgID := seedUser(t, s, "keys@example.com")

	created, err := s.CreateAPIKey(t.Context(), orgID, user.ID, "primary", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if !strings.HasPrefix(created.Secret, "mk_live_") {
		t.Errorf("secret = %q, want an mk_live_ prefix", created.Secret)
	}
	if created.Prefix != created.Secret[:12] {
		t.Errorf("prefix = %q, want the first 12 characters of the secret", created.Prefix)
	}

	// The secret must not be recoverable from the row that was written.
	var stored string
	if err := s.DB().QueryRow(`SELECT hash FROM api_key WHERE id = ?`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored key: %v", err)
	}
	if stored == created.Secret || !strings.Contains(stored, HashSecret(created.Secret)) {
		t.Errorf("stored material = %q, want only the hash of the secret", stored)
	}

	verified, err := s.VerifyAPIKey(t.Context(), created.Secret)
	if err != nil {
		t.Fatalf("verify key: %v", err)
	}
	if verified.OrgID != orgID || verified.ID != created.ID {
		t.Errorf("verify returned %+v, want org %s key %s", verified, orgID, created.ID)
	}
	if verified.LastUsedAt == nil {
		t.Error("verify did not record last use")
	}

	if _, err := s.VerifyAPIKey(t.Context(), created.Secret+"x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("verify of a wrong secret: err = %v, want ErrNotFound", err)
	}

	revoked, err := s.RevokeAPIKey(t.Context(), orgID, user.ID, created.ID)
	if err != nil || !revoked {
		t.Fatalf("revoke: revoked=%v err=%v", revoked, err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), created.Secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("verify after revoke: err = %v, want ErrNotFound", err)
	}
	// Revoking twice is not an error, but it is also not a second revocation.
	if again, err := s.RevokeAPIKey(t.Context(), orgID, user.ID, created.ID); err != nil || again {
		t.Errorf("second revoke: revoked=%v err=%v, want false/nil", again, err)
	}
}

func TestKeyUseIsRecordedAtMostOnceAMinute(t *testing.T) {
	s := newStore(t)
	user, orgID := seedUser(t, s, "ada@example.com")
	key, err := s.CreateAPIKey(t.Context(), orgID, user.ID, "ci", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	// usedAgo backdates the recorded use and returns the stored time.
	usedAgo := func(age time.Duration) int64 {
		at := time.Now().Add(-age).UnixMilli()
		if _, err := s.DB().ExecContext(t.Context(), `UPDATE api_key SET last_used_at = ? WHERE id = ?`, at, key.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		return at
	}
	stored := func() int64 {
		var at int64
		if err := s.DB().QueryRowContext(t.Context(), `SELECT last_used_at FROM api_key WHERE id = ?`, key.ID).Scan(&at); err != nil {
			t.Fatalf("read last use: %v", err)
		}
		return at
	}

	// Within the minute, verifying is a read: nothing is written.
	recent := usedAgo(30 * time.Second)
	if _, err := s.VerifyAPIKey(t.Context(), key.Secret); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got := stored(); got != recent {
		t.Errorf("last use moved from %d to %d within the minute; want no write", recent, got)
	}

	// Once the recorded use is a minute old, the next one is recorded.
	stale := usedAgo(61 * time.Second)
	if _, err := s.VerifyAPIKey(t.Context(), key.Secret); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got := stored(); got <= stale || time.Since(time.UnixMilli(got)) > 5*time.Second {
		t.Errorf("last use = %d after a stale %d; want it recorded as now", got, stale)
	}
}

func TestAPIKeyRevokeIsTenantScoped(t *testing.T) {
	s := newStore(t)
	owner, orgID := seedUser(t, s, "owner@example.com")
	intruder, otherOrg := seedUser(t, s, "intruder@example.com")

	created, err := s.CreateAPIKey(t.Context(), orgID, owner.ID, "primary", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	revoked, err := s.RevokeAPIKey(t.Context(), otherOrg, intruder.ID, created.ID)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked {
		t.Fatal("another tenant revoked a key by guessing its id")
	}
	if _, err := s.VerifyAPIKey(t.Context(), created.Secret); err != nil {
		t.Errorf("key should still work: %v", err)
	}
}

func TestAPIKeyExpiry(t *testing.T) {
	s := newStore(t)
	user, orgID := seedUser(t, s, "expiry@example.com")

	past := time.Now().Add(-time.Minute).UnixMilli()
	expired, err := s.CreateAPIKey(t.Context(), orgID, user.ID, "stale", &past)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), expired.Secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("verify of an expired key: err = %v, want ErrNotFound", err)
	}
}

// keyRow reads a key as the store lists it.
func keyRow(t *testing.T, s *Store, orgID, id string) APIKey {
	t.Helper()
	keys, err := s.ListAPIKeys(t.Context(), orgID)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	for _, k := range keys {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("key %s is not listed", id)
	return APIKey{}
}

func TestRotateKeepsTheOldKeyWorkingForTheOverlap(t *testing.T) {
	s := newStore(t)
	owner, orgID := seedUser(t, s, "owner@example.com")
	admin, _ := seedUser(t, s, "admin@example.com")
	if err := s.AddMember(t.Context(), orgID, admin.ID, RoleAdmin); err != nil {
		t.Fatalf("add admin: %v", err)
	}
	month := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	old, err := s.CreateAPIKey(t.Context(), orgID, owner.ID, "billing worker", &month)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	before := time.Now()
	rotated, err := s.RotateAPIKey(t.Context(), orgID, admin.ID, old.ID, time.Hour)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Label != "billing worker" || rotated.Replaces != old.ID || rotated.Secret == old.Secret {
		t.Errorf("replacement = %+v, want a new secret under the same label, replacing %s", rotated, old.ID)
	}
	// The replacement lasts as long as the original was meant to.
	if lasts := time.UnixMilli(*rotated.ExpiresAt).Sub(before); lasts < 30*24*time.Hour-time.Minute || lasts > 30*24*time.Hour+time.Minute {
		t.Errorf("replacement expires in %v, want the original's 30 days", lasts)
	}
	if stops := time.UnixMilli(rotated.ReplacedStopsAt).Sub(before); stops < time.Hour-time.Minute || stops > time.Hour+time.Minute {
		t.Errorf("old key stops in %v, want the one-hour overlap", stops)
	}

	// Both work during the overlap.
	for _, secret := range []string{old.Secret, rotated.Secret} {
		if _, err := s.VerifyAPIKey(t.Context(), secret); err != nil {
			t.Errorf("verify during the overlap: %v", err)
		}
	}
	listed := keyRow(t, s, orgID, old.ID)
	if listed.ReplacedBy != rotated.ID || listed.ExpiresAt == nil || *listed.ExpiresAt != rotated.ReplacedStopsAt || listed.RevokedAt != nil {
		t.Errorf("old key = %+v, want it replaced by %s and expiring at the overlap's end", listed, rotated.ID)
	}
	if replacement := keyRow(t, s, orgID, rotated.ID); replacement.CreatedBy != admin.ID {
		t.Errorf("replacement created by %q, want the admin who rotated it", replacement.CreatedBy)
	}

	// Once the overlap is over, only the replacement works.
	if _, err := s.DB().ExecContext(t.Context(), `UPDATE api_key SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).UnixMilli(), old.ID); err != nil {
		t.Fatalf("end the overlap: %v", err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), old.Secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("old key after the overlap: err = %v, want ErrNotFound", err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), rotated.Secret); err != nil {
		t.Errorf("replacement after the overlap: %v", err)
	}

	var action, metadata string
	if err := s.DB().QueryRow(`SELECT action, metadata FROM audit_event WHERE target = ? AND action = 'api_key.rotated'`, old.ID).Scan(&action, &metadata); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !strings.Contains(metadata, rotated.ID) {
		t.Errorf("audit metadata = %s, want the replacement's id", metadata)
	}
}

func TestRotateWithoutOverlapRevokesTheOldKey(t *testing.T) {
	s := newStore(t)
	user, orgID := seedUser(t, s, "ada@example.com")
	old, err := s.CreateAPIKey(t.Context(), orgID, user.ID, "leaked", nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	rotated, err := s.RotateAPIKey(t.Context(), orgID, user.ID, old.ID, 0)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.ExpiresAt != nil {
		t.Errorf("replacement expires at %d, want never, like the original", *rotated.ExpiresAt)
	}
	if _, err := s.VerifyAPIKey(t.Context(), old.Secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("old key: err = %v, want it refused at once", err)
	}
	if listed := keyRow(t, s, orgID, old.ID); listed.RevokedAt == nil || listed.ReplacedBy != rotated.ID {
		t.Errorf("old key = %+v, want it revoked and linked to its replacement", listed)
	}
}

func TestRotateRefusesWhatCannotBeHandedOver(t *testing.T) {
	s := newStore(t)
	user, orgID := seedUser(t, s, "ada@example.com")
	intruder, otherOrg := seedUser(t, s, "intruder@example.com")
	key := func(label string, expiresAt *int64) string {
		t.Helper()
		created, err := s.CreateAPIKey(t.Context(), orgID, user.ID, label, expiresAt)
		if err != nil {
			t.Fatalf("create key: %v", err)
		}
		return created.ID
	}

	live := key("live", nil)
	if _, err := s.RotateAPIKey(t.Context(), otherOrg, intruder.ID, live, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Errorf("another tenant's rotation: err = %v, want ErrNotFound", err)
	}
	if listed := keyRow(t, s, orgID, live); listed.ReplacedBy != "" || listed.ExpiresAt != nil {
		t.Errorf("key after another tenant's attempt = %+v, want it untouched", listed)
	}

	if _, err := s.RotateAPIKey(t.Context(), orgID, user.ID, live, time.Hour); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.RotateAPIKey(t.Context(), orgID, user.ID, live, time.Hour); !errors.Is(err, ErrKeyReplaced) {
		t.Errorf("second rotation: err = %v, want ErrKeyReplaced", err)
	}

	revoked := key("revoked", nil)
	if _, err := s.RevokeAPIKey(t.Context(), orgID, user.ID, revoked); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	past := time.Now().Add(-time.Minute).UnixMilli()
	for _, id := range []string{revoked, key("expired", &past)} {
		if _, err := s.RotateAPIKey(t.Context(), orgID, user.ID, id, time.Hour); !errors.Is(err, ErrKeyRetired) {
			t.Errorf("rotating a retired key: err = %v, want ErrKeyRetired", err)
		}
	}
}

func TestRotateNeverExtendsAKey(t *testing.T) {
	s := newStore(t)
	user, orgID := seedUser(t, s, "ada@example.com")
	soon := time.Now().Add(10 * time.Minute).UnixMilli()
	old, err := s.CreateAPIKey(t.Context(), orgID, user.ID, "short-lived", &soon)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	rotated, err := s.RotateAPIKey(t.Context(), orgID, user.ID, old.ID, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.ReplacedStopsAt != soon || *keyRow(t, s, orgID, old.ID).ExpiresAt != soon {
		t.Errorf("old key stops at %d, want its own expiry %d: a week's overlap must not extend it", rotated.ReplacedStopsAt, soon)
	}
}

func TestSecretsMatch(t *testing.T) {
	tests := []struct {
		name  string
		a, b  string
		equal bool
	}{
		{"identical", "mk_live_abc", "mk_live_abc", true},
		{"one byte different", "mk_live_abc", "mk_live_abd", false},
		{"different length", "mk_live_abc", "mk_live_abcd", false},
		{"both empty", "", "", true},
		{"one empty", "", "mk_live_abc", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SecretsMatch(tc.a, tc.b); got != tc.equal {
				t.Errorf("SecretsMatch(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.equal)
			}
		})
	}
}

/* --------------------------------------------------------- magic links -- */

func TestLoginTokenIsSingleUse(t *testing.T) {
	s := newStore(t)

	secret, hash := NewLoginToken()
	if _, err := s.CreateLoginToken(t.Context(), "ada@example.com", hash, "/app.html", "", "", "", time.Minute.Milliseconds()); err != nil {
		t.Fatalf("create token: %v", err)
	}

	claimed, err := s.ConsumeLoginToken(t.Context(), secret)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if claimed.Email != "ada@example.com" || claimed.ReturnTo != "/app.html" {
		t.Errorf("claimed = %+v, want the address and destination it was minted with", claimed)
	}

	if _, err := s.ConsumeLoginToken(t.Context(), secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("second consume: err = %v, want ErrNotFound", err)
	}
}

func TestLoginTokenRejection(t *testing.T) {
	tests := []struct {
		name string
		ttl  time.Duration
		use  func(secret string) string
	}{
		{"expired", -time.Second, func(secret string) string { return secret }},
		{"unknown", time.Minute, func(string) string { return "never-minted" }},
		{"near miss", time.Minute, func(secret string) string { return secret + "x" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			secret, hash := NewLoginToken()
			if _, err := s.CreateLoginToken(t.Context(), "ada@example.com", hash, "", "", "", "", tc.ttl.Milliseconds()); err != nil {
				t.Fatalf("create token: %v", err)
			}
			if _, err := s.ConsumeLoginToken(t.Context(), tc.use(secret)); !errors.Is(err, ErrNotFound) {
				t.Errorf("consume: err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestPruneLoginTokensLeavesLiveOnes(t *testing.T) {
	s := newStore(t)

	liveSecret, liveHash := NewLoginToken()
	if _, err := s.CreateLoginToken(t.Context(), "live@example.com", liveHash, "", "", "", "", time.Hour.Milliseconds()); err != nil {
		t.Fatalf("create live token: %v", err)
	}
	_, staleHash := NewLoginToken()
	if _, err := s.CreateLoginToken(t.Context(), "stale@example.com", staleHash, "", "", "", "", (-time.Hour).Milliseconds()); err != nil {
		t.Fatalf("create stale token: %v", err)
	}

	removed, err := s.PruneLoginTokens(t.Context())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d tokens, want 1", removed)
	}
	if _, err := s.ConsumeLoginToken(t.Context(), liveSecret); err != nil {
		t.Errorf("the live token was pruned: %v", err)
	}
}

/* ------------------------------------------------------------ sessions -- */

func TestSessionRowsStoreOnlyAHash(t *testing.T) {
	s := newStore(t)
	id := NewSessionID()

	if err := s.PutSession(t.Context(), id, []byte(`{"userId":"usr_1"}`), time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("put: %v", err)
	}

	var stored string
	if err := s.DB().QueryRow(`SELECT sid FROM session`).Scan(&stored); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if stored == id {
		t.Fatal("the raw session id was stored; a copy of the file would be replayable as a cookie")
	}
	if stored != HashSecret(id) {
		t.Errorf("sid = %q, want the hash of the session id", stored)
	}
}

func TestSessionExpiry(t *testing.T) {
	s := newStore(t)

	live, expired := NewSessionID(), NewSessionID()
	if err := s.PutSession(t.Context(), live, []byte(`{}`), time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("put live: %v", err)
	}
	if err := s.PutSession(t.Context(), expired, []byte(`{}`), time.Now().Add(-time.Second).UnixMilli()); err != nil {
		t.Fatalf("put expired: %v", err)
	}

	if _, err := s.GetSession(t.Context(), expired); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session: err = %v, want ErrNotFound", err)
	}
	var remaining int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM session WHERE sid = ?`, HashSecret(expired)).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Error("an expired session was not deleted on read")
	}

	if _, err := s.GetSession(t.Context(), live); err != nil {
		t.Errorf("live session: %v", err)
	}
}

func TestSessionTouchMovesOnlyTheExpiry(t *testing.T) {
	s := newStore(t)
	id := NewSessionID()
	document := []byte(`{"userId":"usr_1","orgId":"org_keep"}`)

	if err := s.PutSession(t.Context(), id, document, time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatalf("put: %v", err)
	}
	later := time.Now().Add(time.Hour).UnixMilli()
	if err := s.TouchSession(t.Context(), id, later); err != nil {
		t.Fatalf("touch: %v", err)
	}

	row, err := s.GetSession(t.Context(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ExpiresAt != later {
		t.Errorf("expiry = %d, want %d", row.ExpiresAt, later)
	}
	if string(row.Document) != string(document) {
		t.Errorf("document = %s, want it unchanged", row.Document)
	}
}

// userIDOf reads a test document's owner the way authsvc does.
func userIDOf(document []byte) (string, error) {
	var data struct {
		UserID string `json:"userId"`
	}
	err := json.Unmarshal(document, &data)
	return data.UserID, err
}

func TestViewSessionReadsTheUserAndOnlyThePartsAskedFor(t *testing.T) {
	s := newStore(t)
	user, orgID := seedUser(t, s, "ada@example.com")
	put := func(document string, expires time.Duration) string {
		t.Helper()
		id := NewSessionID()
		if err := s.PutSession(t.Context(), id, []byte(document), time.Now().Add(expires).UnixMilli()); err != nil {
			t.Fatalf("put: %v", err)
		}
		return id
	}
	signedIn := put(`{"userId":"`+user.ID+`"}`, time.Hour)

	bare, err := s.ViewSession(t.Context(), signedIn, 0, userIDOf)
	if err != nil || bare.User == nil || bare.User.ID != user.ID {
		t.Fatalf("view = %+v (err %v), want ada", bare, err)
	}
	if bare.Memberships != nil || bare.Identities != nil {
		t.Errorf("parts read unasked: %+v, %+v", bare.Memberships, bare.Identities)
	}

	full, err := s.ViewSession(t.Context(), signedIn, WithMemberships|WithIdentities, userIDOf)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if len(full.Memberships) != 1 || full.Memberships[0].ID != orgID || full.Memberships[0].Role != RoleOwner {
		t.Errorf("memberships = %+v, want ada owning %s", full.Memberships, orgID)
	}
	if len(full.Identities) != 1 || full.Identities[0].Provider != "email" {
		t.Errorf("identities = %+v, want her email identity", full.Identities)
	}

	// A sign-in in progress has no user yet, and an account can be gone.
	for _, document := range []string{`{"returnTo":"/console"}`, `{"userId":"usr_gone"}`} {
		view, err := s.ViewSession(t.Context(), put(document, time.Hour), WithMemberships, userIDOf)
		if err != nil || view.User != nil || view.Memberships != nil {
			t.Errorf("%s: view = %+v (err %v), want the session with no user", document, view, err)
		}
	}

	unreadable := errors.New("unreadable")
	if _, err := s.ViewSession(t.Context(), signedIn, 0, func([]byte) (string, error) { return "", unreadable }); !errors.Is(err, unreadable) {
		t.Errorf("err = %v, want the document reader's own error", err)
	}
	if _, err := s.ViewSession(t.Context(), NewSessionID(), 0, userIDOf); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: err = %v, want ErrNotFound", err)
	}

	expired := put(`{"userId":"`+user.ID+`"}`, -time.Second)
	if _, err := s.ViewSession(t.Context(), expired, 0, userIDOf); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session: err = %v, want ErrNotFound", err)
	}
	var remaining int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM session WHERE sid = ?`, HashSecret(expired)).Scan(&remaining); err != nil || remaining != 0 {
		t.Errorf("expired session rows = %d (err %v), want it deleted on read", remaining, err)
	}
}

func TestPruneSessionsKeepsLiveOnes(t *testing.T) {
	s := newStore(t)

	live := NewSessionID()
	if err := s.PutSession(t.Context(), live, []byte(`{}`), time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("put live: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.PutSession(t.Context(), NewSessionID(), []byte(`{}`), time.Now().Add(-time.Minute).UnixMilli()); err != nil {
			t.Fatalf("put stale: %v", err)
		}
	}

	removed, err := s.PruneSessions(t.Context())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 3 {
		t.Errorf("pruned %d, want 3", removed)
	}
	if count, err := s.CountSessions(t.Context()); err != nil || count != 1 {
		t.Errorf("live sessions = %d (err %v), want 1", count, err)
	}
}

/* --------------------------------------------------------------- roles -- */

func TestRoleAtLeast(t *testing.T) {
	tests := []struct {
		have, minimum Role
		want          bool
	}{
		{RoleOwner, RoleAdmin, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleMember, RoleAdmin, false},
		{RoleViewer, RoleViewer, true},
		{RoleMember, RoleViewer, true},
		{RoleViewer, RoleMember, false},
		{"", RoleViewer, false},
		{"superuser", RoleViewer, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.have)+"/"+string(tc.minimum), func(t *testing.T) {
			if got := tc.have.AtLeast(tc.minimum); got != tc.want {
				t.Errorf("Role(%q).AtLeast(%q) = %v, want %v", tc.have, tc.minimum, got, tc.want)
			}
		})
	}
}

func TestRoleOfNonMemberIsEmpty(t *testing.T) {
	s := newStore(t)
	_, orgID := seedUser(t, s, "member@example.com")
	outsider, _ := seedUser(t, s, "outsider@example.com")

	role, err := s.RoleOf(t.Context(), orgID, outsider.ID)
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	if role != "" {
		t.Errorf("role = %q, want empty for a non-member", role)
	}
}

/* ---------------------------------------------------------- identities -- */

func TestUpsertLinksOnlyVerifiedAddresses(t *testing.T) {
	tests := []struct {
		name     string
		verified bool
		wantSame bool
		wantErr  error
	}{
		{name: "verified address joins the existing account", verified: true, wantSame: true},
		{name: "unverified address is refused", verified: false, wantErr: ErrIdentityConflict},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			original, _ := seedUser(t, s, "ada@example.com")

			user, created, err := s.UpsertFromProvider(t.Context(), Profile{
				Provider: "github", Subject: "12345",
				Email: "Ada@Example.com", EmailVerified: tc.verified,
			})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if created {
				t.Error("a second account was created for an address that already had one")
			}
			if tc.wantSame && user.ID != original.ID {
				t.Errorf("user = %s, want the existing account %s", user.ID, original.ID)
			}
		})
	}
}

func TestUpsertIsIdempotentPerSubject(t *testing.T) {
	s := newStore(t)
	profile := Profile{Provider: "github", Subject: "999", Email: "new@example.com", EmailVerified: true}

	first, created, err := s.UpsertFromProvider(t.Context(), profile)
	if err != nil || !created {
		t.Fatalf("first upsert: created=%v err=%v", created, err)
	}

	// A changed display name must not fork the account: the subject is what
	// identifies the person, not anything the provider can edit.
	profile.Name = "Renamed"
	second, created, err := s.UpsertFromProvider(t.Context(), profile)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created || second.ID != first.ID {
		t.Errorf("second upsert returned %s (created=%v), want the same account %s", second.ID, created, first.ID)
	}

	identities, err := s.IdentitiesForUser(t.Context(), first.ID)
	if err != nil || len(identities) != 1 {
		t.Errorf("identities = %d (err %v), want 1", len(identities), err)
	}
}

func TestUserLocaleIsSeededOnceAndCanBeChangedExplicitly(t *testing.T) {
	s := newStore(t)
	profile := Profile{
		Provider: "email", Subject: "ada@example.com", Email: "ada@example.com",
		EmailVerified: true,
	}
	user, _, err := s.UpsertFromProvider(t.Context(), profile)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if user.Locale != "" {
		t.Fatalf("first locale = %q, want empty", user.Locale)
	}

	profile.Locale = "ar-xb"
	user, _, err = s.UpsertFromProvider(t.Context(), profile)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if user.Locale != "ar-xb" {
		t.Errorf("second locale = %q, want ar-xb", user.Locale)
	}

	profile.Locale = "en"
	user, _, err = s.UpsertFromProvider(t.Context(), profile)
	if err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	if user.Locale != "ar-xb" {
		t.Errorf("sign-in overwrote saved locale with %q", user.Locale)
	}

	locale := "en-xa"
	user, err = s.UpdateUser(t.Context(), user.ID, nil, nil, &locale)
	if err != nil {
		t.Fatalf("change locale: %v", err)
	}
	if user.Locale != "en-xa" {
		t.Errorf("changed locale = %q, want en-xa", user.Locale)
	}

	locale = ""
	user, err = s.UpdateUser(t.Context(), user.ID, nil, nil, &locale)
	if err != nil {
		t.Fatalf("clear locale: %v", err)
	}
	if user.Locale != "" {
		t.Errorf("cleared locale = %q, want empty", user.Locale)
	}
}

func TestUnlinkRefusesTheLastIdentity(t *testing.T) {
	s := newStore(t)
	user, _ := seedUser(t, s, "solo@example.com")

	only, err := s.IdentitiesForUser(t.Context(), user.ID)
	if err != nil || len(only) != 1 {
		t.Fatalf("identities = %d (err %v), want 1", len(only), err)
	}
	if err := s.UnlinkIdentity(t.Context(), user.ID, only[0].ID); !errors.Is(err, ErrLastIdentity) {
		t.Fatalf("unlink: err = %v, want ErrLastIdentity", err)
	}

	if _, _, err := s.UpsertFromProvider(t.Context(), Profile{
		Provider: "github", Subject: "solo-gh", Email: "solo@example.com", EmailVerified: true,
	}); err != nil {
		t.Fatalf("link second identity: %v", err)
	}
	if err := s.UnlinkIdentity(t.Context(), user.ID, only[0].ID); err != nil {
		t.Errorf("unlink with a spare identity: %v", err)
	}
}

/* ------------------------------------------------------------- tenancy -- */

func TestAuditIsTenantScoped(t *testing.T) {
	s := newStore(t)
	mine, myOrg := seedUser(t, s, "mine@example.com")
	_, theirOrg := seedUser(t, s, "theirs@example.com")

	if err := s.RecordAudit(t.Context(), AuditEvent{OrgID: myOrg, ActorID: mine.ID, Action: "key.created"}); err != nil {
		t.Fatalf("record: %v", err)
	}

	theirs, err := s.RecentAudit(t.Context(), theirOrg, 50)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	for _, event := range theirs {
		if event.OrgID == myOrg {
			t.Fatalf("event %s leaked across tenants", event.ID)
		}
	}
}

// TestAuditKeepsTheNamesOfTheTime: an event names its actor and target as
// they were when it was recorded, so renaming or relabelling later does not
// rewrite history, and someone who left is still named.
func TestAuditKeepsTheNamesOfTheTime(t *testing.T) {
	s := newStore(t)
	ada, org := seedUser(t, s, "ada@example.com")
	bea, _ := seedUser(t, s, "bea@example.com")
	then := "Bea Ng"
	if _, err := s.UpdateUser(t.Context(), bea.ID, &then, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(t.Context(), org, bea.ID, RoleMember); err != nil {
		t.Fatal(err)
	}
	key, err := s.CreateAPIKey(t.Context(), org, ada.ID, "billing worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAudit(t.Context(), AuditEvent{OrgID: org, ActorID: ada.ID, Action: "member.role_changed", Target: bea.ID}); err != nil {
		t.Fatal(err)
	}

	renamed, gone := "Augusta King", "Beatrice"
	if _, err := s.UpdateUser(t.Context(), ada.ID, &renamed, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateUser(t.Context(), bea.ID, &gone, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveMember(t.Context(), org, bea.ID); err != nil {
		t.Fatal(err)
	}

	page, err := s.QueryAudit(t.Context(), org, AuditFilter{}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	byAction := map[string]AuditEvent{}
	for _, e := range page.Events {
		byAction[e.Action] = e
	}
	created, changed := byAction["api_key.created"], byAction["member.role_changed"]
	if created.ActorName != "Ada Lovelace" || created.ActorEmail != "ada@example.com" || created.TargetLabel != "billing worker" || created.Target != key.ID {
		t.Errorf("key event = %+v, want Ada Lovelace's billing worker as they were", created)
	}
	if changed.TargetLabel != then || changed.ActorName != "Ada Lovelace" {
		t.Errorf("role event = %+v, want Ada Lovelace changing %s", changed, then)
	}
	if org := byAction["org.created"]; org.TargetLabel == "" {
		t.Errorf("org event has no label: %+v", org)
	}
}

// TestQueryAuditNarrowsAndPages: each filter narrows the log on its own,
// total counts every match whatever the page, and nothing crosses tenants.
func TestQueryAuditNarrowsAndPages(t *testing.T) {
	s := newStore(t)
	ada, org := seedUser(t, s, "ada@example.com")
	bea, _ := seedUser(t, s, "bea@example.com")
	_, theirs := seedUser(t, s, "theirs@example.com")
	if err := s.AddMember(t.Context(), org, bea.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}

	// record writes an event a clear millisecond after the last, and returns
	// a moment clear of it too.
	record := func(e AuditEvent) int64 {
		t.Helper()
		time.Sleep(2 * time.Millisecond)
		if err := s.RecordAudit(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		return time.Now().UnixMilli()
	}
	billing, err := s.CreateAPIKey(t.Context(), org, ada.ID, "Billing Worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	afterKey := record(AuditEvent{OrgID: org, ActorID: bea.ID, Action: "member.invited", Metadata: `{"email":"cy@example.com"}`})
	record(AuditEvent{OrgID: org, ActorID: bea.ID, Action: "member.role_changed", Target: ada.ID})
	record(AuditEvent{OrgID: theirs, ActorID: bea.ID, Action: "api_key.created", Target: billing.ID})

	count := func(f AuditFilter) int {
		t.Helper()
		page, err := s.QueryAudit(t.Context(), org, f, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != len(page.Events) {
			t.Errorf("%+v: total %d, but %d events on the only page", f, page.Total, len(page.Events))
		}
		for _, e := range page.Events {
			if e.OrgID != org {
				t.Errorf("%+v: event %s of another organisation", f, e.Action)
			}
		}
		return page.Total
	}
	// org.created, api_key.created, member.invited, member.role_changed.
	for _, tc := range []struct {
		filter AuditFilter
		want   int
	}{
		{AuditFilter{}, 4},
		{AuditFilter{Category: "keys"}, 1},
		{AuditFilter{Category: "invitations"}, 1},
		{AuditFilter{Category: "members"}, 1},
		{AuditFilter{Category: "organisation"}, 1},
		{AuditFilter{Category: "nonsense"}, 0},
		{AuditFilter{ActorID: bea.ID}, 2},
		{AuditFilter{Target: "billing"}, 1},
		{AuditFilter{Target: "BILLING WORK"}, 1},
		{AuditFilter{Target: billing.ID}, 1},
		{AuditFilter{Target: "%"}, 0},
		{AuditFilter{Since: afterKey}, 1},
		{AuditFilter{Until: afterKey}, 3},
		{AuditFilter{ActorID: bea.ID, Category: "members"}, 1},
	} {
		if got := count(tc.filter); got != tc.want {
			t.Errorf("%+v matched %d events, want %d", tc.filter, got, tc.want)
		}
	}

	first, err := s.QueryAudit(t.Context(), org, AuditFilter{}, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := s.QueryAudit(t.Context(), org, AuditFilter{}, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 4 || len(first.Events) != 3 || len(rest.Events) != 1 || rest.Events[0].Action != "org.created" {
		t.Errorf("pages = %d+%d of %d, last %v; want 3+1 of 4 ending with org.created", len(first.Events), len(rest.Events), first.Total, rest.Events)
	}
	if first.Events[0].Action != "member.role_changed" {
		t.Errorf("newest = %s, want member.role_changed", first.Events[0].Action)
	}
	if first.Earliest == 0 || first.Earliest > rest.Events[0].CreatedAt {
		t.Errorf("earliest = %d, want the oldest event's %d", first.Earliest, rest.Events[0].CreatedAt)
	}

	actors, err := s.AuditActors(t.Context(), org)
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) != 2 {
		t.Errorf("actors = %+v, want Ada and Bea", actors)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := scratchDir(t)
	path := dir + "/auth.db"

	first, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	user, _ := seedUser(t, first, "durable@example.com")
	first.Close()

	// A restart must find the same data and must not re-run a migration.
	second, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()

	found, err := second.UserByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("user after restart: %v", err)
	}
	if found.Email != "durable@example.com" {
		t.Errorf("email = %q, want it to survive the restart", found.Email)
	}
}

/* ---------------------------------------------------------------- PKCE -- */

func TestPKCEChallengeAndMatch(t *testing.T) {
	// The worked example from RFC 7636 appendix B, so this is checked against
	// the specification rather than against itself.
	const (
		rfcVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	if got := PKCEChallenge(rfcVerifier); got != rfcChallenge {
		t.Fatalf("PKCEChallenge = %q, want %q", got, rfcChallenge)
	}

	tests := []struct {
		name                string
		verifier, challenge string
		want                bool
	}{
		{"the verifier it was derived from", rfcVerifier, rfcChallenge, true},
		{"a different verifier", "something-else", rfcChallenge, false},
		{"no verifier", "", rfcChallenge, false},
		{"no challenge", rfcVerifier, "", false},
		{"neither", "", "", false},
		{"the verifier presented as the challenge", rfcVerifier, rfcVerifier, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PKCEMatches(tc.verifier, tc.challenge); got != tc.want {
				t.Errorf("PKCEMatches(%q, %q) = %v, want %v", tc.verifier, tc.challenge, got, tc.want)
			}
		})
	}
}

// mintDesktopCode stores a sign-in token for ada and returns its secret: a
// desktop code when challenge is set, a mailed link when it is empty. flow is
// the raw flow id, or empty for a code bound to none.
func mintDesktopCode(t *testing.T, s *Store, challenge, flow string) string {
	t.Helper()
	secret, hash := NewLoginToken()
	flowHash := ""
	if flow != "" {
		flowHash = HashSecret(flow)
	}
	if _, err := s.CreateLoginToken(t.Context(), "ada@example.com", hash, "", "", challenge, flowHash, time.Minute.Milliseconds()); err != nil {
		t.Fatalf("mint: %v", err)
	}
	return secret
}

func TestDesktopCodeIsSpentByItsFirstAttempt(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const flow = "Vb5r0sQ2bS3vBqkq5mH5u2P1yWm7wN0gVf4c9b8a7d6"
	ctx := t.Context()
	mint := func(t *testing.T, s *Store, challenge string) string {
		t.Helper()
		if challenge == "" {
			return mintDesktopCode(t, s, "", "")
		}
		return mintDesktopCode(t, s, challenge, flow)
	}

	// Whatever the first verifier, it is the only one the code ever sees: the
	// right one after a wrong one is too late.
	for _, first := range []string{"", "guess", verifier + "x"} {
		s := newStore(t)
		code := mint(t, s, PKCEChallenge(verifier))
		if pending, err := s.DesktopCodePending(ctx, code); err != nil || !pending {
			t.Fatalf("a fresh code: pending=%v err=%v, want pending", pending, err)
		}
		if _, err := s.RedeemDesktopCode(ctx, code, first, flow); !errors.Is(err, ErrPKCEVerificationFailed) {
			t.Fatalf("verifier %q: err = %v, want ErrPKCEVerificationFailed", first, err)
		}
		if _, err := s.RedeemDesktopCode(ctx, code, verifier, flow); !errors.Is(err, ErrNotFound) {
			t.Errorf("after verifier %q, the right one: err = %v, want ErrNotFound (spent)", first, err)
		}
		if pending, _ := s.DesktopCodePending(ctx, code); pending {
			t.Errorf("after verifier %q the code is still pending", first)
		}
	}

	// The right verifier first redeems it, once.
	s := newStore(t)
	code := mint(t, s, PKCEChallenge(verifier))
	if token, err := s.RedeemDesktopCode(ctx, code, verifier, flow); err != nil || token.Email != "ada@example.com" {
		t.Fatalf("redeem: %+v, %v", token, err)
	}
	if _, err := s.RedeemDesktopCode(ctx, code, verifier, flow); !errors.Is(err, ErrNotFound) {
		t.Errorf("replay: err = %v, want ErrNotFound", err)
	}

	// A desktop code is not a mailed link and a mailed link is not a desktop
	// code: neither path spends, or even sees, the other's tokens.
	bound, mailed := mint(t, s, PKCEChallenge(verifier)), mint(t, s, "")
	if _, err := s.ConsumeLoginToken(ctx, bound); !errors.Is(err, ErrNotFound) {
		t.Errorf("a desktop code as a mailed link: err = %v, want ErrNotFound", err)
	}
	if _, err := s.RedeemDesktopCode(ctx, mailed, verifier, flow); !errors.Is(err, ErrNotFound) {
		t.Errorf("a mailed link as a desktop code: err = %v, want ErrNotFound", err)
	}
	if pending, _ := s.DesktopCodePending(ctx, bound); !pending {
		t.Error("a desktop code presented as a mailed link was spent")
	}
	if _, err := s.ConsumeLoginToken(ctx, mailed); err != nil {
		t.Errorf("a mailed link presented as a desktop code was spent: %v", err)
	}
}

func TestDesktopCodeIsRedeemedOnlyByTheFlowThatStartedIt(t *testing.T) {
	// The login CSRF this stops: an attacker starts a desktop sign-in for
	// their own account, keeps the code and verifier, and has the victim's
	// app redeem them. PKCE cannot tell, since the attacker holds the
	// verifier; only the flow id set in the victim's own window can.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const flow = "Vb5r0sQ2bS3vBqkq5mH5u2P1yWm7wN0gVf4c9b8a7d6"
	ctx := t.Context()
	challenge := PKCEChallenge(verifier)

	tests := []struct {
		name       string
		mintedFlow string
		presented  string
	}{
		{"no flow presented", flow, ""},
		{"another window's flow", flow, "Xb5r0sQ2bS3vBqkq5mH5u2P1yWm7wN0gVf4c9b8a7d6"},
		{"the flow's hash presented as the flow", flow, HashSecret(flow)},
		{"a code minted without a flow", "", flow},
		{"a code minted without a flow, presented without one", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			code := mintDesktopCode(t, s, challenge, tc.mintedFlow)
			if _, err := s.RedeemDesktopCode(ctx, code, verifier, tc.presented); !errors.Is(err, ErrDesktopFlowMismatch) {
				t.Fatalf("err = %v, want ErrDesktopFlowMismatch", err)
			}
			// Refused, and spent: the right window cannot use it afterwards.
			if _, err := s.RedeemDesktopCode(ctx, code, verifier, flow); !errors.Is(err, ErrNotFound) {
				t.Errorf("the right flow afterwards: err = %v, want ErrNotFound (spent)", err)
			}
		})
	}

	s := newStore(t)
	code := mintDesktopCode(t, s, challenge, flow)
	if token, err := s.RedeemDesktopCode(ctx, code, verifier, flow); err != nil || token.Email != "ada@example.com" {
		t.Fatalf("the window that started it: %+v, %v", token, err)
	}
}

/* ------------------------------------------------------------- context -- */

func TestCancelledContextAbortsAStoreCall(t *testing.T) {
	// Each case leaves a store call waiting on something it cannot get. It
	// must give up when its context ends, not when the wait does: a cancelled
	// request, or a server past its shutdown deadline, must not be held for
	// the length of somebody else's transaction.
	abandons := func(t *testing.T, call func(ctx context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- call(ctx) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want the context's deadline", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the call kept waiting after its context ended")
		}
	}

	t.Run("waiting for the writer's only connection", func(t *testing.T) {
		s := newStore(t)
		tx, err := s.DB().BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()

		abandons(t, func(ctx context.Context) error {
			return s.TouchSession(ctx, NewSessionID(), time.Now().Add(time.Hour).UnixMilli())
		})
	})

	t.Run("waiting out the busy timeout", func(t *testing.T) {
		path := scratchDir(t) + "/auth.db"
		holder, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("open holder: %v", err)
		}
		defer holder.Close()
		waiter, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("open waiter: %v", err)
		}
		defer waiter.Close()

		// Another handle on the file, as a backup or a second instance in
		// the middle of a deploy would be, holds the write lock.
		tx, err := holder.DB().BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(t.Context(),
			`INSERT INTO schema_migration (name, applied_at) VALUES ('lock-holder', 0)`); err != nil {
			t.Fatalf("take the write lock: %v", err)
		}

		abandons(t, func(ctx context.Context) error {
			return waiter.PutSession(ctx, NewSessionID(), []byte(`{}`), time.Now().Add(time.Hour).UnixMilli())
		})
	})
}

/* --------------------------------------------------------- connections -- */

func TestReadsDoNotQueueBehindTheWriter(t *testing.T) {
	// On a file, reads have a pool of their own. A write transaction holding
	// the writer's only connection, as a sign-in or a key mint does, must
	// not hold up every session lookup behind it.
	s, err := Open(t.Context(), scratchDir(t)+"/auth.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	user, _ := seedUser(t, s, "ada@example.com")

	tx, err := s.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `UPDATE app_user SET name = 'Augusta King' WHERE id = ?`, user.ID); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	during, err := s.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("read during the write: %v", err)
	}
	if during.Name == "Augusta King" {
		t.Error("a read saw a write that had not committed")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	after, err := s.UserByID(t.Context(), user.ID)
	if err != nil || after.Name != "Augusta King" {
		t.Errorf("read after the commit = %+v (err %v), want the committed name", after, err)
	}
}

// TestSnapshotsNeverWaitForASecondConnection: a snapshot holds a connection
// for its whole life, so nothing inside it may wait for another. On
// ":memory:" the pool is that one connection; on a file, every reader may be
// holding one. Session views on a statement cache that has not seen their
// queries yet, beside key verifications and writes from more callers than
// the read pool has connections, all finish inside their deadline. Every
// call carries it, so a wait for a connection fails the test rather than
// hanging it.
func TestSnapshotsNeverWaitForASecondConnection(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"memory", ":memory:"},
		{"file", scratchDir(t) + "/auth.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.Context(), tc.path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer s.Close()
			user, orgID := seedUser(t, s, "ada@example.com")
			id := NewSessionID()
			if err := s.PutSession(t.Context(), id, []byte(`{"userId":"`+user.ID+`"}`), time.Now().Add(time.Hour).UnixMilli()); err != nil {
				t.Fatalf("put: %v", err)
			}
			key, err := s.CreateAPIKey(t.Context(), orgID, user.ID, "worker", nil)
			if err != nil {
				t.Fatalf("key: %v", err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			const workers, calls = 4 * readConns, 25
			var (
				wg     sync.WaitGroup
				failed = make(chan error, workers)
				start  = make(chan struct{})
			)
			for w := range workers {
				wg.Go(func() {
					<-start
					for i := range calls {
						var err error
						switch (w + i) % 4 {
						case 0, 1:
							_, err = s.ViewSession(ctx, id, WithMemberships|WithIdentities, userIDOf)
						case 2:
							_, err = s.VerifyAPIKey(ctx, key.Secret)
						case 3:
							err = s.TouchSession(ctx, id, time.Now().Add(time.Hour).UnixMilli())
						}
						if err != nil {
							failed <- fmt.Errorf("caller %d, call %d: %w", w, i, err)
							return
						}
					}
				})
			}
			close(start)

			finished := make(chan struct{})
			go func() { wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-time.After(20 * time.Second):
				// Past the deadline as well: a wait no context reaches.
				var dump strings.Builder
				_ = pprof.Lookup("goroutine").WriteTo(&dump, 1)
				t.Fatalf("store calls hung past their deadline:\n%s", dump.String())
			}
			close(failed)
			for err := range failed {
				t.Error(err)
			}
		})
	}
}

func TestViewSessionReadsOneSnapshot(t *testing.T) {
	// The reads after the session's come from the state the session was read
	// in, even when a write commits in between. Only a file has a writer
	// that can commit while a read is open.
	s, err := Open(t.Context(), scratchDir(t)+"/auth.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	user, _ := seedUser(t, s, "ada@example.com")
	id := NewSessionID()
	if err := s.PutSession(t.Context(), id, []byte(`{"userId":"`+user.ID+`"}`), time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("put: %v", err)
	}

	view, err := s.ViewSession(t.Context(), id, WithMemberships, func(document []byte) (string, error) {
		renamed := "Augusta King"
		if _, err := s.UpdateUser(t.Context(), user.ID, &renamed, nil, nil); err != nil {
			t.Fatalf("rename: %v", err)
		}
		if _, err := s.CreateOrg(t.Context(), "Analytical Engines", user.ID); err != nil {
			t.Fatalf("join: %v", err)
		}
		return userIDOf(document)
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.User.Name != "Ada Lovelace" || len(view.Memberships) != 1 {
		t.Errorf("view = %q in %d organisations, want the state the session was read in: Ada Lovelace in 1",
			view.User.Name, len(view.Memberships))
	}
}

// TestEndOtherSessionsKeepsThisOne: ending an account's other sessions
// leaves the one that asked and everybody else's.
func TestEndOtherSessionsKeepsThisOne(t *testing.T) {
	s := newStore(t)
	ada, _ := seedUser(t, s, "ada@example.com")
	bea, _ := seedUser(t, s, "bea@example.com")
	put := func(userID string) string {
		t.Helper()
		id := NewSessionID()
		if err := s.PutSession(t.Context(), id, []byte(`{"userId":"`+userID+`"}`), time.Now().Add(time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	here, phone, desk, beas := put(ada.ID), put(ada.ID), put(ada.ID), put(bea.ID)

	if n, err := s.OtherSessions(t.Context(), ada.ID, here); err != nil || n != 2 {
		t.Fatalf("other sessions = %d, %v; want 2", n, err)
	}
	if n, err := s.EndOtherSessions(t.Context(), ada.ID, here); err != nil || n != 2 {
		t.Fatalf("ended = %d, %v; want 2", n, err)
	}
	for id, want := range map[string]bool{here: true, phone: false, desk: false, beas: true} {
		_, err := s.GetSession(t.Context(), id)
		if alive := err == nil; alive != want {
			t.Errorf("session alive = %v, want %v (%v)", alive, want, err)
		}
	}
}

// TestDeleteOrgTakesEverythingWithIt: an organisation goes with its
// memberships, keys, invitations and audit log, and nobody else's.
func TestDeleteOrgTakesEverythingWithIt(t *testing.T) {
	s := newStore(t)
	ada, org := seedUser(t, s, "ada@example.com")
	bea, other := seedUser(t, s, "bea@example.com")
	if err := s.AddMember(t.Context(), org, bea.ID, RoleMember); err != nil {
		t.Fatal(err)
	}
	key, err := s.CreateAPIKey(t.Context(), org, ada.ID, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.CreateAPIKey(t.Context(), other, bea.ID, "theirs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInvitation(t.Context(), org, "cy@example.com", RoleMember, ada.ID, "hash-cy", int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	members, live, err := s.DeleteOrg(t.Context(), org)
	if err != nil || members != 2 || live != 1 {
		t.Fatalf("delete = %d members, %d live keys, %v; want 2, 1", members, live, err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), key.Secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("the deleted organisation's key verifies: %v", err)
	}
	if _, err := s.VerifyAPIKey(t.Context(), theirs.Secret); err != nil {
		t.Errorf("another organisation's key stopped: %v", err)
	}
	var left int
	if err := s.DB().QueryRowContext(t.Context(),
		`SELECT (SELECT count(*) FROM membership WHERE org_id = ?1) + (SELECT count(*) FROM invitation WHERE org_id = ?1)
		      + (SELECT count(*) FROM audit_event WHERE org_id = ?1) + (SELECT count(*) FROM api_key WHERE org_id = ?1)`, org).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d rows outlived the organisation (%v)", left, err)
	}
	if orgs, _ := s.OrgsForUser(t.Context(), bea.ID); len(orgs) != 1 || orgs[0].ID != other {
		t.Errorf("bea's organisations = %+v, want only her own", orgs)
	}
	if _, _, err := s.DeleteOrg(t.Context(), org); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice = %v, want ErrNotFound", err)
	}
}

// TestRenameOrgRecordsTheChange: a rename is audited with both names, and
// renaming to the same name records nothing.
func TestRenameOrgRecordsTheChange(t *testing.T) {
	s := newStore(t)
	ada, org := seedUser(t, s, "ada@example.com")
	previous, err := s.RenameOrg(t.Context(), org, ada.ID, "Analytical Engines")
	if err != nil || previous != "Ada's workspace" {
		t.Fatalf("rename = %q, %v", previous, err)
	}
	if _, err := s.RenameOrg(t.Context(), org, ada.ID, "Analytical Engines"); err != nil {
		t.Fatal(err)
	}
	events, err := s.RecentAudit(t.Context(), org, 10)
	if err != nil {
		t.Fatal(err)
	}
	renames := 0
	for _, e := range events {
		if e.Action == "org.renamed" {
			renames++
			if e.Metadata != `{"from":"Ada's workspace","to":"Analytical Engines"}` || e.TargetLabel != "Analytical Engines" {
				t.Errorf("rename event = %+v", e)
			}
		}
	}
	if renames != 1 {
		t.Errorf("%d rename events, want 1", renames)
	}
	if _, err := s.RenameOrg(t.Context(), "org_missing", ada.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renaming a missing organisation = %v", err)
	}
}
