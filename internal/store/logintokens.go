package store

import (
	"context"
	"database/sql"
	"errors"
)

// LoginToken is a single-use sign-in credential. Only its hash is stored, for
// the same reason API keys are: a leaked database must not grant sign-in.
type LoginToken struct {
	ID        string
	Email     string
	CreatedAt int64
	ExpiresAt int64
	ReturnTo  string
	Locale    string
	// CodeChallenge binds the token to the client that asked for it. Empty
	// for a magic link, which is bound by the mailbox it was sent to instead.
	CodeChallenge string
	// flowHash binds a desktop code to the app window that started its
	// sign-in; see RedeemDesktopCode.
	flowHash string
}

// ErrPKCEVerificationFailed is returned when a desktop code is presented with
// a verifier that does not match its challenge. The code is spent all the
// same: see RedeemDesktopCode.
var ErrPKCEVerificationFailed = errors.New("code verifier does not match the challenge")

// ErrDesktopFlowMismatch is returned when a desktop code is redeemed outside
// the app window that started its sign-in: the flow id that window holds is
// missing or different. The code is spent all the same.
var ErrDesktopFlowMismatch = errors.New("desktop code redeemed outside the window that started its sign-in")

// CreateLoginToken persists a minted token. The caller keeps the secret and
// delivers it; only the hash reaches the database.
//
// codeChallenge is the base64url SHA-256 of the client's verifier, and
// flowHash the HashSecret of the id of the app window that started the
// sign-in, which is kept no more than the token is; both are empty for an
// unbound token.
func (s *Store) CreateLoginToken(ctx context.Context, email, hash, returnTo, locale, codeChallenge, flowHash string, ttlMillis int64) (*LoginToken, error) {
	now := nowMillis()
	t := &LoginToken{
		// The id is derived from the hash rather than minted separately, so
		// one token cannot be inserted twice under different ids.
		ID:            "lnk_" + hash[:16],
		Email:         email,
		CreatedAt:     now,
		ExpiresAt:     now + ttlMillis,
		ReturnTo:      returnTo,
		Locale:        locale,
		CodeChallenge: codeChallenge,
		flowHash:      flowHash,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO login_token (id, email, hash, created_at, expires_at, return_to, locale, code_challenge, flow_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, email, hash, t.CreatedAt, t.ExpiresAt, nullable(returnTo), nullable(locale), nullable(codeChallenge), nullable(t.flowHash),
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// A claim spends a live token and hands it back in one statement. The UPDATE
// is the claim: two requests racing for one token cannot both change its row,
// so exactly one of them gets it. A mailed link and a desktop code are told
// apart by the challenge, so neither path can spend the other's tokens.
const (
	claimMailedLink = `UPDATE login_token SET consumed_at = ?
	  WHERE hash = ? AND consumed_at IS NULL AND expires_at > ? AND code_challenge IS NULL
	  RETURNING id, email, created_at, expires_at, return_to, locale, code_challenge, flow_hash`
	claimDesktopCode = `UPDATE login_token SET consumed_at = ?
	  WHERE hash = ? AND consumed_at IS NULL AND expires_at > ? AND code_challenge IS NOT NULL
	  RETURNING id, email, created_at, expires_at, return_to, locale, code_challenge, flow_hash`
)

// ConsumeLoginToken redeems a mailed sign-in link exactly once, returning
// ErrNotFound when it is unknown, spent or expired. A token bound to a desktop
// app's challenge is not a mailed link: it is left untouched here, for the
// app's own exchange.
func (s *Store) ConsumeLoginToken(ctx context.Context, secret string) (*LoginToken, error) {
	return s.claimLoginToken(ctx, claimMailedLink, secret)
}

// RedeemDesktopCode trades a desktop sign-in code, the app's PKCE verifier and
// the flow id of the window redeeming it for the token behind the code.
//
// The first attempt spends the code, whatever the verifier. Whoever
// intercepted it gets one guess rather than the code's whole lifetime, and the
// app's own exchange then fails, so an interception shows up as a failed
// sign-in instead of passing unnoticed. It returns ErrNotFound for an unknown,
// spent, expired or unbound code; ErrPKCEVerificationFailed for a verifier
// that does not match; and ErrDesktopFlowMismatch for a flow id that is not
// the one the code was minted for, which is what an attacker's own code,
// opened in somebody else's browser, presents. The code is spent either way.
func (s *Store) RedeemDesktopCode(ctx context.Context, secret, verifier, flow string) (*LoginToken, error) {
	t, err := s.claimLoginToken(ctx, claimDesktopCode, secret)
	if err != nil {
		return nil, err
	}
	if !PKCEMatches(verifier, t.CodeChallenge) {
		return nil, ErrPKCEVerificationFailed
	}
	// No flow redeems anything, and a code minted without one, which holds
	// no hash to match, is redeemed by none.
	if flow == "" || !SecretsMatch(HashSecret(flow), t.flowHash) {
		return nil, ErrDesktopFlowMismatch
	}
	return t, nil
}

// DesktopCodePending reports whether secret is a live desktop code nobody has
// tried yet, without spending it: a desktop sign-in link opened in a browser
// is handed on to the app, whose exchange is the one attempt that counts.
func (s *Store) DesktopCodePending(ctx context.Context, secret string) (bool, error) {
	var one int
	err := s.read.QueryRowContext(ctx,
		`SELECT 1 FROM login_token
		  WHERE hash = ? AND consumed_at IS NULL AND expires_at > ? AND code_challenge IS NOT NULL`,
		HashSecret(secret), nowMillis()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) claimLoginToken(ctx context.Context, claim, secret string) (*LoginToken, error) {
	now := nowMillis()
	var (
		t         LoginToken
		returnTo  sql.NullString
		locale    sql.NullString
		challenge sql.NullString
		flowHash  sql.NullString
	)
	err := s.db.QueryRowContext(ctx, claim, now, HashSecret(secret), now).
		Scan(&t.ID, &t.Email, &t.CreatedAt, &t.ExpiresAt, &returnTo, &locale, &challenge, &flowHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.ReturnTo, t.Locale = nullString(returnTo), nullString(locale)
	t.CodeChallenge, t.flowHash = nullString(challenge), nullString(flowHash)
	return &t, nil
}

// PruneLoginTokens removes spent and expired tokens.
func (s *Store) PruneLoginTokens(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM login_token WHERE expires_at <= ? OR consumed_at IS NOT NULL`, nowMillis())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
