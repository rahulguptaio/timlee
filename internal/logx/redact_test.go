package logx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// canary stands for every credential an event could be handed. It must not
// survive into anything a sink receives.
const canary = "C4n4ryS3cretQ7x"

func TestRedactionNeverLeaksTheCanary(t *testing.T) {
	events := Capture(t)
	apiKey := "mk_live_" + canary + "abcdEFGH"

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	// A client chooses its own request id, so it is checked like any value.
	r = Begin(r, "id-"+apiKey)
	Event(r.Context(), "canary.test",
		slog.String("authorization", "Bearer "+canary),
		slog.String("Set-Cookie", "__Host-timlee.sid="+canary), //nolint:sloglint // a header's own name, as a careless caller would pass it
		slog.String("csrf_token", canary),
		slog.String("password", canary),
		slog.String("x-api-key", canary), //nolint:sloglint // likewise
		slog.String("session_id", canary),
		slog.String("note", "sent "+apiKey+" upstream"),
		slog.String("header", "Authorization: Bearer "+canary),
		slog.String("jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiI"+canary+"In0.c2ln"+canary),
		slog.String("callback", "https://auth.test/cb?code="+canary+"&state="+canary+"&ok=1"),
		slog.Group("nested", slog.String("token", canary), slog.String("detail", "key "+apiKey)),
		slog.Any("list", []string{"Bearer " + canary}),
		slog.Any("value", struct{ Key string }{apiKey}),
		Err(fmt.Errorf("verify %s: %w", apiKey, errors.New("denied"))),
		slog.String("invitee", "alice.smith@example.com"),
		// Counts and ids are not credentials and must survive.
		slog.Int("tokens_saved", 42),
		slog.String("key_id", "key_abc123"),
	)

	rec := events.Records("canary.test")[0]
	line, err := rec.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), canary) {
		t.Fatalf("the canary survived redaction: %s", line)
	}
	for _, want := range []string{
		`"authorization":"[redacted]"`, `"Set-Cookie":"[redacted]"`, `"csrf_token":"[redacted]"`,
		`"password":"[redacted]"`, `"x-api-key":"[redacted]"`, `"session_id":"[redacted]"`,
		`"note":"sent [redacted] upstream"`, `"header":"Authorization: Bearer [redacted]"`,
		`"invitee":"a***@example.com"`, `"tokens_saved":42`, `"key_id":"key_abc123"`,
		`"request_id":"id-[redacted]"`, `"chain":["*fmt.wrapError","*errors.errorString"]`,
	} {
		if !strings.Contains(string(line), want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if v, _ := rec.Attr("callback"); v.String() != "https://auth.test/cb?code=[redacted]&state=[redacted]&ok=1" {
		t.Errorf("callback = %s", v)
	}
}

func TestSensitiveKey(t *testing.T) {
	for _, key := range []string{
		"authorization", "Authorization", "cookie", "Set-Cookie", "api_key", "x-api-key", "apikey",
		"token", "access_token", "csrf_token", "secret", "client_secret", "password", "session_id", "sid",
	} {
		if !sensitiveKey(key) {
			t.Errorf("%q should be redacted", key)
		}
	}
	for _, key := range []string{
		"tokens_saved", "est_tokens_before", "prompt_tokens", "key_id", "user_id", "org_id", "route", "keys",
	} {
		if sensitiveKey(key) {
			t.Errorf("%q should be kept", key)
		}
	}
}

func TestErrListsTheChain(t *testing.T) {
	err := fmt.Errorf("opening store: %w", errors.Join(fmt.Errorf("db: %w", fs.ErrNotExist), context.Canceled))
	got := Err(err)
	if got.Key != "error" {
		t.Fatalf("key %q", got.Key)
	}
	var chain []string
	for _, member := range got.Value.Group() {
		if member.Key == "chain" {
			chain, _ = member.Value.Any().([]string)
		}
	}
	want := []string{"*fmt.wrapError", "*errors.joinError", "*fmt.wrapError", "*errors.errorString", "*errors.errorString"}
	if !slices.Equal(chain, want) {
		t.Fatalf("chain %v, want %v", chain, want)
	}
	if Err(nil).Key != "" {
		t.Fatal("Err(nil) must be an empty attribute, which handlers drop")
	}
}
