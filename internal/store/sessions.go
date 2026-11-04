package store

import (
	"context"
	"database/sql"
	"errors"
)

// Browser session rows.
//
// The primary key is SHA-256 of the session id, not the id itself, so a copy
// of the database file cannot be turned back into working cookies. `expires_at`
// (epoch milliseconds) is authoritative: expired rows are invisible to reads
// and pruned periodically.

// SessionRow is a stored session document and its server-side expiry.
type SessionRow struct {
	Document  []byte
	ExpiresAt int64
}

// PutSession writes or replaces a session.
func (s *Store) PutSession(ctx context.Context, id string, document []byte, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session (sid, sess, expires_at) VALUES (?, ?, ?)
		 ON CONFLICT(sid) DO UPDATE SET sess = excluded.sess, expires_at = excluded.expires_at`,
		HashSecret(id), string(document), expiresAt,
	)
	return err
}

// GetSession reads a live session, returning ErrNotFound when it is absent or
// has expired. An expired row is deleted on the way out rather than left for
// the pruner: it has just been proven dead by a request that presented it.
func (s *Store) GetSession(ctx context.Context, id string) (*SessionRow, error) {
	key := HashSecret(id)

	var row SessionRow
	var document string
	err := s.read.QueryRowContext(ctx, `SELECT sess, expires_at FROM session WHERE sid = ?`, key).Scan(&document, &row.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.ExpiresAt <= nowMillis() {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM session WHERE sid = ?`, key)
		return nil, ErrNotFound
	}
	row.Document = []byte(document)
	return &row, nil
}

// SessionParts are what a caller reads about a session's user besides the
// user.
type SessionParts uint8

const (
	// WithMemberships reads the user's organisations and the role in each.
	WithMemberships SessionParts = 1 << iota
	// WithIdentities reads the user's sign-in methods.
	WithIdentities
)

// SessionView is a live session and what was read about its user, all from
// one snapshot of the database.
type SessionView struct {
	SessionRow
	// User is nil when the document names nobody, as during a sign-in that
	// has not finished, or names an account that no longer exists. The parts
	// are read only for a user.
	User        *User
	Memberships []Membership
	Identities  []Identity
}

// ViewSession reads a live session, the user its document names and the
// parts asked for, in one read-only transaction: every signed-in request
// starts here, and a page view used to make four separate reads of it.
//
// userOf names the user a document belongs to; the store reads documents no
// further. It returns ErrNotFound when no live session has this id, and an
// error from userOf as it is.
func (s *Store) ViewSession(ctx context.Context, id string, parts SessionParts, userOf func(document []byte) (string, error)) (*SessionView, error) {
	key := HashSecret(id)
	var view *SessionView
	expired := false
	err := s.read.snapshot(ctx, func(q queryer) error {
		var (
			document string
			row      SessionRow
		)
		err := q.QueryRowContext(ctx, `SELECT sess, expires_at FROM session WHERE sid = ?`, key).Scan(&document, &row.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.ExpiresAt <= nowMillis() {
			expired = true
			return ErrNotFound
		}
		row.Document = []byte(document)
		view = &SessionView{SessionRow: row}

		userID, err := userOf(row.Document)
		if err != nil || userID == "" {
			return err
		}
		user, err := userByID(ctx, q, userID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		view.User = user
		if parts&WithMemberships != 0 {
			if view.Memberships, err = orgsForUser(ctx, q, userID); err != nil {
				return err
			}
		}
		if parts&WithIdentities != 0 {
			if view.Identities, err = identitiesForUser(ctx, q, userID); err != nil {
				return err
			}
		}
		return nil
	})
	if expired {
		// As GetSession does, but after the snapshot: the read pool cannot
		// write.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM session WHERE sid = ?`, key)
	}
	if err != nil {
		return nil, err
	}
	return view, nil
}

// TouchSession moves a rolling session's expiry without rewriting it.
func (s *Store) TouchSession(ctx context.Context, id string, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE session SET expires_at = ? WHERE sid = ?`, expiresAt, HashSecret(id))
	return err
}

// DeleteSession ends one session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE sid = ?`, HashSecret(id))
	return err
}

// PruneSessions deletes expired rows and reports how many went.
func (s *Store) PruneSessions(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE expires_at <= ?`, nowMillis())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CountSessions counts live sessions.
func (s *Store) CountSessions(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM session WHERE expires_at > ?`, nowMillis()).Scan(&n)
	return n, err
}

// OtherSessions counts the live sessions of userID other than the one whose
// id is keep. The owner is read from the document, by the expression
// migration 009 indexes.
func (s *Store) OtherSessions(ctx context.Context, userID, keep string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT count(*) FROM session
		  WHERE json_extract(sess, '$.userId') = ? AND sid <> ? AND expires_at > ?`,
		userID, HashSecret(keep), nowMillis()).Scan(&n)
	return n, err
}

// EndOtherSessions ends every live session of userID except the one whose
// id is keep, and reports how many it ended. Each is gone for the very next
// request that presents it.
func (s *Store) EndOtherSessions(ctx context.Context, userID, keep string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM session
		  WHERE json_extract(sess, '$.userId') = ? AND sid <> ? AND expires_at > ?`,
		userID, HashSecret(keep), nowMillis())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
