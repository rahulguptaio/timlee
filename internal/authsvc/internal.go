package authsvc

import (
	"errors"
	"net/http"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/store"
)

// Verification for the inference data plane, which keeps no database of its
// own and must stay dependency-free.
//
// Both routes are oracles: anyone who can call them can test candidate
// credentials. They therefore require the shared internal token, are rate
// limited on their own budget, and are exempt from the cross-site guard
// because they authenticate with that token rather than an ambient cookie.

// internalOnly demands the data plane's shared secret, compared in constant
// time, and charges the call to the internal rate-limit budget.
func (s *Service) internalOnly(next http.Handler) http.Handler {
	limited := s.limits.internal.Middleware(s.logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !store.SecretsMatch(r.Header.Get("X-Internal-Token"), s.cfg.InternalToken) {
			s.logger.Warn("rejected internal call",
				"route", r.URL.Path, "ip", httpx.ClientIP(r), "request_id", httpx.IDOf(r))
			httpx.WriteError(w, http.StatusForbidden, "Forbidden.", "not_authorized")
			return
		}
		next.ServeHTTP(w, r)
	}))
	return limited
}

func (s *Service) handleVerifyKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Send a JSON body.", "bad_request")
		return
	}
	if body.Key == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Missing key.", "bad_request")
		return
	}

	key, err := s.store.VerifyAPIKey(r.Context(), body.Key)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteJSON(w, r, http.StatusUnauthorized, map[string]any{"valid": false})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"valid":     true,
		"orgId":     key.OrgID,
		"keyId":     key.ID,
		"label":     key.Label,
		"scopes":    key.Scopes,
		"rateLimit": dataPlaneRateLimit,
	})
}

// handleVerifySession resolves a browser session relayed by the data plane,
// so the console can call /v1/* with its session cookie instead of a key.
//
// The caller forwards the browser's original Cookie header verbatim. Nothing
// is written: no session is created, extended or re-issued here, because the
// browser's own requests to /auth and /api must keep sole control of that.
func (s *Service) handleVerifySession(w http.ResponseWriter, r *http.Request) {
	user := userOf(r)
	if user == nil {
		httpx.WriteJSON(w, r, http.StatusUnauthorized, map[string]any{"valid": false})
		return
	}

	// Membership is re-checked: a session can outlive the user's access to
	// the organisation it last selected. Read with the session
	// (sessionParts).
	current := sessionOf(r)
	orgID := ""
	if active := activeOrg(current.memberships, current); active != nil {
		orgID = active.ID
	}

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"valid":     true,
		"userId":    user.ID,
		"orgId":     orgID,
		"email":     user.Email,
		"rateLimit": dataPlaneRateLimit,
	})
}
