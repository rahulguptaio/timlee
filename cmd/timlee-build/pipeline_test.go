package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"

	"github.com/rahulguptaio/timlee/internal/assets"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// file is the output filename for a logical manifest name.
func file(t *testing.T, m *assets.Manifest, name string) string {
	t.Helper()
	entry, ok := m.Lookup(name)
	if !ok {
		t.Fatalf("manifest has no %q, only %v", name, m.Names())
	}
	return entry.File
}

// fixture lays out a source tree that exercises the conventions the pipeline
// relies on: entries live at the top level, modules live below it.
func fixture(t *testing.T) (root, src, out string) {
	t.Helper()
	root = t.TempDir()
	src = filepath.Join(root, "assets")
	out = filepath.Join(root, "dist")

	write := func(name, body string) {
		full := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("app.ts", `
import { greet } from './ts/greet';

type Target = { title: string; lang: string };

export function boot(target: Target): void {
  document.title = greet(target.title);
  document.documentElement.lang = target.lang;
  for (const el of document.querySelectorAll<HTMLElement>('[data-greet]')) {
    el.textContent = greet(el.dataset.greet ?? 'world');
    el.setAttribute('aria-label', 'greeting for ' + el.dataset.greet);
  }
  if (__DEV__) console.info('development build with a long explanatory message');
}

boot({ title: 'timlee', lang: 'en' });
`)
	write("ts/greet.ts", `
export function greet(who: string): string {
  return 'hello ' + who;
}
export function unusedButExported(): string {
  return 'this identifier must not survive tree shaking in the bundle';
}
`)
	write("ts/env.d.ts", "declare const __DEV__: boolean;\n")
	write("app.css", "@import \"./css/tokens.css\";\n@import \"./css/base.css\";\n")
	write("css/tokens.css", ":root {\n  --brand: #0f7b80;\n  /* a comment long enough to prove minification ran */\n  --brand-dark: #11666b;\n  --brand-glow: #5fd0d0;\n  --surface: #ffffff;\n  --text: #1b2a41;\n}\n")
	write("css/base.css", "*,\n*::before,\n*::after {\n  box-sizing: border-box;\n}\n\nbody {\n  margin: 0;\n  background: var(--surface);\n  color: var(--text);\n  font-family: system-ui, sans-serif;\n}\n\na {\n  color: var(--brand);\n  text-decoration-thickness: 1px;\n}\n")
	// Nesting is how a file declares itself a module; this must not become
	// an entry point of its own.
	write("_ignored.ts", "export const x = 1;\n")
	return root, src, out
}

func build(t *testing.T, opts options) *assets.Manifest {
	t.Helper()
	opts.Log = discard()

	p, err := newPipeline(opts)
	if err != nil {
		t.Fatalf("newPipeline: %v", err)
	}
	defer p.close()

	if err := p.buildOnce(); err != nil {
		t.Fatalf("buildOnce: %v", err)
	}
	m, err := assets.LoadManifest(assets.Dir(opts.Out))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return m
}

func TestProductionBuild(t *testing.T) {
	root, src, out := fixture(t)
	m := build(t, options{Root: root, Src: src, Out: out, Base: "/assets/"})

	for _, name := range []string{"app.js", "app.css"} {
		entry, ok := m.Lookup(name)
		if !ok {
			t.Fatalf("manifest has no %q, only %v", name, m.Names())
		}
		if !strings.HasPrefix(entry.Path, "/assets/") {
			t.Errorf("%s path = %q, want it under the base", name, entry.Path)
		}
		if entry.File == name {
			t.Errorf("%s was emitted unhashed as %q", name, entry.File)
		}
		if !strings.HasPrefix(entry.Integrity, "sha384-") {
			t.Errorf("%s integrity = %q, want a sha384 value", name, entry.Integrity)
		}

		body, err := os.ReadFile(filepath.Join(out, entry.File))
		if err != nil {
			t.Fatalf("read %s: %v", entry.File, err)
		}
		if int64(len(body)) != entry.Size {
			t.Errorf("%s size = %d, manifest says %d", name, len(body), entry.Size)
		}
		if assets.SRI(body) != entry.Integrity {
			t.Errorf("%s integrity does not match its bytes", name)
		}
		// Every text output must have a .gz sibling for the handler to find.
		gz, err := os.Stat(filepath.Join(out, entry.File+".gz"))
		if err != nil {
			t.Fatalf("no .gz sibling for %s: %v", entry.File, err)
		}
		if gz.Size() >= int64(len(body)) {
			t.Errorf("%s.gz is %d bytes, not smaller than the %d it compresses",
				entry.File, gz.Size(), len(body))
		}
	}

	js, err := os.ReadFile(filepath.Join(out, file(t, m, "app.js")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "unusedButExported") {
		t.Error("tree shaking left an unreachable export in the bundle")
	}
	if strings.Contains(string(js), "console.info") {
		t.Error("the __DEV__ branch survived a production build")
	}
	if strings.Contains(string(js), "\n  ") {
		t.Error("output is not minified")
	}

	css, err := os.ReadFile(filepath.Join(out, file(t, m, "app.css")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "--brand:") || strings.Contains(string(css), "/*") {
		t.Errorf("css was not inlined and minified: %q", css)
	}

	// Nothing may escape to the network or shell: no sourcemaps in production.
	assertNoFilesMatching(t, out, ".map")
}

// TestDynamicImportsBecomeChunks covers a vendored module that loads part of
// itself with import(). esbuild records the target as an entry point of its
// own; it must be emitted as a hashed chunk the bundle fetches, not refused
// and not listed in the manifest as if a template could link it.
func TestDynamicImportsBecomeChunks(t *testing.T) {
	root, src, out := fixture(t)
	vendor := filepath.Join(root, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "lib.js"),
		[]byte("export const later = () => import('./part.js');\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "part.js"),
		[]byte("export const answer = 'loaded on demand';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "lazy.ts"),
		[]byte("import { later } from '../vendor/lib.js';\nvoid later().then((m) => console.log(m.answer));\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := build(t, options{Root: root, Src: src, Out: out, Base: "/assets/"})

	if got := strings.Join(m.Names(), ","); got != "app.css,app.js,lazy.js" {
		t.Errorf("manifest names = %s, want only the declared entries", got)
	}
	var chunk string
	_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") {
			if body, _ := os.ReadFile(p); strings.Contains(string(body), "loaded on demand") {
				chunk = filepath.Base(p)
			}
		}
		return nil
	})
	if chunk == "" || chunk == "part.js" {
		t.Fatalf("the dynamically imported module was not emitted as a hashed chunk (got %q)", chunk)
	}
	lazy, err := os.ReadFile(filepath.Join(out, file(t, m, "lazy.js")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lazy), chunk) {
		t.Errorf("lazy.js does not load %s", chunk)
	}
}

func TestDevBuildKeepsSourcemapsAndDebugCode(t *testing.T) {
	root, src, out := fixture(t)
	m := build(t, options{Root: root, Src: src, Out: out, Base: "/assets/", Dev: true})

	js, err := os.ReadFile(filepath.Join(out, file(t, m, "app.js")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "console.info") {
		t.Error("a development build dropped the __DEV__ branch")
	}
	if !strings.Contains(string(js), "sourceMappingURL=/assets/") {
		t.Error("a development build emitted no sourcemap link")
	}
	if _, err := os.Stat(filepath.Join(out, file(t, m, "app.js")+".map")); err != nil {
		t.Errorf("no sourcemap beside the bundle: %v", err)
	}
}

// TestRebuildPrunesStaleHashes is why the output directory is written rather
// than merely added to: hashed names accumulate, so a directory that is never
// pruned grows a copy of every edit ever made.
func TestRebuildPrunesStaleHashes(t *testing.T) {
	root, src, out := fixture(t)
	opts := options{Root: root, Src: src, Out: out, Base: "/assets/"}

	first := build(t, opts).URL("app.css")
	if err := os.WriteFile(filepath.Join(src, "css", "tokens.css"),
		[]byte(":root{--brand:#112233;--second:#445566}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := build(t, opts).URL("app.css")

	if first == second {
		t.Fatalf("changed content kept the hash %q, so caches would never see the edit", first)
	}
	stale := filepath.Base(first)
	if _, err := os.Stat(filepath.Join(out, stale)); !os.IsNotExist(err) {
		t.Errorf("%s survived the rebuild (err = %v)", stale, err)
	}
	if _, err := os.Stat(filepath.Join(out, stale+".gz")); !os.IsNotExist(err) {
		t.Errorf("%s.gz survived the rebuild", stale)
	}
}

// TestRefusesUnmanagedOutputDirectory keeps the pruning above from turning a
// mistyped -out into a delete.
func TestRefusesUnmanagedOutputDirectory(t *testing.T) {
	root, src, _ := fixture(t)
	out := filepath.Join(root, "precious")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := newPipeline(options{Root: root, Src: src, Out: out, Base: "/assets/", Log: discard()})
	if err != nil {
		t.Fatalf("newPipeline: %v", err)
	}
	defer p.close()

	if err := p.buildOnce(); err == nil {
		t.Fatal("expected the build to refuse a directory it does not own")
	}
	if _, err := os.Stat(filepath.Join(out, "notes.txt")); err != nil {
		t.Errorf("the refused build still deleted a file: %v", err)
	}
}

// TestManagedOutputDirectory covers the states a real output directory passes
// through, including the dotfile placeholder an embed target starts life with.
func TestManagedOutputDirectory(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{name: "absent", files: nil, want: true},
		{name: "empty", files: map[string]string{}, want: true},
		{name: "only a dotfile placeholder", files: map[string]string{".gitignore": "*"}, want: true},
		{
			name: "a previous build, manifest not first alphabetically",
			files: map[string]string{
				"app-ABCD2345.js": "x", "manifest.json": "{}", "zz.css": "y",
			},
			want: true,
		},
		{name: "somebody else's directory", files: map[string]string{"notes.txt": "mine"}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "out")
			if tc.files != nil {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				for name, body := range tc.files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := isManaged(dir)
			if err != nil {
				t.Fatalf("isManaged: %v", err)
			}
			if got != tc.want {
				t.Errorf("isManaged = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLogicalName(t *testing.T) {
	entryRels := []string{"app.ts", "app.css", "admin/panel.ts", "panel.ts"}

	tests := []struct {
		name       string
		entryPoint string
		outExt     string
		want       string
		wantErr    bool
	}{
		{name: "typescript becomes javascript", entryPoint: "web/assets/app.ts", outExt: ".js", want: "app.js"},
		{name: "css keeps its extension", entryPoint: "web/assets/app.css", outExt: ".css", want: "app.css"},
		{
			name: "a nested entry keeps its directory and beats the shorter match",
			// Both "admin/panel.ts" and "panel.ts" are suffixes here.
			entryPoint: "web/assets/admin/panel.ts", outExt: ".js", want: "admin/panel.js",
		},
		{
			name: "a path spelled through a symlink still matches",
			// The metafile's spelling of a path need not be ours.
			entryPoint: "/private/var/folders/x/assets/app.ts", outExt: ".js", want: "app.js",
		},
		{name: "an unknown entry point is an error", entryPoint: "elsewhere/other.ts", outExt: ".js", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := logicalName(entryRels, tc.entryPoint, tc.outExt)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("logicalName = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("logicalName: %v", err)
			}
			if got != tc.want {
				t.Errorf("logicalName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrecompress(t *testing.T) {
	long := strings.Repeat("body{color:#0f7b80}", 64)

	tests := []struct {
		name     string
		file     string
		body     string
		wantSfx  []string
		because  string
		roundTry bool
	}{
		{
			name: "a large stylesheet gets both sidecars",
			file: "app.css", body: long, wantSfx: []string{".gz", ".br"}, roundTry: true,
		},
		{
			name: "a large bundle gets both sidecars",
			file: "app.js", body: long, wantSfx: []string{".gz", ".br"}, roundTry: true,
		},
		{
			name: "a tiny file is skipped", file: "app.js", body: "x",
			because: "a compressed header and trailer cost more than they save",
		},
		{
			name: "an image is skipped", file: "logo.png", body: long,
			because: "it is already compressed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := precompress(tc.file, []byte(tc.body))
			if err != nil {
				t.Fatalf("precompress: %v", err)
			}

			var suffixes []string
			bySuffix := map[string][]byte{}
			for _, s := range got {
				suffixes = append(suffixes, s.Suffix)
				bySuffix[s.Suffix] = s.Contents
			}
			if strings.Join(suffixes, ",") != strings.Join(tc.wantSfx, ",") {
				t.Fatalf("sidecars = %v, want %v (%s)", suffixes, tc.wantSfx, tc.because)
			}
			if !tc.roundTry {
				return
			}

			gz, br := bySuffix[".gz"], bySuffix[".br"]
			if len(gz) >= len(tc.body) {
				t.Errorf("gzip produced %d bytes from %d", len(gz), len(tc.body))
			}
			// Brotli only earns a sidecar by beating gzip, since it is
			// offered to the same clients that would otherwise take the gzip.
			if len(br) >= len(gz) {
				t.Errorf("brotli produced %d bytes, not smaller than gzip's %d", len(br), len(gz))
			}

			// The handler serves these bytes verbatim under a Content-Encoding
			// header, so each has to be a valid member of its format.
			gzr, err := gzip.NewReader(bytes.NewReader(gz))
			if err != nil {
				t.Fatalf("output is not valid gzip: %v", err)
			}
			assertDecodes(t, "gzip", gzr, tc.body)
			assertDecodes(t, "brotli", brotli.NewReader(bytes.NewReader(br)), tc.body)
		})
	}
}

func assertDecodes(t *testing.T, label string, r io.Reader, want string) {
	t.Helper()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s decompress: %v", label, err)
	}
	if string(got) != want {
		t.Errorf("%s output does not decompress to the original bytes", label)
	}
}

func TestEntryPointsConvention(t *testing.T) {
	_, src, _ := fixture(t)
	found, err := entryPoints(src)
	if err != nil {
		t.Fatalf("entryPoints: %v", err)
	}

	var names []string
	for _, p := range found {
		names = append(names, filepath.Base(p))
	}
	if got := strings.Join(names, ","); got != "app.css,app.ts" {
		t.Errorf("entry points = %v, want [app.css app.ts]; "+
			"nested files and _-prefixed files are modules, not entries", names)
	}
}

// TestEmbedMirror covers single-binary deployment: the same tree must land in
// the embed target without disturbing the placeholder that keeps it buildable.
func TestEmbedMirror(t *testing.T) {
	root, src, out := fixture(t)
	mirror := filepath.Join(root, "embedded", "static")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mirror, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := build(t, options{Root: root, Src: src, Out: out, Base: "/assets/", EmbedTo: mirror})

	for _, name := range m.Names() {
		entry, _ := m.Lookup(name)
		onDisk, err := os.ReadFile(filepath.Join(out, entry.File))
		if err != nil {
			t.Fatal(err)
		}
		embedded, err := os.ReadFile(filepath.Join(mirror, entry.File))
		if err != nil {
			t.Fatalf("%s missing from the embed mirror: %v", entry.File, err)
		}
		if string(onDisk) != string(embedded) {
			t.Errorf("%s differs between the output directory and the embed mirror", entry.File)
		}
	}
	if _, err := os.Stat(filepath.Join(mirror, ".gitignore")); err != nil {
		t.Errorf("the mirror's placeholder was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mirror, assets.ManifestName)); err != nil {
		t.Errorf("the embed mirror has no manifest: %v", err)
	}
}

// TestManifestIsValidJSON pins the on-disk shape other services parse.
func TestManifestIsValidJSON(t *testing.T) {
	root, src, out := fixture(t)
	build(t, options{Root: root, Src: src, Out: out, Base: "static"})

	raw, err := os.ReadFile(filepath.Join(out, assets.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if generic["base"] != "/static/" {
		t.Errorf("base = %v, want /static/ (the -base flag should be normalised)", generic["base"])
	}
}

func assertNoFilesMatching(t *testing.T, dir, suffix string) {
	t.Helper()
	err := fs.WalkDir(os.DirFS(dir), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(name, suffix) {
			t.Errorf("unexpected %s file in the output: %s", suffix, name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

// TestDynamicImportBecomesAChunk covers lazy loading: esbuild records the
// target of an import() as an entry point of its own, but it is a chunk the
// page fetches on demand, not something a template names, so it must build,
// be written and compressed, and stay out of the manifest.
func TestDynamicImportBecomesAChunk(t *testing.T) {
	root, src, out := fixture(t)
	if err := os.WriteFile(filepath.Join(src, "app.ts"),
		[]byte("document.body.addEventListener('click', () => import('./ts/lazy').then((m) => m.later()));\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "ts", "lazy.ts"),
		[]byte("import { vendored } from '../../vendor/lib.js';\nexport function later(): string { return vendored('loaded on demand, well after the first paint of the page'); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Vendored code lives beside the asset tree, not in it, as web/vendor does.
	if err := os.MkdirAll(filepath.Join(root, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor", "lib.js"),
		[]byte("export function vendored(s) { return s + ' from a vendored library'; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := build(t, options{Root: root, Src: src, Out: out, Base: "/assets/"})

	if names := m.Names(); len(names) != 2 {
		t.Errorf("manifest = %v, want only app.css and app.js", names)
	}
	app, err := os.ReadFile(filepath.Join(out, file(t, m, "app.js")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(app), "loaded on demand") {
		t.Error("the lazily imported module was inlined into the entry bundle")
	}

	var chunk string
	err = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") {
			body, readErr := os.ReadFile(p)
			if readErr == nil && strings.Contains(string(body), "loaded on demand") {
				chunk = p
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if chunk == "" {
		t.Fatal("no output holds the lazily imported module")
	}
	if dir := filepath.Dir(chunk); dir != out {
		t.Errorf("chunk written to %s, want the output root", dir)
	}
	if rel, _ := filepath.Rel(out, chunk); !strings.Contains(string(app), filepath.ToSlash(filepath.Base(rel))) {
		t.Errorf("app.js does not reference its chunk %s", rel)
	}
}
