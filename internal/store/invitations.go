package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Organisation invitations.
//
// An invitation is a bearer credential scoped to one organisation and one
// address: whoever holds the token may join that org at the role it names.
// It is therefore stored the way every other credential here is — hashed,
// expiring, and claimed exactly once.

// Invitation is a pending or settled invitation. The token never appears on
// this type: it is returned once by NewInvitationToken and mailed out.
type Invitation struct {
	ID        string `json:"id"`
	OrgID     string `json:"orgId"`
	Email     string `json:"email"`
	Role      Role   `json:"role"`
	InvitedBy string `json:"invitedBy"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// ErrInvitationEmailMismatch is returned when the signed-in account is not
// the one that was invited.
//
// It is distinct from ErrNotFound on purpose. The caller already holds the
// token, so telling them it belongs to another address reveals nothing they
// could not test by guessing, and it is the difference between a usable error
// message and "this link is invalid".
var ErrInvitationEmailMismatch = errors.New("invitation was sent to a different address")

// NewInvitationToken mints an invitation credential, returning the secret to
// mail and the hash to persist.
func NewInvitationToken() (secret, hash string) {
	secret = token(32)
	return secret, HashSecret(secret)
}

// CreateInvitation invites an address, rotating any invitation that address
// already holds for this organisation rather than minting a second one.
//
// Re-inviting is how an admin re-sends a link that was lost or has expired,
// so it refreshes the token, the expiry and the role in place. The previous
// token stops working, which is the point: one live invitation per address.
func (s *Store) CreateInvitation(ctx context.Context, orgID, email string, role Role, invitedBy, hash string, ttlMillis int64) (*Invitation, error) {
	now := nowMillis()
	address := strings.ToLower(strings.TrimSpace(email))

	invitation := &Invitation{
		OrgID:     orgID,
		Email:     address,
		Role:      role,
		InvitedBy: invitedBy,
		CreatedAt: now,
		ExpiresAt: now + ttlMillis,
	}

	err := s.transact(ctx, func(q queryer) error {
		var existing string
		err := q.QueryRowContext(ctx,
			`SELECT id FROM invitation
			  WHERE org_id = ? AND email = ? AND accepted_at IS NULL AND revoked_at IS NULL`,
			orgID, address,
		).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		if existing != "" {
			invitation.ID = existing
			_, err := q.ExecContext(ctx,
				`UPDATE invitation
				    SET role = ?, hash = ?, invited_by = ?, created_at = ?, expires_at = ?
				  WHERE id = ?`,
				string(role), hash, nullable(invitedBy), now, invitation.ExpiresAt, existing,
			)
			return err
		}

		invitation.ID = "inv_" + token(12)
		_, err = q.ExecContext(ctx,
			`INSERT INTO invitation (id, org_id, email, role, hash, invited_by, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			invitation.ID, orgID, address, string(role), hash, nullable(invitedBy), now, invitation.ExpiresAt,
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return invitation, nil
}

// PendingInvitations lists the invitations an organisation has outstanding,
// newest first.
//
// Expired ones are included: an admin looking at the panel needs to see that
// a link went stale, because the fix is to re-invite. ExpiresAt says which is
// which.
func (s *Store) PendingInvitations(ctx context.Context, orgID string) ([]Invitation, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, org_id, email, role, invited_by, created_at, expires_at
		   FROM invitation
		  WHERE org_id = ? AND accepted_at IS NULL AND revoked_at IS NULL
		  ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Invitation{}
	for rows.Next() {
		var (
			i         Invitation
			role      string
			invitedBy sql.NullString
		)
		if err := rows.Scan(&i.ID, &i.OrgID, &i.Email, &role, &invitedBy, &i.CreatedAt, &i.ExpiresAt); err != nil {
			return nil, err
		}
		i.Role, i.InvitedBy = Role(role), nullString(invitedBy)
		out = append(out, i)
	}
	return out, rows.Err()
}

// InvitationByID returns one invitation regardless of its state, scoped to
// the organisation so one tenant cannot read another's by guessing an id.
func (s *Store) InvitationByID(ctx context.Context, orgID, id string) (*Invitation, error) {
	var (
		i         Invitation
		role      string
		invitedBy sql.NullString
	)
	err := s.read.QueryRowContext(ctx,
		`SELECT id, org_id, email, role, invited_by, created_at, expires_at
		   FROM invitation WHERE id = ? AND org_id = ?`, id, orgID,
	).Scan(&i.ID, &i.OrgID, &i.Email, &role, &invitedBy, &i.CreatedAt, &i.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	i.Role, i.InvitedBy = Role(role), nullString(invitedBy)
	return &i, nil
}

// RevokeInvitation withdraws a pending invitation, reporting whether there
// was one to withdraw. The org id is part of the WHERE clause so one tenant
// cannot revoke another's invitation by guessing its id.
func (s *Store) RevokeInvitation(ctx context.Context, orgID, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE invitation SET revoked_at = ?
		  WHERE id = ? AND org_id = ? AND accepted_at IS NULL AND revoked_at IS NULL`,
		nowMillis(), id, orgID,
	)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// AcceptInvitation redeems a token exactly once and installs the membership.
//
// The address on the invitation must be the signed-in account's own. Without
// that check anyone who intercepts a link — a forwarded mail, a shared
// screen, a log — joins someone else's organisation, which is account
// takeover with extra steps.
//
// An existing member is never demoted by accepting: the membership ends up at
// the higher of what they had and what they were offered. Lowering a role is
// what PATCH on the member is for, and that path has the last-owner guard
// this one would otherwise have to duplicate.
func (s *Store) AcceptInvitation(ctx context.Context, secret, userID, email string) (*Invitation, error) {
	hash := HashSecret(secret)
	address := strings.ToLower(strings.TrimSpace(email))
	now := nowMillis()

	var invitation *Invitation
	err := s.transact(ctx, func(q queryer) error {
		var (
			i         Invitation
			role      string
			invitedBy sql.NullString
		)
		err := q.QueryRowContext(ctx,
			`SELECT id, org_id, email, role, invited_by, created_at, expires_at
			   FROM invitation
			  WHERE hash = ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?`,
			hash, now,
		).Scan(&i.ID, &i.OrgID, &i.Email, &role, &invitedBy, &i.CreatedAt, &i.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			// Unknown, spent, revoked and expired are one answer: telling
			// them apart would say whether a guessed token ever existed.
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		i.Role, i.InvitedBy = Role(role), nullString(invitedBy)

		if i.Email != address {
			return ErrInvitationEmailMismatch
		}

		// The claim is the UPDATE, not the SELECT above: two requests
		// arriving with the same token race for the row, and only the one
		// that changed it may proceed.
		result, err := q.ExecContext(ctx,
			`UPDATE invitation SET accepted_at = ?, accepted_by = ?
			  WHERE id = ? AND accepted_at IS NULL AND revoked_at IS NULL`,
			now, userID, i.ID,
		)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}

		current, err := roleOf(ctx, q, i.OrgID, userID)
		if err != nil {
			return err
		}
		granted := i.Role
		if current.Outranks(granted) {
			granted = current
		}
		if err := addMember(ctx, q, i.OrgID, userID, granted); err != nil {
			return err
		}

		i.Role = granted
		invitation = &i
		return nil
	})
	if err != nil {
		return nil, err
	}
	return invitation, nil
}

// PruneInvitations removes settled and expired rows.
func (s *Store) PruneInvitations(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM invitation
		  WHERE expires_at <= ? OR accepted_at IS NOT NULL OR revoked_at IS NOT NULL`, nowMillis())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
