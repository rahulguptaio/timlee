package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Role is a membership level. Ranked so a check can be "at least this role".
type Role string

// Roles, weakest first.
const (
	RoleViewer Role = "viewer"
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

var roleRank = map[Role]int{RoleViewer: 0, RoleMember: 1, RoleAdmin: 2, RoleOwner: 3}

// AtLeast reports whether r satisfies a minimum. An unknown role satisfies
// nothing, so a row with a corrupt value fails closed.
func (r Role) AtLeast(minimum Role) bool {
	have, ok := roleRank[r]
	if !ok {
		return false
	}
	need, ok := roleRank[minimum]
	return ok && have >= need
}

// Outranks reports whether r is strictly stronger than other. An unknown role
// outranks nothing, for the same fail-closed reason.
func (r Role) Outranks(other Role) bool {
	have, ok := roleRank[r]
	if !ok {
		return false
	}
	theirs, ok := roleRank[other]
	return !ok || have > theirs
}

// Valid reports whether r is a role this system recognises.
func (r Role) Valid() bool {
	_, ok := roleRank[r]
	return ok
}

// ErrLastOwner is returned by any change that would leave an organisation
// with nobody who can administer it. A tenant that demotes or removes its
// only owner cannot undo that itself: there is no one left with the authority
// to promote anyone, so support has to reach into the database. Refusing the
// change is the only recoverable outcome.
var ErrLastOwner = errors.New("an organisation must keep at least one owner")

// Org is a tenant.
type Org struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	CreatedAt int64  `json:"-"`
}

// Membership is an org as seen by one of its members.
type Membership struct {
	Org
	Role Role `json:"role"`
}

// Member is a user as seen from inside an org.
type Member struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	Role      Role   `json:"role"`
	JoinedAt  int64  `json:"joinedAt"`
}

// CreateOrg makes an organisation and installs ownerID as its owner.
func (s *Store) CreateOrg(ctx context.Context, name, ownerID string) (string, error) {
	var id string
	err := s.transact(ctx, func(q queryer) error {
		var err error
		id, err = createOrg(ctx, q, name, ownerID)
		return err
	})
	return id, err
}

func createOrg(ctx context.Context, q queryer, name, ownerID string) (string, error) {
	id := newOrgID()
	now := nowMillis()
	// The id fragment keeps slugs unique without a retry loop on collision.
	slug := slugify(name) + "-" + strings.ToLower(id[4:10])

	if _, err := q.ExecContext(ctx, `INSERT INTO org (id, name, slug, created_at) VALUES (?, ?, ?, ?)`, id, name, slug, now); err != nil {
		return "", err
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO membership (org_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		id, ownerID, string(RoleOwner), now,
	); err != nil {
		return "", err
	}
	return id, nil
}

// AddMember grants a user a role in an org, replacing any existing one.
func (s *Store) AddMember(ctx context.Context, orgID, userID string, role Role) error {
	return s.transact(ctx, func(q queryer) error { return addMember(ctx, q, orgID, userID, role) })
}

func addMember(ctx context.Context, q queryer, orgID, userID string, role Role) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO membership (org_id, user_id, role, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(org_id, user_id) DO UPDATE SET role = excluded.role`,
		orgID, userID, string(role), nowMillis(),
	)
	return err
}

// SetMemberRole changes an existing member's role, refusing the change that
// would leave the organisation without an owner.
func (s *Store) SetMemberRole(ctx context.Context, orgID, userID string, role Role) error {
	return s.transact(ctx, func(q queryer) error {
		current, err := roleOf(ctx, q, orgID, userID)
		if err != nil {
			return err
		}
		if current == "" {
			return ErrNotFound
		}
		if current == role {
			return nil
		}
		// Demoting an owner is only safe while another owner remains.
		if current == RoleOwner {
			owners, err := countOwners(ctx, q, orgID)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrLastOwner
			}
		}
		_, err = q.ExecContext(ctx, `UPDATE membership SET role = ? WHERE org_id = ? AND user_id = ?`,
			string(role), orgID, userID)
		return err
	})
}

// RemoveMember ends a membership. The account itself is untouched: a person
// leaving one organisation keeps their identity, their other memberships and
// their sign-in methods.
//
// Keys that member minted for this organisation are revoked in the same
// transaction. They are org-scoped credentials issued on the authority of a
// membership that no longer exists, and leaving them live would let someone
// keep calling the inference endpoint after their access was withdrawn.
// revokedKeys counts the keys that stopped working: one already past its
// expiry is left as it is.
func (s *Store) RemoveMember(ctx context.Context, orgID, userID string) (revokedKeys int64, err error) {
	err = s.transact(ctx, func(q queryer) error {
		current, err := roleOf(ctx, q, orgID, userID)
		if err != nil {
			return err
		}
		if current == "" {
			return ErrNotFound
		}
		if current == RoleOwner {
			owners, err := countOwners(ctx, q, orgID)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrLastOwner
			}
		}

		if _, err := q.ExecContext(ctx, `DELETE FROM membership WHERE org_id = ? AND user_id = ?`, orgID, userID); err != nil {
			return err
		}

		now := nowMillis()
		result, err := q.ExecContext(ctx,
			`UPDATE api_key SET revoked_at = ?
			  WHERE org_id = ? AND created_by = ? AND revoked_at IS NULL
			    AND (expires_at IS NULL OR expires_at > ?)`,
			now, orgID, userID, now,
		)
		if err != nil {
			return err
		}
		revokedKeys, err = result.RowsAffected()
		return err
	})
	return revokedKeys, err
}

// RenameOrg changes an organisation's name and records who did it, with the
// old and new names. It reports the name it replaced.
func (s *Store) RenameOrg(ctx context.Context, orgID, actorID, name string) (previous string, err error) {
	err = s.transact(ctx, func(q queryer) error {
		err := q.QueryRowContext(ctx, `SELECT name FROM org WHERE id = ?`, orgID).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if previous == name {
			return nil
		}
		if _, err := q.ExecContext(ctx, `UPDATE org SET name = ? WHERE id = ?`, name, orgID); err != nil {
			return err
		}
		metadata, err := json.Marshal(map[string]string{"from": previous, "to": name})
		if err != nil {
			return err
		}
		return recordAudit(ctx, q, AuditEvent{
			OrgID: orgID, ActorID: actorID, Action: "org.renamed", Target: orgID, Metadata: string(metadata),
		})
	})
	return previous, err
}

// DeleteOrg deletes an organisation with everything that belongs to it:
// its memberships, API keys, invitations and audit log, which the schema
// deletes with it. It reports how many members it had and how many of its
// keys still worked.
func (s *Store) DeleteOrg(ctx context.Context, orgID string) (members, liveKeys int64, err error) {
	err = s.transact(ctx, func(q queryer) error {
		now := nowMillis()
		err := q.QueryRowContext(ctx,
			`SELECT (SELECT count(*) FROM membership WHERE org_id = ?1),
			        (SELECT count(*) FROM api_key WHERE org_id = ?1 AND revoked_at IS NULL
			                                        AND (expires_at IS NULL OR expires_at > ?2))`,
			orgID, now).Scan(&members, &liveKeys)
		if err != nil {
			return err
		}
		result, err := q.ExecContext(ctx, `DELETE FROM org WHERE id = ?`, orgID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return cmp.Or(err, ErrNotFound)
		}
		return nil
	})
	return members, liveKeys, err
}

// CountOwners reports how many owners an organisation has.
func (s *Store) CountOwners(ctx context.Context, orgID string) (int, error) {
	return countOwners(ctx, s.read, orgID)
}

func countOwners(ctx context.Context, q queryer, orgID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM membership WHERE org_id = ? AND role = ?`,
		orgID, string(RoleOwner)).Scan(&n)
	return n, err
}

// OrgByID returns one organisation, or ErrNotFound.
func (s *Store) OrgByID(ctx context.Context, id string) (*Org, error) {
	var o Org
	err := s.read.QueryRowContext(ctx, `SELECT id, name, slug, created_at FROM org WHERE id = ?`, id).
		Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// OrgsForUser lists the organisations a user belongs to, oldest first, with
// the role they hold in each.
func (s *Store) OrgsForUser(ctx context.Context, userID string) ([]Membership, error) {
	return orgsForUser(ctx, s.read, userID)
}

// The likelihood() in these two reads changes no plan: both still search
// their user_id index. It hands the planner the term's selectivity, so the
// planner no longer estimates it from the bound value. When it does, SQLite
// marks the statement for re-preparing should that value change, and the
// driver clears every statement's bindings after use (sqlite3_clear_bindings),
// which re-prepares any statement so marked: these two were recompiled on
// every call.
func orgsForUser(ctx context.Context, q queryer, userID string) ([]Membership, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT org.id, org.name, org.slug, org.created_at, membership.role
		   FROM org
		   JOIN membership ON membership.org_id = org.id
		  WHERE likelihood(membership.user_id = ?, 0.0625)
		  ORDER BY org.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Membership{}
	for rows.Next() {
		var m Membership
		var role string
		if err := rows.Scan(&m.ID, &m.Name, &m.Slug, &m.CreatedAt, &role); err != nil {
			return nil, err
		}
		m.Role = Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// RoleOf returns the caller's role, or "" when they are not a member.
func (s *Store) RoleOf(ctx context.Context, orgID, userID string) (Role, error) {
	return roleOf(ctx, s.read, orgID, userID)
}

func roleOf(ctx context.Context, q queryer, orgID, userID string) (Role, error) {
	var role string
	err := q.QueryRowContext(ctx, `SELECT role FROM membership WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return Role(role), nil
}

// Members lists everyone in an org, in join order.
func (s *Store) Members(ctx context.Context, orgID string) ([]Member, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT app_user.id, app_user.email, app_user.name, app_user.avatar_url, membership.role, membership.created_at
		   FROM membership
		   JOIN app_user ON app_user.id = membership.user_id
		  WHERE membership.org_id = ?
		  ORDER BY membership.created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Member{}
	for rows.Next() {
		var (
			m      Member
			name   sql.NullString
			avatar sql.NullString
			role   string
		)
		if err := rows.Scan(&m.ID, &m.Email, &name, &avatar, &role, &m.JoinedAt); err != nil {
			return nil, err
		}
		m.Name, m.AvatarURL, m.Role = nullString(name), nullString(avatar), Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(value string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(value), "-"), "-")
	if len(s) > 32 {
		s = s[:32]
	}
	if s == "" {
		return "workspace"
	}
	return s
}
