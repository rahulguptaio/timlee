package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// User is an account. One user may hold several identities.
type User struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	Name          string `json:"name"`
	AvatarURL     string `json:"avatarUrl"`
	Locale        string `json:"locale"`
	CreatedAt     int64  `json:"createdAt"`
	LastSeenAt    *int64 `json:"lastSeenAt"`
	DisabledAt    *int64 `json:"disabledAt"`
}

// Identity is one provider's view of a user.
type Identity struct {
	ID         string `json:"id"`
	UserID     string `json:"-"`
	Provider   string `json:"provider"`
	Subject    string `json:"-"`
	Email      string `json:"email"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
}

// Profile is a normalised provider profile, whatever shape it arrived in.
type Profile struct {
	Provider string
	// Subject is the provider's immutable identifier. Never the email:
	// addresses get reassigned, subjects do not.
	Subject string
	Email   string
	// EmailVerified is the provider's assertion, not ours. It alone decides
	// whether this identity may join an existing account.
	EmailVerified bool
	Name          string
	AvatarURL     string
	Locale        string
}

// ErrIdentityConflict is returned when an unverified address collides with an
// existing account. Linking would be account takeover and creating a second
// user would strand the address, so the sign-in is refused instead.
var ErrIdentityConflict = errors.New("email already registered to another sign-in method")

// ErrLastIdentity is returned when unlinking would lock the user out.
var ErrLastIdentity = errors.New("cannot unlink the only sign-in method")

const userColumns = `id, email, email_verified, name, avatar_url, locale, created_at, last_seen_at, disabled_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u          User
		name       sql.NullString
		avatar     sql.NullString
		locale     sql.NullString
		lastSeen   sql.NullInt64
		disabledAt sql.NullInt64
		verified   int
	)
	if err := row.Scan(&u.ID, &u.Email, &verified, &name, &avatar, &locale, &u.CreatedAt, &lastSeen, &disabledAt); err != nil {
		return nil, err
	}
	u.EmailVerified = verified == 1
	u.Name = nullString(name)
	u.AvatarURL = nullString(avatar)
	u.Locale = nullString(locale)
	u.LastSeenAt = nullTime(lastSeen)
	u.DisabledAt = nullTime(disabledAt)
	return &u, nil
}

// UserByID returns the account, or ErrNotFound.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return userByID(ctx, s.read, id)
}

func userByID(ctx context.Context, q queryer, id string) (*User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// UserByEmail looks an account up by its lower-cased address.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return userByEmail(ctx, s.read, email)
}

func userByEmail(ctx context.Context, q queryer, email string) (*User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM app_user WHERE email = ?`, strings.ToLower(email)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// UpdateUser applies the supplied fields; a nil field is left alone.
func (s *Store) UpdateUser(ctx context.Context, id string, name, avatarURL, locale *string) (*User, error) {
	var n, a, l any
	if name != nil {
		n = *name
	}
	if avatarURL != nil {
		a = *avatarURL
	}
	if locale != nil {
		l = nullable(*locale)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE app_user
		    SET name = COALESCE(?, name),
		        avatar_url = COALESCE(?, avatar_url),
		        locale = CASE WHEN ? IS NULL AND ? = 1 THEN NULL ELSE COALESCE(?, locale) END
		  WHERE id = ?`,
		n, a, l, locale != nil, l, id,
	); err != nil {
		return nil, err
	}
	return s.UserByID(ctx, id)
}

// IdentitiesForUser lists every sign-in method on an account.
func (s *Store) IdentitiesForUser(ctx context.Context, userID string) ([]Identity, error) {
	return identitiesForUser(ctx, s.read, userID)
}

func identitiesForUser(ctx context.Context, q queryer, userID string) ([]Identity, error) {
	// likelihood(): see orgsForUser.
	rows, err := q.QueryContext(ctx,
		`SELECT id, user_id, provider, subject, email, created_at, last_used_at
		   FROM identity WHERE likelihood(user_id = ?, 0.0625) ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Identity{}
	for rows.Next() {
		var (
			i        Identity
			email    sql.NullString
			lastUsed sql.NullInt64
		)
		if err := rows.Scan(&i.ID, &i.UserID, &i.Provider, &i.Subject, &email, &i.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		i.Email = nullString(email)
		i.LastUsedAt = nullTime(lastUsed)
		out = append(out, i)
	}
	return out, rows.Err()
}

// UnlinkIdentity removes one sign-in method, refusing to remove the last one.
func (s *Store) UnlinkIdentity(ctx context.Context, userID, identityID string) error {
	return s.transact(ctx, func(q queryer) error {
		remaining, err := identitiesForUser(ctx, q, userID)
		if err != nil {
			return err
		}
		if len(remaining) <= 1 {
			return ErrLastIdentity
		}
		result, err := q.ExecContext(ctx, `DELETE FROM identity WHERE id = ? AND user_id = ?`, identityID, userID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// UpsertFromProvider resolves a provider profile to a user, creating the
// account and its first organisation on first sign-in.
//
// Automatic linking by email happens only when the provider asserts the
// address is verified. Without that check, anyone who can register at a
// provider using someone else's unverified address takes over their account.
func (s *Store) UpsertFromProvider(ctx context.Context, p Profile) (user *User, created bool, err error) {
	email := strings.ToLower(strings.TrimSpace(p.Email))

	var conflictUserID string
	err = s.transact(ctx, func(q queryer) error {
		now := nowMillis()

		existing, err := findIdentity(ctx, q, p.Provider, p.Subject)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if existing != nil {
			if _, err := q.ExecContext(ctx, `UPDATE identity SET last_used_at = ? WHERE id = ?`, now, existing.ID); err != nil {
				return err
			}
			found, err := userByID(ctx, q, existing.UserID)
			if err != nil {
				return err
			}
			if _, err := q.ExecContext(ctx, `UPDATE app_user SET last_seen_at = ? WHERE id = ?`, now, found.ID); err != nil {
				return err
			}
			if found.Locale == "" && p.Locale != "" {
				if _, err := q.ExecContext(ctx, `UPDATE app_user SET locale = ? WHERE id = ?`, p.Locale, found.ID); err != nil {
					return err
				}
				found.Locale = p.Locale
			}
			user = found
			return nil
		}

		if email != "" {
			byEmail, err := userByEmail(ctx, q, email)
			switch {
			case err != nil && !errors.Is(err, ErrNotFound):
				return err
			case byEmail != nil && p.EmailVerified:
				// The provider vouched for the address, so this is the same person.
				user = byEmail
			case byEmail != nil:
				conflictUserID = byEmail.ID
				return ErrIdentityConflict
			}
		}

		if user == nil {
			id := newUserID()
			address := email
			if address == "" {
				address = id + "@users.noreply.timlee.local"
			}
			verified := 0
			if p.EmailVerified {
				verified = 1
			}
			if _, err := q.ExecContext(ctx,
				`INSERT INTO app_user (id, email, email_verified, name, avatar_url, locale, created_at, last_seen_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, address, verified, nullable(p.Name), nullable(p.AvatarURL), nullable(p.Locale), now, now,
			); err != nil {
				return err
			}

			found, err := userByID(ctx, q, id)
			if err != nil {
				return err
			}
			user, created = found, true
		}

		if user.Locale == "" && p.Locale != "" {
			if _, err := q.ExecContext(ctx, `UPDATE app_user SET locale = ? WHERE id = ?`, p.Locale, user.ID); err != nil {
				return err
			}
			user.Locale = p.Locale
		}

		if _, err := q.ExecContext(ctx,
			`INSERT INTO identity (id, user_id, provider, subject, email, created_at, last_used_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			newIdentityID(), user.ID, p.Provider, p.Subject, nullable(email), now, now,
		); err != nil {
			return err
		}

		// A brand-new user needs somewhere to work.
		if created {
			orgID, err := createOrg(ctx, q, defaultOrgName(p.Name, email), user.ID)
			if err != nil {
				return err
			}
			if err := recordAudit(ctx, q, AuditEvent{OrgID: orgID, ActorID: user.ID, Action: "org.created", Target: orgID}); err != nil {
				return err
			}
		}
		return nil
	})

	if errors.Is(err, ErrIdentityConflict) {
		// Recorded outside the transaction, which has just rolled back. A
		// burst of these against one address is an attempted takeover.
		_ = recordAudit(ctx, s.db, AuditEvent{
			ActorID:  conflictUserID,
			Action:   "auth.link_refused",
			Target:   p.Provider,
			Metadata: fmt.Sprintf(`{"reason":"unverified_email_collision","provider":%q}`, p.Provider),
		})
	}
	if err != nil {
		return nil, false, err
	}
	return user, created, nil
}

func findIdentity(ctx context.Context, q queryer, provider, subject string) (*Identity, error) {
	var (
		i        Identity
		email    sql.NullString
		lastUsed sql.NullInt64
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, user_id, provider, subject, email, created_at, last_used_at
		   FROM identity WHERE provider = ? AND subject = ?`, provider, subject,
	).Scan(&i.ID, &i.UserID, &i.Provider, &i.Subject, &email, &i.CreatedAt, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	i.Email = nullString(email)
	i.LastUsedAt = nullTime(lastUsed)
	return &i, nil
}

func defaultOrgName(name, email string) string {
	if name != "" {
		return strings.Split(name, " ")[0] + "'s workspace"
	}
	if email != "" {
		return strings.Split(email, "@")[0] + "'s workspace"
	}
	return "My workspace"
}
