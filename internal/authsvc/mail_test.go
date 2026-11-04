package authsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mailocales "github.com/rahulguptaio/timlee/internal/authsvc/locales"
	"github.com/rahulguptaio/timlee/internal/i18n"
)

func TestEmailMessagesGolden(t *testing.T) {
	t.Parallel()

	bundle, err := i18n.Load(mailocales.FS(), nil, true)
	if err != nil {
		t.Fatalf("load email catalogs: %v", err)
	}
	now := time.Date(2026, time.October, 30, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		msg  message
	}{
		{
			name: "magic-en",
			msg: magicLinkMessage(
				bundle.Default().Localizer(),
				"ada@example.com",
				"https://auth.example/magic?token=secret",
				now,
				now.Add(magicLinkTTL),
			),
		},
		{
			name: "invite-en",
			msg: invitationMessage(
				bundle.Default().Localizer(),
				"bea@example.com",
				"Analytical Engines",
				"owner@example.com",
				"https://timlee.example/invite?token=secret",
				now,
				now.Add(invitationTTL),
			),
		},
		{
			name: "magic-ar-xb",
			msg: magicLinkMessage(
				mustMailLocale(t, bundle, "ar-xb"),
				"مريم@example.com",
				"https://auth.example/magic?token=secret&locale=ar-xb",
				now,
				now.Add(magicLinkTTL),
			),
		},
		{
			name: "invite-ar-xb",
			msg: invitationMessage(
				mustMailLocale(t, bundle, "ar-xb"),
				"مريم@example.com",
				"فريق Acme",
				"owner@example.com",
				"https://timlee.example/ar-xb/invite?token=secret",
				now,
				now.Add(invitationTTL),
			),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(struct {
				Subject string `json:"subject"`
				Text    string `json:"text"`
				HTML    string `json:"html"`
			}{Subject: tc.msg.Subject, Text: tc.msg.Text, HTML: tc.msg.HTML}, "", "  ")
			if err != nil {
				t.Fatalf("marshal message: %v", err)
			}
			got = append(got, '\n')

			path := filepath.Join("testdata", "email", tc.name+".golden")
			if os.Getenv("UPDATE_EMAIL_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("create golden directory: %v", err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s differs; run UPDATE_EMAIL_GOLDEN=1 go test ./internal/authsvc", path)
			}
		})
	}
}

func TestEmailRenderingIsBidiSafe(t *testing.T) {
	t.Parallel()

	bundle, err := i18n.Load(mailocales.FS(), nil, true)
	if err != nil {
		t.Fatalf("load email catalogs: %v", err)
	}
	l := mustMailLocale(t, bundle, "ar-xb")
	now := time.Date(2026, time.October, 30, 9, 30, 0, 0, time.UTC)
	msg := invitationMessage(
		l,
		"مريم@example.com",
		"فريق Acme",
		"owner@example.com",
		"https://timlee.example/ar-xb/invite?token=secret",
		now,
		now.Add(invitationTTL),
	)

	if !strings.Contains(msg.HTML, `<html lang="ar-XB" dir="rtl">`) {
		t.Errorf("HTML root is not RTL: %s", msg.HTML)
	}
	for _, value := range []string{"مريم@example.com", "فريق Acme", "owner@example.com"} {
		if !strings.Contains(msg.HTML, "<bdi>"+value+"</bdi>") {
			t.Errorf("HTML does not isolate %q", value)
		}
		if !strings.Contains(msg.Text, "\u2068"+value+"\u2069") {
			t.Errorf("plain text does not isolate %q", value)
		}
	}
	if !strings.Contains(msg.HTML, "<bdi>https://timlee.example/ar-xb/invite?token=secret</bdi>") {
		t.Error("HTML does not isolate the visible URL")
	}
}

func mustMailLocale(t *testing.T, bundle *i18n.Bundle, id string) *i18n.Localizer {
	t.Helper()
	loc, ok := bundle.ByID(id)
	if !ok {
		t.Fatalf("locale %q is not loaded", id)
	}
	return loc.Localizer()
}
