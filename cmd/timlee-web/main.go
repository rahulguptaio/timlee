// Command timlee-web serves Timlee's website.
//
// Every page leaves this process complete: the copy, the footer links and —
// the part that matters — the signed-in state and the theme are all decided here, from the session cookie and the theme
// cookie the request carried. A browser with scripting switched off sees the
// same page as one without.
//
// Configuration comes entirely from the environment; see internal/config.
package main

import (
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"strconv"
	"time"

	"github.com/rahulguptaio/timlee/internal/appfactory"
	"github.com/rahulguptaio/timlee/internal/assets"
	"github.com/rahulguptaio/timlee/internal/assets/embedded"
	"github.com/rahulguptaio/timlee/internal/buildinfo"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/render"
	"github.com/rahulguptaio/timlee/web/locales"
	"github.com/rahulguptaio/timlee/web/public"
	templates "github.com/rahulguptaio/timlee/web/templates"
)

func main() {
	buildinfo.HandleVersionFlag("timlee-web")
	var (
		addr      = flag.String("addr", "", "listen address; overrides TIMLEE_WEB_ADDR")
		dev       = flag.Bool("dev", envBool("TIMLEE_WEB_DEV"), "re-read templates from disk on every request")
		tmplDir   = flag.String("templates", envOr("TIMLEE_TEMPLATE_DIR", templates.Dir), "template directory used in -dev")
		assetsDir = flag.String("assets", os.Getenv("TIMLEE_ASSET_DIR"), "built asset directory; empty uses the embedded tree")
	)
	flag.Parse()

	cfg := config.Load()

	level := slog.LevelInfo
	if !cfg.IsProduction() {
		level = slog.LevelDebug
	}
	info := buildinfo.Read()
	// Batched: a request line costs a copy, not a write system call made
	// under the logger's lock. Warnings and errors are written at once.
	logOut := httpx.NewLogWriter(os.Stdout)
	defer logOut.Close()
	logger := logx.New(logx.Options{
		Level: level, Writer: logOut,
		Service: "timlee-web", Version: info.Version, Commit: info.Commit, Env: cfg.Env,
	}).With("service", "timlee-web")
	// Before the first request: the session client shares
	// http.DefaultTransport's idle pool.
	httpx.TuneDefaultTransport()

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	// Behind our own proxy, believe its X-Forwarded-For: the reader's address
	// is what this site forwards to the planes so their limits apply per
	// reader (forward.go), and what its own logs and limits use.
	httpx.SetTrustProxy(cfg.TrustProxy)

	// Compiled before the listener opens, like the templates: a locale
	// without a catalog, or a message that does not parse, is a failed deploy.
	bundle, err := i18n.Load(locales.FS(), cfg.Locales, cfg.PseudoLocales)
	if err != nil {
		logger.Error("could not load the message catalogs", "error", err)
		os.Exit(1)
	}

	assetFS, manifest, err := loadAssets(*assetsDir)
	if err != nil {
		logger.Error("could not load the built assets", "error", err,
			"hint", "run: go run ./cmd/timlee-build")
		os.Exit(1)
	}

	funcs := render.FuncMap()
	// The manifest's helpers fail the render on an unknown name, which is the
	// only moment a mistyped asset is cheap to find.
	maps.Copy(funcs, template.FuncMap(manifest.FuncMap()))

	// Parsed before the listener opens: a malformed template must stop this
	// process, not greet a reader with a 500.
	engine, err := render.New(render.Options{
		FS:     templates.FS(),
		Dir:    *tmplDir,
		Dev:    *dev,
		Funcs:  funcs,
		Logger: logger,
	})
	if err != nil {
		logger.Error("could not parse templates", "error", err)
		os.Exit(1)
	}

	listen := cfg.WebAddr
	if *addr != "" {
		listen = *addr
	}

	rootFiles, err := loadRootFiles(public.FS(), cfg.PublicOrigin)
	if err != nil {
		logger.Error("could not load the root files", "error", err)
		os.Exit(1)
	}

	srv := &server{
		cfg:    cfg,
		log:    logger,
		engine: engine,
		origins: render.Origins{
			Public:  trimSlash(cfg.PublicOrigin),
			Control: trimSlash(cfg.ControlOrigin),
		},
		auth:      newAuthClient(cfg.ControlOrigin, cfg.PublicOrigin, logger),
		rootFiles: rootFiles,
		i18n:      bundle,
	}

	// The app workspace is optional: without it the site still serves, and
	// /apps is simply not routed.
	if apps, err := appfactory.New(appfactory.Config{
		Workspace:    cfg.Workspace,
		Model:        cfg.CopilotModel,
		MaxAICredits: cfg.CopilotMaxAICredits,
		Logger:       logger.With("component", "appfactory"),
	}); err != nil {
		logger.Warn("app workspace unavailable; /apps is disabled", "workspace", cfg.Workspace, "error", err)
	} else {
		srv.apps = apps
		srv.previews = newPreviewSigner(cfg.SessionSecret, cfg.PreviewOrigin)
		preview := httpx.NewServer(httpx.ServerOptions{
			Addr:    cfg.PreviewAddr,
			Handler: httpx.Chain(previewHandler(apps, srv.previews, cfg.PublicOrigin), httpx.Recover(logger)),
			Logger:  logger.With("component", "preview"),
		})
		go func() {
			// The workspace is useless without its previews; fail loudly
			// (for example, when the port is taken) rather than serve half.
			if err := preview.Run(); err != nil {
				logger.Error("preview server failed", "addr", cfg.PreviewAddr, "error", err)
				os.Exit(1)
			}
		}()
		logger.Info("app workspace ready", "workspace", apps.Workspace(), "preview", cfg.PreviewOrigin)
	}

	assetHandler := assets.NewHandler(assets.HandlerOptions{
		FS:       assetFS,
		Manifest: manifest,
		Logger:   logger,
	})
	mux, handler := srv.handler(assetHandler)

	server := httpx.NewServer(httpx.ServerOptions{
		Addr:    listen,
		Handler: handler,
		Logger:  logger,
		// HTML responses are finite, so a write deadline is safe here in a
		// way it is not on the services that stream.
		WriteTimeout: 30 * time.Second,
	})
	mux.Handle("GET /readyz", server.ReadinessHandler(func() error {
		// Ready means "can answer a page": templates parsed and assets
		// resolvable. Control-plane health is deliberately not included —
		// an unreachable auth service degrades a page to signed out, it
		// does not make this instance unable to serve.
		for _, name := range []string{"app.css", "app.js"} {
			if manifest.URL(name) == "" {
				return fmt.Errorf("asset %q is missing from the manifest", name)
			}
		}
		return nil
	}))

	logger.Info("timlee web starting",
		"addr", listen,
		"env", cfg.Env,
		"dev_templates", *dev,
		"pages", engine.Pages(),
		"public", cfg.PublicOrigin,
		"control", cfg.ControlOrigin,
	)

	if err := server.Run(); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// loadAssets resolves the built asset tree: a directory when one is named, so
// a rebuild is visible without recompiling, and the embedded tree otherwise,
// so a release binary is the only artefact that ships.
func loadAssets(dir string) (fs.FS, *assets.Manifest, error) {
	var fsys fs.FS
	if dir != "" {
		fsys = assets.Dir(dir)
	} else {
		embeddedFS, err := embedded.FS()
		if err != nil {
			return nil, nil, err
		}
		fsys = embeddedFS
	}

	manifest, err := assets.LoadManifest(fsys)
	if err != nil {
		return nil, nil, err
	}
	return fsys, manifest, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	parsed, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && parsed
}
