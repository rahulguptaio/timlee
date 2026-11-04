package authsvc

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/store"
)

// The API key lifecycle beyond create and revoke: a lifetime chosen when the
// key is made, and rotation to a replacement with an overlap window.

const testInternalToken = "internal-token-for-tests-0123456789abcdef"

// verifies reports whether the data plane's check accepts a secret.
func (h *harness) verifies(t *testing.T, secret string) bool {
	t.Helper()
	res := h.call(t, http.MethodPost, "/internal/verify-key", `{"key":"`+secret+`"}`,
		map[string]string{"X-Internal-Token": testInternalToken})
	switch res.Code {
	case http.StatusOK:
		return true
	case http.StatusUnauthorized:
		return false
	}
	t.Fatalf("verify-key: status %d, body %s", res.Code, res.Body)
	return false
}

func TestKeyExpiryIsChosenWhenTheKeyIsMade(t *testing.T) {
	h := newHarness(t)
	_, orgID, cookie := h.founder(t, "ada@example.com")

	before := time.Now()
	res := h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"ci","expiresInDays":30}`, map[string]string{"Cookie": cookie})
	if res.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", res.Code, res.Body)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q on the response carrying the secret, want no-store", got)
	}
	body := decode(t, res)
	expiresAt, _ := body["expiresAt"].(float64)
	if lasts := time.UnixMilli(int64(expiresAt)).Sub(before); lasts < 30*24*time.Hour-time.Minute || lasts > 30*24*time.Hour+time.Minute {
		t.Errorf("expiresAt = %v, %v from now; want 30 days", expiresAt, lasts)
	}
	secret, _ := body["secret"].(string)
	if !h.verifies(t, secret) {
		t.Fatal("a key that has not expired was refused")
	}

	// Past its expiry, the data plane's check refuses it.
	if _, err := h.store.DB().ExecContext(t.Context(), `UPDATE api_key SET expires_at = ? WHERE org_id = ?`,
		time.Now().Add(-time.Second).UnixMilli(), orgID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if h.verifies(t, secret) {
		t.Error("an expired key was accepted")
	}

	// Without a lifetime the key never expires, as before.
	never := decode(t, h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"forever"}`, map[string]string{"Cookie": cookie}))
	if never["expiresAt"] != nil {
		t.Errorf("expiresAt = %v with no lifetime asked for, want null", never["expiresAt"])
	}

	for _, days := range []string{"0", "366", "-1", "1.5", `"30"`} {
		res := h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"bad","expiresInDays":`+days+`}`, map[string]string{"Cookie": cookie})
		if res.Code != http.StatusBadRequest {
			t.Errorf("expiresInDays %s: status %d, want 400", days, res.Code)
		}
	}
}

func TestRotateKeepsBothKeysWorkingForTheOverlap(t *testing.T) {
	h := newHarness(t)
	_, orgID, cookie := h.founder(t, "ada@example.com")
	old := decode(t, h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"worker"}`, map[string]string{"Cookie": cookie}))
	oldID, oldSecret := old["id"].(string), old["secret"].(string)

	res := h.call(t, http.MethodPost, "/api/v1/keys/"+oldID+"/rotate", `{"overlapHours":24}`, map[string]string{"Cookie": cookie})
	if res.Code != http.StatusCreated {
		t.Fatalf("rotate: status %d, body %s", res.Code, res.Body)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q on the response carrying the secret, want no-store", got)
	}
	rotated := decode(t, res)
	newSecret, _ := rotated["secret"].(string)
	if rotated["label"] != "worker" || rotated["replaces"] != oldID || newSecret == "" || newSecret == oldSecret {
		t.Fatalf("rotation = %v, want a new secret for %q replacing %s", rotated, "worker", oldID)
	}
	stops, _ := rotated["replacedKeyStopsAt"].(float64)
	if in := time.Until(time.UnixMilli(int64(stops))); in < 23*time.Hour || in > 25*time.Hour {
		t.Errorf("old key stops in %v, want 24 hours", in)
	}
	if !h.verifies(t, oldSecret) || !h.verifies(t, newSecret) {
		t.Fatal("both keys must work during the overlap")
	}

	// The overlap ends.
	if _, err := h.store.DB().ExecContext(t.Context(), `UPDATE api_key SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixMilli(), oldID); err != nil {
		t.Fatalf("end the overlap: %v", err)
	}
	if h.verifies(t, oldSecret) || !h.verifies(t, newSecret) {
		t.Error("after the overlap the old key must be refused and the new one accepted")
	}

	again := h.call(t, http.MethodPost, "/api/v1/keys/"+oldID+"/rotate", `{}`, map[string]string{"Cookie": cookie})
	if again.Code != http.StatusConflict {
		t.Errorf("rotating a replaced key: status %d, want 409", again.Code)
	}
	actions := map[string]int{}
	for _, action := range h.auditActions(t, orgID) {
		actions[action]++
	}
	if actions["api_key.rotated"] != 1 {
		t.Errorf("audit = %v, want the rotation recorded once", actions)
	}
}

func TestRotateWithNoOverlapStopsTheOldKeyAtOnce(t *testing.T) {
	h := newHarness(t)
	_, _, cookie := h.founder(t, "ada@example.com")
	old := decode(t, h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"leaked"}`, map[string]string{"Cookie": cookie}))

	res := h.call(t, http.MethodPost, "/api/v1/keys/"+old["id"].(string)+"/rotate", `{"overlapHours":0}`, map[string]string{"Cookie": cookie})
	if res.Code != http.StatusCreated {
		t.Fatalf("rotate: status %d, body %s", res.Code, res.Body)
	}
	if h.verifies(t, old["secret"].(string)) {
		t.Error("the old key still works after a rotation with no overlap")
	}
	if !h.verifies(t, decode(t, res)["secret"].(string)) {
		t.Error("the replacement does not work")
	}

	for _, hours := range []int{-1, 169} {
		res := h.call(t, http.MethodPost, "/api/v1/keys/"+old["id"].(string)+"/rotate",
			`{"overlapHours":`+strconv.Itoa(hours)+`}`, map[string]string{"Cookie": cookie})
		if res.Code != http.StatusBadRequest {
			t.Errorf("overlapHours %d: status %d, want 400", hours, res.Code)
		}
	}
}

func TestOnlyAdminsRotateKeysOfTheirOwnOrganisation(t *testing.T) {
	h := newHarness(t)
	_, orgID, ownerCookie := h.founder(t, "owner@example.com")
	key := decode(t, h.call(t, http.MethodPost, "/api/v1/keys", `{"label":"shared"}`, map[string]string{"Cookie": ownerCookie}))
	keyID := key["id"].(string)

	_, memberCookie := h.member(t, "member@example.com", orgID, store.RoleMember)
	if res := h.call(t, http.MethodPost, "/api/v1/keys/"+keyID+"/rotate", `{}`, map[string]string{"Cookie": memberCookie}); res.Code != http.StatusForbidden {
		t.Errorf("member rotating: status %d, want 403", res.Code)
	}

	_, _, outsiderCookie := h.founder(t, "outsider@example.com")
	if res := h.call(t, http.MethodPost, "/api/v1/keys/"+keyID+"/rotate", `{}`, map[string]string{"Cookie": outsiderCookie}); res.Code != http.StatusNotFound {
		t.Errorf("another organisation's owner rotating: status %d, want 404", res.Code)
	}
	if !h.verifies(t, key["secret"].(string)) {
		t.Error("a refused rotation changed the key")
	}
}
