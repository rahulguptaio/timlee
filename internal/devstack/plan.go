package devstack

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Turning a developer's intent into an exact set of processes.
//
// Everything a child sees is constructed here and nothing is inherited
// implicitly, because the services must agree on shared secrets and origins:
// a stale value left in a shell would otherwise look like a bug in a service.

// managedEnv lists the variables this supervisor owns. They are stripped from
// the inherited environment and then set explicitly per service.
var managedEnv = []string{
	"TIMLEE_ENV", "NODE_ENV",
	"TIMLEE_CONTROL_ADDR", "TIMLEE_WEB_ADDR",
	"TIMLEE_PUBLIC_ORIGIN", "TIMLEE_CONTROL_ORIGIN",
	"SESSION_SECRET", "TIMLEE_INTERNAL_TOKEN",
	"TIMLEE_DB", "TIMLEE_MAIL_OUTBOX", "TIMLEE_ALLOW_DEV_LOGIN",
	"TIMLEE_ASSET_DIR",
	"TIMLEE_WORKSPACE", "TIMLEE_PREVIEW_ADDR", "TIMLEE_PREVIEW_ORIGIN",
}

// ServiceNames are the services this command can run, in dependency order.
//
// The order is the contract: the site resolves every session against the
// control plane, so auth is started and proven healthy before web.
var ServiceNames = []string{"auth", "web"}

// DevEmail is the account the development sign-in uses when no address is
// given. The stack allowlists it unless TIMLEE_ALLOWED_EMAILS is set.
const DevEmail = "dev@timlee.local"

// packages maps a service to the command that implements it.
var packages = map[string]string{
	"auth": "cmd/timlee-auth",
	"web":  "cmd/timlee-web",
}

// Stack is a resolved local deployment.
type Stack struct {
	Root     string
	Host     string
	Services []string
	Ports    map[string]int
	Secrets  map[string]string

	BinDir  string
	DataDir string
	MailDir string

	HealthTimeout time.Duration

	// Inherit is the environment to derive from; nil means the process's own.
	Inherit []string
}

// Addr is the listen address for one service.
func (s Stack) Addr(service string) string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Ports[service]))
}

// URL is how another process reaches one service.
func (s Stack) URL(service string) string { return "http://" + s.Addr(service) }

// PublicOrigin is the origin a browser uses. It is the site when the site is
// running, and the control plane otherwise, so the cross-site guard still has
// a first-party origin to allow.
func (s Stack) PublicOrigin() string {
	if s.Enabled("web") {
		return "http://localhost:" + strconv.Itoa(s.Ports["web"])
	}
	return s.URL("auth")
}

// Enabled reports whether a service is part of this run.
func (s Stack) Enabled(name string) bool {
	for _, candidate := range s.Services {
		if candidate == name {
			return true
		}
	}
	return false
}

// env builds one service's complete environment.
func (s Stack) env(service string) []string {
	inherit := s.Inherit
	if inherit == nil {
		inherit = os.Environ()
	}

	managed := make(map[string]bool, len(managedEnv))
	for _, name := range managedEnv {
		managed[name] = true
	}

	values := map[string]string{}
	for _, entry := range inherit {
		key, value, found := strings.Cut(entry, "=")
		if !found || managed[key] {
			continue
		}
		values[key] = value
	}

	values["TIMLEE_ENV"] = "development"
	values["TIMLEE_PUBLIC_ORIGIN"] = s.PublicOrigin()
	values["TIMLEE_CONTROL_ORIGIN"] = s.URL("auth")

	switch service {
	case "auth":
		values["TIMLEE_CONTROL_ADDR"] = s.Addr("auth")
		values["SESSION_SECRET"] = s.Secrets["SESSION_SECRET"]
		values["TIMLEE_INTERNAL_TOKEN"] = s.Secrets["TIMLEE_INTERNAL_TOKEN"]
		values["TIMLEE_DB"] = filepath.Join(s.DataDir, "timlee.db")
		values["TIMLEE_MAIL_OUTBOX"] = s.MailDir
		// A fresh clone has no identity provider configured, so without this
		// there would be no way to sign in at all.
		values["TIMLEE_ALLOW_DEV_LOGIN"] = "1"
		// Prompts are allowlisted and the allowlist fails closed, so the
		// development account is allowed unless the developer chose a list.
		if values["TIMLEE_ALLOWED_EMAILS"] == "" {
			values["TIMLEE_ALLOWED_EMAILS"] = DevEmail
		}

	case "web":
		values["TIMLEE_WEB_ADDR"] = s.Addr("web")
		// What BuildAssets and -watch write (cmd/timlee-build's default
		// -out), not the tree compiled into the binary.
		values["TIMLEE_ASSET_DIR"] = filepath.Join(s.Root, "web", "dist")
		values["TIMLEE_WORKSPACE"] = filepath.Join(s.Root, "workspace")
		// Generated apps are previewed on the next port at preview.localhost:
		// browsers resolve *.localhost to the loopback, and it is a different
		// site from both the website (localhost) and the control plane
		// (127.0.0.1), so previews never carry either's cookies. (An IPv6
		// literal would also do, but CSP frame-src cannot name one.)
		previewPort := strconv.Itoa(s.Ports["web"] + 1)
		values["TIMLEE_PREVIEW_ADDR"] = net.JoinHostPort(s.Host, previewPort)
		values["TIMLEE_PREVIEW_ORIGIN"] = "http://preview.localhost:" + previewPort
	}

	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

// Specs returns the services to run, in dependency order.
func (s Stack) Specs() []Spec {
	timeout := s.HealthTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	specs := make([]Spec, 0, len(s.Services))
	for index, name := range ServiceNames {
		if !s.Enabled(name) {
			continue
		}

		health := &Health{URL: s.URL(name) + "/health", Timeout: timeout}
		if name == "web" {
			// Serving the landing page with anything that is not a server
			// error proves the listener is up and the templates render.
			health.URL = s.URL(name) + "/"
			health.Accept = func(status int) bool { return status < 500 }
		}

		specs = append(specs, Spec{
			Name:    name,
			Command: filepath.Join(s.BinDir, binaryName(name)),
			Dir:     s.Root,
			Env:     s.env(name),
			Color:   Palette[index%len(Palette)],
			Port:    s.Ports[name],
			Health:  health,
		})
	}
	return specs
}

func binaryName(service string) string {
	name := filepath.Base(packages[service])
	if isWindows() {
		return name + ".exe"
	}
	return name
}

/* -------------------------------------------------------------- compile -- */

// Builder compiles the service binaries.
//
// Services are compiled and then executed directly rather than run with
// `go run`. That wrapper would sit between the supervisor and the process
// that actually holds the port, and killing the wrapper leaves the service
// running — which is exactly how a local stack ends up unable to restart.
type Builder struct {
	Root    string
	BinDir  string
	Printer *Printer
}

// ErrNotImplemented reports a service whose package exists but has no Go
// files yet, which is a normal state mid-migration rather than a failure.
type ErrNotImplemented struct{ Service, Package string }

func (e *ErrNotImplemented) Error() string {
	return e.Package + " has no Go files yet"
}

// Available reports whether a service's package has any source to build.
func (b Builder) Available(service string) bool {
	matches, err := filepath.Glob(filepath.Join(b.Root, packages[service], "*.go"))
	return err == nil && len(matches) > 0
}

// Build compiles one service into BinDir.
func (b Builder) Build(ctx context.Context, service string) error {
	pkg, ok := packages[service]
	if !ok {
		return fmt.Errorf("unknown service %q", service)
	}
	if !b.Available(service) {
		return &ErrNotImplemented{Service: service, Package: pkg}
	}
	if err := os.MkdirAll(b.BinDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", b.BinDir, err)
	}

	output := filepath.Join(b.BinDir, binaryName(service))
	cmd := exec.CommandContext(ctx, goTool(), "build", "-o", output, "./"+pkg)
	cmd.Dir = b.Root
	cmd.Env = os.Environ()

	combined, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(combined))
		if detail == "" {
			return fmt.Errorf("build %s: %w", pkg, err)
		}
		return fmt.Errorf("build %s: %w\n%s", pkg, err, detail)
	}
	return nil
}

// BuildAssets runs the asset pipeline once, so the site has something to
// serve. Its output goes straight to the terminal: a compile error belongs in
// front of the developer unprefixed.
func BuildAssets(ctx context.Context, root string, dev bool) error {
	args := []string{"run", "./cmd/timlee-build"}
	if dev {
		args = append(args, "-dev")
	}

	cmd := exec.CommandContext(ctx, goTool(), args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("asset build failed: %w", err)
	}
	return nil
}

// AssetWatchSpec supervises the asset pipeline in watch mode, as one more
// child so it is streamed, signalled and reaped like everything else.
func AssetWatchSpec(root string) Spec {
	return Spec{
		Name:    "assets",
		Command: goTool(),
		Args:    []string{"run", "./cmd/timlee-build", "-watch"},
		Dir:     root,
		Env:     os.Environ(),
		Color:   "yellow",
	}
}

func goTool() string {
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		candidate := filepath.Join(goroot, "bin", "go")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "go"
}

func isWindows() bool { return os.PathSeparator == '\\' }
