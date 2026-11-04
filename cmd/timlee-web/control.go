package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
)

// The control plane's account and organisation API, called on a reader's
// behalf.
//
// The settings pages never let the browser talk to the control plane: every
// read and every change goes through this process, which presents the
// reader's own session cookie and — on anything that changes state — this
// site's public origin, the one the control plane's cross-site guard allows.
// That is what lets every settings form be a plain POST that works with
// scripting switched off, while the control plane keeps enforcing who may do
// what. Nothing here decides permissions; it only relays them.

// apiError is a refusal from the control plane, kept in its own terms so the
// handler that asked can decide what to tell a person.
type apiError struct {
	Status  int
	Message string
	Type    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("control plane %d %s: %s", e.Status, e.Type, e.Message)
}

// errUnreachable wraps a control plane that did not answer at all.
var errUnreachable = errors.New("control plane unreachable")

// statusOf returns the HTTP status behind err, or 0 when the control plane
// never answered.
func statusOf(err error) int {
	var refused *apiError
	if errors.As(err, &refused) {
		return refused.Status
	}
	return 0
}

/* -------------------------------------------------------------- shapes -- */

// role mirrors the control plane's ranking. It is restated here rather than
// imported so this binary does not link the database driver for four
// strings; a test holds the two to the same answers.
type role string

const (
	roleViewer role = "viewer"
	roleMember role = "member"
	roleAdmin  role = "admin"
	roleOwner  role = "owner"
)

// roles lists every role, weakest first, which is also the order a picker
// offers them in.
var roles = []role{roleViewer, roleMember, roleAdmin, roleOwner}

var roleRank = map[role]int{roleViewer: 0, roleMember: 1, roleAdmin: 2, roleOwner: 3}

func (r role) valid() bool {
	_, ok := roleRank[r]
	return ok
}

// atLeast fails closed on an unknown role, as the control plane does.
func (r role) atLeast(minimum role) bool {
	have, ok := roleRank[r]
	if !ok {
		return false
	}
	need, ok := roleRank[minimum]
	return ok && have >= need
}

// outranks reports whether r is strictly stronger than other. An unknown
// role outranks nothing, and is outranked by everything known.
func (r role) outranks(other role) bool {
	have, ok := roleRank[r]
	if !ok {
		return false
	}
	theirs, ok := roleRank[other]
	return !ok || have > theirs
}

// Label is the role as a person reads it.
func (r role) Label(l *i18n.Localizer) string {
	switch r {
	case roleOwner:
		return l.T("settings.roles.owner")
	case roleAdmin:
		return l.T("settings.roles.admin")
	case roleMember:
		return l.T("settings.roles.member")
	case roleViewer:
		return l.T("settings.roles.viewer")
	}
	return l.T("settings.roles.none")
}

// Blurb says what the role may do, in terms of what the control plane
// actually enforces today.
func (r role) Blurb(l *i18n.Localizer) string {
	switch r {
	case roleOwner:
		return l.T("settings.roles.ownerBlurb")
	case roleAdmin:
		return l.T("settings.roles.adminBlurb")
	case roleMember:
		return l.T("settings.roles.memberBlurb")
	case roleViewer:
		return l.T("settings.roles.viewerBlurb")
	}
	return ""
}

// canManage is the control plane's gate on every member, key and audit route.
func canManage(actor role) bool { return actor.atLeast(roleAdmin) }

// canGrant mirrors authsvc: nobody hands out more than they hold.
func canGrant(actor, granted role) bool { return granted.valid() && actor.atLeast(granted) }

// canActOn mirrors authsvc: nobody changes someone who outranks them.
func canActOn(actor, target role) bool { return canManage(actor) && !target.outranks(actor) }

type sessionUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
}

type membership struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role role   `json:"role"`
}

type identity struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Email      string `json:"email"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
}

// accountSession is GET /auth/session in full: the settings pages need the
// memberships for the organisation switcher and the identities for the
// profile, both of which the session already carries.
type accountSession struct {
	User       *sessionUser `json:"user"`
	Org        *membership  `json:"org"`
	Orgs       []membership `json:"orgs"`
	Identities []identity   `json:"identities"`
}

type member struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	Role      role   `json:"role"`
	JoinedAt  int64  `json:"joinedAt"`
}

// display is the name to show for a member, falling back to their address.
func (m member) display() string {
	if name := strings.TrimSpace(m.Name); name != "" {
		return name
	}
	return m.Email
}

type orgDetail struct {
	Org struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"org"`
	Role    role     `json:"role"`
	Members []member `json:"members"`
}

type apiKey struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	CreatedBy  string   `json:"createdBy"`
	CreatedAt  int64    `json:"createdAt"`
	LastUsedAt *int64   `json:"lastUsedAt"`
	ExpiresAt  *int64   `json:"expiresAt"`
	RevokedAt  *int64   `json:"revokedAt"`
	ReplacedBy string   `json:"replacedBy"`
}

// createdKey is a new key's one sight of its secret, from creating it or
// from rotating another key into it.
type createdKey struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Prefix    string `json:"prefix"`
	Secret    string `json:"secret"`
	ExpiresAt *int64 `json:"expiresAt"`
	// ReplacedKeyStopsAt is set by a rotation: when the key it replaces stops
	// working.
	ReplacedKeyStopsAt int64 `json:"replacedKeyStopsAt"`
}

type invitation struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      role   `json:"role"`
	InvitedBy string `json:"invitedBy"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// auditEvent is one event. ActorName, ActorEmail and TargetLabel are the
// names at the time it was recorded, empty for an event older than them.
type auditEvent struct {
	ID          string `json:"id"`
	ActorID     string `json:"actorId"`
	ActorName   string `json:"actorName"`
	ActorEmail  string `json:"actorEmail"`
	Action      string `json:"action"`
	Target      string `json:"target"`
	TargetLabel string `json:"targetLabel"`
	Metadata    string `json:"metadata"`
	CreatedAt   int64  `json:"createdAt"`
}

// auditQuery is a filter and a page of the audit log, as the control plane
// takes them. Zero fields do not filter.
type auditQuery struct {
	Category, Actor, Target string
	// Since and Until are epoch milliseconds, Since inclusive and Until
	// exclusive.
	Since, Until  int64
	Offset, Limit int
	// Actors asks for everyone who appears in the log as well.
	Actors bool
}

func (q auditQuery) values() url.Values {
	v := url.Values{}
	for name, value := range map[string]string{"category": q.Category, "actor": q.Actor, "target": q.Target} {
		if value != "" {
			v.Set(name, value)
		}
	}
	for name, value := range map[string]int64{"since": q.Since, "until": q.Until, "offset": int64(q.Offset), "limit": int64(q.Limit)} {
		if value > 0 {
			v.Set(name, strconv.FormatInt(value, 10))
		}
	}
	if q.Actors {
		v.Set("actors", "1")
	}
	return v
}

// auditPage is one page of matching events, how many match in all, when
// the oldest kept event was recorded (0 for none), and, when asked for,
// everyone in the log.
type auditPage struct {
	Events   []auditEvent `json:"events"`
	Total    int          `json:"total"`
	Earliest int64        `json:"earliest"`
	Actors   []auditActor `json:"actors"`
}

type auditActor struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type acceptedInvitation struct {
	Org struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"org"`
	Role role `json:"role"`
}

/* ------------------------------------------------------------ transport -- */

// upstream is one reader's conversation with the control plane for the
// length of one request.
//
// It also carries back the session cookie the control plane refreshes on
// every call. Rolling expiry only works if the refreshed cookie reaches the
// browser, and the browser never talks to the control plane directly here.
type upstream struct {
	c *authClient
	r *http.Request

	mu      sync.Mutex
	cookies map[string]*http.Cookie
}

func (c *authClient) upstream(r *http.Request) *upstream {
	return &upstream{c: c, r: r, cookies: map[string]*http.Cookie{}}
}

// relay writes the session cookies the control plane set during this request.
// Call it before the status line; only the session cookie is ever relayed.
func (u *upstream) relay(w http.ResponseWriter) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range u.cookies {
		http.SetCookie(w, c)
	}
}

func (u *upstream) keep(cookies []*http.Cookie) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range cookies {
		for _, name := range sessionCookieNames {
			if c.Name == name {
				u.cookies[c.Name] = c
			}
		}
	}
}

// do sends one call and decodes a successful answer into out.
func (u *upstream) do(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.c.origin+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		// The control plane refuses a mutation that is not JSON, which is
		// precisely what a cross-site HTML form cannot send.
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", u.c.publicOrigin)
	}
	if id := httpx.IDOf(u.r); id != "" {
		req.Header.Set("X-Request-Id", id)
	}
	if cookie, ok := sessionCookie(u.r); ok {
		req.AddCookie(cookie)
	}

	resp, err := u.c.http.Do(req)
	if err != nil {
		u.c.log.Warn("control plane unreachable", "path", path, "error", err, "request_id", httpx.IDOf(u.r))
		return fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer resp.Body.Close()
	u.keep(resp.Cookies())

	limited := io.LimitReader(resp.Body, 1<<20)
	if resp.StatusCode >= 400 {
		refused := &apiError{Status: resp.StatusCode}
		var envelope httpx.ErrorBody
		if json.NewDecoder(limited).Decode(&envelope) == nil {
			refused.Message = strings.TrimSpace(envelope.Error.Message)
			refused.Type = envelope.Error.Type
		}
		return refused
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("control plane answered %s %s with unreadable JSON: %w", method, path, err)
	}
	return nil
}

/* -------------------------------------------------------------- calls -- */

func (u *upstream) Session(ctx context.Context) (*accountSession, error) {
	var out accountSession
	if err := u.do(ctx, http.MethodGet, "/auth/session", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (u *upstream) Org(ctx context.Context) (*orgDetail, error) {
	var out orgDetail
	if err := u.do(ctx, http.MethodGet, "/api/v1/org", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (u *upstream) Keys(ctx context.Context) ([]apiKey, error) {
	var out struct {
		Keys []apiKey `json:"keys"`
	}
	err := u.do(ctx, http.MethodGet, "/api/v1/keys", nil, &out)
	return out.Keys, err
}

// CreateKey mints a key that expires after expiresInDays, or never when that
// is nil.
func (u *upstream) CreateKey(ctx context.Context, label string, expiresInDays *int) (*createdKey, error) {
	var out createdKey
	payload := map[string]any{"label": label, "expiresInDays": expiresInDays}
	if err := u.do(ctx, http.MethodPost, "/api/v1/keys", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateKey replaces a key with a new one, keeping the old one working for
// overlapHours; 0 revokes it at once.
func (u *upstream) RotateKey(ctx context.Context, id string, overlapHours int) (*createdKey, error) {
	var out createdKey
	payload := map[string]int{"overlapHours": overlapHours}
	if err := u.do(ctx, http.MethodPost, "/api/v1/keys/"+url.PathEscape(id)+"/rotate", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (u *upstream) RevokeKey(ctx context.Context, id string) error {
	return u.do(ctx, http.MethodDelete, "/api/v1/keys/"+url.PathEscape(id), nil, nil)
}

func (u *upstream) Invitations(ctx context.Context) ([]invitation, error) {
	var out struct {
		Invitations []invitation `json:"invitations"`
	}
	err := u.do(ctx, http.MethodGet, "/api/v1/org/invitations", nil, &out)
	return out.Invitations, err
}

// Invite asks the control plane to mail an invitation. It answers 503 when
// the mail cannot go out, and records nothing in that case.
func (u *upstream) Invite(ctx context.Context, email string, granted role, locale string) error {
	payload := map[string]string{"email": email, "role": string(granted)}
	if locale != "" && locale != i18n.DefaultID {
		payload["locale"] = locale
	}
	return u.do(ctx, http.MethodPost, "/api/v1/org/invitations", payload, nil)
}

func (u *upstream) RevokeInvitation(ctx context.Context, id string) error {
	return u.do(ctx, http.MethodDelete, "/api/v1/org/invitations/"+url.PathEscape(id), nil, nil)
}

func (u *upstream) AcceptInvitation(ctx context.Context, token string) (*acceptedInvitation, error) {
	var out acceptedInvitation
	if err := u.do(ctx, http.MethodPost, "/api/v1/org/invitations/"+url.PathEscape(token)+"/accept", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (u *upstream) SetRole(ctx context.Context, userID string, granted role) error {
	return u.do(ctx, http.MethodPatch, "/api/v1/org/members/"+url.PathEscape(userID),
		map[string]string{"role": string(granted)}, nil)
}

func (u *upstream) RemoveMember(ctx context.Context, userID string) (revokedKeys int, err error) {
	var out struct {
		RevokedKeys int `json:"revokedKeys"`
	}
	err = u.do(ctx, http.MethodDelete, "/api/v1/org/members/"+url.PathEscape(userID), nil, &out)
	return out.RevokedKeys, err
}

func (u *upstream) UpdateName(ctx context.Context, name string) error {
	return u.do(ctx, http.MethodPatch, "/api/v1/me", map[string]string{"name": name}, nil)
}

func (u *upstream) UnlinkIdentity(ctx context.Context, id string) error {
	return u.do(ctx, http.MethodDelete, "/api/v1/me/identities/"+url.PathEscape(id), nil, nil)
}

func (u *upstream) SwitchOrg(ctx context.Context, orgID string) error {
	return u.do(ctx, http.MethodPost, "/auth/org", map[string]string{"orgId": orgID}, nil)
}

// OtherSessions counts the account's sessions besides this one.
func (u *upstream) OtherSessions(ctx context.Context) (int, error) {
	var out struct {
		Others int `json:"others"`
	}
	err := u.do(ctx, http.MethodGet, "/auth/sessions", nil, &out)
	return out.Others, err
}

// EndOtherSessions signs the account out everywhere but this session.
func (u *upstream) EndOtherSessions(ctx context.Context) (ended int, err error) {
	var out struct {
		Ended int `json:"ended"`
	}
	err = u.do(ctx, http.MethodPost, "/auth/sessions/end-others", map[string]string{}, &out)
	return out.Ended, err
}

// RenameOrg renames the active organisation.
func (u *upstream) RenameOrg(ctx context.Context, name string) error {
	return u.do(ctx, http.MethodPatch, "/api/v1/org", map[string]string{"name": name}, nil)
}

// LeaveOrg takes the reader out of the active organisation, revoking the
// API keys they created for it.
func (u *upstream) LeaveOrg(ctx context.Context) (revokedKeys int, err error) {
	var out struct {
		RevokedKeys int `json:"revokedKeys"`
	}
	err = u.do(ctx, http.MethodPost, "/api/v1/org/leave", map[string]string{}, &out)
	return out.RevokedKeys, err
}

// DeleteOrg deletes the active organisation; confirmName must be its name.
func (u *upstream) DeleteOrg(ctx context.Context, confirmName string) (members, revokedKeys int, err error) {
	var out struct {
		Members     int `json:"members"`
		RevokedKeys int `json:"revokedKeys"`
	}
	err = u.do(ctx, http.MethodDelete, "/api/v1/org", map[string]string{"confirmName": confirmName}, &out)
	return out.Members, out.RevokedKeys, err
}

// Audit reads one page of the audit log, filtered on the control plane.
func (u *upstream) Audit(ctx context.Context, q auditQuery) (*auditPage, error) {
	var out auditPage
	if err := u.do(ctx, http.MethodGet, "/api/v1/audit?"+q.values().Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MailAvailable reports whether the control plane can send an invitation at
// all. ok is false when it could not be asked, which is not the same as no.
func (u *upstream) MailAvailable(ctx context.Context) (available, ok bool) {
	found, err := u.c.Providers(ctx)
	if err != nil {
		return false, false
	}
	return found.MagicLink, true
}
