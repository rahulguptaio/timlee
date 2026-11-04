package authsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/buildinfo"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/store"
)

const testOrigin = "http://localhost:5173"

type harness struct {
	*Service
	store  *store.Store
	outbox string
}

// newHarness builds the real pipeline over a private database, so the tests
// exercise the middleware chain a browser meets rather than bare handlers.
func newHarness(t *testing.T, adjust ...func(*config.Config)) *harness {
	t.Helper()

	dir := scratchDir(t)
	cfg := config.Config{
		Env:           "development",
		PublicOrigin:  testOrigin,
		ControlOrigin: "http://127.0.0.1:8788",
		SessionSecret: "session-secret-for-tests-0123456789abcdef",
		InternalToken: "internal-token-for-tests-0123456789abcdef",
		SessionTTL:    time.Hour,
		AllowDevLogin: true,
		MailOutbox:    filepath.Join(dir, "mail"),
		Providers:     map[string]config.Provider{},
	}
	for _, fn := range adjust {
		fn(&cfg)
	}

	// Process-wide, exactly as main sets it, and restored afterwards so one
	// test cannot leave another trusting forwarded headers.
	httpx.SetTrustProxy(cfg.TrustProxy)
	t.Cleanup(func() { httpx.SetTrustProxy(false) })

	db, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &harness{Service: New(context.Background(), cfg, db, logger), store: db, outbox: cfg.MailOutbox}
}

func scratchDir(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(".scratch-probe", 0o700); err != nil {
		t.Fatalf("scratch root: %v", err)
	}
	dir, err := os.MkdirTemp(".scratch-probe", "authsvc-")
	if err != nil {
		t.Fatalf("scratch dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// call sends one request through the whole chain.
func (h *harness) call(t *testing.T, method, target string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, reader)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		r.Header.Set("Origin", testOrigin)
	}
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
			continue
		}
		r.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// signIn establishes a session through the development route and returns the
// cookie a browser would hold.
func (h *harness) signIn(t *testing.T, email string) string {
	t.Helper()

	res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"`+email+`"}`, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("dev sign-in: status %d, body %s", res.Code, res.Body)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("dev sign-in set %d cookies, want exactly 1", len(cookies))
	}
	return cookies[0].Name + "=" + cookies[0].Value
}

func decode(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", res.Body.String(), err)
	}
	return out
}

/* ------------------------------------------------------------ sessions -- */

func TestSessionIssueAndResolve(t *testing.T) {
	h := newHarness(t)

	signedOut := h.call(t, http.MethodGet, "/auth/session", "", nil)
	if signedOut.Code != http.StatusOK {
		t.Fatalf("signed-out session: status %d", signedOut.Code)
	}
	if got := strings.TrimSpace(signedOut.Body.String()); got != "{}" {
		t.Errorf("signed-out session body = %s, want {}", got)
	}

	cookie := h.signIn(t, "ada@example.com")

	signedIn := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": cookie})
	body := decode(t, signedIn)
	user, _ := body["user"].(map[string]any)
	if user == nil || user["email"] != "ada@example.com" {
		t.Fatalf("session = %v, want ada@example.com", body)
	}
	if _, ok := body["org"].(map[string]any); !ok {
		t.Error("a new user has no organisation")
	}
	// Both lists come from the same read as the session itself.
	if orgs, _ := body["orgs"].([]any); len(orgs) != 1 || orgs[0].(map[string]any)["role"] != "owner" {
		t.Errorf("orgs = %v, want the one organisation the new user owns", body["orgs"])
	}
	if identities, _ := body["identities"].([]any); len(identities) != 1 ||
		identities[0].(map[string]any)["provider"] != "email" {
		t.Errorf("identities = %v, want the email identity the user signed in with", body["identities"])
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		wantName   string
		wantSecure bool
	}{
		{"development", "development", "timlee.sid", false},
		{"production", "production", "__Host-timlee.sid", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) {
				c.Env = tc.env
				// TLS is terminated by the edge, so the forwarded scheme
				// below is the only evidence this request was secure.
				c.TrustProxy = true
			})

			// A production cookie is Secure, and a Secure cookie is only ever
			// set on a request the edge terminated as https.
			res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"ada@example.com"}`,
				map[string]string{"X-Forwarded-Proto": "https"})
			if res.Code != http.StatusOK {
				t.Fatalf("dev sign-in: status %d", res.Code)
			}

			cookies := res.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("set %d cookies, want 1", len(cookies))
			}
			c := cookies[0]
			if c.Name != tc.wantName {
				t.Errorf("cookie name = %q, want %q", c.Name, tc.wantName)
			}
			if c.Secure != tc.wantSecure {
				t.Errorf("Secure = %v, want %v", c.Secure, tc.wantSecure)
			}
			if !c.HttpOnly {
				t.Error("the session cookie is readable from script")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax so the OAuth return navigation carries it", c.SameSite)
			}
			if c.Path != "/" {
				t.Errorf("Path = %q, want /", c.Path)
			}
			if c.Domain != "" {
				t.Error("a __Host- cookie must carry no Domain")
			}
		})
	}
}

func TestSignInFailsLoudlyWhenASecureCookieCannotBeSet(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Env = "production" })
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))

	// A Secure cookie set on a plain-http response is discarded by the
	// browser without a word, so reporting success here would land the user
	// signed out with nothing written down anywhere.
	res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"ada@example.com"}`, nil)
	if cookies := res.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("set %d cookies over plain http, want none: a browser would drop them silently", len(cookies))
	}
	if res.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 rather than a success the user cannot act on", res.Code)
	}
	// The operator needs the cause, and the usual one is a missing setting.
	if !strings.Contains(logs.String(), "TIMLEE_TRUST_PROXY") {
		t.Errorf("log = %q, want the refusal and its likely fix", logs.String())
	}

	// And the unreachable session is not left behind in the database.
	if live, err := h.store.CountSessions(t.Context()); err != nil || live != 0 {
		t.Errorf("live sessions = %d (err %v), want 0", live, err)
	}
}

func TestForgedSessionCookieIsRefused(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")
	name, value, _ := strings.Cut(cookie, "=")
	id, _, _ := strings.Cut(value, ".")

	tests := []struct {
		name   string
		cookie string
	}{
		{"unsigned id", name + "=" + id},
		{"wrong signature", name + "=" + id + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"unknown id", name + "=" + store.NewSessionID() + ".sig"},
		{"empty", name + "="},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": tc.cookie})
			if got := strings.TrimSpace(res.Body.String()); got != "{}" {
				t.Errorf("session = %s, want {} for a cookie we did not sign", got)
			}
		})
	}
}

func TestSessionIsRefreshedAtMostOncePerInterval(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")
	_, value, _ := strings.Cut(cookie, "=")
	id, _, _ := strings.Cut(value, ".")
	expiry := func() int64 {
		row, err := h.store.GetSession(t.Context(), id)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		return row.ExpiresAt
	}
	headers := map[string]string{"Cookie": cookie}

	// Moments after sign-in a page view is a read: nothing written, no cookie.
	issued := expiry()
	if res := h.call(t, http.MethodGet, "/auth/session", "", headers); len(res.Result().Cookies()) != 0 {
		t.Error("re-issued the cookie of a session refreshed moments ago")
	}
	if got := expiry(); got != issued {
		t.Errorf("expiry moved from %d to %d; want no write within the interval", issued, got)
	}

	// Once the last refresh is older than the interval, the next request
	// pushes the expiry back and re-issues the cookie to match.
	stale := time.Now().Add(h.cfg.SessionTTL - h.sessions.refreshEvery() - time.Minute).UnixMilli()
	if err := h.store.TouchSession(t.Context(), id, stale); err != nil {
		t.Fatalf("age the session: %v", err)
	}
	if res := h.call(t, http.MethodGet, "/auth/session", "", headers); len(res.Result().Cookies()) != 1 {
		t.Errorf("set %d cookies on a session due a refresh, want the refreshed one", len(res.Result().Cookies()))
	}
	if got := expiry(); got <= stale {
		t.Errorf("expiry = %d, want it pushed back past %d", got, stale)
	}
}

func TestExpiredSessionIsSignedOut(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	_, value, _ := strings.Cut(cookie, "=")
	id, _, _ := strings.Cut(value, ".")
	if err := h.store.TouchSession(t.Context(), id, time.Now().Add(-time.Second).UnixMilli()); err != nil {
		t.Fatalf("expire session: %v", err)
	}

	res := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": cookie})
	if got := strings.TrimSpace(res.Body.String()); got != "{}" {
		t.Errorf("session = %s, want {} once the row has expired", got)
	}
	if protected := h.call(t, http.MethodGet, "/api/v1/org", "", map[string]string{"Cookie": cookie}); protected.Code != http.StatusUnauthorized {
		t.Errorf("/api/v1/org with an expired session: status %d, want 401", protected.Code)
	}
}

func TestDisabledAccountLosesItsSessionImmediately(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	user, err := h.store.UserByEmail(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatalf("find user: %v", err)
	}
	if _, err := h.store.DB().Exec(`UPDATE app_user SET disabled_at = ? WHERE id = ?`, time.Now().UnixMilli(), user.ID); err != nil {
		t.Fatalf("disable user: %v", err)
	}

	// The cookie is still valid; the account behind it is not. Nothing was
	// cached in the cookie, so the change takes effect on the next request.
	res := h.call(t, http.MethodGet, "/api/v1/org", "", map[string]string{"Cookie": cookie})
	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a disabled account", res.Code)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	out := h.call(t, http.MethodPost, "/auth/logout", "", map[string]string{"Cookie": cookie})
	if out.Code != http.StatusOK {
		t.Fatalf("logout: status %d", out.Code)
	}
	cleared := out.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Value != "" || cleared[0].MaxAge >= 0 {
		t.Errorf("logout cookies = %+v, want one expiring clear", cleared)
	}

	after := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": cookie})
	if got := strings.TrimSpace(after.Body.String()); got != "{}" {
		t.Errorf("session after logout = %s, want {}", got)
	}
}

func TestSignOutThatCannotEndTheSessionCanBeRetried(t *testing.T) {
	h := newHarness(t)
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	cookie := h.signIn(t, "ada@example.com")

	// The database refuses the delete, as a full disk or a held lock would.
	if _, err := h.store.DB().ExecContext(t.Context(), `CREATE TRIGGER refuse_delete BEFORE DELETE ON session
		BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatalf("make deletes fail: %v", err)
	}

	failed := h.call(t, http.MethodPost, "/auth/logout", "", map[string]string{"Cookie": cookie})
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("logout: status %d, want 500 rather than a success that left the session alive", failed.Code)
	}
	for _, c := range failed.Result().Cookies() {
		if c.MaxAge < 0 {
			t.Fatal("the cookie was cleared, leaving the client no way to retry")
		}
	}
	if !strings.Contains(logs.String(), "could not end the session") {
		t.Errorf("log = %q, want the failure recorded", logs.String())
	}

	// Once the database recovers, the retry ends the session for real.
	if _, err := h.store.DB().ExecContext(t.Context(), `DROP TRIGGER refuse_delete`); err != nil {
		t.Fatalf("restore deletes: %v", err)
	}
	if retry := h.call(t, http.MethodPost, "/auth/logout", "", map[string]string{"Cookie": cookie}); retry.Code != http.StatusOK {
		t.Fatalf("retry: status %d", retry.Code)
	}
	after := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": cookie})
	if got := strings.TrimSpace(after.Body.String()); got != "{}" {
		t.Errorf("session after the retry = %s, want {}", got)
	}
}

// TestASessionThatCannotBeReadIsAServerError: a fault in the database is
// neither signed in nor signed out. Answering {} would send the console to
// sign-in, and a 401 is a refusal the data plane caches.
func TestASessionThatCannotBeReadIsAServerError(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	// The user read fails, as it would on a damaged file.
	if _, err := h.store.DB().ExecContext(t.Context(), `ALTER TABLE app_user RENAME TO app_user_gone`); err != nil {
		t.Fatalf("break the user read: %v", err)
	}

	if res := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": cookie}); res.Code != http.StatusInternalServerError {
		t.Errorf("page view: status %d, body %s; want 500", res.Code, res.Body)
	}
	res := h.call(t, http.MethodPost, "/internal/verify-session", `{}`, map[string]string{
		"X-Internal-Token": "internal-token-for-tests-0123456789abcdef",
		"Cookie":           cookie,
	})
	if res.Code != http.StatusInternalServerError {
		t.Errorf("relayed lookup: status %d, body %s; want 500, which the data plane retries", res.Code, res.Body)
	}
}

func TestSignInReplacesTheSessionID(t *testing.T) {
	h := newHarness(t)

	// A fixed pre-authentication cookie must not survive into the
	// authenticated session it was presented to.
	first := h.signIn(t, "ada@example.com")
	res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"ada@example.com"}`, map[string]string{"Cookie": first})
	second := res.Result().Cookies()[0]

	if first == second.Name+"="+second.Value {
		t.Fatal("the session id was reused across a sign-in")
	}
	after := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": first})
	if got := strings.TrimSpace(after.Body.String()); got != "{}" {
		t.Errorf("the old cookie still resolves: %s", got)
	}
}

/* -------------------------------------------------------- role gating -- */

func TestRoleGating(t *testing.T) {
	tests := []struct {
		name   string
		role   store.Role
		path   string
		status int
	}{
		{"owner reads keys", store.RoleOwner, "/api/v1/keys", http.StatusOK},
		{"admin reads keys", store.RoleAdmin, "/api/v1/keys", http.StatusOK},
		{"member cannot read keys", store.RoleMember, "/api/v1/keys", http.StatusForbidden},
		{"viewer cannot read keys", store.RoleViewer, "/api/v1/keys", http.StatusForbidden},
		{"member cannot read the audit log", store.RoleMember, "/api/v1/audit", http.StatusForbidden},
		{"admin reads the audit log", store.RoleAdmin, "/api/v1/audit", http.StatusOK},
		{"viewer reads the org", store.RoleViewer, "/api/v1/org", http.StatusOK},
		{"member reads the org", store.RoleMember, "/api/v1/org", http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			cookie := h.signIn(t, "ada@example.com")

			user, err := h.store.UserByEmail(t.Context(), "ada@example.com")
			if err != nil {
				t.Fatalf("find user: %v", err)
			}
			orgs, err := h.store.OrgsForUser(t.Context(), user.ID)
			if err != nil || len(orgs) == 0 {
				t.Fatalf("orgs = %v, err = %v", orgs, err)
			}
			if err := h.store.AddMember(t.Context(), orgs[0].ID, user.ID, tc.role); err != nil {
				t.Fatalf("set role: %v", err)
			}

			res := h.call(t, http.MethodGet, tc.path, "", map[string]string{"Cookie": cookie})
			if res.Code != tc.status {
				t.Errorf("%s as %s: status %d, want %d (body %s)", tc.path, tc.role, res.Code, tc.status, res.Body)
			}
		})
	}
}

func TestAPIRequiresASession(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/v1/org", "/api/v1/keys", "/api/v1/audit", "/api/v1/me/identities"} {
		if res := h.call(t, http.MethodGet, path, "", nil); res.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", path, res.Code)
		}
	}
}

func TestOrgSwitchRequiresMembership(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	outsider, _, err := h.store.UpsertFromProvider(t.Context(), store.Profile{
		Provider: "email", Subject: "bob@example.com", Email: "bob@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("seed outsider: %v", err)
	}
	theirs, err := h.store.OrgsForUser(t.Context(), outsider.ID)
	if err != nil || len(theirs) == 0 {
		t.Fatalf("outsider orgs = %v, err = %v", theirs, err)
	}

	res := h.call(t, http.MethodPost, "/auth/org", `{"orgId":"`+theirs[0].ID+`"}`, map[string]string{"Cookie": cookie})
	if res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: the client's claim of membership is never trusted", res.Code)
	}
}

/* ------------------------------------------------------------ api keys -- */

func TestAPIKeyIsShownOnceThenOnlyAsAPrefix(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	created := h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"primary"}`, map[string]string{"Cookie": cookie})
	if created.Code != http.StatusCreated {
		t.Fatalf("create key: status %d, body %s", created.Code, created.Body)
	}
	body := decode(t, created)
	secret, _ := body["secret"].(string)
	if !strings.HasPrefix(secret, "mk_live_") {
		t.Fatalf("secret = %q, want an mk_live_ prefix", secret)
	}

	listed := h.call(t, http.MethodGet, "/api/v1/keys", "", map[string]string{"Cookie": cookie})
	if strings.Contains(listed.Body.String(), secret) {
		t.Error("the list route returned a usable secret")
	}

	verified := h.call(t, http.MethodPost, "/internal/verify-key", `{"key":"`+secret+`"}`,
		map[string]string{"X-Internal-Token": "internal-token-for-tests-0123456789abcdef"})
	if verified.Code != http.StatusOK {
		t.Fatalf("verify: status %d, body %s", verified.Code, verified.Body)
	}
	if valid, _ := decode(t, verified)["valid"].(bool); !valid {
		t.Error("a freshly minted key did not verify")
	}

	id, _ := body["id"].(string)
	if revoked := h.call(t, http.MethodDelete, "/api/v1/keys/"+id, "", map[string]string{"Cookie": cookie}); revoked.Code != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", revoked.Code, revoked.Body)
	}

	after := h.call(t, http.MethodPost, "/internal/verify-key", `{"key":"`+secret+`"}`,
		map[string]string{"X-Internal-Token": "internal-token-for-tests-0123456789abcdef"})
	if after.Code != http.StatusUnauthorized {
		t.Errorf("verify after revoke: status %d, want 401", after.Code)
	}
}

func TestInternalRoutesDemandTheSharedToken(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name   string
		header map[string]string
	}{
		{"no token", nil},
		{"wrong token", map[string]string{"X-Internal-Token": "internal-token-for-tests-0123456789abcdee"}},
		{"truncated token", map[string]string{"X-Internal-Token": "internal-token"}},
	}
	for _, path := range []string{"/internal/verify-key", "/internal/verify-session"} {
		for _, tc := range tests {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				res := h.call(t, http.MethodPost, path, `{"key":"mk_live_whatever"}`, tc.header)
				if res.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", res.Code)
				}
			})
		}
	}
}

func TestVerifySessionNeitherExtendsNorReissues(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	_, value, _ := strings.Cut(cookie, "=")
	id, _, _ := strings.Cut(value, ".")
	before, err := h.store.GetSession(t.Context(), id)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}

	res := h.call(t, http.MethodPost, "/internal/verify-session", `{}`, map[string]string{
		"X-Internal-Token": "internal-token-for-tests-0123456789abcdef",
		"Cookie":           "theme=dark; " + cookie + "; other=1",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("verify-session: status %d, body %s", res.Code, res.Body)
	}
	if body := decode(t, res); body["email"] != "ada@example.com" {
		t.Errorf("body = %v, want the signed-in address", body)
	}
	if cookies := res.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("a relayed lookup re-issued %d cookies, want none", len(cookies))
	}

	after, err := h.store.GetSession(t.Context(), id)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if after.ExpiresAt != before.ExpiresAt {
		t.Errorf("expiry moved from %d to %d; a relay must not extend the browser's session", before.ExpiresAt, after.ExpiresAt)
	}
}

/* --------------------------------------------------------- magic links -- */

func TestMagicLinkIsSingleUse(t *testing.T) {
	h := newHarness(t)

	sent := h.call(t, http.MethodPost, "/auth/magic-link", `{"email":"Ada@Example.com"}`, nil)
	if sent.Code != http.StatusOK {
		t.Fatalf("request link: status %d, body %s", sent.Code, sent.Body)
	}

	link := h.lastLink(t)
	if !strings.HasPrefix(link, "http://127.0.0.1:8788/auth/magic-link/consume?token=") {
		t.Fatalf("link = %q, want one pointing at this service", link)
	}

	consumed := h.call(t, http.MethodGet, strings.TrimPrefix(link, "http://127.0.0.1:8788"), "", nil)
	if consumed.Code != http.StatusFound {
		t.Fatalf("consume: status %d, want 302", consumed.Code)
	}
	cookies := consumed.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("consume set %d cookies, want 1", len(cookies))
	}

	session := h.call(t, http.MethodGet, "/auth/session", "",
		map[string]string{"Cookie": cookies[0].Name + "=" + cookies[0].Value})
	user, _ := decode(t, session)["user"].(map[string]any)
	if user == nil || user["email"] != "ada@example.com" {
		t.Errorf("session = %v, want the lower-cased address", user)
	}

	replayed := h.call(t, http.MethodGet, strings.TrimPrefix(link, "http://127.0.0.1:8788"), "", nil)
	if replayed.Code != http.StatusSeeOther {
		t.Fatalf("replay: status %d, want 303", replayed.Code)
	}
	if location := replayed.Header().Get("Location"); !strings.Contains(location, "error=link_expired") {
		t.Errorf("replay sent the browser to %q, want the expired-link page", location)
	}
}

func TestMagicLinkUsesAndPersistsTheRequestedLocale(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.PseudoLocales = true })

	sent := h.call(
		t,
		http.MethodPost,
		"/auth/magic-link",
		`{"email":"mariam@example.com","locale":"ar-xb"}`,
		nil,
	)
	if sent.Code != http.StatusOK {
		t.Fatalf("request link: status %d, body %s", sent.Code, sent.Body)
	}
	msg := h.lastMessage(t)
	if !strings.Contains(msg.HTML, `<html lang="ar-XB" dir="rtl">`) {
		t.Errorf("email is not RTL pseudo-localized: %s", msg.HTML)
	}
	link := msg.Links[0]
	if !strings.Contains(link, "&locale=ar-xb") {
		t.Errorf("link = %q, want the locale for replay-safe error redirects", link)
	}

	consumed := h.call(t, http.MethodGet, strings.TrimPrefix(link, "http://127.0.0.1:8788"), "", nil)
	if consumed.Code != http.StatusFound {
		t.Fatalf("consume: status %d, want 302", consumed.Code)
	}
	cookie := consumed.Result().Cookies()[0]
	session := h.call(
		t,
		http.MethodGet,
		"/auth/session",
		"",
		map[string]string{"Cookie": cookie.Name + "=" + cookie.Value},
	)
	user, _ := decode(t, session)["user"].(map[string]any)
	if user["locale"] != "ar-xb" {
		t.Errorf("session user locale = %v, want ar-xb", user["locale"])
	}

	replayed := h.call(t, http.MethodGet, strings.TrimPrefix(link, "http://127.0.0.1:8788"), "", nil)
	if got := replayed.Header().Get("Location"); got != testOrigin+"/ar-xb/sign-in?error=link_expired" {
		t.Errorf("replay redirected to %q, want the localized sign-in page", got)
	}
}

func TestUnknownEmailLocaleFallsBackToEnglish(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.PseudoLocales = true })

	sent := h.call(
		t,
		http.MethodPost,
		"/auth/magic-link",
		`{"email":"ada@example.com","locale":"xx-unknown"}`,
		nil,
	)
	if sent.Code != http.StatusOK {
		t.Fatalf("request link: status %d, body %s", sent.Code, sent.Body)
	}
	msg := h.lastMessage(t)
	if !strings.Contains(msg.HTML, `<html lang="en" dir="ltr">`) {
		t.Errorf("email did not fall back to English: %s", msg.HTML)
	}
	if strings.Contains(msg.Links[0], "locale=") {
		t.Errorf("fallback link unexpectedly carries a locale: %s", msg.Links[0])
	}
}

func TestSignInFailuresLandOnTheSitesSignInPage(t *testing.T) {
	h := newHarness(t)
	// The site's sign-in page explains both; the old static paths no longer exist.
	for path, want := range map[string]string{
		"/auth/failed": testOrigin + "/sign-in?error=oauth_failed",
		"/auth/magic-link/consume?token=never-issued": testOrigin + "/sign-in?error=link_expired",
	} {
		res := h.call(t, http.MethodGet, path, "", nil)
		if res.Code != http.StatusSeeOther || res.Header().Get("Location") != want {
			t.Errorf("%s: status %d to %q, want 303 to %q", path, res.Code, res.Header().Get("Location"), want)
		}
	}
}

func TestMagicLinkExpires(t *testing.T) {
	h := newHarness(t)

	if res := h.call(t, http.MethodPost, "/auth/magic-link", `{"email":"ada@example.com"}`, nil); res.Code != http.StatusOK {
		t.Fatalf("request link: status %d", res.Code)
	}
	if _, err := h.store.DB().Exec(`UPDATE login_token SET expires_at = ?`, time.Now().Add(-time.Second).UnixMilli()); err != nil {
		t.Fatalf("expire token: %v", err)
	}

	link := h.lastLink(t)
	res := h.call(t, http.MethodGet, strings.TrimPrefix(link, "http://127.0.0.1:8788"), "", nil)
	if res.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 for an expired link", res.Code)
	}
	if len(res.Result().Cookies()) != 0 {
		t.Error("an expired link started a session")
	}
}

func TestMagicLinkRequiresAnAddress(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name string
		body string
	}{
		{"empty object", `{}`},
		{"blank", `{"email":"   "}`},
		{"no domain", `{"email":"ada"}`},
		{"no tld", `{"email":"ada@example"}`},
		{"spaces", `{"email":"ada lovelace@example.com"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := h.call(t, http.MethodPost, "/auth/magic-link", tc.body, nil)
			if res.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", res.Code)
			}
		})
	}

	var minted int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM login_token`).Scan(&minted); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if minted != 0 {
		t.Errorf("%d tokens were minted for invalid addresses, want 0", minted)
	}
}

func TestMagicLinkReturnsOnlyToOurOwnSite(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name     string
		returnTo string
		want     string
	}{
		{"same site path", "/settings?tab=keys", testOrigin + "/settings?tab=keys"},
		{"foreign origin", "https://evil.example/steal", testOrigin + "/apps"},
		{"protocol relative", "//evil.example/steal", testOrigin + "/apps"},
		{"absent", "", testOrigin + "/apps"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"email":"ada+` + strings.ReplaceAll(tc.name, " ", "") + `@example.com","returnTo":"` + tc.returnTo + `"}`
			if res := h.call(t, http.MethodPost, "/auth/magic-link", body, nil); res.Code != http.StatusOK {
				t.Fatalf("request link: status %d, body %s", res.Code, res.Body)
			}
			link := h.lastLink(t)
			res := h.call(t, http.MethodGet, strings.TrimPrefix(link, "http://127.0.0.1:8788"), "", nil)
			if got := res.Header().Get("Location"); got != tc.want {
				t.Errorf("landed at %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMagicLinkIsRefusedWithoutAnOutbox(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.MailOutbox = "" })

	res := h.call(t, http.MethodPost, "/auth/magic-link", `{"email":"ada@example.com"}`, nil)
	if res.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 rather than a success for a message never sent", res.Code)
	}
	if providers := decode(t, h.call(t, http.MethodGet, "/auth/providers", "", nil)); providers["magicLink"] != false {
		t.Errorf("providers advertised magicLink = %v, want false", providers["magicLink"])
	}
}

// lastLink reads the newest outbox message and returns the sign-in link in it.
func (h *harness) lastLink(t *testing.T) string {
	t.Helper()

	return h.lastMessage(t).Links[0]
}

func (h *harness) lastMessage(t *testing.T) message {
	t.Helper()

	entries, err := os.ReadDir(h.outbox)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}

	// Ordered by modification time, not by name: the name carries only
	// millisecond precision, and two messages written in one millisecond
	// would otherwise be ranked by the random part of their id.
	newest, newestAt := "", time.Time{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		if newest == "" || info.ModTime().After(newestAt) {
			newest, newestAt = e.Name(), info.ModTime()
		}
	}
	if newest == "" {
		t.Fatal("no message in the outbox")
	}

	raw, err := os.ReadFile(filepath.Join(h.outbox, newest))
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	var msg message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	// The outbox shape is a contract: other tooling reads these fields.
	if msg.To == "" || msg.Subject == "" || len(msg.Links) != 1 {
		t.Fatalf("message = %+v, want to/subject/links populated", msg)
	}
	return msg
}

// outboxLinks returns every link the outbox currently holds.
func (h *harness) outboxLinks(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(h.outbox)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	links := []string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(h.outbox, e.Name()))
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		var msg struct {
			Links []string `json:"links"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		links = append(links, msg.Links...)
	}
	return links
}

/* -------------------------------------------------------- origin guard -- */

func TestOriginGuard(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	tests := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		body    string
		status  int
	}{
		{
			name: "same origin passes", method: http.MethodPost, path: "/api/v1/keys",
			body: `{"label":"ok"}`, status: http.StatusCreated,
		},
		{
			name: "foreign origin is refused", method: http.MethodPost, path: "/api/v1/keys",
			headers: map[string]string{"Origin": "https://evil.example"},
			body:    `{"label":"stolen"}`, status: http.StatusForbidden,
		},
		{
			name: "sibling subdomain is refused", method: http.MethodPost, path: "/api/v1/keys",
			headers: map[string]string{"Origin": "https://evil.localhost:5173"},
			body:    `{"label":"stolen"}`, status: http.StatusForbidden,
		},
		{
			name: "opaque origin is refused", method: http.MethodPost, path: "/auth/logout",
			headers: map[string]string{"Origin": "null"}, status: http.StatusForbidden,
		},
		{
			name: "no origin but the browser says cross-site", method: http.MethodPost, path: "/auth/org",
			headers: map[string]string{"Origin": "", "Sec-Fetch-Site": "cross-site"},
			body:    `{"orgId":"org_x"}`, status: http.StatusForbidden,
		},
		{
			name: "reads are not guarded", method: http.MethodGet, path: "/auth/session",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, status: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{"Cookie": cookie}
			for k, v := range tc.headers {
				headers[k] = v
			}
			res := h.call(t, tc.method, tc.path, tc.body, headers)
			if res.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", res.Code, tc.status, res.Body)
			}
		})
	}

	// The refused logout did not sign anyone out.
	session := h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": cookie})
	if user, _ := decode(t, session)["user"].(map[string]any); user == nil {
		t.Error("a refused cross-site logout still ended the session")
	}
}

func TestAPIMutationsMustBeJSON(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	// A cross-site HTML form can only send these three, so demanding JSON
	// forces a preflight that only our own origins pass.
	for _, contentType := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		t.Run(contentType, func(t *testing.T) {
			res := h.call(t, http.MethodPost, "/api/v1/keys", `label=form`,
				map[string]string{"Cookie": cookie, "Content-Type": contentType})
			if res.Code != http.StatusUnsupportedMediaType {
				t.Errorf("status = %d, want 415", res.Code)
			}
		})
	}

	// A bodiless DELETE has nothing to type-check.
	created := h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"primary"}`, map[string]string{"Cookie": cookie})
	id, _ := decode(t, created)["id"].(string)
	if res := h.call(t, http.MethodDelete, "/api/v1/keys/"+id, "", map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Errorf("bodiless DELETE: status %d, want 200", res.Code)
	}
}

func TestInternalRoutesAreExemptFromTheOriginGuard(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn(t, "ada@example.com")

	// /internal/* authenticates with its token, not with an ambient cookie,
	// so it is not a cross-site forgery target.
	res := h.call(t, http.MethodPost, "/internal/verify-session", `{}`, map[string]string{
		"Origin":           "https://evil.example",
		"X-Internal-Token": "internal-token-for-tests-0123456789abcdef",
		"Cookie":           cookie,
	})
	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body %s)", res.Code, res.Body)
	}
}

/* ------------------------------------------------------- rate limiting -- */

func TestMagicLinkRateLimit(t *testing.T) {
	h := newHarness(t)

	// Five per hour per source and address; the sixth is refused.
	for i := 0; i < 5; i++ {
		res := h.call(t, http.MethodPost, "/auth/magic-link", `{"email":"ada@example.com"}`, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, body %s", i+1, res.Code, res.Body)
		}
	}

	refused := h.call(t, http.MethodPost, "/auth/magic-link", `{"email":"ada@example.com"}`, nil)
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("sixth request: status %d, want 429", refused.Code)
	}
	if refused.Header().Get("Retry-After") == "" {
		t.Error("a refusal did not say when to try again")
	}

	// A different address has its own budget, so one victim cannot be used to
	// lock everyone else out.
	other := h.call(t, http.MethodPost, "/auth/magic-link", `{"email":"grace@example.com"}`, nil)
	if other.Code != http.StatusOK {
		t.Errorf("a second address: status %d, want 200", other.Code)
	}
}

func TestSignInRateLimit(t *testing.T) {
	h := newHarness(t)

	// Twenty attempts per quarter hour on the sign-in surface.
	var last int
	for i := 0; i < 21; i++ {
		last = h.call(t, http.MethodGet, "/auth/magic-link/consume?token=guess", "", nil).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("twenty-first attempt: status %d, want 429", last)
	}
}

func TestInternalTrafficHasItsOwnBudget(t *testing.T) {
	h := newHarness(t)

	// The general ceiling is 300 a minute; the data plane's is far higher, so
	// exhausting the browser budget must not throttle it.
	for i := 0; i < 301; i++ {
		h.call(t, http.MethodGet, "/auth/providers", "", nil)
	}
	if res := h.call(t, http.MethodGet, "/auth/providers", "", nil); res.Code != http.StatusTooManyRequests {
		t.Fatalf("/auth/providers after 300 calls: status %d, want 429", res.Code)
	}

	// Probes keep answering. A 429 on readiness takes this instance out of
	// rotation and a 429 on liveness gets it restarted, so neither may ever
	// be refused for being too frequent.
	for _, probe := range []string{"/health", "/readyz"} {
		if res := h.call(t, http.MethodGet, probe, "", nil); res.Code != http.StatusOK {
			t.Errorf("%s under an exhausted general budget: status %d, want 200", probe, res.Code)
		}
	}

	res := h.call(t, http.MethodPost, "/internal/verify-key", `{"key":"mk_live_nope"}`,
		map[string]string{"X-Internal-Token": "internal-token-for-tests-0123456789abcdef"})
	if res.Code != http.StatusUnauthorized {
		t.Errorf("/internal/verify-key: status %d, want 401 rather than a shared 429", res.Code)
	}
}

/* ------------------------------------------------------------- surface -- */

func TestUnknownRouteIsAJSON404(t *testing.T) {
	h := newHarness(t)

	for _, target := range []string{"/nope", "/api/v1/nope", "/auth/nope"} {
		res := h.call(t, http.MethodGet, target, "", nil)
		if res.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, res.Code)
		}
		if got := res.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("%s: content-type %q, want JSON", target, got)
		}
	}
}

func TestSecurityHeadersAndCSP(t *testing.T) {
	h := newHarness(t)

	for _, target := range []string{"/health", "/api/v1/keys"} {
		res := h.call(t, http.MethodGet, target, "", nil)
		for _, header := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options"} {
			if res.Header().Get(header) == "" {
				t.Errorf("%s: missing %s", target, header)
			}
		}
	}

	// A JSON endpoint renders nothing, so its policy denies everything; the
	// pages this service redirects through get a nonce instead.
	api := h.call(t, http.MethodGet, "/api/v1/keys", "", nil).Header().Get("Content-Security-Policy")
	if api != "default-src 'none'; frame-ancestors 'none'; base-uri 'none'" {
		t.Errorf("/api CSP = %q, want the deny-everything policy", api)
	}
	page := h.call(t, http.MethodGet, "/health", "", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(page, "'nonce-") {
		t.Errorf("page CSP = %q, want a per-response nonce", page)
	}
}

func TestDevLoginIsAbsentInProduction(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Env = "production"
		c.AllowDevLogin = false
	})

	res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"ada@example.com"}`, nil)
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the route must not exist at all", res.Code)
	}
	if providers := decode(t, h.call(t, http.MethodGet, "/auth/providers", "", nil)); providers["devLogin"] != false {
		t.Errorf("providers advertised devLogin = %v, want false", providers["devLogin"])
	}
}

func TestHealthReportsTheService(t *testing.T) {
	// What -ldflags -X .../buildinfo.Version=... does to a release binary.
	unstamped := buildinfo.Version
	buildinfo.Version = "1.4.0-test"
	t.Cleanup(func() { buildinfo.Version = unstamped })
	h := newHarness(t)

	body := decode(t, h.call(t, http.MethodGet, "/health", "", nil))
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if body["version"] != "1.4.0-test" {
		t.Errorf("version = %v, want the build's own stamp, as --version prints it", body["version"])
	}
	if _, ok := body["providers"].([]any); !ok {
		t.Errorf("providers = %v, want an array even when none are configured", body["providers"])
	}
	if _, ok := body["uptimeSeconds"].(float64); !ok {
		t.Errorf("uptimeSeconds = %v, want a number", body["uptimeSeconds"])
	}
}

func TestSessionReportsAllowlist(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AllowedEmails = []string{"ada@example.com"} })

	allowed := func(email string) any {
		t.Helper()
		cookie := h.signIn(t, email)
		body := decode(t, h.call(t, http.MethodGet, "/auth/session", "", map[string]string{"Cookie": cookie}))
		user, _ := body["user"].(map[string]any)
		if user == nil {
			t.Fatalf("session for %s has no user: %v", email, body)
		}
		return user["allowed"]
	}

	if got := allowed("Ada@Example.com"); got != true {
		t.Errorf("allowlisted user: allowed = %v, want true", got)
	}
	if got := allowed("eve@example.com"); got != false {
		t.Errorf("other user: allowed = %v, want false", got)
	}
}
