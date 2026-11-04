package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/evanw/esbuild/pkg/api"

	"github.com/rahulguptaio/timlee/internal/assets"
)

type options struct {
	Root    string // absolute working directory esbuild resolves against
	Src     string // absolute entry-point directory
	Out     string // absolute output directory
	Base    string // URL prefix the output is served under
	EmbedTo string // absolute mirror directory, or "" for none
	Dev     bool
	Watch   bool
	Log     *slog.Logger
}

// entryExtensions are the file types that may be an entry point. A .d.ts file
// is type-only and is skipped; esbuild has nothing to emit for it.
var entryExtensions = map[string]bool{".ts": true, ".tsx": true, ".js": true, ".css": true}

// compressible lists the output types worth pre-compressing. Images and fonts
// are already compressed; a second pass only costs bytes.
var compressible = map[string]bool{
	".css": true, ".js": true, ".json": true, ".map": true,
	".mjs": true, ".svg": true, ".txt": true, ".webmanifest": true,
}

// minCompressSize is the point below which a gzip member's own header and
// trailer outweigh anything it saves.
const minCompressSize = 256

type pipeline struct {
	opts options
	ctx  api.BuildContext
	// entryRels are the entry points relative to the source directory, which
	// is the only form that survives the round trip through esbuild's
	// metafile on a filesystem with symlinks in the path.
	entryRels []string

	mu      sync.Mutex
	lastErr error
}

func newPipeline(opts options) (*pipeline, error) {
	entries, err := entryPoints(opts.Src)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entry points in %s (looked for %s)",
			opts.Src, strings.Join(sortedKeys(entryExtensions), ", "))
	}

	p := &pipeline{opts: opts}
	for _, entry := range entries {
		rel, err := filepath.Rel(opts.Src, entry)
		if err != nil {
			return nil, fmt.Errorf("locate entry point %s: %w", entry, err)
		}
		p.entryRels = append(p.entryRels, filepath.ToSlash(rel))
	}

	buildCtx, ctxErr := api.Context(p.buildOptions(entries))
	if ctxErr != nil {
		return nil, fmt.Errorf("esbuild: %s", ctxErr.Error())
	}
	p.ctx = buildCtx

	opts.Log.Debug("entry points", "count", len(entries), "files", entries)
	return p, nil
}

func (p *pipeline) close() {
	if p.ctx != nil {
		p.ctx.Dispose()
	}
}

func (p *pipeline) buildOptions(entries []string) api.BuildOptions {
	o := p.opts

	sourcemap := api.SourceMapNone
	sourcesContent := api.SourcesContentExclude
	if o.Dev {
		sourcemap = api.SourceMapLinked
		sourcesContent = api.SourcesContentInclude
	}

	return api.BuildOptions{
		AbsWorkingDir: o.Root,
		EntryPoints:   entries,
		Outdir:        o.Out,
		// Outbase anchors the output tree to the source tree, so a nested
		// entry keeps its subdirectory instead of colliding at the root.
		Outbase:  o.Src,
		Bundle:   true,
		Platform: api.PlatformBrowser,
		// ESM is what code splitting requires and what every target browser
		// for es2022 already supports; entries load with type="module".
		Format:      api.FormatESModule,
		Splitting:   true,
		Target:      api.ES2022,
		TreeShaking: api.TreeShakingTrue,
		Charset:     api.CharsetUTF8,

		MinifyWhitespace:  !o.Dev,
		MinifyIdentifiers: !o.Dev,
		MinifySyntax:      !o.Dev,
		LegalComments:     api.LegalCommentsNone,
		Sourcemap:         sourcemap,
		SourcesContent:    sourcesContent,

		// The hash is the whole point: a changed byte changes the URL, which
		// is what lets the server mark these immutable for a year.
		EntryNames: "[dir]/[name]-[hash]",
		// Chunks sit at the root: their names are content hashes, and a
		// directory derived from their sources would climb out of the tree
		// ("_.._/vendor/…") for code vendored beside it.
		ChunkNames: "chunk-[hash]",
		AssetNames: "[dir]/[name]-[hash]",
		PublicPath: assets.NormalizeBase(o.Base),

		Loader: map[string]api.Loader{
			".svg": api.LoaderFile, ".png": api.LoaderFile, ".webp": api.LoaderFile,
			".woff": api.LoaderFile, ".woff2": api.LoaderFile,
		},
		// Dead-code elimination removes dev-only branches from the release
		// bundle, so debugging aids cost production nothing.
		Define: map[string]string{"__DEV__": fmt.Sprint(o.Dev)},
		// Keeping tsconfig here rather than in a JSON file means the build
		// has exactly one source of truth and no stray Node-shaped config.
		TsconfigRaw: `{"compilerOptions":{"useDefineForClassFields":true,"target":"es2022"}}`,

		Metafile: true,
		// Writing is ours: the output directory has to be pruned of the
		// previous build's hashes and every file needs a .gz sibling.
		Write: false,
		// esbuild's own diagnostics follow the caller's logger, so a test or
		// a tool that silenced us does not get a second channel of output.
		LogLevel: esbuildLogLevel(o.Log),
		Color:    api.ColorIfTerminal,

		Plugins: []api.Plugin{{
			Name: "timlee-emit",
			Setup: func(build api.PluginBuild) {
				build.OnEnd(func(result *api.BuildResult) (api.OnEndResult, error) {
					return p.onEnd(result), nil
				})
			},
		}},
	}
}

// onEnd runs after every build, including each rebuild in watch mode, which is
// why emission lives here rather than beside the one-shot call.
func (p *pipeline) onEnd(result *api.BuildResult) api.OnEndResult {
	if len(result.Errors) > 0 {
		p.setErr(errBuildFailed)
		return api.OnEndResult{}
	}
	if err := p.emit(result); err != nil {
		p.setErr(err)
		p.opts.Log.Error("emit failed", "error", err)
		return api.OnEndResult{Errors: []api.Message{{Text: err.Error()}}}
	}
	p.setErr(nil)
	return api.OnEndResult{}
}

func (p *pipeline) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastErr = err
}

func (p *pipeline) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

func (p *pipeline) buildOnce() error {
	start := time.Now()
	result := p.ctx.Rebuild()
	if len(result.Errors) > 0 {
		if err := p.err(); err != nil && !errors.Is(err, errBuildFailed) {
			return err
		}
		return errBuildFailed
	}
	if err := p.err(); err != nil {
		return err
	}
	p.opts.Log.Info("build complete", "mode", mode(p.opts.Dev), "out", p.opts.Out, "took", since(start))
	return nil
}

func (p *pipeline) watch(ctx context.Context) error {
	if err := p.ctx.Watch(api.WatchOptions{}); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	p.opts.Log.Info("watching", "src", p.opts.Src, "out", p.opts.Out)
	<-ctx.Done()
	p.opts.Log.Info("watch stopped")
	return nil
}

func mode(dev bool) string {
	if dev {
		return "development"
	}
	return "production"
}

func esbuildLogLevel(log *slog.Logger) api.LogLevel {
	if !log.Enabled(context.Background(), slog.LevelError) {
		return api.LogLevelSilent
	}
	return api.LogLevelWarning
}

/* ------------------------------------------------------------- emission -- */

// emit writes the build to disk: the bundles, their .gz siblings and the
// manifest, after pruning the previous build's now-unreachable hashes.
func (p *pipeline) emit(result *api.BuildResult) error {
	outputs, err := p.collect(result)
	if err != nil {
		return err
	}

	manifest := assets.NewManifest(p.opts.Base)
	for _, o := range outputs {
		if o.logical != "" {
			manifest.Add(o.logical, o.rel, o.contents)
		}
	}
	manifestJSON, err := manifest.Encode()
	if err != nil {
		return err
	}

	files := map[string][]byte{assets.ManifestName: manifestJSON}
	totals := map[string]int{}
	for _, o := range outputs {
		files[o.rel] = o.contents
		totals["raw"] += len(o.contents)

		sidecars, err := precompress(o.rel, o.contents)
		if err != nil {
			return err
		}
		for _, s := range sidecars {
			files[o.rel+s.Suffix] = s.Contents
			totals[s.Suffix] += len(s.Contents)
			totals["count"+s.Suffix]++
		}
	}

	if err := writeTree(p.opts.Out, files, p.opts.Log); err != nil {
		return err
	}
	if p.opts.EmbedTo != "" {
		if err := writeTree(p.opts.EmbedTo, files, p.opts.Log); err != nil {
			return err
		}
		p.opts.Log.Info("mirrored for embed.FS", "dir", p.opts.EmbedTo)
	}

	p.opts.Log.Info("wrote assets",
		"entries", len(manifest.Entries),
		"files", len(files),
		"bytes", totals["raw"],
		"gzipped", totals["count.gz"],
		"gzip_bytes", totals[".gz"],
		"brotlied", totals["count.br"],
		"brotli_bytes", totals[".br"],
	)
	for _, name := range manifest.Names() {
		e, _ := manifest.Lookup(name)
		p.opts.Log.Debug("entry", "name", name, "path", e.Path, "bytes", e.Size,
			"gzip_bytes", len(files[e.File+".gz"]), "brotli_bytes", len(files[e.File+".br"]))
	}
	return nil
}

type output struct {
	rel      string // path relative to the output directory, slash-separated
	logical  string // manifest key, or "" for chunks and sourcemaps
	contents []byte
}

// collect pairs esbuild's output files with the entry points that produced
// them, which only the metafile records.
//
// The metafile spells every path relative to the working directory, while the
// output files are absolute. Those two do not always reduce to the same
// string — a working directory reached through a symlink is enough to break it
// — so the correspondence is made on the part of the path that is stable: what
// each file is called beneath its own directory.
func (p *pipeline) collect(result *api.BuildResult) ([]output, error) {
	var meta struct {
		Outputs map[string]struct {
			EntryPoint string `json:"entryPoint"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(result.Metafile), &meta); err != nil {
		return nil, fmt.Errorf("parse metafile: %w", err)
	}

	entryPointOf := make(map[string]string, len(meta.Outputs))
	for key, out := range meta.Outputs {
		entryPointOf[path.Clean(filepath.ToSlash(key))] = out.EntryPoint
	}

	seen := map[string]string{}
	outputs := make([]output, 0, len(result.OutputFiles))
	for _, file := range result.OutputFiles {
		rel, err := filepath.Rel(p.opts.Out, file.Path)
		if err != nil {
			return nil, fmt.Errorf("locate output %s: %w", file.Path, err)
		}
		rel = filepath.ToSlash(rel)

		logical := ""
		entryPoint := suffixLookup(entryPointOf, rel)
		// A sourcemap shares its bundle's entry point but is not itself an
		// entry; listing it would overwrite the bundle in the manifest. And
		// esbuild records the target of a dynamic import() as an entry point
		// too: that output is a chunk the bundle loads by its hashed name, so
		// it stays out of the manifest like any other chunk.
		if entryPoint != "" && path.Ext(rel) != ".map" && isDeclaredEntry(p.entryRels, entryPoint) {
			logical, err = logicalName(p.entryRels, entryPoint, path.Ext(rel))
			if err != nil {
				return nil, err
			}
			if prev, dup := seen[logical]; dup {
				return nil, fmt.Errorf("two outputs claim the manifest name %q: %s and %s", logical, prev, rel)
			}
			seen[logical] = rel
		}

		outputs = append(outputs, output{rel: rel, logical: logical, contents: file.Contents})
	}

	// Every entry point must have been recognised in the metafile; one that
	// was spelt differently there would otherwise vanish from the manifest
	// as if it were a chunk.
	for _, rel := range p.entryRels {
		ext := ".js"
		if path.Ext(rel) == ".css" {
			ext = ".css"
		}
		if _, ok := seen[strings.TrimSuffix(rel, path.Ext(rel))+ext]; !ok {
			return nil, fmt.Errorf("entry point %q produced no output the metafile attributes to it", rel)
		}
	}

	slices.SortFunc(outputs, func(a, b output) int { return strings.Compare(a.rel, b.rel) })
	return outputs, nil
}

// isDeclaredEntry reports whether the metafile's entry point is one of the
// entries this build was given, matched by suffix as logicalName does.
func isDeclaredEntry(entryRels []string, entryPoint string) bool {
	want := path.Clean(filepath.ToSlash(entryPoint))
	for _, rel := range entryRels {
		if want == rel || strings.HasSuffix(want, "/"+rel) {
			return true
		}
	}
	return false
}

// suffixLookup finds the value whose key is, or ends with, the given path.
func suffixLookup(index map[string]string, suffix string) string {
	for key, value := range index {
		if key == suffix || strings.HasSuffix(key, "/"+suffix) {
			return value
		}
	}
	return ""
}

// logicalName is the manifest key for an entry point: its path relative to the
// source directory, carrying the output's extension rather than the source's.
// web/assets/app.ts emitted as JavaScript becomes "app.js".
//
// entryRels is matched by suffix for the same reason collect does: the
// metafile's spelling of a path need not be this process's spelling of it.
func logicalName(entryRels []string, entryPoint, outExt string) (string, error) {
	want := path.Clean(filepath.ToSlash(entryPoint))

	best := ""
	for _, rel := range entryRels {
		if want == rel || strings.HasSuffix(want, "/"+rel) {
			// The longest match wins, so "panel.ts" cannot claim an output
			// that belongs to "admin/panel.ts".
			if len(rel) > len(best) {
				best = rel
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("output claims entry point %q, which is not one of %v", entryPoint, entryRels)
	}
	return strings.TrimSuffix(best, path.Ext(best)) + outExt, nil
}

/* ---------------------------------------------------------- compression -- */

// sidecar is one pre-compressed representation written beside an output.
type sidecar struct {
	// Suffix is appended to the asset's filename, matching the suffixes
	// assets.Encodings looks for at request time.
	Suffix   string
	Contents []byte
}

// precompress produces the .br and .gz siblings the handler serves to clients
// that accept them.
//
// Quality is maxed on both because this runs once per build: the cost is paid
// by the build machine and never by a request. That only pays off because the
// handler serves these files directly — a host that ignores the sidecars and
// recompresses at its edge throws the whole step away, which is the reason
// serving them is part of this package rather than left to infrastructure.
func precompress(name string, contents []byte) ([]sidecar, error) {
	if !compressible[path.Ext(name)] || len(contents) < minCompressSize {
		return nil, nil
	}

	gz, err := gzipBytes(contents)
	if err != nil {
		return nil, fmt.Errorf("gzip %s: %w", name, err)
	}
	br, err := brotliBytes(contents)
	if err != nil {
		return nil, fmt.Errorf("brotli %s: %w", name, err)
	}

	var out []sidecar
	// Incompressible input comes back larger; shipping that would be a
	// slower response for the same bytes.
	if len(gz) < len(contents) {
		out = append(out, sidecar{Suffix: ".gz", Contents: gz})
	}
	// Brotli has to beat the raw bytes *and* gzip to earn its place: it is
	// only ever offered to clients that would otherwise have taken the gzip,
	// so a larger .br is wasted disk and a slower decode for no saving.
	if len(br) < len(contents) && len(br) < len(gz) {
		out = append(out, sidecar{Suffix: ".br", Contents: br})
	}
	return out, nil
}

func gzipBytes(contents []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(contents); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func brotliBytes(contents []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	if _, err := w.Write(contents); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

/* --------------------------------------------------------------- output -- */

// writeTree makes dir contain exactly files and nothing else.
//
// Pruning is not optional: hashed names accumulate, so without it every edit
// leaves its predecessor behind and the directory grows without bound. It is
// also why dir must be one this command created — identified by the manifest
// it leaves behind — rather than any path handed in on the command line.
func writeTree(dir string, files map[string][]byte, log *slog.Logger) error {
	managed, err := isManaged(dir)
	if err != nil {
		return err
	}
	if !managed {
		return fmt.Errorf("refusing to write %s: not empty and holds no %s, so it is not a build output directory",
			dir, assets.ManifestName)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	for name, contents := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, contents, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", full, err)
		}
		log.Debug("wrote", "file", name, "bytes", len(contents))
	}
	return prune(dir, files, log)
}

// isManaged reports whether dir is safe to prune: absent, holding nothing but
// dotfiles, or carrying the manifest a previous run of this command wrote.
func isManaged(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", dir, err)
	}
	content := false
	for _, e := range entries {
		switch {
		case e.Name() == assets.ManifestName:
			return true, nil
		// A placeholder keeping an embed target compilable before its first
		// build does not make the directory someone else's.
		case strings.HasPrefix(e.Name(), "."):
			continue
		default:
			content = true
		}
	}
	return !content, nil
}

func prune(dir string, keep map[string][]byte, log *slog.Logger) error {
	root := os.DirFS(dir)
	var stale []string
	err := fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		// Dotfiles are never build output; one of them is the placeholder
		// that keeps an embed target compilable.
		case d.IsDir() || strings.HasPrefix(path.Base(name), "."):
			return nil
		}
		if _, wanted := keep[name]; !wanted {
			stale = append(stale, name)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan %s: %w", dir, err)
	}

	for _, name := range stale {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf("remove stale %s: %w", name, err)
		}
		log.Debug("pruned", "file", name)
	}
	if len(stale) > 0 {
		log.Debug("pruned stale outputs", "count", len(stale))
	}
	return nil
}

/* ----------------------------------------------------------- discovery -- */

// entryPoints are the files directly inside src with a buildable extension.
// Nesting is how a file says it is a module rather than an entry, which keeps
// the convention visible in the directory listing instead of in a config file.
func entryPoints(src string) ([]string, error) {
	dir, err := os.ReadDir(src)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", src, err)
	}

	var entries []string
	for _, e := range dir {
		name := e.Name()
		switch {
		case e.IsDir(), strings.HasPrefix(name, "."), strings.HasPrefix(name, "_"):
			continue
		case strings.HasSuffix(name, ".d.ts"):
			continue
		case !entryExtensions[filepath.Ext(name)]:
			continue
		}
		entries = append(entries, filepath.Join(src, name))
	}
	slices.Sort(entries)
	return entries, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
