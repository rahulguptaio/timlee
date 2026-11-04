package devstack

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envOf indexes one service's resolved environment.
func envOf(t *testing.T, stack Stack, service string) map[string]string {
	t.Helper()
	values := map[string]string{}
	for _, entry := range stack.env(service) {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func testStack() Stack {
	return Stack{
		Root:    "/repo",
		Host:    "127.0.0.1",
		Ports:   map[string]int{"auth": 18788, "web": 15173},
		Secrets: map[string]string{"SESSION_SECRET": "session-value", "TIMLEE_INTERNAL_TOKEN": "shared-token"},
		BinDir:  "/repo/.local/bin",
		DataDir: "/repo/.local/data",
		MailDir: "/repo/.local/mail",
		// A developer's shell holding a stale token is the case that silently
		// breaks service-to-service calls, so it is always part of the fixture.
		Inherit: []string{
			"PATH=/usr/bin",
			"TIMLEE_INTERNAL_TOKEN=stale-from-the-shell",
			"TIMLEE_CONTROL_ADDR=:9999",
			"SOME_TOOL_TOKEN=passed-through",
		},
		Services: []string{"auth", "web"},
	}
}

func TestEnvScrubsManagedVariables(t *testing.T) {
	stack := testStack()
	auth := envOf(t, stack, "auth")

	if auth["TIMLEE_INTERNAL_TOKEN"] != "shared-token" {
		t.Fatalf("a stale inherited token was not scrubbed: %q", auth["TIMLEE_INTERNAL_TOKEN"])
	}
	if auth["TIMLEE_CONTROL_ADDR"] != "127.0.0.1:18788" {
		t.Fatalf("the listen address must come from the flags, got %q", auth["TIMLEE_CONTROL_ADDR"])
	}
	if auth["SOME_TOOL_TOKEN"] != "passed-through" {
		t.Fatal("unmanaged variables must still be inherited")
	}
	if envOf(t, stack, "web")["SESSION_SECRET"] != "" {
		t.Fatal("the site has no use for the session secret and must not receive it")
	}
}

func TestEnvAllowlistsTheDevAccountUnlessTheDeveloperChose(t *testing.T) {
	stack := testStack()
	if got := envOf(t, stack, "auth")["TIMLEE_ALLOWED_EMAILS"]; got != DevEmail {
		t.Fatalf("default allowlist = %q, want %q", got, DevEmail)
	}

	stack.Inherit = append(stack.Inherit, "TIMLEE_ALLOWED_EMAILS=ada@example.com")
	if got := envOf(t, stack, "auth")["TIMLEE_ALLOWED_EMAILS"]; got != "ada@example.com" {
		t.Fatalf("explicit allowlist = %q, want the developer's", got)
	}
}

func TestEnvPointsEachServiceAtTheOthers(t *testing.T) {
	stack := testStack()

	for _, service := range []string{"auth", "web"} {
		values := envOf(t, stack, service)
		if values["TIMLEE_CONTROL_ORIGIN"] != "http://127.0.0.1:18788" {
			t.Fatalf("%s: control origin %q", service, values["TIMLEE_CONTROL_ORIGIN"])
		}
		// The site is the browser's origin, so it is what the cross-site
		// guard must allow.
		if values["TIMLEE_PUBLIC_ORIGIN"] != "http://localhost:15173" {
			t.Fatalf("%s: public origin %q", service, values["TIMLEE_PUBLIC_ORIGIN"])
		}
	}
}

// The site must serve what the asset pipeline just wrote. Left to itself it
// falls back to the tree compiled into the binary, which is empty in a fresh
// clone and stale under -watch.
func TestEnvServesTheSiteFromTheBuildOutput(t *testing.T) {
	stack := testStack()
	stack.Inherit = append(stack.Inherit, "TIMLEE_ASSET_DIR=/somewhere/else")

	if got, want := envOf(t, stack, "web")["TIMLEE_ASSET_DIR"], filepath.Join(stack.Root, "web", "dist"); got != want {
		t.Fatalf("web asset dir = %q, want %q", got, want)
	}
	if got := envOf(t, stack, "auth")["TIMLEE_ASSET_DIR"]; got != "" {
		t.Fatalf("only the site serves assets, auth got %q", got)
	}
}

// Generated apps must preview on a different site from the one holding the
// session cookie: another host, not only another port.
func TestEnvPreviewsAppsOnAnotherSite(t *testing.T) {
	env := envOf(t, testStack(), "web")
	preview, err := url.Parse(env["TIMLEE_PREVIEW_ORIGIN"])
	if err != nil {
		t.Fatal(err)
	}
	public, err := url.Parse(env["TIMLEE_PUBLIC_ORIGIN"])
	if err != nil {
		t.Fatal(err)
	}
	control, err := url.Parse(env["TIMLEE_CONTROL_ORIGIN"])
	if err != nil {
		t.Fatal(err)
	}
	if preview.Hostname() == public.Hostname() || preview.Hostname() == control.Hostname() {
		t.Fatalf("preview %q shares a host with the site %q or control plane %q", preview, public, control)
	}
	if !strings.HasSuffix(env["TIMLEE_PREVIEW_ADDR"], ":"+preview.Port()) {
		t.Fatalf("preview listens on %q but browsers use %q", env["TIMLEE_PREVIEW_ADDR"], preview)
	}
}

// Without the site there is still a first-party origin, or the cross-site
// guard would have nothing to allow and every POST would be refused.
func TestPublicOriginFallsBackToTheControlPlaneWhenTheSiteIsNotRunning(t *testing.T) {
	stack := testStack()
	stack.Services = []string{"auth"}

	if got := stack.PublicOrigin(); got != "http://127.0.0.1:18788" {
		t.Fatalf("public origin without the site: %q", got)
	}
}

func TestSpecsAreInDependencyOrderWhateverWasRequested(t *testing.T) {
	stack := testStack()
	stack.Services = []string{"web", "auth"}

	var names []string
	for _, spec := range stack.Specs() {
		names = append(names, spec.Name)
	}
	if strings.Join(names, ",") != "auth,web" {
		t.Fatalf("specs must follow dependency order, got %v", names)
	}

	specs := stack.Specs()
	if specs[0].Health.URL != "http://127.0.0.1:18788/health" {
		t.Fatalf("auth health url: %q", specs[0].Health.URL)
	}
	// Anything that is not a server error proves the site is serving.
	if specs[1].Health.Accept == nil || !specs[1].Health.Accept(404) || specs[1].Health.Accept(500) {
		t.Fatal("the site's readiness check should accept any non-server-error status")
	}
	if filepath.Base(specs[1].Command) != "timlee-web" {
		t.Fatalf("services must run their compiled binary, got %q", specs[1].Command)
	}
}

func TestSecretsAreGeneratedOnceAndReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".local", "secrets.env")

	first, err := LoadSecrets(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if len(first.Created) != len(SecretNames) {
		t.Fatalf("first run should generate every secret, created %v", first.Created)
	}
	for _, name := range SecretNames {
		if len(first.Values[name]) < 32 {
			t.Fatalf("%s is too short to be a credential", name)
		}
	}

	second, err := LoadSecrets(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(second.Created) != 0 {
		t.Fatalf("a second run must reuse secrets, regenerated %v", second.Created)
	}
	// Regenerating the session secret would sign every developer out on
	// every restart.
	for _, name := range SecretNames {
		if second.Values[name] != first.Values[name] {
			t.Fatalf("%s changed between runs", name)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the secrets file must be owner-only, got %04o", mode)
	}
}

func TestSecretsKeepUnknownKeysAndFillOnlyWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")

	existing := "# comment\nSESSION_SECRET=kept-value\nCUSTOM_KEY=also-kept\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	secrets, err := LoadSecrets(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if secrets.Values["SESSION_SECRET"] != "kept-value" {
		t.Fatal("an existing secret must never be overwritten")
	}
	if secrets.Values["CUSTOM_KEY"] != "also-kept" {
		t.Fatal("unrelated keys in the file must survive a rewrite")
	}
	if len(secrets.Created) != 1 || secrets.Created[0] != "TIMLEE_INTERNAL_TOKEN" {
		t.Fatalf("only the missing secret should be generated, created %v", secrets.Created)
	}
}

func TestBuilderReportsAPackageWithNoSourceYet(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "timlee-web"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	builder := Builder{Root: root, BinDir: filepath.Join(root, ".local", "bin")}
	if builder.Available("web") {
		t.Fatal("an empty package is not available to build")
	}

	var missing *ErrNotImplemented
	if err := builder.Build(t.Context(), "web"); !errors.As(err, &missing) {
		t.Fatalf("want ErrNotImplemented so the stack can skip it, got %v", err)
	}
	if missing.Service != "web" {
		t.Fatalf("wrong service: %q", missing.Service)
	}
}
