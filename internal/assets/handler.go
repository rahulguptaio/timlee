package assets

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Cache-Control values. A hashed name is a promise that these bytes never
// change, so the response can be cached for the maximum a year-based
// max-age allows and never revalidated. Everything else — the manifest, a
// favicon, anything hand-named — gets the opposite promise: cacheable, but
// only after asking.
const (
	immutableCacheControl  = "public, max-age=31536000, immutable"
	revalidateCacheControl = "no-cache"
)

// hashedName matches cmd/timlee-build's "[name]-[hash]" output. esbuild's
// hash is upper-case base32, so the pattern cannot collide with a hand-written
// name unless that name is also shouted.
//
// The manifest is the authority for entry points; this only has to classify
// the files the manifest does not list — code-split chunks and sourcemaps.
var hashedName = regexp.MustCompile(`-[A-Z0-9]{6,}\.[A-Za-z0-9]+$`)

// Encoding is a pre-compressed representation written beside an asset.
type Encoding struct {
	// Name is the Content-Encoding token.
	Name string
	// Suffix is appended to the asset's filename to find the sibling.
	Suffix string
}

// Encodings are offered to clients in this order, best ratio first.
// cmd/timlee-build writes both sidecars at maximum quality; a missing one
// simply falls through to the next option.
var Encodings = []Encoding{
	{Name: "br", Suffix: ".br"},
	{Name: "gzip", Suffix: ".gz"},
}

// HandlerOptions configures Handler.
type HandlerOptions struct {
	// FS is the build output tree: assets.Dir("web/dist") in development,
	// dist.FS() in a release binary.
	FS fs.FS
	// Manifest, when set, is the authority on which files are content-hashed.
	// Optional: without it the handler falls back to the name pattern.
	Manifest *Manifest
	// Base is the URL prefix the handler is mounted under. It defaults to
	// the manifest's Base, then to "/assets/". The prefix is stripped before
	// the lookup, so this handler can be mounted directly on a ServeMux
	// without http.StripPrefix.
	Base string
	// ExposeManifest allows GET of the manifest itself. Off by default: it is
	// a build artefact the server reads, not something a browser needs.
	ExposeManifest bool
	Logger         *slog.Logger
}

type handler struct {
	fsys           fs.FS
	base           string
	immutable      map[string]bool
	exposeManifest bool
	log            *slog.Logger
	// modTime is this process's start, used as a Last-Modified for trees
	// that carry no timestamps of their own. embed.FS reports a zero time,
	// which would leave conditional requests with nothing to match on.
	modTime time.Time
}

// NewHandler serves a built asset tree.
//
// Mount it directly, prefix included:
//
//	mux.Handle("/assets/", assets.NewHandler(assets.HandlerOptions{...}))
func NewHandler(opts HandlerOptions) http.Handler {
	base := opts.Base
	if base == "" && opts.Manifest != nil {
		base = opts.Manifest.Base
	}
	base = NormalizeBase(base)
	if base == "/" {
		base = "/assets/"
	}

	h := &handler{
		fsys:           opts.FS,
		base:           base,
		immutable:      map[string]bool{},
		exposeManifest: opts.ExposeManifest,
		log:            opts.Logger,
		modTime:        time.Now().UTC().Truncate(time.Second),
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	for _, e := range opts.Manifest.entriesOrNil() {
		h.immutable[e.File] = true
	}
	return h
}

func (m *Manifest) entriesOrNil() map[string]Entry {
	if m == nil {
		return nil
	}
	return m.Entries
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	name, ok := h.resolve(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if name == ManifestName && !h.exposeManifest {
		http.NotFound(w, r)
		return
	}

	// Content-Type comes from the *logical* name. Deriving it from whatever
	// file is opened below would label a gzip sibling application/gzip and
	// hand the browser a download instead of a stylesheet.
	contentType := contentTypeOf(name)

	// Nothing is cached until a file is known to exist: a 404 that inherited
	// the immutable Cache-Control below would be cached for a year, and the
	// asset would stay missing long after the deploy that added it.
	file, info, encoding, err := h.open(name, r.Header.Get("Accept-Encoding"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.log.Error("asset read failed", "asset", name, "error", err)
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	header := w.Header()
	header.Set("Cache-Control", h.cacheControl(name))
	// Even when nothing was negotiated, a shared cache must be told the
	// response could have varied, or it will hand a gzip body to a client
	// that never asked for one.
	header.Add("Vary", "Accept-Encoding")
	header.Set("Content-Type", contentType)
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
		// net/http omits Content-Length whenever Content-Encoding is set,
		// because it assumes the handler wraps the writer in a compressor and
		// cannot know the wire size. These bytes are already compressed and
		// the size is known, so the response need not fall back to chunked.
		header.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	// A cache keyed on URL alone must never reuse the gzip body for an
	// identity request, so the encoding is part of the validator.
	if tag := etag(name, info, encoding); tag != "" {
		header.Set("ETag", tag)
	}

	content, err := seeker(file)
	if err != nil {
		h.log.Error("asset read failed", "asset", name, "error", err)
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		return
	}

	modTime := info.ModTime()
	if modTime.IsZero() {
		modTime = h.modTime
	}
	// ServeContent would sniff a Content-Type of its own from the name, but
	// the header set above takes precedence, so the compressed bytes are
	// never inspected.
	http.ServeContent(w, r, name, modTime, content)
}

// contentTypes pins the types this pipeline emits rather than trusting
// mime.TypeByExtension, whose answer depends on the host's /etc mime files
// and so can differ between a developer's laptop and the deployed image.
var contentTypes = map[string]string{
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".mjs":         "text/javascript; charset=utf-8",
	".svg":         "image/svg+xml",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

func contentTypeOf(name string) string {
	if t, ok := contentTypes[path.Ext(name)]; ok {
		return t
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// resolve turns a request path into a name inside the output tree, or reports
// that the request is not ours to answer.
func (h *handler) resolve(urlPath string) (string, bool) {
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	// Containment does not rest on this: fs.ValidPath below rejects any name
	// that could leave the tree. Cleaning normalises the prefix test, and a
	// request that started inside the mount but climbed out of it
	// ("/assets/../app.js") is refused rather than re-rooted by the
	// StripPrefix tolerance below.
	underBase := strings.HasPrefix(urlPath, h.base)
	urlPath = path.Clean(urlPath)
	if underBase && !strings.HasPrefix(urlPath, h.base) {
		return "", false
	}

	switch {
	case strings.HasPrefix(urlPath, h.base):
		urlPath = strings.TrimPrefix(urlPath, h.base)
	case urlPath == strings.TrimSuffix(h.base, "/"):
		return "", false
	default:
		// Tolerate being mounted behind http.StripPrefix, where the prefix
		// is already gone by the time the request arrives.
		urlPath = strings.TrimPrefix(urlPath, "/")
	}

	if urlPath == "" || urlPath == "." || !fs.ValidPath(urlPath) {
		return "", false
	}
	return urlPath, true
}

// cacheControl decides whether this name may be cached forever.
func (h *handler) cacheControl(name string) string {
	if h.isImmutable(name) {
		return immutableCacheControl
	}
	return revalidateCacheControl
}

func (h *handler) isImmutable(name string) bool {
	if h.immutable[name] {
		return true
	}
	// A sourcemap inherits its bundle's hash, so classify on the name it
	// belongs to rather than on ".map".
	return hashedName.MatchString(strings.TrimSuffix(name, ".map"))
}

// open returns the best representation of name the client will accept,
// together with the Content-Encoding to declare for it.
func (h *handler) open(name, acceptEncoding string) (fs.File, fs.FileInfo, string, error) {
	for _, enc := range Encodings {
		if !acceptsEncoding(acceptEncoding, enc.Name) {
			continue
		}
		file, err := h.fsys.Open(name + enc.Suffix)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			file.Close()
			continue
		}
		return file, info, enc.Name, nil
	}

	file, err := h.fsys.Open(name)
	if err != nil {
		return nil, nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, "", err
	}
	if info.IsDir() {
		file.Close()
		return nil, nil, "", fs.ErrNotExist
	}
	return file, info, "", nil
}

// seeker adapts a file to what http.ServeContent needs. os and embed files
// already seek; an arbitrary fs.FS need not, and assets are small enough that
// buffering such a file is cheaper than giving up ranges and If-Range.
func seeker(file fs.File) (io.ReadSeeker, error) {
	if rs, ok := file.(io.ReadSeeker); ok {
		return rs, nil
	}
	buf, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(buf), nil
}

// etag derives a validator. For a hashed name the filename already is a
// content digest, so there is nothing to compute; for anything else size and
// modification time are the best that can be had without reading the file.
func etag(name string, info fs.FileInfo, encoding string) string {
	suffix := ""
	if encoding != "" {
		suffix = "-" + encoding
	}
	if hashedName.MatchString(strings.TrimSuffix(name, ".map")) {
		return strconv.Quote(path.Base(name) + suffix)
	}
	if info.ModTime().IsZero() {
		return ""
	}
	return strconv.Quote(strconv.FormatInt(info.Size(), 36) + "-" +
		strconv.FormatInt(info.ModTime().UnixNano(), 36) + suffix)
}

// acceptsEncoding reports whether the client said it would accept enc.
//
// The q-value matters: "gzip;q=0" is an explicit refusal, and treating the
// token's presence as consent would send a body the client cannot read.
func acceptsEncoding(header, enc string) bool {
	if header == "" {
		return false
	}
	accepted := false
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		token = strings.TrimSpace(token)
		if !strings.EqualFold(token, enc) && token != "*" {
			continue
		}
		if quality(params) == 0 {
			// An explicit refusal of the exact token wins over a wildcard.
			if strings.EqualFold(token, enc) {
				return false
			}
			continue
		}
		accepted = true
	}
	return accepted
}

func quality(params string) float64 {
	for _, param := range strings.Split(params, ";") {
		key, value, found := strings.Cut(param, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 1
		}
		return q
	}
	return 1
}
