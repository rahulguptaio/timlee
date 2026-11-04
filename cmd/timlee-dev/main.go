// Command timlee-dev runs the whole Timlee stack locally.
//
// It compiles each service, starts them in dependency order, waits for each to
// report healthy before launching the next, streams their logs into one
// terminal with a short coloured prefix, and stops everything cleanly on
// Ctrl-C — including anything a service spawned, so no stray process keeps a
// port bound.
//
// Development secrets are generated once into .local/secrets.env and reused,
// so a restart does not sign everyone out. They are never printed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rahulguptaio/timlee/internal/buildinfo"
	"github.com/rahulguptaio/timlee/internal/devstack"
)

const usage = `timlee-dev — run the Timlee stack locally

Usage:
  go run ./cmd/timlee-dev [flags]

Services, started in this order and each proven healthy before the next:
  auth    control plane: sessions, OAuth, organisations, API keys, allowlist
  web     server-rendered site

Flags:
  -services auth,web        which services to run (default all)
  -auth-port 8788           control plane port
  -web-port  5173           site port
  -host 127.0.0.1           address the services bind to
  -assets                   build frontend assets before starting (default true)
  -watch                    rebuild assets on change, supervised like a service
  -timeout 30s              how long a service may take to become healthy
  -grace 8s                 how long a service gets to exit on SIGTERM
  -h, --help                this message

The development sign-in account (dev@timlee.local) is allowlisted unless
TIMLEE_ALLOWED_EMAILS is set in the environment.

Examples:
  go run ./cmd/timlee-dev                       everything, default ports
  go run ./cmd/timlee-dev -services auth        control plane only
  go run ./cmd/timlee-dev -watch                rebuild assets while you edit

Ctrl-C stops everything and waits for it to exit. A second Ctrl-C kills
immediately.`

func main() {
	buildinfo.HandleVersionFlag("timlee-dev")
	os.Exit(run())
}

func run() int {
	flags := flag.NewFlagSet("timlee-dev", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprintln(os.Stderr, usage) }

	var (
		services = flags.String("services", strings.Join(devstack.ServiceNames, ","), "comma-separated services to run")
		authPort = flags.Int("auth-port", 8788, "control plane port")
		webPort  = flags.Int("web-port", 5173, "site port")
		host     = flags.String("host", "127.0.0.1", "address the services bind to")
		assets   = flags.Bool("assets", true, "build frontend assets before starting")
		watch    = flags.Bool("watch", false, "rebuild assets on change")
		timeout  = flags.Duration("timeout", 30*time.Second, "health-check budget per service")
		grace    = flags.Duration("grace", devstack.DefaultGrace, "shutdown grace period per service")
	)

	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return 1
	}

	enabled, err := parseServices(*services)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		fmt.Fprintln(os.Stderr, "\n"+usage)
		return 2
	}

	colored := devstack.ColorEnabled(os.Stdout, os.Getenv)
	printer := devstack.NewPrinter(os.Stdout, widthOf(enabled), colored)
	fail := func(message string) int {
		printer.Raw(devstack.Paint("✗ ", "red", colored) + message + "\n")
		return 1
	}

	local := filepath.Join(root, ".local")
	secrets, err := devstack.LoadSecrets(filepath.Join(local, "secrets.env"))
	if err != nil {
		return fail(err.Error())
	}
	if len(secrets.Created) > 0 {
		// Names only. A secret that reaches a terminal reaches a scrollback
		// buffer, a screen share and a pasted bug report.
		printer.Line("stack", "gray", fmt.Sprintf("generated %s in %s (reused from now on)",
			strings.Join(secrets.Created, " and "), rel(root, secrets.Path)))
	}

	stack := devstack.Stack{
		Root:          root,
		Host:          *host,
		Secrets:       secrets.Values,
		Ports:         map[string]int{"auth": *authPort, "web": *webPort},
		BinDir:        filepath.Join(local, "bin"),
		DataDir:       filepath.Join(local, "data"),
		MailDir:       filepath.Join(local, "mail"),
		HealthTimeout: *timeout,
	}
	for _, dir := range []string{stack.DataDir, stack.MailDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(err.Error())
		}
	}

	// Signals are trapped before anything is spawned, so a Ctrl-C during the
	// build still unwinds through the same shutdown path.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	interrupted, forced := devstack.SignalPhases(signals)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-interrupted
		cancel()
	}()

	builder := devstack.Builder{Root: root, BinDir: stack.BinDir, Printer: printer}
	stack.Services = nil
	for _, name := range enabled {
		printer.Line("stack", "gray", "building "+name+"…")
		if err := builder.Build(ctx, name); err != nil {
			if ctx.Err() != nil {
				return 0
			}
			return fail(err.Error())
		}
		stack.Services = append(stack.Services, name)
	}

	if *assets || *watch {
		printer.Line("stack", "gray", "building assets…")
		if err := devstack.BuildAssets(ctx, root, *watch); err != nil {
			if ctx.Err() != nil {
				return 0
			}
			return fail(err.Error())
		}
	}

	specs := stack.Specs()
	if *watch {
		specs = append(specs, devstack.AssetWatchSpec(root))
	}
	if err := devstack.Preflight(*host, specs); err != nil {
		return fail(err.Error())
	}

	supervisor := devstack.NewSupervisor(printer, *grace)
	runner := &devstack.Runner{Supervisor: supervisor, Printer: printer}

	// Ctrl-C is how this command is meant to end, so a clean stop is a
	// success. Only a service dying on its own is a failure.
	code := 0

	switch err := runner.StartAll(ctx, specs); {
	case err != nil && ctx.Err() != nil:
		// Interrupted mid-startup; fall through to shutdown.
	case err != nil:
		printer.Raw("\n" + devstack.Paint("✗ ", "red", colored) + err.Error() + "\n")
		code = 1
	default:
		printer.Raw(banner(stack, colored))

		select {
		case <-interrupted:
			printer.Line("stack", "gray", "stopping… (Ctrl-C again to kill)")
		case crashed, ok := <-supervisor.Failures():
			if ok {
				printer.Raw("\n" + devstack.Paint("✗ ", "red", colored) + crashed.Spec.Name +
					" exited unexpectedly (" + crashed.ExitDescription() + "). Stopping the stack.\n")
				for _, line := range crashed.LastLines() {
					printer.Raw("    " + line + "\n")
				}
				code = 1
			}
		}
	}

	done := make(chan struct{})
	go func() {
		supervisor.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-forced:
		printer.Line("stack", "gray", "second signal — killing everything now")
		supervisor.Kill()
		<-done
		code = 130
	}

	printer.Line("stack", "gray", "stopped.")
	return code
}

func parseServices(raw string) ([]string, error) {
	known := map[string]bool{}
	for _, name := range devstack.ServiceNames {
		known[name] = true
	}

	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !known[name] {
			return nil, fmt.Errorf("unknown service %q (choose from %s)", name, strings.Join(devstack.ServiceNames, ", "))
		}
		seen[name] = true
	}
	if len(seen) == 0 {
		return nil, errors.New("-services needs at least one service")
	}

	// Always dependency order, whatever order the flag listed them in.
	var ordered []string
	for _, name := range devstack.ServiceNames {
		if seen[name] {
			ordered = append(ordered, name)
		}
	}
	return ordered, nil
}

// repoRoot walks up from the working directory looking for go.mod, so the
// command works from anywhere inside the checkout.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not inside the Timlee checkout (no go.mod found above the working directory)")
		}
		dir = parent
	}
}

func widthOf(services []string) int {
	width := len("assets")
	for _, name := range services {
		width = max(width, len(name))
	}
	return width
}

func rel(root, path string) string {
	if relative, err := filepath.Rel(root, path); err == nil {
		return relative
	}
	return path
}

func banner(stack devstack.Stack, colored bool) string {
	bold := func(text string) string { return devstack.Paint(text, "bold", colored) }
	row := func(label, value string) string { return fmt.Sprintf("    %-10s %s\n", label, value) }

	var b strings.Builder
	b.WriteString("\n  " + devstack.Paint("✔", "green", colored) + " " + bold("Timlee is running") + "\n\n")

	if stack.Enabled("web") {
		b.WriteString(row("Site", stack.PublicOrigin()+"/"))
		b.WriteString(row("Sign in", stack.PublicOrigin()+"/sign-in  (development sign-in as "+devstack.DevEmail+")"))
	}
	if stack.Enabled("auth") {
		b.WriteString(row("Control", stack.URL("auth")+"/health"))
	}
	b.WriteString("\n  Ctrl-C stops everything.\n\n")
	return b.String()
}
