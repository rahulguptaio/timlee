// Command timlee-build compiles Timlee's frontend assets.
//
// It replaces the Vite/npm toolchain outright: esbuild is linked in as a Go
// library, so a build is `go run ./cmd/timlee-build` and the toolchain is the
// Go toolchain. There is no Node on the build machine, no lockfile to drift
// and no second language runtime in CI.
//
// Usage:
//
//	go run ./cmd/timlee-build                 # production build + embedded assets
//	go run ./cmd/timlee-build -dev -watch     # rebuild on change, sourcemaps
//	go run ./cmd/timlee-build -out <dir>       # custom output, no embed mirror
//	go run ./cmd/timlee-build -embed internal/assets/embedded/static
//
// Every file directly inside -src whose extension is .ts, .tsx, .js or .css is
// an entry point; everything in a subdirectory is a module reached by import.
// Each entry is emitted as "<name>-<hash>.<ext>" and recorded in manifest.json
// under its logical name — see internal/assets for the schema.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rahulguptaio/timlee/internal/buildinfo"
)

const defaultEmbedDir = "internal/assets/embedded/static"

func main() {
	buildinfo.HandleVersionFlag("timlee-build")
	var (
		src     = flag.String("src", "web/assets", "directory holding the asset entry points")
		out     = flag.String("out", "web/dist", "directory to write the build into")
		base    = flag.String("base", "/assets/", "URL prefix the output is served under")
		embedTo = flag.String("embed", "", "mirror the output here for embed.FS builds")
		dev     = flag.Bool("dev", false, "skip minification and emit sourcemaps")
		watch   = flag.Bool("watch", false, "rebuild when a source file changes (implies -dev)")
		verbose = flag.Bool("v", false, "log every written file")
	)
	flag.Parse()

	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	// Watching is a development activity; minified output with no sourcemaps
	// is never what someone asked for by typing -watch.
	if *watch && !explicit["dev"] {
		*dev = true
	}
	*embedTo = embedDestination(*embedTo, explicit, *dev, *watch)

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(options{
		Src:     *src,
		Out:     *out,
		Base:    *base,
		EmbedTo: *embedTo,
		Dev:     *dev,
		Watch:   *watch,
		Log:     log,
	}); err != nil {
		log.Error("build failed", "error", err)
		os.Exit(1)
	}
}

// embedDestination makes the zero-flag command safe for the production build
// documented in README.md: the binary and its content-hashed asset manifest
// must be compiled from the same build. Custom output, source or URL layouts
// are used by checks and experiments, so they stay disk-only unless the caller
// explicitly asks for an embed mirror.
func embedDestination(current string, explicit map[string]bool, dev, watch bool) string {
	if explicit["embed"] {
		return current
	}
	if dev || watch || explicit["out"] || explicit["src"] || explicit["base"] {
		return ""
	}
	return defaultEmbedDir
}

func run(opts options) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	opts.Root = cwd
	opts.Src = absolute(cwd, opts.Src)
	opts.Out = absolute(cwd, opts.Out)
	if opts.EmbedTo != "" {
		opts.EmbedTo = absolute(cwd, opts.EmbedTo)
	}

	p, err := newPipeline(opts)
	if err != nil {
		return err
	}
	defer p.close()

	if !opts.Watch {
		return p.buildOnce()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return p.watch(ctx)
}

func absolute(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// errBuildFailed stands in for diagnostics esbuild has already printed, so the
// same text is not reported twice.
var errBuildFailed = errors.New("esbuild reported errors")

// since formats a duration the way a build log wants to read it.
func since(start time.Time) string {
	return fmt.Sprintf("%.0fms", float64(time.Since(start).Microseconds())/1000)
}
