package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rahulguptaio/timlee/internal/appfactory"
	"github.com/rahulguptaio/timlee/internal/assets"
	"github.com/rahulguptaio/timlee/internal/config"
)

// appsStack is the settings stack with a scratch app workspace and an
// allowlist of one.
func appsStack(t *testing.T) (*stack, *appfactory.Factory) {
	t.Helper()
	st := newStack(t, func(c *config.Config) {
		c.AllowedEmails = []string{"ada@example.com"}
		c.PreviewOrigin = "http://127.0.0.1:5174"
	})

	ws := filepath.Join(t.TempDir(), "workspace")
	for _, dir := range []string{"templates/app", "packages/design/src"} {
		if err := os.CopyFS(filepath.Join(ws, dir), os.DirFS(filepath.Join("..", "..", "workspace", dir))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "MODULE.bazel"), []byte("module(name = \"t\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", ws}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	apps, err := appfactory.New(appfactory.Config{Workspace: ws, Bazel: "false", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}

	// Routes are registered when the handler is built, so rebuild it with
	// the factory in place.
	st.srv.apps = apps
	st.srv.previews = newPreviewSigner("test-secret", "http://127.0.0.1:5174")
	manifest := assets.NewManifest("/assets/")
	manifest.Add("app.css", "app-TEST.css", []byte("body{}"))
	manifest.Add("app.js", "app-TEST.js", []byte("export{}"))
	_, st.web = st.srv.handler(assets.NewHandler(assets.HandlerOptions{FS: fstest.MapFS{}, Manifest: manifest}))
	return st, apps
}

func TestAppsRequireAnAllowlistedAccount(t *testing.T) {
	st, _ := appsStack(t)

	anon := &browser{t: t, st: st, jar: map[string]string{}}
	if rec := anon.get("/apps"); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/sign-in") {
		t.Errorf("signed out: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	stranger := st.signIn(t, "eve@example.com")
	if rec := stranger.get("/apps"); rec.Code != http.StatusForbidden {
		t.Errorf("not allowlisted GET: %d", rec.Code)
	}
	if rec := stranger.submit("/apps", url.Values{"name": {"x"}}); rec.Code != http.StatusForbidden {
		t.Errorf("not allowlisted POST: %d", rec.Code)
	}

	ada := st.signIn(t, "ada@example.com")
	if rec := ada.post("/apps", url.Values{"name": {"x"}}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("POST without a CSRF token: %d", rec.Code)
	}
	body := ada.page("/apps")
	wantContains(t, body, `action="/apps"`, `name="_csrf"`)
}

func TestAppWorkspaceCreatesEditsAndConfinesFiles(t *testing.T) {
	st, apps := appsStack(t)
	ada := st.signIn(t, "ada@example.com")

	rec := ada.submit("/apps", url.Values{"name": {"Recipe Box"}, "description": {"Keeps recipes"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/apps/recipe-box" {
		t.Fatalf("create: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	wantContains(t, ada.page("/apps/recipe-box"), "Recipe Box", `action="/apps/recipe-box/prompts"`)
	wantContains(t, ada.page("/apps/recipe-box?view=code&file=src/App.svelte"), "src/App.svelte", `data-code-form`, "Recipe Box")
	theme := ada.page("/apps/recipe-box?view=theme")
	wantContains(t, theme, `name="light:--timlee-brand"`, `value="#cc3a12"`, `<option value="Helvetica" selected>`)

	// Saving a file writes it inside the app even though the (fake) build fails.
	save := ada.submit("/apps/recipe-box/files", url.Values{"path": {"src/lib/Note.svelte"}, "content": {"<p>hi</p>\r\n"}})
	if save.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", save.Code, save.Body)
	}
	if got, err := apps.ReadFile("recipe-box", "src/lib/Note.svelte"); err != nil || got != "<p>hi</p>\n" {
		t.Errorf("saved file = %q, %v", got, err)
	}

	for _, path := range []string{"../../MODULE.bazel", "/etc/hosts", "src/theme.override.css"} {
		r := httptest.NewRequest(http.MethodPost, "/apps/recipe-box/files", strings.NewReader(url.Values{
			"path": {path}, "content": {"x"}, "_csrf": {ada.token()},
		}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", testPublic)
		r.Header.Set("Accept", "application/json")
		if rec := ada.send(r); rec.Code != http.StatusBadRequest {
			t.Errorf("save %q: %d %s", path, rec.Code, rec.Body)
		}
	}

	// A theme value that would inject CSS is refused.
	r := httptest.NewRequest(http.MethodPost, "/apps/recipe-box/theme", strings.NewReader(url.Values{
		"font": {"Helvetica"}, "light:--timlee-brand": {"red;}*{display:none"}, "_csrf": {ada.token()},
	}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", testPublic)
	r.Header.Set("Accept", "application/json")
	if rec := ada.send(r); rec.Code != http.StatusBadRequest {
		t.Errorf("theme injection: %d %s", rec.Code, rec.Body)
	}
}

func TestPreviewIsIsolatedFromTheSite(t *testing.T) {
	st, apps := appsStack(t)
	ada := st.signIn(t, "ada@example.com")
	if rec := ada.submit("/apps", url.Values{"name": {"demo"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
	if dir := apps.PreviewDir("demo"); dir != "" {
		t.Fatalf("preview published before any successful build: %s", dir)
	}

	// The site may frame only the preview origin.
	rec := ada.get("/apps/demo")
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-src http://127.0.0.1:5174") {
		t.Errorf("site CSP = %q", csp)
	}

	// Publish a fake build, then only a valid, unexpired capability serves it.
	dir := filepath.Join(filepath.Dir(apps.Workspace()), ".local", "previews", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>demo</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	signer := st.srv.previews
	h := previewHandler(apps, signer, testPublic)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	good := strings.TrimPrefix(signer.URL("demo", time.Now()), "http://127.0.0.1:5174")
	if rec := get(good); rec.Code != http.StatusOK || rec.Header().Get("X-Robots-Tag") == "" || !strings.Contains(rec.Body.String(), "demo") {
		t.Errorf("valid capability: %d %q", rec.Code, rec.Body)
	}
	capability := strings.Split(good, "/")[1]
	expired := strings.TrimPrefix(signer.URL("demo", time.Now().Add(-2*previewTTL)), "http://127.0.0.1:5174")
	for _, path := range []string{"/demo/", "/x.y/demo/", expired, "/" + capability + "/other/", "/" + capability + "/../MODULE.bazel"} {
		if rec := get(path); rec.Code != http.StatusNotFound {
			t.Errorf("preview %s: %d", path, rec.Code)
		}
	}
}
