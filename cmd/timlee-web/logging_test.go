package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/logx"
)

// TestSessionLookupNamesTheCallerOnEvents: once the site has resolved who is
// reading, the request's events carry their ids, and only their ids.
func TestSessionLookupNamesTheCallerOnEvents(t *testing.T) {
	events := logx.Capture(t)
	h := newHarness(t)
	h.control.signedIn = true

	r := logx.Begin(httptest.NewRequest(http.MethodGet, "/console", nil), "req-web")
	r.AddCookie(session())
	if user := h.srv.auth.User(r.Context(), r); user == nil {
		t.Fatal("the stub session did not resolve")
	}
	logx.Event(r.Context(), "probe")

	rec := events.Records("probe")[0]
	for key, want := range map[string]string{"user_id": "u1", "org_id": "o1", "request_id": "req-web"} {
		if got, _ := rec.Attr(key); got.String() != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if line, _ := rec.MarshalJSON(); strings.Contains(string(line), "ada") || strings.Contains(string(line), "Acme") {
		t.Errorf("more than ids reached the event: %s", line)
	}
}
