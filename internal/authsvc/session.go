package authsvc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/store"
)

// Browser sessions.
//
// The cookie carries nothing but an opaque session id and a signature over
// it. Who that session belongs to, which organisation it has selected and
// what the user may do are all read back from the database on every request,
// so a role change or a disabled account takes effect on the next request
// rather than at the next sign-in.

type contextKey int

const (
	sessionKey contextKey = iota
	userKey
	magicTargetKey
)

// sessionData is the stored document. Everything here is server-side; none of
// it is visible to or editable by the browser.
type sessionData struct {
	UserID   string `json:"userId,omitempty"`
	OrgID    string `json:"orgId,omitempty"`
	ReturnTo string `json:"returnTo,omitempty"`
	// Desktop is where a native client is waiting for its sign-in code, and
	// the PKCE challenge it must redeem that code against. Nil for a browser.
	Desktop *desktopHandoff `json:"desktop,omitempty"`
	Flow    *oauthFlow      `json:"flow,omitempty"`
}

// oauthFlow is the one-shot state an authorisation round-trip must remember
// across the redirect to the provider and back.
type oauthFlow struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Nonce    string `json:"nonce,omitempty"`
	Verifier string `json:"verifier,omitempty"`
	Locale   string `json:"locale,omitempty"`
}

type session struct {
	id      string
	data    sessionData
	expires time.Time
	// What the request read about the session's user besides the user,
	// with the session: the parts its route asked for (see sessionParts).
	memberships []store.Membership
	identities  []store.Identity
}

// sessions issues, resolves and expires browser sessions.
type sessions struct {
	store  *store.Store
	secret []byte
	ttl    time.Duration
	name   string
	secure bool
	logger *slog.Logger
}

func newSessions(st *store.Store, secret string, ttl time.Duration, production bool, logger *slog.Logger) *sessions {
	name := "timlee.sid"
	if production {
		// The __Host- prefix makes the browser refuse the cookie unless it is
		// Secure, has Path=/ and no Domain, so a sibling subdomain or a
		// plain-http response cannot plant or overwrite a session cookie.
		// Development runs over http, where such a cookie is rejected outright.
		name = "__Host-timlee.sid"
	}
	return &sessions{store: st, secret: []byte(secret), ttl: ttl, name: name, secure: production, logger: logger}
}

// sign returns the cookie value for a session id: the id and an HMAC over it,
// so a forged or edited id is rejected before it ever reaches the database.
func (m *sessions) sign(id string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(id))
	return id + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *sessions) unsign(value string) (string, bool) {
	id, _, found := strings.Cut(value, ".")
	if !found || id == "" {
		return "", false
	}
	// Constant time: the comparison is over attacker-supplied material.
	if !store.SecretsMatch(value, m.sign(id)) {
		return "", false
	}
	return id, true
}

// cookieID is the session id the request's cookie carries, if its signature
// holds. It says nothing about whether that session still exists.
func (m *sessions) cookieID(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(m.name)
	if err != nil {
		return "", false
	}
	return m.unsign(cookie.Value)
}

// resolve reads the session a request presents, the user it belongs to and
// the parts of that user the route reads, from one snapshot of the database
// (store.ViewSession). The user is read on every request, so a disabled
// account loses access at once. It never writes: the relayed lookup on
// /internal/verify-session must not create, extend or re-issue anything on
// the browser's behalf.
//
// No cookie, an unknown or expired session and an unreadable one all resolve
// to nil. An error means the store could not answer, and the request can be
// served neither as signed in nor as signed out.
func (m *sessions) resolve(r *http.Request, parts store.SessionParts) (*session, *store.User, error) {
	id, ok := m.cookieID(r)
	if !ok {
		return nil, nil, nil
	}

	s := &session{id: id}
	unreadable := false
	view, err := m.store.ViewSession(r.Context(), id, parts, func(document []byte) (string, error) {
		if err := json.Unmarshal(document, &s.data); err != nil {
			unreadable = true
			return "", err
		}
		return s.data.UserID, nil
	})
	switch {
	case unreadable:
		// A corrupt row costs one session, not every request that presents it.
		m.logger.Warn("discarding unreadable session row")
		_ = m.store.DeleteSession(r.Context(), id)
		return nil, nil, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, nil, nil
	case err != nil:
		return nil, nil, err
	}
	s.expires = time.UnixMilli(view.ExpiresAt)
	s.memberships, s.identities = view.Memberships, view.Identities
	return s, view.User, nil
}

// issue starts a new session, replacing any the caller already held.
//
// The id is always new: a fixed pre-authentication cookie must not be usable
// to ride the session the sign-in just created.
func (m *sessions) issue(w http.ResponseWriter, r *http.Request, data sessionData) (*session, error) {
	if previous, ok := m.cookieID(r); ok {
		_ = m.store.DeleteSession(r.Context(), previous)
	}
	s := &session{id: store.NewSessionID(), data: data}
	if err := m.save(w, r, s); err != nil {
		// Nothing can ever present a session whose cookie was refused, so it
		// is removed rather than left to expire. A misconfigured deployment
		// would otherwise write one unreachable row per sign-in attempt.
		_ = m.store.DeleteSession(r.Context(), s.id)
		return nil, err
	}
	return s, nil
}

// save persists the document and refreshes the cookie.
func (m *sessions) save(w http.ResponseWriter, r *http.Request, s *session) error {
	document, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	expires := time.Now().Add(m.ttl)
	if err := m.store.PutSession(r.Context(), s.id, document, expires.UnixMilli()); err != nil {
		return err
	}
	s.expires = expires
	return m.setCookie(w, r, m.sign(s.id), expires)
}

// refreshEvery is how often an active session's expiry is pushed back: a
// tenth of its lifetime, at most five minutes. Refreshing on every request
// made each signed-in page view a write; this way an idle session lapses at
// most that much sooner than it would have.
func (m *sessions) refreshEvery() time.Duration { return min(5*time.Minute, m.ttl/10) }

// touch extends a rolling session without rewriting its document, at most
// once per refreshEvery.
func (m *sessions) touch(w http.ResponseWriter, r *http.Request, s *session) {
	if time.Until(s.expires) > m.ttl-m.refreshEvery() {
		return
	}
	expires := time.Now().Add(m.ttl)
	if err := m.store.TouchSession(r.Context(), s.id, expires.UnixMilli()); err != nil {
		m.logger.Warn("could not extend session", "error", err)
		return
	}
	s.expires = expires
	// A refusal here costs a rolling refresh, not the request: the caller
	// already holds a working cookie, or it could not have got this far.
	if err := m.setCookie(w, r, m.sign(s.id), expires); err != nil {
		m.logger.Warn("could not refresh the session cookie", "error", err, "path", r.URL.Path)
	}
}

// destroy ends the session the request presents and clears its cookie.
//
// The id comes from the signed cookie rather than a lookup, so a session that
// cannot be read is still deleted. When the delete fails the cookie is kept:
// clearing it would leave the session valid on the server until it expires,
// with no way left for the browser to retry the sign-out.
func (m *sessions) destroy(w http.ResponseWriter, r *http.Request) error {
	if id, ok := m.cookieID(r); ok {
		if err := m.store.DeleteSession(r.Context(), id); err != nil {
			return err
		}
	}
	// Browsers ignore a __Host- Set-Cookie without Secure, deletions
	// included, so the clearing cookie must carry the same attributes.
	replaceCookie(w, &http.Cookie{
		Name:     m.name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// errInsecureRequest is returned when a Secure cookie cannot honestly be
// set, because the request did not arrive over https.
//
// The browser would discard such a cookie without telling anyone, so the
// alternative is a user who completes sign-in and lands signed out with
// nothing logged. Almost always a deployment fault rather than a user one:
// TLS is terminated by a proxy and TIMLEE_TRUST_PROXY was never set, so the
// forwarded scheme is not believed.
var errInsecureRequest = errors.New(
	"refusing to issue a Secure session cookie: this request did not arrive over https " +
		"(set TIMLEE_TRUST_PROXY=1 if TLS is terminated by a trusted proxy)")

func (m *sessions) setCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time) error {
	if m.secure && !isSecureRequest(r) {
		return errInsecureRequest
	}
	replaceCookie(w, &http.Cookie{
		Name:    m.name,
		Value:   value,
		Path:    "/",
		Expires: expires,
		MaxAge:  int(time.Until(expires).Seconds()),
		// Cannot be Strict: an identity provider redirects the browser back
		// to us as a cross-site navigation, and a Strict cookie would not be
		// sent on it.
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   m.secure,
	})
	return nil
}

// replaceCookie sets a cookie, dropping any earlier Set-Cookie for the same
// name on this response. A rolling refresh followed by a re-issued session
// would otherwise emit two, leaving which one survives up to the client.
func replaceCookie(w http.ResponseWriter, c *http.Cookie) {
	if existing := w.Header().Values("Set-Cookie"); len(existing) > 0 {
		kept := make([]string, 0, len(existing))
		for _, line := range existing {
			if name, _, _ := strings.Cut(line, "="); name != c.Name {
				kept = append(kept, line)
			}
		}
		w.Header()["Set-Cookie"] = kept
	}
	http.SetCookie(w, c)
}

// isSecureRequest reports whether this request reached us over https.
//
// X-Forwarded-Proto is only believed when a trusted proxy is declared, for
// the same reason X-Forwarded-For is: any client can send the header, and
// believing it unconditionally lets one claim a plain-http request was
// secure. The rightmost entry is the one our own proxy appended.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !httpx.TrustingProxy() {
		return false
	}
	forwarded := r.Header.Get("X-Forwarded-Proto")
	if forwarded == "" {
		return false
	}
	parts := strings.Split(forwarded, ",")
	return strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "https")
}

// prune deletes expired sessions, spent sign-in tokens and settled
// invitations until ctx ends. Housekeeping, not correctness: reads already
// ignore expired rows.
func (m *sessions) prune(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := m.store.PruneSessions(ctx); err != nil {
				m.logger.Warn("session prune failed", "error", err)
			} else if n > 0 {
				m.logger.Debug("pruned expired sessions", "count", n)
			}
			if _, err := m.store.PruneLoginTokens(ctx); err != nil {
				m.logger.Warn("login token prune failed", "error", err)
			}
			if _, err := m.store.PruneInvitations(ctx); err != nil {
				m.logger.Warn("invitation prune failed", "error", err)
			}
		}
	}
}

/* ------------------------------------------------------- request scope -- */

func sessionOf(r *http.Request) *session {
	s, _ := r.Context().Value(sessionKey).(*session)
	return s
}

func userOf(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// withMagicTarget carries the address a sign-in link was requested for, so
// the rate limiter can charge the budget to the source and the target both.
func withMagicTarget(r *http.Request, email string) context.Context {
	return context.WithValue(r.Context(), magicTargetKey, email)
}
