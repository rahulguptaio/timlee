package appfactory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestFactory builds a throwaway workspace from the real template and token
// catalog, with its own git repository.
func newTestFactory(t *testing.T) *Factory {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	real := filepath.Join(filepath.Dir(file), "testdata", "workspace")
	ws := filepath.Join(t.TempDir(), "workspace")
	for _, dir := range []string{"templates/app", "packages/design/src"} {
		if err := os.CopyFS(filepath.Join(ws, dir), os.DirFS(filepath.Join(real, dir))); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(ws, "MODULE.bazel"), "module(name = \"test\")\n")
	mustWrite(t, filepath.Join(ws, "AGENTS.md"), "rules\n")
	git(t, ws, "init", "-q", "-b", "main")
	git(t, ws, "add", "-A")
	git(t, ws, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	f, err := New(Config{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestFillPlaceholdersEscapesPerFileType(t *testing.T) {
	name := `A<b>"{x}"`
	html := string(fillPlaceholders("index.html", []byte(`<title>__APP_NAME__</title>`), "s", name, ""))
	if html != `<title>A&lt;b&gt;&#34;{x}&#34;</title>` {
		t.Errorf("html: %s", html)
	}
	svelte := string(fillPlaceholders("src/App.svelte", []byte(`<h1>__APP_NAME__</h1>`), "s", name, ""))
	if strings.ContainsAny(strings.TrimSuffix(strings.TrimPrefix(svelte, "<h1>"), "</h1>"), "{}<>") {
		t.Errorf("svelte not escaped: %s", svelte)
	}
	js := string(fillPlaceholders("timlee.json", []byte(`{"name": "__APP_NAME__"}`), "s", name, ""))
	if js != `{"name": "A\u003cb\u003e\"{x}\""}` {
		t.Errorf("json: %s", js)
	}
}

func TestCreateCommitsAndDeduplicatesSlug(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	a, err := f.Create(ctx, "My Shop!", "Sells things")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Create(ctx, "my shop", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Slug != "my-shop" || b.Slug != "my-shop-2" {
		t.Fatalf("slugs = %q, %q", a.Slug, b.Slug)
	}
	index, _ := os.ReadFile(filepath.Join(f.appDir("my-shop"), "index.html"))
	if !strings.Contains(string(index), "<title>My Shop!</title>") || strings.Contains(string(index), "__APP_") {
		t.Errorf("index.html not filled:\n%s", index)
	}
	if status := git(t, f.Workspace(), "status", "--porcelain"); status != "" {
		t.Errorf("uncommitted changes after create:\n%s", status)
	}
}

func TestEditorPathsStayInsideApp(t *testing.T) {
	f := newTestFactory(t)
	if _, err := f.Create(context.Background(), "demo", ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../other/x", "/etc/passwd", "src/../../x", ".git/config", "dist/index.html", "src//a.js", ""} {
		if _, err := f.ReadFile("demo", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ReadFile(%q) err = %v, want ErrInvalid", bad, err)
		}
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	mustWrite(t, secret, "s3cret")
	if err := os.Symlink(secret, filepath.Join(f.appDir("demo"), "src", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if got, err := f.ReadFile("demo", "src/link.txt"); err == nil {
		t.Errorf("read through symlink escaped the app: %q", got)
	}
	if _, err := f.SaveFile(context.Background(), "demo", "src/theme.override.css", "x"); !errors.Is(err, ErrGenerated) {
		t.Errorf("saving a generated file: err = %v", err)
	}
}

func TestThemeValidationAndRendering(t *testing.T) {
	f := newTestFactory(t)
	c, err := f.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	bad := Tokens{Light: map[string]string{"--timlee-brand": "red; } body { display:none"}}
	if _, err := validateTheme(c, "Helvetica", bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("CSS injection accepted: %v", err)
	}
	if _, err := validateTheme(c, "Comic Sans", Tokens{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown font accepted: %v", err)
	}
	if _, err := validateTheme(c, "Helvetica", Tokens{Light: map[string]string{"--timlee-radius-scale": "9"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("out-of-range number accepted: %v", err)
	}
	tokens := Tokens{
		Light: map[string]string{"--timlee-brand": "#112233", "--timlee-text": "#141414"}, // second equals the default
		Dark:  map[string]string{},
	}
	font, err := validateTheme(c, "Inter", tokens)
	if err != nil {
		t.Fatal(err)
	}
	css := renderThemeCSS(c, font, tokens)
	if !strings.Contains(css, "--timlee-brand: #112233;") || strings.Contains(css, "--timlee-text:") {
		t.Errorf("unexpected overrides:\n%s", css)
	}
	if !strings.Contains(css, `--timlee-font-body: "Inter Variable"`) || strings.Contains(css, ".wa-dark") {
		t.Errorf("unexpected font or dark block:\n%s", css)
	}
	if js := renderFontJS(font); !strings.Contains(js, "import '@fontsource-variable/inter';") {
		t.Errorf("font.js = %q", js)
	}
}

func TestEnforceBoundaryRevertsOutsideChanges(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	if _, err := f.Create(ctx, "demo", ""); err != nil {
		t.Fatal(err)
	}
	ws := f.Workspace()
	base := git(t, ws, "rev-parse", "HEAD")

	mustWrite(t, filepath.Join(f.appDir("demo"), "src", "App.svelte"), "<h1>kept</h1>\n")
	mustWrite(t, filepath.Join(ws, "AGENTS.md"), "tampered\n")
	mustWrite(t, filepath.Join(ws, "evil.sh"), "rm -rf /\n")
	mustWrite(t, filepath.Join(ws, "templates", "app", "x.js"), "x\n")
	git(t, ws, "add", "templates")
	git(t, ws, "-c", "user.name=a", "-c", "user.email=a@a", "commit", "-q", "-m", "agent commit")
	mustWrite(t, filepath.Join(f.appDir("demo"), "timlee.json"), `{"name":"hijacked"}`)

	restored, err := f.enforceBoundary(ctx, "demo", base)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 3 {
		t.Errorf("restored = %v, want 3 paths", restored)
	}
	if got := git(t, ws, "rev-parse", "HEAD"); got != base {
		t.Errorf("agent commit kept: HEAD %s, want %s", got, base)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "AGENTS.md")); string(b) != "rules\n" {
		t.Errorf("AGENTS.md not restored: %q", b)
	}
	for _, p := range []string{"evil.sh", "templates/app/x.js"} {
		if _, err := os.Stat(filepath.Join(ws, p)); err == nil {
			t.Errorf("%s not removed", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.appDir("demo"), "src", "App.svelte")); string(b) != "<h1>kept</h1>\n" {
		t.Errorf("own change lost: %q", b)
	}
	if app, _ := f.Get("demo"); app.Name != "demo" {
		t.Errorf("timlee.json not restored: %+v", app)
	}
}

func TestParseEventsSurfacesProgress(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"tool.execution_start","data":{"toolName":"apply_patch","toolTitle":"Apply patch","arguments":"secret args"}}`,
		`{"type":"assistant.message","data":{"content":"","toolRequests":[{}]}}`,
		`not json`,
		`{"type":"assistant.message","data":{"content":"Added a hero section."}}`,
		`{"type":"result","exitCode":0,"usage":{"premiumRequests":1,"codeChanges":{"linesAdded":12,"linesRemoved":3}}}`,
	}, "\n")
	job := NewJobs().start("demo", "p")
	res := parseEvents(strings.NewReader(stream), job)
	events, _ := job.Next(context.Background(), 0)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind+":"+e.Text)
	}
	want := []string{"tool:Apply patch", "message:Added a hero section.", "usage:+12 / -3 lines, 1 premium request(s)"}
	if res.exitCode != 0 || strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Errorf("events = %q", kinds)
	}
}

func TestPromptRunEndToEndWithFakeTools(t *testing.T) {
	f := newTestFactory(t)
	if _, err := f.Create(context.Background(), "demo", ""); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho fake-token\n")
	mustWrite(t, filepath.Join(bin, "copilot"), `#!/bin/sh
[ "$COPILOT_GITHUB_TOKEN" = fake-token ] || exit 3
echo '<h1>Generated</h1>' > src/App.svelte
echo 'outside' > ../../stray.txt
printf '[filter "x"]\n\tclean = touch pwned\n' >> ../../.git/config
printf '#!/bin/sh\ntouch ../hooked\n' > ../../.git/hooks/pre-commit && chmod +x ../../.git/hooks/pre-commit
echo '{"type":"tool.execution_start","data":{"toolTitle":"Edit App.svelte"}}'
echo '{"type":"result","exitCode":0,"usage":{"codeChanges":{"linesAdded":1,"linesRemoved":0}}}'
`)
	mustWrite(t, filepath.Join(bin, "bazel"), fakeBazel)
	f.cfg.GH, f.cfg.Copilot, f.cfg.Bazel = filepath.Join(bin, "gh"), filepath.Join(bin, "copilot"), filepath.Join(bin, "bazel")
	f.cfg.CopilotHome = filepath.Join(t.TempDir(), "home")

	job, err := f.StartPrompt("demo", "Make a heading")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.StartPrompt("demo", "again"); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent prompt on same app: err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var all []Event
	for done := false; !done; {
		var events []Event
		events, done = job.Next(ctx, len(all))
		if ctx.Err() != nil {
			t.Fatal("timed out")
		}
		all = append(all, events...)
	}
	if _, ok := job.Done(); !ok {
		t.Fatalf("job failed: %+v", all)
	}
	preview, err := os.ReadFile(filepath.Join(f.PreviewDir("demo"), "index.html"))
	if err != nil || !strings.Contains(string(preview), "Generated") {
		t.Errorf("preview not published: %q %v", preview, err)
	}
	if _, err := os.Stat(filepath.Join(f.Workspace(), "stray.txt")); err == nil {
		t.Error("file written outside the app was kept")
	}
	if msg := git(t, f.Workspace(), "log", "-1", "--format=%s"); msg != "Prompt: Make a heading" {
		t.Errorf("commit message = %q", msg)
	}
	if cfg, _ := os.ReadFile(filepath.Join(f.Workspace(), ".git", "config")); strings.Contains(string(cfg), "filter") {
		t.Errorf("git config change kept:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(f.Workspace(), "..", "hooked")); err == nil {
		t.Error("a planted git hook ran when Timlee committed")
	}
}

// fakeBazel answers `bazel info bazel-bin` and builds by copying App.svelte.
const fakeBazel = `#!/bin/sh
if [ "$1" = info ]; then echo "$PWD/bazel-bin"; exit 0; fi
for a; do target=$a; done
slug=$(echo "$target" | sed 's#//apps/\(.*\):dist#\1#')
mkdir -p "bazel-bin/apps/$slug/dist" && cp "apps/$slug/src/App.svelte" "bazel-bin/apps/$slug/dist/index.html"
`

func TestOperationsRunOneAtATime(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	if _, err := f.Create(ctx, "one", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Create(ctx, "two", ""); err != nil {
		t.Fatal(err)
	}
	end, err := f.begin("one")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Busy("one") || f.Busy("two") {
		t.Error("Busy does not track the running operation")
	}
	// A save to another app while a prompt runs would commit under the run,
	// and the run's boundary check would then rewind past it.
	if _, err := f.SaveFile(ctx, "two", "src/x.js", "x"); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent save: err = %v, want ErrBusy", err)
	}
	if _, err := f.StartPrompt("two", "hi"); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent prompt: err = %v, want ErrBusy", err)
	}
	end()
	if f.Busy("one") {
		t.Error("still busy after the operation ended")
	}
}

func TestOperationsRefuseToDiscardUncommittedWork(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	if _, err := f.Create(ctx, "demo", ""); err != nil {
		t.Fatal(err)
	}
	// A developer edits the shared design system and has not committed yet.
	shared := filepath.Join(f.Workspace(), "packages", "design", "src", "theme.css")
	mustWrite(t, shared, "/* work in progress */\n")
	if _, err := f.StartPrompt("demo", "anything"); !errors.Is(err, ErrDirty) {
		t.Fatalf("prompt over uncommitted work: err = %v, want ErrDirty", err)
	}
	if _, err := f.SaveFile(ctx, "demo", "src/x.js", "x"); !errors.Is(err, ErrDirty) {
		t.Fatalf("save over uncommitted work: err = %v, want ErrDirty", err)
	}
	if b, _ := os.ReadFile(shared); string(b) != "/* work in progress */\n" {
		t.Errorf("uncommitted work was changed: %q", b)
	}
	if f.Busy("demo") {
		t.Error("a refused operation left the app busy")
	}
}

func TestNewInitializesAWorkspaceWithoutItsOwnRepository(t *testing.T) {
	// A fresh clone of the Timlee repository: the workspace's files sit inside
	// the outer repository, without a .git of their own.
	outer := t.TempDir()
	git(t, outer, "init", "-q", "-b", "main")
	f := newTestFactory(t)
	ws := filepath.Join(outer, "workspace")
	if err := os.CopyFS(ws, os.DirFS(f.Workspace())); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(ws, ".git")); err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Create(context.Background(), "fresh", ""); err != nil {
		t.Fatal(err)
	}
	if top := git(t, ws, "rev-parse", "--show-toplevel"); filepath.Base(top) != "workspace" {
		t.Errorf("workspace commits went to %s", top)
	}
	if out := git(t, outer, "rev-list", "--all", "--count"); out != "0" {
		t.Errorf("outer repository gained commits: %s", out)
	}
}

func TestThemeRejectsUnreadableAndMalformedValues(t *testing.T) {
	f := newTestFactory(t)
	c, err := f.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for name, tokens := range map[string]Tokens{
		"NaN":                 {Light: map[string]string{"--timlee-radius-scale": "NaN"}},
		"hex float":           {Light: map[string]string{"--timlee-radius-scale": "0x1p0"}},
		"white on yellow":     {Light: map[string]string{"--timlee-brand": "#ffee00", "--timlee-brand-on": "#ffffff"}},
		"grey on grey (dark)": {Dark: map[string]string{"--timlee-text": "#333333"}},
	} {
		if _, err := validateTheme(c, "Helvetica", tokens); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if contrastRatio("#000000", "#ffffff") < 20.9 {
		t.Error("contrastRatio is wrong for black on white")
	}
}

func TestThemeFilesReplaceSymlinksInsteadOfFollowingThem(t *testing.T) {
	f := newTestFactory(t)
	app, err := f.Create(context.Background(), "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "victim.txt")
	mustWrite(t, outside, "keep")
	css := filepath.Join(f.appDir("demo"), "src", "theme.override.css")
	if err := os.Remove(css); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, css); err != nil {
		t.Fatal(err)
	}
	if err := f.writeThemeFiles(app); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Errorf("wrote through a symlink: %q", b)
	}
	if info, err := os.Lstat(css); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("generated file is still a symlink: %v", err)
	}
}
