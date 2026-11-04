package authsvc

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/logx"
)

// The user actions below are recorded as events, which name people by id and
// never by what they typed. Adding them adds no audit_event row: the store
// already writes api_key.created, api_key.revoked and org.created itself.

// only returns the single event named name, failing otherwise.
func only(t *testing.T, events *logx.Memory, name string) logx.Record {
	t.Helper()
	got := events.Records(name)
	if len(got) != 1 {
		t.Fatalf("want one %s, got %v", name, events.Records())
	}
	return got[0]
}

func attrs(t *testing.T, rec logx.Record, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if got, ok := rec.Attr(key); !ok || got.String() != value {
			t.Errorf("%s: %s = %q, want %q", rec.Name, key, got, value)
		}
	}
}

func TestAPIKeyEvents(t *testing.T) {
	h := newHarness(t)
	userID, orgID, cookie := h.founder(t, "founder@example.com")
	events := logx.Capture(t)

	res := h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"deploys for bob@example.com"}`,
		map[string]string{"Cookie": cookie, "X-Request-Id": "req-create-key"})
	if res.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", res.Code, res.Body)
	}
	key := decode(t, res)
	keyID, secret := key["id"].(string), key["secret"].(string)

	created := only(t, events, "audit.api_key.created")
	attrs(t, created, map[string]string{
		"key_id": keyID, "user_id": userID, "org_id": orgID, "request_id": "req-create-key",
		// A label is free text: an address in it is masked like any other.
		"label": "deploys for b***@example.com",
	})
	if line, _ := created.MarshalJSON(); strings.Contains(string(line), secret) || strings.Contains(string(line), secret[12:]) {
		t.Fatalf("the key reached the event: %s", line)
	}

	if res := h.call(t, http.MethodDelete, "/api/v1/keys/"+keyID, "", map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", res.Code, res.Body)
	}
	attrs(t, only(t, events, "audit.api_key.revoked"), map[string]string{"key_id": keyID, "user_id": userID, "org_id": orgID})

	rows := map[string]int{}
	for _, action := range h.auditActions(t, orgID) {
		rows[action]++
	}
	if rows["api_key.created"] != 1 || rows["api_key.revoked"] != 1 {
		t.Fatalf("one audit row per key action, the store's own: %v", rows)
	}
}

func TestFailedSignInEvents(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Providers = map[string]config.Provider{"github": {ClientID: "id", ClientSecret: "secret"}}
	})
	events := logx.Capture(t)

	// Nothing in progress.
	h.call(t, http.MethodGet, "/auth/github/callback?state=x&code=y", "", nil)

	// A flow, answered with the wrong state and then with a provider error
	// whose text anyone could have written.
	start := h.call(t, http.MethodGet, "/auth/github/start", "", nil)
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil || start.Code != http.StatusFound {
		t.Fatalf("start: status %d, location %q", start.Code, start.Header().Get("Location"))
	}
	cookie := start.Result().Cookies()[0]
	flowCookie := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	h.call(t, http.MethodGet, "/auth/github/callback?state=wrong", "", flowCookie)
	h.call(t, http.MethodGet, "/auth/github/callback?state="+url.QueryEscape(location.Query().Get("state"))+
		"&error=access_denied+for+eve%40example.com", "", flowCookie)

	h.call(t, http.MethodGet, "/auth/failed", "", nil)
	h.call(t, http.MethodGet, "/auth/magic-link/consume?token=never-issued", "", nil)

	got := events.Records("audit.auth.sign_in_failed")
	want := []map[string]string{
		{"provider": "github", "reason": "no_flow"},
		{"provider": "github", "reason": "state_mismatch"},
		{"provider": "github", "reason": "provider_denied"},
		{"provider": "", "reason": "unknown"},
		{"provider": "email", "reason": "link_invalid"},
	}
	if len(got) != len(want) {
		t.Fatalf("want %d sign_in_failed events, got %v", len(want), got)
	}
	for i := range want {
		attrs(t, got[i], want[i])
		if line, _ := got[i].MarshalJSON(); strings.Contains(string(line), "access_denied") || strings.Contains(string(line), "example.com") {
			t.Errorf("the provider's error text reached the event: %s", line)
		}
	}
}

func TestWorkspaceSessionAndSettingsEvents(t *testing.T) {
	h := newHarness(t)
	events := logx.Capture(t)

	cookie := h.signIn(t, "new@example.com")
	user, err := h.store.UserByEmail(t.Context(), "new@example.com")
	if err != nil {
		t.Fatal(err)
	}
	orgs, err := h.store.OrgsForUser(t.Context(), user.ID)
	if err != nil || len(orgs) != 1 {
		t.Fatalf("orgs = %v, err = %v", orgs, err)
	}
	attrs(t, only(t, events, "audit.org.created"), map[string]string{"user_id": user.ID, "org_id": orgs[0].ID})

	// Signing in again creates nothing.
	h.signIn(t, "new@example.com")
	only(t, events, "audit.org.created")

	if res := h.call(t, http.MethodPatch, "/api/v1/me", `{"name":"Grace Hopper"}`, map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("update: status %d, body %s", res.Code, res.Body)
	}
	changed := only(t, events, "audit.settings.changed")
	attrs(t, changed, map[string]string{"settings": "name", "user_id": user.ID})
	if line, _ := changed.MarshalJSON(); strings.Contains(string(line), "Grace") {
		t.Fatalf("a setting's value reached the event: %s", line)
	}

	if res := h.call(t, http.MethodPost, "/auth/logout", "", map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("logout: status %d, body %s", res.Code, res.Body)
	}
	attrs(t, only(t, events, "audit.session.revoked"), map[string]string{"user_id": user.ID})
}

// TestServerErrorsAreReportedOnce: a 500 leaves one request.failed event
// carrying the cause the handler had, with its route, and nothing else.
func TestServerErrorsAreReportedOnce(t *testing.T) {
	h := newHarness(t)
	events := logx.Capture(t)
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}

	res := h.call(t, http.MethodPost, "/auth/dev", `{"email":"someone@example.com"}`, map[string]string{"X-Request-Id": "req-broken-store"})
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, body %s", res.Code, res.Body)
	}
	failed := only(t, events, "request.failed")
	attrs(t, failed, map[string]string{"route": "POST /auth/dev", "status": "500", "request_id": "req-broken-store"})
	cause, _ := failed.Attr("error")
	if !strings.Contains(cause.String(), "database is closed") {
		t.Errorf("error = %s", cause)
	}
	if line, _ := failed.MarshalJSON(); strings.Contains(string(line), "someone") {
		t.Errorf("the address reached the event: %s", line)
	}
}

// TestEverySessionRequestNamesItsCaller: a route outside authed and scoped,
// sign-out here, still carries the caller's ids, read from the session.
func TestEverySessionRequestNamesItsCaller(t *testing.T) {
	h := newHarness(t)
	userID, orgID, cookie := h.founder(t, "reader@example.com")
	if res := h.call(t, http.MethodPost, "/auth/org", `{"orgId":"`+orgID+`"}`, map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("switch org: status %d, body %s", res.Code, res.Body)
	}
	events := logx.Capture(t)

	if res := h.call(t, http.MethodPost, "/auth/logout", "", map[string]string{"Cookie": cookie}); res.Code != http.StatusOK {
		t.Fatalf("logout: status %d, body %s", res.Code, res.Body)
	}
	attrs(t, only(t, events, "audit.session.revoked"), map[string]string{"user_id": userID, "org_id": orgID})
}
