package config

import (
	"strings"
	"testing"
)

// production returns a Config that would be safe to serve real users, so each
// test can spoil exactly one thing and prove that one thing is caught.
func production() Config {
	return Config{
		Env:                 "production",
		PublicOrigin:        "https://timlee.example",
		ControlOrigin:       "https://timlee.example",
		PreviewOrigin:       "https://timlee-preview.example",
		CopilotMaxAICredits: "5",
		SessionSecret:       strings.Repeat("s", 48),
		InternalToken:       strings.Repeat("t", 48),
		AllowDevLogin:       false,
	}
}

func TestValidateAcceptsASafeProductionConfig(t *testing.T) {
	if err := production().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRefusesUnsafeProductionConfig(t *testing.T) {
	tests := []struct {
		name   string
		spoil  func(*Config)
		expect string
	}{
		{
			// Booting with a published default is worse than not booting,
			// because the failure is silent and every session is forgeable.
			name:   "development session secret",
			spoil:  func(c *Config) { c.SessionSecret = devSessionSecret },
			expect: "SESSION_SECRET",
		},
		{
			name:   "short session secret",
			spoil:  func(c *Config) { c.SessionSecret = "too-short" },
			expect: "SESSION_SECRET",
		},
		{
			name:   "development internal token",
			spoil:  func(c *Config) { c.InternalToken = devInternalToken },
			expect: "TIMLEE_INTERNAL_TOKEN",
		},
		{
			// Dev sign-in needs no password, so leaving it on in production
			// is an open door to every account.
			name:   "development sign-in left enabled",
			spoil:  func(c *Config) { c.AllowDevLogin = true },
			expect: "TIMLEE_ALLOW_DEV_LOGIN",
		},
		{
			name:   "preview served from the site's own host",
			spoil:  func(c *Config) { c.PreviewOrigin = "https://timlee.example:8443" },
			expect: "TIMLEE_PREVIEW_ORIGIN",
		},
		{
			// A subdomain is same-site: its requests carry SameSite cookies.
			name:   "preview on a subdomain of the site",
			spoil:  func(c *Config) { c.PreviewOrigin = "https://preview.timlee.example" },
			expect: "TIMLEE_PREVIEW_ORIGIN",
		},
		{
			name: "preview sharing the control plane's site",
			spoil: func(c *Config) {
				c.ControlOrigin = "https://auth.timlee-cp.example"
				c.PreviewOrigin = "https://apps.timlee-cp.example"
			},
			expect: "TIMLEE_PREVIEW_ORIGIN",
		},
		{
			name:   "no per-prompt credit cap",
			spoil:  func(c *Config) { c.CopilotMaxAICredits = "" },
			expect: "TIMLEE_COPILOT_MAX_AI_CREDITS",
		},
		{
			// Session cookies are Secure in production; over http the browser
			// would never send them back and nothing would work.
			name:   "insecure public origin",
			spoil:  func(c *Config) { c.PublicOrigin = "http://timlee.example" },
			expect: "TIMLEE_PUBLIC_ORIGIN",
		},
		{
			// ControlOrigin builds OAuth redirect URIs and magic-link URLs.
			// Left at its loopback default, production mails links to
			// 127.0.0.1 that no recipient can open.
			name:   "control origin left at loopback",
			spoil:  func(c *Config) { c.ControlOrigin = "http://127.0.0.1:8788" },
			expect: "TIMLEE_CONTROL_ORIGIN",
		},
		{
			// The outbox writes single-use sign-in tokens to disk. In
			// production that is a pile of live account-takeover credentials,
			// and no mail is ever actually sent.
			name:   "mail outbox configured",
			spoil:  func(c *Config) { c.MailOutbox = ".local/mail" },
			expect: "TIMLEE_MAIL_OUTBOX",
		},
		{
			// Pseudo-locales are machine-made test text: served in
			// production, a reader or a crawler would see it.
			name:   "pseudo-locales enabled",
			spoil:  func(c *Config) { c.PseudoLocales = true },
			expect: "TIMLEE_PSEUDO_LOCALES",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := production()
			tt.spoil(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate accepted an unsafe production config")
			}
			if !strings.Contains(err.Error(), tt.expect) {
				t.Fatalf("error %q does not name %s, so an operator cannot tell what to fix", err, tt.expect)
			}
		})
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	cfg := production()
	cfg.SessionSecret = devSessionSecret
	cfg.InternalToken = devInternalToken
	cfg.AllowDevLogin = true

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted an unsafe production config")
	}
	// Fixing one problem only to be told about the next is a miserable
	// deployment loop, so all of them are reported together.
	for _, want := range []string{"SESSION_SECRET", "TIMLEE_INTERNAL_TOKEN", "TIMLEE_ALLOW_DEV_LOGIN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestValidateIsLenientOutsideProduction(t *testing.T) {
	// A fresh clone has no secrets configured and must still run, or nobody
	// can work on it.
	for _, env := range []string{"development", "test"} {
		cfg := Config{Env: env, SessionSecret: devSessionSecret, InternalToken: devInternalToken, AllowDevLogin: true}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate rejected %s config: %v", env, err)
		}
	}
}

func TestAllowedOriginsDedupesAndTrustsNoWebviewOrigin(t *testing.T) {
	cfg := Config{
		Env:           "production",
		PublicOrigin:  "https://timlee.example",
		ControlOrigin: "https://timlee.example",
	}

	origins := cfg.AllowedOrigins()

	seen := map[string]int{}
	for _, o := range origins {
		seen[o]++
	}
	for o, n := range seen {
		if n > 1 {
			t.Errorf("origin %q listed %d times", o, n)
		}
	}
	// Every Tauri app on a machine shares these origins, and Timlee's own
	// loads the site rather than calling it cross-origin.
	for _, webview := range []string{"tauri://localhost", "http://tauri.localhost", "https://tauri.localhost"} {
		if seen[webview] != 0 {
			t.Errorf("credentialed requests allowed from %q, which any Tauri app can claim", webview)
		}
	}
	// Localhost must not be trusted by a production deployment.
	for o := range seen {
		if strings.Contains(o, "localhost:5173") {
			t.Errorf("production allows development origin %q", o)
		}
	}
}

func TestProviderConfigured(t *testing.T) {
	tests := []struct {
		name string
		p    Provider
		want bool
	}{
		{"both halves", Provider{ClientID: "id", ClientSecret: "secret"}, true},
		{"missing secret", Provider{ClientID: "id"}, false},
		{"missing id", Provider{ClientSecret: "secret"}, false},
		{"empty", Provider{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Configured(); got != tt.want {
				t.Fatalf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMailOutboxIsNotDefaultedInProduction(t *testing.T) {
	// The regression this guards: an unconditional default made
	// production believe it had a mail transport, so magic-link requests
	// reported success while writing live sign-in tokens to disk and
	// sending nothing.
	t.Setenv("TIMLEE_ENV", "production")
	t.Setenv("TIMLEE_MAIL_OUTBOX", "")

	if got := Load().MailOutbox; got != "" {
		t.Fatalf("MailOutbox = %q in production, want empty", got)
	}
}

func TestMailOutboxIsDefaultedOutsideProduction(t *testing.T) {
	// Development has no mail transport, and reading the link from the
	// outbox is the only way to sign in.
	t.Setenv("TIMLEE_ENV", "development")
	t.Setenv("TIMLEE_MAIL_OUTBOX", "")

	if Load().MailOutbox == "" {
		t.Fatal("MailOutbox empty in development; magic-link sign-in would be unusable")
	}
}

func TestTrustProxyDefaultsOff(t *testing.T) {
	// X-Forwarded-For is client-supplied. Believing it without a proxy in
	// front lets a caller forge its address and bypass rate limiting.
	t.Setenv("TIMLEE_TRUST_PROXY", "")
	if Load().TrustProxy {
		t.Fatal("TrustProxy defaulted on")
	}

	t.Setenv("TIMLEE_TRUST_PROXY", "true")
	if !Load().TrustProxy {
		t.Fatal("TIMLEE_TRUST_PROXY=true was not honoured")
	}
}

func TestLocalesDefaultToEnglishAndPseudoToOff(t *testing.T) {
	t.Setenv("NODE_ENV", "")
	t.Setenv("TIMLEE_ENV", "development")
	t.Setenv("TIMLEE_LOCALES", "")
	t.Setenv("TIMLEE_PSEUDO_LOCALES", "")
	cfg := Load()
	if len(cfg.Locales) != 1 || cfg.Locales[0] != "en" || cfg.PseudoLocales {
		t.Fatalf("Locales = %q, PseudoLocales = %v; want [en] and off", cfg.Locales, cfg.PseudoLocales)
	}

	t.Setenv("TIMLEE_LOCALES", "en,de,ar")
	t.Setenv("TIMLEE_PSEUDO_LOCALES", "1")
	cfg = Load()
	if strings.Join(cfg.Locales, " ") != "en de ar" || !cfg.PseudoLocales {
		t.Fatalf("Locales = %q, PseudoLocales = %v", cfg.Locales, cfg.PseudoLocales)
	}
	// Outside production the pseudo-locales are allowed: they are how the
	// gates exercise long and right-to-left text.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestEmailAllowedIsCaseInsensitiveAndFailsClosed(t *testing.T) {
	t.Setenv("TIMLEE_ALLOWED_EMAILS", " Ada@Example.com, ,grace@example.com ")
	cfg := Load()

	for _, email := range []string{"ada@example.com", "ADA@EXAMPLE.COM", " grace@example.com "} {
		if !cfg.EmailAllowed(email) {
			t.Errorf("EmailAllowed(%q) = false, want true", email)
		}
	}
	for _, email := range []string{"", "eve@example.com", "ada@example.co"} {
		if cfg.EmailAllowed(email) {
			t.Errorf("EmailAllowed(%q) = true, want false", email)
		}
	}

	t.Setenv("TIMLEE_ALLOWED_EMAILS", "")
	if Load().EmailAllowed("ada@example.com") {
		t.Error("an empty allowlist allowed an address; it must fail closed")
	}
}
