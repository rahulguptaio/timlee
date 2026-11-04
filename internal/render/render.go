// Package render turns page data into complete HTML on the server.
//
// Every page Timlee serves is finished before it leaves this process: the
// markup a browser receives in the first response is the markup a reader
// sees, including the signed-in state and the chosen theme. Nothing here
// produces a shell for JavaScript to fill in later.
//
// Two decisions follow from that. Templates are parsed once at startup, so a
// malformed template is a boot failure rather than a 500 in front of a
// reader; and a page is rendered into a buffer before a single byte reaches
// the socket, so a template error cannot leave half a page behind a 200.
//
// The tree is laid out by role:
//
//	layouts/   the document shell, one per surface (base, auth, console)
//	partials/  the repeated pieces (nav, footer, cards, tabs)
//	pages/     one file per route, each defining "content"
//
// Parse order is layouts, then partials, then the page, so a page may
// override a block the layout defines empty.
package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ext is the extension every template file carries. A distinct extension
// keeps editors in HTML mode while still marking the file as templated.
const ext = ".gohtml"

const (
	layoutsGlob  = "layouts/*" + ext
	partialsGlob = "partials/*" + ext
	pagesGlob    = "pages/*" + ext
)

// DefaultLayout is used by a page that does not name one.
const DefaultLayout = "base"

// Options configures New.
type Options struct {
	// FS is the embedded template tree used in production.
	FS fs.FS
	// Dir is the same tree on disk. When Dev is set it replaces FS, so an
	// edit shows up on the next request.
	Dir string
	// Dev re-parses from Dir on every render.
	Dev bool
	// Funcs are merged into every template set. assets.FuncMap belongs here:
	// it fails the render on an unknown asset name, which is the only moment
	// the mistake is cheap to find.
	Funcs template.FuncMap
	// Logger receives re-parse failures in development.
	Logger *slog.Logger
}

// Engine holds the parsed template sets.
type Engine struct {
	dev    bool
	source fs.FS
	funcs  template.FuncMap
	log    *slog.Logger

	// Guards pages, which development replaces wholesale on every request
	// while other requests may be reading it.
	mu    sync.RWMutex
	pages map[string]*template.Template
}

// New parses the template tree, returning the first parse error.
//
// Development parses here too even though every render re-parses: a broken
// template should fail the process that started, not the first reader.
func New(opts Options) (*Engine, error) {
	source := opts.FS
	if opts.Dev {
		if opts.Dir == "" {
			return nil, fmt.Errorf("render: dev mode needs a template directory")
		}
		source = os.DirFS(opts.Dir)
	}
	if source == nil {
		return nil, fmt.Errorf("render: no template source")
	}

	e := &Engine{
		dev:    opts.Dev,
		source: source,
		funcs:  opts.Funcs,
		log:    opts.Logger,
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}

	pages, err := e.parse()
	if err != nil {
		return nil, err
	}
	e.pages = pages
	return e, nil
}

// parse builds one template set per page: the layouts and partials every page
// shares, plus that page alone.
//
// Pages are kept apart rather than merged into one set because each defines
// "content"; a single set would leave the last file parsed as the only page
// the server could render. The shared files are parsed once and cloned into
// each page's set, which is less than half the work of parsing them again for
// every page.
func (e *Engine) parse() (map[string]*template.Template, error) {
	shared, err := globAll(e.source, layoutsGlob, partialsGlob)
	if err != nil {
		return nil, err
	}
	if len(shared) == 0 {
		return nil, fmt.Errorf("render: no layouts or partials found (looked for %s and %s)", layoutsGlob, partialsGlob)
	}

	pageFiles, err := fs.Glob(e.source, pagesGlob)
	if err != nil {
		return nil, fmt.Errorf("render: glob %s: %w", pagesGlob, err)
	}
	if len(pageFiles) == 0 {
		return nil, fmt.Errorf("render: no pages found (looked for %s)", pagesGlob)
	}
	sort.Strings(pageFiles)

	base, err := template.New("").Funcs(e.funcs).ParseFS(e.source, shared...)
	if err != nil {
		return nil, fmt.Errorf("render: parse layouts and partials: %w", err)
	}

	pages := make(map[string]*template.Template, len(pageFiles))
	for _, file := range pageFiles {
		// Each page gets its own copy of the shared trees: html/template
		// rewrites a tree when it first escapes it, so pages must not share.
		// The copy keeps the base's unnamed root, because a page may share
		// its name with a layout ("console") that a root of that name would
		// replace.
		tmpl, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("render: clone layouts for %s: %w", file, err)
		}

		// The page is parsed after the layouts and partials so it may
		// replace a block a layout defines empty; html/template keeps the
		// last non-empty definition of a name.
		if _, err := tmpl.ParseFS(e.source, file); err != nil {
			return nil, fmt.Errorf("render: parse %s: %w", file, err)
		}
		pages[pageName(file)] = tmpl
	}
	return pages, nil
}

func globAll(fsys fs.FS, patterns ...string) ([]string, error) {
	var all []string
	for _, pattern := range patterns {
		matches, err := fs.Glob(fsys, pattern)
		if err != nil {
			return nil, fmt.Errorf("render: glob %s: %w", pattern, err)
		}
		sort.Strings(matches)
		all = append(all, matches...)
	}
	return all, nil
}

// pageName turns "pages/sign-in.gohtml" into "sign-in".
func pageName(file string) string {
	return strings.TrimSuffix(strings.TrimPrefix(file, "pages/"), ext)
}

// Pages lists the renderable page names in sorted order.
func (e *Engine) Pages() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	names := make([]string, 0, len(e.pages))
	for name := range e.pages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// lookup returns the set for a page, re-parsing the whole tree first in
// development.
func (e *Engine) lookup(page string) (*template.Template, error) {
	if e.dev {
		pages, err := e.parse()
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		e.pages = pages
		e.mu.Unlock()
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	tmpl, ok := e.pages[page]
	if !ok {
		names := make([]string, 0, len(e.pages))
		for name := range e.pages {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("render: no page %q (have %v)", page, names)
	}
	return tmpl, nil
}

// HTML renders a page into bytes without touching a response.
//
// Exposed because it is what makes the buffering above testable, and what a
// caller needs when the markup is going somewhere other than a socket.
func (e *Engine) HTML(page string, p *Page) ([]byte, error) {
	var buf bytes.Buffer
	if err := e.execute(&buf, page, p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e *Engine) execute(buf *bytes.Buffer, page string, p *Page) error {
	tmpl, err := e.lookup(page)
	if err != nil {
		return err
	}

	layout := p.layout()
	if tmpl.Lookup(layout) == nil {
		return fmt.Errorf("render: page %q names layout %q, which no template defines", page, layout)
	}

	if err := tmpl.ExecuteTemplate(buf, layout, p); err != nil {
		return fmt.Errorf("render: execute %s/%s: %w", page, layout, err)
	}
	return nil
}

// buffers holds the buffers Render builds pages in. A page runs to hundreds
// of kilobytes, and growing a fresh buffer to that size by doubling was 40%
// of every byte the site allocated under load, each large allocation also
// asking the OS for memory back.
var buffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// maxPooledBuffer keeps one outsized page from pinning its buffer forever.
const maxPooledBuffer = 1 << 20

// Render writes a page with the given status.
//
// The markup is built in full before the status line is written, so a
// template that fails mid-execution returns an error to a caller that can
// still send a 500 instead of trailing a broken document behind a 200.
func (e *Engine) Render(w http.ResponseWriter, status int, page string, p *Page) error {
	buf := buffers.Get().(*bytes.Buffer)
	defer func() {
		if buf.Cap() <= maxPooledBuffer {
			buf.Reset()
			buffers.Put(buf)
		}
	}()

	if err := e.execute(buf, page, p); err != nil {
		return err
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// The session and the theme both change the markup, so a shared cache
	// must not hand one reader's page to another.
	h.Add("Vary", "Cookie")
	// Every page carries a fresh CSP nonce and may be personalised, so it is
	// revalidated on each use, and a signed-in page stays out of shared
	// caches. A handler that set a stricter policy (no-store) keeps it.
	if h.Get("Cache-Control") == "" {
		if p.SignedIn() {
			h.Set("Cache-Control", "private, no-cache")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
	}
	h.Set("Content-Length", strconv.Itoa(buf.Len()))

	w.WriteHeader(status)
	_, err := w.Write(buf.Bytes())
	return err
}
