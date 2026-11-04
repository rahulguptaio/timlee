// Package config loads service configuration from the environment.
//
// Every service reads the same struct, so a value means the same thing
// everywhere and a deployment is configured in one vocabulary. Defaults are
// development-shaped; Validate refuses to let those defaults reach production.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved configuration for a Timlee process.
type Config struct {
	// Env is "development", "test" or "production".
	Env string

	// ControlAddr is the auth service's listen address.
	ControlAddr string
	// WebAddr is the server-rendered site's listen address.
	WebAddr string

	// PublicOrigin is the externally visible origin of the site.
	PublicOrigin string
	// ControlOrigin is where browsers reach the auth service.
	ControlOrigin string

	// SessionSecret signs session cookies.
	SessionSecret string
	// InternalToken authenticates service-to-service calls to /internal/*.
	InternalToken string

	// DatabasePath is the SQLite file backing accounts and sessions.
	DatabasePath string

	// SessionTTL is how long a browser session stays valid.
	SessionTTL time.Duration

	// AllowDevLogin enables the password-less development sign-in route.
	AllowDevLogin bool

	// TrustProxy says whether X-Forwarded-For and X-Forwarded-Proto may be
	// believed. Off by default: those headers are client-supplied, so
	// trusting them without a proxy in front lets any caller forge its own
	// address and mint a fresh rate-limit bucket per request.
	TrustProxy bool

	// AllowedEmails are the lower-cased account addresses that may submit
	// prompts (TIMLEE_ALLOWED_EMAILS, comma-separated, case-insensitive).
	// Empty allows nobody: the allowlist fails closed.
	AllowedEmails []string

	// Providers holds OAuth/OIDC credentials, keyed by provider name.
	Providers map[string]Provider

	// MailOutbox is a directory magic-link mails are written to in
	// development instead of being sent.
	MailOutbox string

	// Locales are the IDs of the locales timlee-web serves (TIMLEE_LOCALES,
	// comma-separated, default "en"). The default locale is always served,
	// and internal/i18n refuses a locale without a catalog at start-up.
	Locales []string
	// PseudoLocales adds the generated test locales en-xa and ar-xb
	// (TIMLEE_PSEUDO_LOCALES). Never in production.
	PseudoLocales bool

	// Workspace is the Bazel monorepo generated apps live in (TIMLEE_WORKSPACE).
	Workspace string
	// PreviewAddr is the listen address of the preview server that serves each
	// app's last good build (TIMLEE_PREVIEW_ADDR).
	PreviewAddr string
	// PreviewOrigin is where browsers reach the preview server. It must be a
	// different site from PublicOrigin, so generated code never shares the
	// site's cookies or origin (TIMLEE_PREVIEW_ORIGIN).
	PreviewOrigin string
	// CopilotModel optionally pins the model prompts run with (TIMLEE_COPILOT_MODEL).
	CopilotModel string
	// CopilotMaxAICredits optionally caps AI credits per prompt
	// (TIMLEE_COPILOT_MAX_AI_CREDITS).
	CopilotMaxAICredits string
}

// Provider is one identity provider's credentials.
type Provider struct {
	ClientID     string
	ClientSecret string
	// Tenant is Microsoft Entra's directory; empty means "common".
	Tenant string
}

// Configured reports whether both halves of the credential are present.
func (p Provider) Configured() bool { return p.ClientID != "" && p.ClientSecret != "" }

// Development defaults that must never survive into production.
const (
	devSessionSecret = "dev-session-secret-change-me"
	devInternalToken = "dev-internal-token"
)

// Load reads configuration from the process environment.
func Load() Config {
	env := envOr("NODE_ENV", envOr("TIMLEE_ENV", "development"))

	cfg := Config{
		Env:           env,
		ControlAddr:   envOr("TIMLEE_CONTROL_ADDR", "127.0.0.1:8788"),
		WebAddr:       envOr("TIMLEE_WEB_ADDR", "127.0.0.1:5173"),
		PublicOrigin:  envOr("TIMLEE_PUBLIC_ORIGIN", "http://localhost:5173"),
		ControlOrigin: envOr("TIMLEE_CONTROL_ORIGIN", "http://127.0.0.1:8788"),
		SessionSecret: envOr("SESSION_SECRET", devSessionSecret),
		InternalToken: envOr("TIMLEE_INTERNAL_TOKEN", devInternalToken),
		DatabasePath:  envOr("TIMLEE_DB", ".local/data/timlee.db"),
		SessionTTL:    durationOr("TIMLEE_SESSION_TTL", 14*24*time.Hour),
		// Deliberately not defaulted. The outbox exists so development can
		// read sign-in links without a mail transport; defaulting it would
		// make production believe it had one, so magic-link requests would
		// report success while writing live account credentials to disk and
		// emailing nobody.
		MailOutbox: os.Getenv("TIMLEE_MAIL_OUTBOX"),
		Providers: map[string]Provider{
			"github": {
				ClientID:     os.Getenv("GITHUB_CLIENT_ID"),
				ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
			},
			"google": {
				ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
				ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
			},
			"microsoft": {
				ClientID:     os.Getenv("MICROSOFT_CLIENT_ID"),
				ClientSecret: os.Getenv("MICROSOFT_CLIENT_SECRET"),
				Tenant:       envOr("MICROSOFT_TENANT", "common"),
			},
		},
	}

	// Development sign-in is on by default outside production, because a
	// fresh clone has no identity provider configured and would otherwise
	// have no way in at all.
	cfg.AllowDevLogin = boolOr("TIMLEE_ALLOW_DEV_LOGIN", !cfg.IsProduction())

	// Outside production the outbox is how magic links are read, so it is
	// defaulted there and only there.
	if cfg.MailOutbox == "" && !cfg.IsProduction() {
		cfg.MailOutbox = ".local/mail"
	}

	cfg.TrustProxy = boolOr("TIMLEE_TRUST_PROXY", false)
	cfg.AllowedEmails = ParseEmailList(os.Getenv("TIMLEE_ALLOWED_EMAILS"))

	cfg.Locales = strings.Split(envOr("TIMLEE_LOCALES", "en"), ",")
	cfg.PseudoLocales = boolOr("TIMLEE_PSEUDO_LOCALES", false)

	cfg.Workspace = envOr("TIMLEE_WORKSPACE", "workspace")
	cfg.PreviewAddr = envOr("TIMLEE_PREVIEW_ADDR", "127.0.0.1:5174")
	cfg.PreviewOrigin = envOr("TIMLEE_PREVIEW_ORIGIN", "http://preview.localhost:5174")
	cfg.CopilotModel = os.Getenv("TIMLEE_COPILOT_MODEL")
	cfg.CopilotMaxAICredits = os.Getenv("TIMLEE_COPILOT_MAX_AI_CREDITS")

	return cfg
}

// ParseEmailList splits a comma-separated list of addresses, trimming and
// lower-casing each one and dropping blanks.
func ParseEmailList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if email := strings.ToLower(strings.TrimSpace(part)); email != "" {
			out = append(out, email)
		}
	}
	return out
}

// EmailAllowed reports whether email is on the allowlist. The comparison is
// case-insensitive, and an empty allowlist or address is never allowed.
func (c Config) EmailAllowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	return slices.Contains(c.AllowedEmails, email)
}

// IsProduction reports whether this process is serving real users.
func (c Config) IsProduction() bool { return c.Env == "production" }

// Validate refuses a configuration that would be unsafe in production.
//
// These are startup errors rather than warnings on purpose: a service that
// boots with a known secret is worse than one that does not boot, because the
// failure is silent.
func (c Config) Validate() error {
	if !c.IsProduction() {
		return nil
	}

	var problems []string
	if c.SessionSecret == devSessionSecret || len(c.SessionSecret) < 32 {
		problems = append(problems, "SESSION_SECRET must be set to at least 32 random bytes")
	}
	if c.InternalToken == devInternalToken || len(c.InternalToken) < 32 {
		problems = append(problems, "TIMLEE_INTERNAL_TOKEN must be set to at least 32 random bytes")
	}
	if c.AllowDevLogin {
		problems = append(problems, "TIMLEE_ALLOW_DEV_LOGIN must be off in production")
	}
	if !isHTTPSOrigin(c.PublicOrigin) {
		problems = append(problems, "TIMLEE_PUBLIC_ORIGIN must be https in production")
	}
	// ControlOrigin builds OAuth redirect URIs and magic-link URLs. Left at
	// its loopback default it boots happily and mails links nobody can open.
	if !isHTTPSOrigin(c.ControlOrigin) {
		problems = append(problems, "TIMLEE_CONTROL_ORIGIN must be https in production")
	}
	// Generated apps run arbitrary code. On the same site as the website or
	// the control plane, their requests would carry those cookies.
	if !isHTTPSOrigin(c.PreviewOrigin) || sameSite(c.PreviewOrigin, c.PublicOrigin) || sameSite(c.PreviewOrigin, c.ControlOrigin) {
		problems = append(problems, "TIMLEE_PREVIEW_ORIGIN must be https and on a different site (registrable domain) from TIMLEE_PUBLIC_ORIGIN and TIMLEE_CONTROL_ORIGIN in production")
	}
	// Every prompt spends the server owner's Copilot credits.
	if credits, err := strconv.ParseFloat(c.CopilotMaxAICredits, 64); err != nil || credits <= 0 {
		problems = append(problems, "TIMLEE_COPILOT_MAX_AI_CREDITS must cap AI credits per prompt in production")
	}
	// The outbox writes single-use sign-in tokens to the filesystem. That is
	// a development convenience and an account-takeover hazard anywhere else.
	if c.MailOutbox != "" {
		problems = append(problems, "TIMLEE_MAIL_OUTBOX must be empty in production; configure a real mail transport")
	}
	// Pseudo-locales are generated test text; a crawler or a reader must
	// never be served them.
	if c.PseudoLocales {
		problems = append(problems, "TIMLEE_PSEUDO_LOCALES must be off in production; pseudo-locales are for tests")
	}

	if len(problems) > 0 {
		return fmt.Errorf("refusing to start in production:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// sameSite reports whether two origins may share cookies or count as
// same-site: the same host (ports are ignored, as cookies ignore them), or
// hostnames ending in the same two labels. The second rule is deliberately
// conservative: without the public suffix list, "a.example.co.uk" and
// "b.example.co.uk" are treated as one site, which only ever refuses a
// configuration, never admits one.
func sameSite(a, b string) bool {
	pa, errA := url.Parse(a)
	pb, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return true
	}
	ha, hb := strings.ToLower(pa.Hostname()), strings.ToLower(pb.Hostname())
	if ha == hb {
		return true
	}
	if net.ParseIP(ha) != nil || net.ParseIP(hb) != nil {
		return false
	}
	return lastLabels(ha, 2) == lastLabels(hb, 2)
}

func lastLabels(host string, n int) string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) > n {
		labels = labels[len(labels)-n:]
	}
	return strings.Join(labels, ".")
}

func isHTTPSOrigin(raw string) bool {
	parsed, err := url.ParseRequestURI(raw)
	return err == nil &&
		parsed.Scheme == "https" &&
		parsed.Host != "" &&
		parsed.User == nil &&
		(parsed.Path == "" || parsed.Path == "/") &&
		parsed.RawQuery == "" &&
		parsed.Fragment == ""
}

// AllowedOrigins lists the origins permitted to make credentialed requests.
// The desktop app is not among them: it loads the site itself, so its
// requests come from the public origin, and the pages it bundles make none.
func (c Config) AllowedOrigins() []string {
	origins := []string{c.PublicOrigin, c.ControlOrigin}
	if !c.IsProduction() {
		origins = append(origins,
			"http://localhost:5173", "http://127.0.0.1:5173",
			"http://localhost:4173", "http://127.0.0.1:4173",
		)
	}

	seen := make(map[string]bool, len(origins))
	unique := origins[:0]
	for _, o := range origins {
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		unique = append(unique, o)
	}
	return unique
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func boolOr(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationOr(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return parsed
}
