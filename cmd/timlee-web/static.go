package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"time"
)

// rootFileCacheControl is for files addressed by a fixed name. They change
// rarely but they can change, and nothing in the URL says when, so they are
// never immutable: a day fresh, then served stale for up to a week while the
// cache revalidates in the background.
const rootFileCacheControl = "public, max-age=86400, stale-while-revalidate=604800"

// rootFileTypes pins the types served here rather than trusting the host's
// mime tables, which differ between a laptop and the deployed image.
var rootFileTypes = map[string]string{
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// rootFile is one file served at the site root, held in memory: together
// they are a few tens of kilobytes, read once at startup.
type rootFile struct {
	name        string
	contentType string
	etag        string
	body        []byte
}

func (f rootFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Cache-Control", rootFileCacheControl)
	// A content digest rather than a timestamp: embedded files carry no
	// modification time, and a validator that changed on every restart
	// would make every revalidation a full download.
	h.Set("ETag", f.etag)
	// A zero time sends no Last-Modified, so the digest is the only
	// validator and a restart cannot make a cached copy look stale.
	http.ServeContent(w, r, f.name, time.Time{}, bytes.NewReader(f.body))
}

// loadRootFiles reads every file at the top of fsys into a handler, and adds
// robots.txt for the given public origin.
func loadRootFiles(fsys fs.FS, publicOrigin string) (map[string]rootFile, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read root files: %w", err)
	}

	files := make(map[string]rootFile, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		contentType := rootFileTypes[path.Ext(name)]
		if contentType == "" {
			contentType = mime.TypeByExtension(path.Ext(name))
		}
		if contentType == "" {
			return nil, fmt.Errorf("root file %s has no known content type", name)
		}

		files[name] = newRootFile(name, contentType, body)
	}

	files["robots.txt"] = newRootFile("robots.txt", rootFileTypes[".txt"], robotsTxt(publicOrigin))
	return files, nil
}

func newRootFile(name, contentType string, body []byte) rootFile {
	sum := sha256.Sum256(body)
	return rootFile{
		name:        name,
		contentType: contentType,
		etag:        `"` + hex.EncodeToString(sum[:8]) + `"`,
		body:        body,
	}
}

// robotsTxt allows everything and names the sitemap. Pages that must stay out
// of an index say so with a robots meta tag instead: a crawler that is
// disallowed from fetching a page never sees its noindex.
func robotsTxt(publicOrigin string) []byte {
	return []byte("User-agent: *\nAllow: /\n\nSitemap: " + trimSlash(publicOrigin) + "/sitemap.xml\n")
}

// handleRootFiles registers one exact route per file, so a typo in a URL is
// still the site's own 404 page rather than a directory listing.
func handleRootFiles(mux *http.ServeMux, files map[string]rootFile) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mux.Handle("GET /"+name, files[name])
	}
}
