// Package assets resolves and serves the frontend build output produced by
// cmd/timlee-build.
//
// There are two things a server needs from a built asset tree and they pull in
// opposite directions. Templates need a stable name ("app.js") because markup
// cannot know a content hash. Browsers need the opposite — a name that changes
// whenever the bytes change — or they will serve last week's stylesheet for a
// year. The manifest is the join between the two: the build writes the mapping,
// the template reads it, and the handler is then free to mark every hashed file
// immutable because a changed file is, by construction, a different URL.
//
// The same tree is addressed through an fs.FS so one implementation covers both
// deployments: os.DirFS("web/dist") during development, where a rebuild is
// visible without recompiling Go, and an embed.FS in release, where the binary
// is the only artifact that ships.
//
// # Manifest schema
//
// cmd/timlee-build writes manifest.json into the output directory. Version 1
// looks like this and is the contract every consumer may rely on:
//
//	{
//	  "version": 1,
//	  "base": "/assets/",
//	  "entries": {
//	    "app.css": {
//	      "path":      "/assets/app-FEPGMDJK.css",
//	      "file":      "app-FEPGMDJK.css",
//	      "integrity": "sha384-fX5B…",
//	      "size":      15209
//	    },
//	    "app.js": { "path": "/assets/app-TVWDLM6W.js", … }
//	  }
//	}
//
// The key is the logical name: the entry point's path relative to the source
// directory, carrying the extension of its *output*. web/assets/app.ts is
// "app.js"; web/assets/admin/panel.ts is "admin/panel.js". "path" is always
// "base" followed by "file", so a template needs only "path" and a file server
// needs only "file".
//
// In a template:
//
//	tmpl.Funcs(template.FuncMap(manifest.FuncMap()))
//	<link rel="stylesheet" href="{{ asset "app.css" }}"
//	      integrity="{{ assetIntegrity "app.css" }}" crossorigin="anonymous">
//	<script type="module" src="{{ asset "app.js" }}"
//	        integrity="{{ assetIntegrity "app.js" }}" crossorigin="anonymous"></script>
//
// Bundles are ES modules, so a script tag must carry type="module".
package assets

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// ManifestName is the file cmd/timlee-build writes into the output directory.
const ManifestName = "manifest.json"

// ManifestVersion is the schema version written and accepted by this package.
// A reader must refuse a version it does not understand rather than guess at a
// layout, because a silently misread manifest 404s on every page.
const ManifestVersion = 1

// Entry is one built asset.
type Entry struct {
	// Path is the request path, e.g. "/assets/app-TRJZB2WX.js". This is what
	// goes in a src or href attribute.
	Path string `json:"path"`
	// File is the same asset relative to the output directory, e.g.
	// "app-TRJZB2WX.js". Serving code needs this; markup does not.
	File string `json:"file"`
	// Integrity is the subresource-integrity value for Path, e.g. "sha384-…",
	// usable in an integrity attribute verbatim.
	Integrity string `json:"integrity"`
	// Size is the uncompressed length in bytes.
	Size int64 `json:"size"`
}

// Manifest maps logical entry names to built assets.
//
// The logical name is the entry point's path relative to the source directory
// carrying the *output* extension: web/assets/app.ts becomes "app.js" and
// web/assets/app.css becomes "app.css".
type Manifest struct {
	Version int `json:"version"`
	// Base is the URL prefix every Entry.Path carries, with a trailing slash.
	Base    string           `json:"base"`
	Entries map[string]Entry `json:"entries"`
}

// NewManifest returns an empty manifest for base, normalised to the
// leading-and-trailing-slash form the rest of the package assumes.
func NewManifest(base string) *Manifest {
	return &Manifest{
		Version: ManifestVersion,
		Base:    NormalizeBase(base),
		Entries: map[string]Entry{},
	}
}

// NormalizeBase turns any spelling of a mount point into "/prefix/".
func NormalizeBase(base string) string {
	base = strings.TrimSpace(base)
	if base == "" || base == "/" {
		return "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base
}

// LoadManifest reads ManifestName from fsys.
func LoadManifest(fsys fs.FS) (*Manifest, error) {
	return LoadManifestFile(fsys, ManifestName)
}

// LoadManifestFile reads a manifest stored under a non-default name.
func LoadManifestFile(fsys fs.FS, name string) (*Manifest, error) {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("assets: read %s: %w", name, err)
	}

	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("assets: parse %s: %w", name, err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("assets: %s has version %d, want %d; rebuild with cmd/timlee-build",
			name, m.Version, ManifestVersion)
	}
	if m.Entries == nil {
		m.Entries = map[string]Entry{}
	}
	m.Base = NormalizeBase(m.Base)
	return &m, nil
}

// Lookup returns the entry for a logical name.
func (m *Manifest) Lookup(name string) (Entry, bool) {
	if m == nil {
		return Entry{}, false
	}
	e, ok := m.Entries[name]
	return e, ok
}

// URL resolves a logical name to its request path: URL("app.js") returns
// "/assets/app-TRJZB2WX.js".
//
// An unknown name returns "" rather than a guess, because a guessed path fails
// at the browser where it is expensive to notice. Templates should prefer the
// FuncMap helpers, which fail the render instead.
func (m *Manifest) URL(name string) string {
	e, ok := m.Lookup(name)
	if !ok {
		return ""
	}
	return e.Path
}

// Integrity resolves a logical name to its subresource-integrity value, or ""
// if the name is unknown.
func (m *Manifest) Integrity(name string) string {
	e, _ := m.Lookup(name)
	return e.Integrity
}

// Names lists the logical entry names in sorted order.
func (m *Manifest) Names() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.Entries))
	for name := range m.Entries {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Add records a built asset under a logical name. The build command owns this;
// servers only read.
func (m *Manifest) Add(name, file string, contents []byte) {
	file = strings.TrimPrefix(path.Clean(file), "/")
	m.Entries[name] = Entry{
		Path:      m.Base + file,
		File:      file,
		Integrity: SRI(contents),
		Size:      int64(len(contents)),
	}
}

// Encode writes the manifest indented, so a diff between two builds reads as
// one line per changed asset rather than one line total.
func (m *Manifest) Encode() ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("assets: encode manifest: %w", err)
	}
	return append(raw, '\n'), nil
}

// SRI computes the subresource-integrity value for contents. sha384 is the
// middle strength browsers accept and the one the HTML spec's examples use.
func SRI(contents []byte) string {
	sum := sha512.Sum384(contents)
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}

// FuncMap returns template helpers that fail the render on an unknown name.
//
// A missing asset is always a build mistake, and a template that renders
// src="" makes the browser re-request the current page as a script. Failing
// loudly at render time is the cheaper discovery.
func (m *Manifest) FuncMap() map[string]any {
	resolve := func(name string) (Entry, error) {
		e, ok := m.Lookup(name)
		if !ok {
			return Entry{}, fmt.Errorf("assets: no manifest entry %q (have %v)", name, m.Names())
		}
		return e, nil
	}
	return map[string]any{
		"asset": func(name string) (string, error) {
			e, err := resolve(name)
			return e.Path, err
		},
		"assetIntegrity": func(name string) (string, error) {
			e, err := resolve(name)
			return e.Integrity, err
		},
	}
}

// Dir addresses a build output directory on disk.
//
// Development uses this so an asset rebuild is picked up by the next request
// instead of the next `go build`.
func Dir(dir string) fs.FS { return os.DirFS(dir) }
