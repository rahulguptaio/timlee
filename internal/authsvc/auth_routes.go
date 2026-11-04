package authsvc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/store"
)

// Sign-in routes.
//
// The browser flow is a standard redirect round-trip. The desktop flow uses
// the system browser with a loopback callback, because RFC 8252 requires a
// public client to use the user's own browser rather than an embedded
// webview — and because providers reject tauri:// redirect URIs.

// magicLinkTTL is how long a mailed sign-in link stays usable.
const magicLinkTTL = 15 * time.Minute

// desktopCodeTTL is how long the desktop app has to collect its one-time
// code from its loopback listener. It is seconds of work, so the window is
// deliberately narrow.
const desktopCodeTTL = 2 * time.Minute

// emailPattern is a deliberately loose shape check. Full RFC 5322 validation
// rejects addresses that work and accepts ones that do not; possession of the
// mailbox is what actually proves the address, and that is what the link does.
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// handleProviders is what the sign-in screen renders buttons from.
func (s *Service) handleProviders(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"providers": s.Providers(),
		"magicLink": s.mail.available(),
		"devLogin":  s.cfg.AllowDevLogin,
		// Tells the screen the "email" is a local file, so it can say where
		// to look instead of "check your inbox".
		"mailOutbox": s.mail.available() && !s.cfg.IsProduction(),
	})
}

// sessionResponse is what the console boots from. Every field is omitted when
// signed out, so a signed-out caller receives {}.
type sessionResponse struct {
	User           *sessionUser       `json:"user,omitempty"`
	Org            *store.Membership  `json:"org,omitempty"`
	Orgs           []store.Membership `json:"orgs,omitempty"`
	Identities     []store.Identity   `json:"identities,omitempty"`
	SignInRequired bool               `json:"signInRequired,omitempty"`
}

type sessionUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	Locale    string `json:"locale"`
	// Allowed reports whether the account is on TIMLEE_ALLOWED_EMAILS and
	// may therefore submit prompts.
	Allowed bool `json:"allowed"`
}

func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	user := userOf(r)
	if user == nil {
		httpx.WriteJSON(w, r, http.StatusOK, sessionResponse{})
		return
	}

	// Read with the session (sessionParts): a user implies a session.
	current := sessionOf(r)
	response := sessionResponse{
		User: &sessionUser{
			ID: user.ID, Email: user.Email, Name: user.Name, AvatarURL: user.AvatarURL, Locale: user.Locale,
			Allowed: s.cfg.EmailAllowed(user.Email),
		},
		Orgs:           current.memberships,
		Identities:     current.identities,
		Org:            activeOrg(current.memberships, current),
		SignInRequired: s.cfg.IsProduction(),
	}
	httpx.WriteJSON(w, r, http.StatusOK, response)
}

// activeOrg picks the session's selected organisation, falling back to the
// first membership. The selection is re-checked against live memberships: a
// session can outlive the user's access to the org it last chose.
func activeOrg(memberships []store.Membership, current *session) *store.Membership {
	if len(memberships) == 0 {
		return nil
	}
	if current != nil && current.data.OrgID != "" {
		for i := range memberships {
			if memberships[i].ID == current.data.OrgID {
				return &memberships[i]
			}
		}
	}
	return &memberships[0]
}

/* ------------------------------------------------------ OAuth round-trip */

func (s *Service) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p, ok := s.providers[name]
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, name+" sign-in is not configured.", "not_found")
		return
	}

	flow := &oauthFlow{
		Provider: name,
		State:    store.NewSessionID(),
		Nonce:    store.NewSessionID(),
		Verifier: oauth2.GenerateVerifier(),
		Locale:   s.mailLocale(r.URL.Query().Get("locale")).ID,
	}

	// Remembered across the redirect so the user lands where they started.
	data := sessionData{
		ReturnTo: s.safeReturnTo(r.URL.Query().Get("returnTo")),
		Flow:     flow,
	}
	if r.URL.Query().Get("desktop") == "1" {
		handoff, err := desktopHandoffFrom(r.URL.Query())
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Desktop sign-in could not start: "+err.Error()+".", "bad_request")
			return
		}
		data.Desktop = handoff
	}
	if current := sessionOf(r); current != nil {
		// A signed-in user linking another provider keeps their session.
		data.UserID, data.OrgID = current.data.UserID, current.data.OrgID
	}

	if _, err := s.sessions.issue(w, r, data); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, p.authCodeURL(flow), http.StatusFound)
}

func (s *Service) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p, ok := s.providers[name]
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, name+" sign-in is not configured.", "not_found")
		return
	}

	current := sessionOf(r)
	if current == nil || current.data.Flow == nil || current.data.Flow.Provider != name {
		s.signInFailed(w, r, name, "no_flow", "no sign-in in progress")
		return
	}
	flow := current.data.Flow

	// The state parameter is the only thing tying this callback to the
	// authorisation request this browser started.
	if presented := r.URL.Query().Get("state"); !store.SecretsMatch(presented, flow.State) {
		s.signInFailed(w, r, name, "state_mismatch", "state mismatch")
		return
	}
	if reason := r.URL.Query().Get("error"); reason != "" {
		s.signInFailed(w, r, name, "provider_denied", reason)
		return
	}

	profile, err := p.exchange(r.Context(), r.URL.Query().Get("code"), flow)
	if err != nil {
		s.signInFailed(w, r, name, "exchange_failed", err.Error())
		return
	}
	profile.Locale = flow.Locale

	user, created, err := s.store.UpsertFromProvider(r.Context(), profile)
	if err != nil {
		if errors.Is(err, store.ErrIdentityConflict) {
			s.signInFailed(w, r, name, "identity_conflict", "identity_conflict")
			return
		}
		s.fail(w, r, err)
		return
	}
	if created {
		s.workspaceCreated(r, user.ID)
	}

	s.completeSignIn(w, r, user, name, current.data.ReturnTo, current.data.Desktop)
}

// completeSignIn finishes a successful sign-in with a fresh session id, so a
// fixed pre-authentication cookie cannot be used to ride the new session.
//
// A desktop sign-in is for the app, which gets a session of its own by
// redeeming the code delivered below. The browser it passed through keeps
// what it had, signed in or not, rather than being signed in as a side
// effect on what may be a shared machine; only the finished round trip's
// one-shot state is cleared from it.
func (s *Service) completeSignIn(w http.ResponseWriter, r *http.Request, user *store.User, provider, returnTo string, desktop *desktopHandoff) {
	if desktop == nil {
		if _, err := s.sessions.issue(w, r, sessionData{UserID: user.ID}); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if current := sessionOf(r); current != nil && current.data.Flow != nil {
		current.data.Flow, current.data.Desktop = nil, nil
		if err := s.sessions.save(w, r, current); err != nil {
			s.logger.Warn("could not clear a finished desktop sign-in", "error", err, "request_id", httpx.IDOf(r))
		}
	}

	if err := s.store.RecordAudit(r.Context(), store.AuditEvent{
		ActorID:  user.ID,
		Action:   "auth.signed_in",
		Metadata: `{"provider":"` + provider + `"}`,
		IP:       httpx.ClientIP(r),
	}); err != nil {
		s.logger.Error("could not record audit event", "action", "auth.signed_in", "error", err)
	}
	s.logger.Info("signed in", "provider", provider, "user", user.ID)

	if desktop != nil {
		redirect, err := s.desktopRedirect(r.Context(), user.Email, user.Locale, desktop)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		// The Location carries the app's code: not to be cached, and the
		// callback URL, with the provider's code in it, not to follow it on
		// to the listener as a Referer.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, redirect, http.StatusFound)
		return
	}

	http.Redirect(w, r, s.safeReturnTo(returnTo), http.StatusFound)
}

// desktopHandoff is where a native sign-in is delivered, and the proof the
// receiving client must present to redeem it.
type desktopHandoff struct {
	Port      string `json:"port"`
	Challenge string `json:"challenge"`
	// FlowHash is the hash of the flow id the site set as a cookie in the app
	// window that started the sign-in; see newDesktopHandoff.
	FlowHash string `json:"flowHash"`
}

// pkceChallengePattern matches an S256 challenge: base64url of 32 bytes,
// unpadded, which is always 43 characters.
var pkceChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// desktopHandoffFrom validates the loopback destination, the PKCE challenge
// and the flow id a native client's sign-in supplies in a query string.
func desktopHandoffFrom(query url.Values) (*desktopHandoff, error) {
	return newDesktopHandoff(query.Get("port"), query.Get("code_challenge_method"), query.Get("code_challenge"), query.Get("flow"))
}

// desktopRequest is the same handoff in a JSON body, which is how the site's
// sign-in forms pass it on for the email and development routes.
type desktopRequest struct {
	Port                string `json:"port"`
	CodeChallenge       string `json:"codeChallenge"`
	CodeChallengeMethod string `json:"codeChallengeMethod"`
	Flow                string `json:"flow"`
}

func (d *desktopRequest) handoff() (*desktopHandoff, error) {
	return newDesktopHandoff(d.Port, d.CodeChallengeMethod, d.CodeChallenge, d.Flow)
}

// newDesktopHandoff checks the three parts of a native sign-in request.
//
// Each matters. Without a port check the service can be talked into
// redirecting to any local port, including one it has no business reaching;
// without the challenge the delivered code is a bearer credential that any
// process which won the race for that port can spend (RFC 8252 §8.9).
// Without the flow id, whoever holds a code and its verifier, such as the
// attacker who started the sign-in for their own account, can redeem it in
// somebody else's browser and sign them in as the attacker (login CSRF): the
// site sets the flow id as a cookie in the app window that starts the
// sign-in, and the code redeems only alongside it. Only its hash is kept.
func newDesktopHandoff(portValue, method, challenge, flow string) (*desktopHandoff, error) {
	port, ok := loopbackPort(portValue)
	if !ok {
		return nil, errors.New("the loopback port must be between 1024 and 65535")
	}
	if method != "S256" {
		// "plain" is in the spec and is worth nothing here: it puts the
		// verifier itself in the authorisation request.
		return nil, errors.New("code_challenge_method must be S256")
	}
	if !pkceChallengePattern.MatchString(challenge) {
		return nil, errors.New("code_challenge must be an unpadded base64url SHA-256")
	}
	// The site mints a flow id the same shape as a challenge: 32 random
	// bytes, base64url.
	if !pkceChallengePattern.MatchString(flow) {
		return nil, errors.New("start the sign-in from the Timlee app")
	}
	return &desktopHandoff{Port: port, Challenge: challenge, FlowHash: store.HashSecret(flow)}, nil
}

// loopbackPort accepts a port a native app can listen on. Below 1024 needs
// privileges such an app does not have, so a request to deliver there is
// either a mistake or an attempt to make this service talk to something else
// on the machine.
func loopbackPort(value string) (string, bool) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1024 || port > 65535 {
		return "", false
	}
	return strconv.Itoa(port), true
}

// loopbackURL is where the browser hands a native client its code: always
// 127.0.0.1, never "localhost", which a hosts file can point elsewhere
// (RFC 8252 section 8.3).
func loopbackURL(port, code string) string {
	return "http://127.0.0.1:" + port + "/callback?code=" + url.QueryEscape(code)
}

// desktopRedirect mints a single-use code for the desktop app and returns the
// loopback URL that delivers it. The app exchanges the code for a session of
// its own; the code is bound to the verifier the app holds, so a different
// process listening on that port receives something it cannot redeem.
func (s *Service) desktopRedirect(ctx context.Context, email, locale string, to *desktopHandoff) (string, error) {
	secret, hash := store.NewLoginToken()
	if _, err := s.store.CreateLoginToken(
		ctx,
		email,
		hash,
		"",
		s.mailLocale(locale).ID,
		to.Challenge,
		to.FlowHash,
		desktopCodeTTL.Milliseconds(),
	); err != nil {
		return "", err
	}
	return loopbackURL(to.Port, secret), nil
}

// signInFailed sends the browser back to the sign-in page, which explains the
// failure in context. The detail stays in the log: it describes our own
// configuration or a provider's response, not anything the visitor did. The
// event carries reason, one of a fixed set, instead: the detail can be a
// provider's own words, or a query parameter anyone can write.
func (s *Service) signInFailed(w http.ResponseWriter, r *http.Request, provider, reason, detail string) {
	s.logger.Info("sign-in failed", "reason", detail, "request_id", httpx.IDOf(r))
	logx.Event(r.Context(), "audit.auth.sign_in_failed", slog.String("provider", provider), slog.String("reason", reason))
	locale := ""
	if current := sessionOf(r); current != nil && current.data.Flow != nil {
		locale = current.data.Flow.Locale
	}
	http.Redirect(w, r, s.localizedPublicPath(locale, "/sign-in")+"?error=oauth_failed", http.StatusSeeOther)
}

func (s *Service) handleFailed(w http.ResponseWriter, r *http.Request) {
	s.signInFailed(w, r, "", "unknown", "unknown")
}

// workspaceCreated records the workspace a first sign-in creates for a new
// user (store.UpsertFromProvider). Its id costs a lookup, made only while
// events are kept.
func (s *Service) workspaceCreated(r *http.Request, userID string) {
	if !logx.Enabled() {
		return
	}
	orgID := ""
	if memberships, err := s.store.OrgsForUser(r.Context(), userID); err == nil && len(memberships) > 0 {
		orgID = memberships[0].ID
	}
	logx.Event(r.Context(), "audit.org.created", slog.String("user_id", userID), slog.String("org_id", orgID))
}

/* ------------------------------------------------------------ magic link */

func (s *Service) handleMagicLinkRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		ReturnTo string `json:"returnTo"`
		Locale   string `json:"locale"`
		// Desktop is set when the link is for the desktop app.
		Desktop *desktopRequest `json:"desktop"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	var desktop *desktopHandoff
	if body.Desktop != nil {
		handoff, err := body.Desktop.handoff()
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Desktop sign-in could not start: "+err.Error()+".", "bad_request")
			return
		}
		desktop = handoff
	}

	// Validated before anything else, including availability: an empty or
	// malformed address is the caller's mistake either way, and reporting the
	// server's configuration first would hide it.
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !emailPattern.MatchString(email) {
		httpx.WriteError(w, http.StatusBadRequest, "Enter a valid email address.", "bad_request")
		return
	}

	// The budget is charged per source and target, which needs the address,
	// so this cannot be middleware.
	if !s.limits.magicLink.Allow(r.WithContext(withMagicTarget(r, email))) {
		w.Header().Set("Retry-After", "60")
		httpx.WriteError(w, http.StatusTooManyRequests,
			"Too many requests. Slow down and try again shortly.", "rate_limit")
		return
	}

	// Without a transport the link would go nowhere; say so rather than
	// reporting success for a message that was never sent.
	if !s.mail.available() {
		httpx.WriteError(w, http.StatusServiceUnavailable,
			"Email sign-in is not available on this server.", "mail_unavailable")
		return
	}

	// A link for the desktop app is bound to the app's challenge and flow as
	// well as to the mailbox: whoever opens it is sent on to the app's
	// loopback listener, and only the app holds the verifier that redeems it,
	// in the window that started the sign-in.
	challenge, flowHash := "", ""
	if desktop != nil {
		challenge, flowHash = desktop.Challenge, desktop.FlowHash
	}
	secret, hash := store.NewLoginToken()
	locale := s.mailLocale(body.Locale)
	token, err := s.store.CreateLoginToken(
		r.Context(),
		email,
		hash,
		s.safeReturnTo(body.ReturnTo),
		locale.ID,
		challenge,
		flowHash,
		magicLinkTTL.Milliseconds(),
	)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	link := s.cfg.ControlOrigin + "/auth/magic-link/consume?token=" + url.QueryEscape(secret)
	if locale.ID != i18n.DefaultID {
		link += "&locale=" + url.QueryEscape(locale.ID)
	}
	if desktop != nil {
		link += "&desktop_port=" + desktop.Port
	}
	if _, err := s.mail.send(magicLinkMessage(
		locale.Localizer(),
		email,
		link,
		time.UnixMilli(token.CreatedAt),
		time.UnixMilli(token.ExpiresAt),
	)); err != nil {
		s.fail(w, r, err)
		return
	}

	// The same response either way, so this endpoint cannot be used to
	// discover which addresses have accounts.
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"sent": true})
}

func (s *Service) handleMagicLinkConsume(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("token")

	// A desktop sign-in link, opened in a browser, signs nobody in here. A
	// live code goes on to the app's listener unspent: the app's exchange,
	// with the verifier only it holds, is the one attempt the code gets.
	if port, ok := loopbackPort(r.URL.Query().Get("desktop_port")); ok {
		pending, err := s.store.DesktopCodePending(r.Context(), secret)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if pending {
			// The URL carries the code, so it must not be cached or travel on
			// in a Referer.
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.Redirect(w, r, loopbackURL(port, secret), http.StatusFound)
			return
		}
	}

	token, err := s.store.ConsumeLoginToken(r.Context(), secret)
	if err != nil {
		logx.Event(r.Context(), "audit.auth.sign_in_failed", slog.String("provider", "email"), slog.String("reason", "link_invalid"))
		// Unknown, spent, expired and bound to an app are one answer on
		// purpose: telling them apart would say whether a guessed token
		// ever existed.
		http.Redirect(
			w,
			r,
			s.localizedPublicPath(r.URL.Query().Get("locale"), "/sign-in")+"?error=link_expired",
			http.StatusSeeOther,
		)
		return
	}

	user, created, err := s.store.UpsertFromProvider(r.Context(), store.Profile{
		Provider: "email",
		Subject:  token.Email,
		Email:    token.Email,
		Locale:   token.Locale,
		// Possession of the mailbox is precisely what was just proven.
		EmailVerified: true,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if created {
		s.workspaceCreated(r, user.ID)
	}

	// The destination travels with the token rather than in the pre-sign-in
	// session, because the link may be opened in a different browser.
	s.completeSignIn(w, r, user, "email", token.ReturnTo, nil)
}

/* ------------------------------------------------------- desktop session */

// handleDesktopSession redeems a desktop sign-in code for a session.
//
// The site calls it, server to server, when the desktop app's window opens
// /sign-in/desktop/complete, and relays the cookie — so the session lands in
// the app's own webview and on the site's origin, which is where the console
// reads it. The verifier is required: it is what proves the caller is the
// app that started the sign-in, not whoever caught the code. So is the flow
// id, which the site reads from the window's own cookie: it is what proves
// the window is the one that started it, not one an attacker sent a code to.
func (s *Service) handleDesktopSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"codeVerifier"`
		Flow         string `json:"flow"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	if body.Code == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Send the sign-in code and its code verifier.", "bad_request")
		return
	}

	// This attempt spends the code whatever the verifier and flow, malformed
	// or missing included, so a code gets exactly one try (RedeemDesktopCode).
	token, err := s.store.RedeemDesktopCode(r.Context(), body.Code, body.CodeVerifier, body.Flow)
	if err != nil {
		reason := "link_invalid"
		switch {
		case errors.Is(err, store.ErrPKCEVerificationFailed):
			s.logger.Warn("rejected a desktop sign-in code presented with the wrong verifier",
				"ip", httpx.ClientIP(r), "request_id", httpx.IDOf(r))
			reason = "pkce_failed"
		case errors.Is(err, store.ErrDesktopFlowMismatch):
			s.logger.Warn("rejected a desktop sign-in code redeemed outside the window that started it",
				"ip", httpx.ClientIP(r), "request_id", httpx.IDOf(r))
			reason = "flow_mismatch"
		}
		logx.Event(r.Context(), "audit.auth.sign_in_failed", slog.String("provider", "desktop"), slog.String("reason", reason))
		httpx.WriteError(w, http.StatusBadRequest,
			"That sign-in link has expired or was already used. Sign in again.", "link_expired")
		return
	}

	user, created, err := s.store.UpsertFromProvider(r.Context(), store.Profile{
		Provider: "email", Subject: token.Email, Email: token.Email, EmailVerified: true, Locale: token.Locale,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if created {
		s.workspaceCreated(r, user.ID)
	}
	if _, err := s.sessions.issue(w, r, sessionData{UserID: user.ID}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.RecordAudit(r.Context(), store.AuditEvent{
		ActorID:  user.ID,
		Action:   "auth.signed_in",
		Metadata: `{"provider":"desktop"}`,
		IP:       httpx.ClientIP(r),
	}); err != nil {
		s.logger.Error("could not record audit event", "action", "auth.signed_in", "error", err)
	}
	s.logger.Info("signed in", "provider", "desktop", "user", user.ID)
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

/* ------------------------------------------------------------ dev log-in */

// handleDevLogin signs a visitor in by email with no password. It is
// registered only outside production, where a fresh clone has no identity
// provider configured and would otherwise have no way in at all.
func (s *Service) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email   string          `json:"email"`
		Name    string          `json:"name"`
		Locale  string          `json:"locale"`
		Desktop *desktopRequest `json:"desktop"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	var desktop *desktopHandoff
	if body.Desktop != nil {
		handoff, err := body.Desktop.handoff()
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Desktop sign-in could not start: "+err.Error()+".", "bad_request")
			return
		}
		desktop = handoff
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		email = "dev@timlee.local"
	}
	name := body.Name
	if name == "" {
		name = "Dev User"
	}

	user, created, err := s.store.UpsertFromProvider(r.Context(), store.Profile{
		Provider: "email", Subject: email, Email: email, EmailVerified: true, Name: name,
		Locale: s.mailLocale(body.Locale).ID,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if created {
		s.workspaceCreated(r, user.ID)
	}
	if desktop != nil {
		// The session is for the desktop app, not for this browser: hand
		// back where to deliver the app's code, as the OAuth callback does.
		redirect, err := s.desktopRedirect(r.Context(), user.Email, user.Locale, desktop)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true, "desktopRedirect": redirect})
		return
	}
	if _, err := s.sessions.issue(w, r, sessionData{UserID: user.ID}); err != nil {
		s.fail(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"ok":   true,
		"user": map[string]string{"id": user.ID, "email": user.Email},
	})
}

/* ------------------------------------------------------------- sign out */

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.destroy(w, r); err != nil {
		// Reported, not swallowed: the user believes the session is over, so
		// it must not quietly stay usable. The cookie is still theirs, which
		// is what lets them try again.
		s.logger.Error("could not end the session", "error", err, "request_id", httpx.IDOf(r))
		logx.Fail(r, err)
		httpx.WriteError(w, http.StatusInternalServerError, "Could not sign you out. Try again.", "server_error")
		return
	}
	if user := userOf(r); user != nil {
		if err := s.store.RecordAudit(r.Context(), store.AuditEvent{
			ActorID: user.ID, Action: "auth.signed_out", IP: httpx.ClientIP(r),
		}); err != nil {
			s.logger.Error("could not record audit event", "action", "auth.signed_out", "error", err)
		}
		logx.Event(r.Context(), "audit.session.revoked", slog.String("user_id", user.ID))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

// handleSwitchOrg changes the active organisation for this session.
func (s *Service) handleSwitchOrg(w http.ResponseWriter, r *http.Request) {
	user := userOf(r)
	if user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "Not signed in.", "unauthorized")
		return
	}

	var body struct {
		OrgID string `json:"orgId"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}

	// Membership is re-checked here; the client's claim is never trusted.
	role, err := s.store.RoleOf(r.Context(), body.OrgID, user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if role == "" {
		httpx.WriteError(w, http.StatusForbidden, "Not a member of that organisation.", "forbidden")
		return
	}

	current := sessionOf(r)
	current.data.OrgID = body.OrgID
	if err := s.sessions.save(w, r, current); err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true, "orgId": body.OrgID})
}

// fail logs the cause and tells the caller nothing about it. The request's
// request.failed event carries the cause too.
func (s *Service) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("request failed", "path", r.URL.Path, "error", err, "request_id", httpx.IDOf(r))
	logx.Fail(r, err)
	httpx.WriteError(w, http.StatusInternalServerError, "Internal server error.", "server_error")
}
