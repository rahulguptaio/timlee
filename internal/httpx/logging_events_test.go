package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/logx"
)

// TestLoggingIsTheEventBoundary: the request line stays what it was, and the
// event channel gets exactly one request.failed per failed request, with the
// ids the handler learned and the cause it recorded.
func TestLoggingIsTheEventBoundary(t *testing.T) {
	events := logx.Capture(t)
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok/{id}", func(w http.ResponseWriter, r *http.Request) {
		logx.Identify(r.Context(), logx.IDs{UserID: "usr_1", OrgID: "org_1"})
		WriteJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /fail/{id}", func(w http.ResponseWriter, r *http.Request) {
		logx.Identify(r.Context(), logx.IDs{UserID: "usr_1", OrgID: "org_1"})
		logx.Fail(r, errors.New("store unavailable"))
		WriteError(w, http.StatusServiceUnavailable, "Try again.", "unavailable")
	})
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	handler := Chain(mux, RequestID(), Logging(logger), Recover(logger))

	for _, path := range []string{"/ok/usr_1", "/fail/usr_1", "/panic"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Request-Id", "req"+strings.ReplaceAll(path, "/", "-"))
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	failed := events.Records("request.failed")
	if len(failed) != 2 {
		t.Fatalf("want two request.failed events, got %v", events.Records())
	}
	want := []map[string]string{
		{"request_id": "req-fail-usr_1", "route": "GET /fail/{id}", "status": "503", "user_id": "usr_1", "org_id": "org_1"},
		{"request_id": "req-panic", "route": "GET /panic", "status": "500", "panic": "true"},
	}
	for i, fields := range want {
		for key, value := range fields {
			if got, _ := failed[i].Attr(key); got.String() != value {
				t.Errorf("event %d: %s = %q, want %q", i, key, got, value)
			}
		}
	}
	if cause, _ := failed[0].Attr("error"); !strings.Contains(cause.String(), "store unavailable") {
		t.Errorf("error = %s", cause)
	}

	// The operational line is unchanged: no ids, whatever the scope knew.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var request map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &request); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for key := range request {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if want := []string{"bytes", "duration_ms", "level", "method", "msg", "path", "request_id", "status", "time"}; !slices.Equal(keys, want) {
		t.Errorf("request line keys %v, want %v", keys, want)
	}
}
