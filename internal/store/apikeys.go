package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultScopes is what a key may do unless told otherwise.
const DefaultScopes = "inference:read,inference:write"

// APIKey is a data-plane credential. The secret itself is never stored, so it
// never appears on this type. LastUsedAt is accurate to lastUsedResolution: a
// use is recorded only once the stored time is that old.
type APIKey struct {
	ID     string   `json:"id"`
	OrgID  string   `json:"-"`
	Label  string   `json:"label"`
	Prefix string   `json:"prefix"`
	Scopes []string `json:"scopes"`
	// CreatedBy is the member who created the key, "" once their account is
	// gone. Removing them from the organisation revokes it.
	CreatedBy  string `json:"createdBy"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
	ExpiresAt  *int64 `json:"expiresAt"`
	RevokedAt  *int64 `json:"revokedAt"`
	// ReplacedBy is the key a rotation replaced this one with.
	ReplacedBy string `json:"replacedBy,omitempty"`
}

const apiKeyColumns = `id, org_id, label, prefix, scopes, created_by, created_at, last_used_at, expires_at, revoked_at, replaced_by`

// lastUsedResolution is how stale a key's recorded last use may get. Writing
// it on every verification made the data plane's hottest read a write too,
// for a time the console only shows to the minute.
const lastUsedResolution = time.Minute

// CreatedKey carries the one and only sight of a new key's secret.
type CreatedKey struct {
	ID        string
	Label     string
	Prefix    string
	Secret    string
	ExpiresAt *int64
}

// CreateAPIKey mints a key for an org. The returned secret is not recoverable
// afterwards: only its hash and visible prefix are persisted. A nil expiresAt
// is a key that works until it is revoked.
func (s *Store) CreateAPIKey(ctx context.Context, orgID, userID, label string, expiresAt *int64) (CreatedKey, error) {
	secret, prefix, hash := NewAPIKey()
	id := newKeyID()

	err := s.transact(ctx, func(q queryer) error {
		if err := insertAPIKey(ctx, q, id, orgID, userID, label, prefix, hash, expiresAt); err != nil {
			return err
		}
		event := AuditEvent{OrgID: orgID, ActorID: userID, Action: "api_key.created", Target: id}
		if expiresAt != nil {
			event.Metadata = fmt.Sprintf(`{"expiresAt":%d}`, *expiresAt)
		}
		return recordAudit(ctx, q, event)
	})
	if err != nil {
		return CreatedKey{}, err
	}
	return CreatedKey{ID: id, Label: label, Prefix: prefix, Secret: secret, ExpiresAt: expiresAt}, nil
}

func insertAPIKey(ctx context.Context, q queryer, id, orgID, userID, label, prefix, hash string, expiresAt *int64) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO api_key (id, org_id, created_by, label, prefix, hash, scopes, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, orgID, userID, label, prefix, hash, DefaultScopes, nowMillis(), expiresAt,
	)
	return err
}

// ErrKeyRetired is returned for rotating a key that is revoked or has
// expired: there is nothing left to hand over from, only a new key to create.
var ErrKeyRetired = errors.New("the key is revoked or expired")

// ErrKeyReplaced is returned for rotating a key a second time. Each rotation
// would leave another replacement working beside it; it is the replacement
// that gets rotated next.
var ErrKeyReplaced = errors.New("the key has already been replaced")

// RotatedKey is a rotation's outcome: the replacement, whose secret is seen
// this once, and when the key it replaces stops being accepted.
type RotatedKey struct {
	CreatedKey
	Replaces string
	// ReplacedStopsAt is the end of the overlap (epoch milliseconds), or the
	// moment of rotation when there is none, in which case the old key is
	// revoked rather than left to expire.
	ReplacedStopsAt int64
}

// RotateAPIKey replaces a key with a new one under the same label, keeping
// the old one working for overlap so the new one can be deployed first. With
// no overlap the old key is revoked at once; an overlap never extends a key
// beyond the expiry it already had.
//
// The replacement lasts as long as the original was meant to: a key made to
// expire after 30 days is replaced by one that expires 30 days from now, and
// one that never expires by one that never does. The org id is part of the
// lookup, so one tenant cannot rotate another's key by guessing its id.
func (s *Store) RotateAPIKey(ctx context.Context, orgID, userID, keyID string, overlap time.Duration) (RotatedKey, error) {
	secret, prefix, hash := NewAPIKey()
	out := RotatedKey{CreatedKey: CreatedKey{ID: newKeyID(), Prefix: prefix, Secret: secret}, Replaces: keyID}

	err := s.transact(ctx, func(q queryer) error {
		var (
			createdAt            int64
			expiresAt, revokedAt sql.NullInt64
			replacedBy           sql.NullString
		)
		err := q.QueryRowContext(ctx,
			`SELECT label, created_at, expires_at, revoked_at, replaced_by FROM api_key WHERE id = ? AND org_id = ?`,
			keyID, orgID,
		).Scan(&out.Label, &createdAt, &expiresAt, &revokedAt, &replacedBy)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		now := nowMillis()
		switch {
		case revokedAt.Valid || (expiresAt.Valid && expiresAt.Int64 <= now):
			return ErrKeyRetired
		case replacedBy.Valid:
			return ErrKeyReplaced
		}

		if expiresAt.Valid {
			lasts := now + (expiresAt.Int64 - createdAt)
			out.ExpiresAt = &lasts
		}
		if err := insertAPIKey(ctx, q, out.ID, orgID, userID, out.Label, prefix, hash, out.ExpiresAt); err != nil {
			return err
		}

		out.ReplacedStopsAt = now + overlap.Milliseconds()
		if expiresAt.Valid && expiresAt.Int64 < out.ReplacedStopsAt {
			out.ReplacedStopsAt = expiresAt.Int64
		}
		update := `UPDATE api_key SET expires_at = ?, replaced_by = ? WHERE id = ? AND org_id = ?`
		if overlap <= 0 {
			out.ReplacedStopsAt = now
			update = `UPDATE api_key SET revoked_at = ?, replaced_by = ? WHERE id = ? AND org_id = ?`
		}
		if _, err := q.ExecContext(ctx, update, out.ReplacedStopsAt, out.ID, keyID, orgID); err != nil {
			return err
		}
		return recordAudit(ctx, q, AuditEvent{
			OrgID: orgID, ActorID: userID, Action: "api_key.rotated", Target: keyID,
			Metadata: fmt.Sprintf(`{"replacement":%q,"stopsAt":%d}`, out.ID, out.ReplacedStopsAt),
		})
	})
	if err != nil {
		return RotatedKey{}, err
	}
	return out, nil
}

// ListAPIKeys returns an org's keys, newest first.
func (s *Store) ListAPIKeys(ctx context.Context, orgID string) ([]APIKey, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+apiKeyColumns+` FROM api_key WHERE org_id = ? ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// RevokeAPIKey disables a key, reporting whether it had been live. The org id
// is part of the WHERE clause so one tenant cannot revoke another's key by
// guessing its id.
func (s *Store) RevokeAPIKey(ctx context.Context, orgID, userID, keyID string) (bool, error) {
	revoked := false
	err := s.transact(ctx, func(q queryer) error {
		result, err := q.ExecContext(ctx,
			`UPDATE api_key SET revoked_at = ? WHERE id = ? AND org_id = ? AND revoked_at IS NULL`,
			nowMillis(), keyID, orgID,
		)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		revoked = n > 0
		if !revoked {
			return nil
		}
		return recordAudit(ctx, q, AuditEvent{OrgID: orgID, ActorID: userID, Action: "api_key.revoked", Target: keyID})
	})
	return revoked, err
}

// VerifyAPIKey authenticates a presented secret, returning ErrNotFound for an
// unknown, revoked or expired key.
//
// The lookup is by hash, so the secret itself is never compared against
// stored material and an attacker who can read the table still cannot sign in
// with what they found.
//
// The use is recorded only when the stored last-used time is missing or at
// least lastUsedResolution old, so a key in steady use costs one read here,
// not a read and a write.
func (s *Store) VerifyAPIKey(ctx context.Context, secret string) (*APIKey, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+apiKeyColumns+` FROM api_key WHERE hash = ?`, HashSecret(secret))

	key, err := scanAPIKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	now := nowMillis()
	if key.RevokedAt != nil || (key.ExpiresAt != nil && *key.ExpiresAt < now) {
		return nil, ErrNotFound
	}
	if key.LastUsedAt == nil || now-*key.LastUsedAt >= lastUsedResolution.Milliseconds() {
		if _, err := s.db.ExecContext(ctx, `UPDATE api_key SET last_used_at = ? WHERE id = ?`, now, key.ID); err != nil {
			return nil, err
		}
		key.LastUsedAt = &now
	}
	return key, nil
}

func scanAPIKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	var (
		k                    APIKey
		scopes               string
		createdBy, replaced  sql.NullString
		lastUsed             sql.NullInt64
		expiresAt, revokedAt sql.NullInt64
	)
	if err := row.Scan(&k.ID, &k.OrgID, &k.Label, &k.Prefix, &scopes, &createdBy, &k.CreatedAt, &lastUsed, &expiresAt, &revokedAt, &replaced); err != nil {
		return nil, err
	}
	k.Scopes = strings.Split(scopes, ",")
	k.CreatedBy, k.ReplacedBy = nullString(createdBy), nullString(replaced)
	k.LastUsedAt, k.ExpiresAt, k.RevokedAt = nullTime(lastUsed), nullTime(expiresAt), nullTime(revokedAt)
	return &k, nil
}
