// Package i18n localizes Timlee's server-rendered pages: the locales Timlee
// ships, their message catalogs, plural rules and number, money, date and
// time formats. docs/I18N.md is the design.
//
// Everything is prepared at start-up: Load reads the ARB catalogs, parses
// every message, checks each translation against its English source and
// fails on any error, the way a malformed template fails the boot. A request
// only looks messages up and walks their parsed form; a message without
// arguments is a precomputed string.
//
// The formatters reproduce the browser's Intl for the same locale, from the
// same CLDR release (package cldr), so a value the server writes and an
// island later rewrites never changes on screen.
package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/text/language"

	"github.com/rahulguptaio/timlee/internal/i18n/cldr"
)

// Key is a message ID, "settings.members.summary". Its first segment names
// the catalog file it lives in. Structs that carry copy hold Keys, never
// English.
type Key string

// Locale is one language Timlee can serve.
type Locale struct {
	// ID is the URL segment and catalog directory: "de", "ar", "en-xa".
	ID string
	// Tag is Lang parsed.
	Tag language.Tag
	// Lang is the BCP 47 tag for <html lang> and hreflang: "de", "en-XA".
	Lang string
	// Dir is the base direction, "ltr" or "rtl". It comes from this table,
	// never from guessing at the tag.
	Dir string
	// Intl is the formatting locale, for CLDR here and Intl in the browser:
	// "en-GB" for English, "ar-u-nu-latn" for Arabic with Latin digits.
	Intl string
	// OG is the og:locale value, "de_DE"; empty for pseudo-locales.
	OG string
	// Name is the language's name in itself, for the language switcher.
	Name string
	// Pseudo marks a generated test locale.
	Pseudo bool

	l *Localizer // set by Load for the bundle the locale belongs to
}

// Localizer returns the localizer of the bundle loc belongs to, or nil for
// a nil Locale.
func (loc *Locale) Localizer() *Localizer {
	if loc == nil {
		return nil
	}
	return loc.l
}

// table is every locale Timlee knows, the default first. Shipping one is
// configuration (TIMLEE_LOCALES); its catalogs must exist.
var table = []Locale{
	{ID: "en", Lang: "en", Dir: "ltr", Intl: "en-GB", OG: "en_GB", Name: "English"},
	{ID: "de", Lang: "de", Dir: "ltr", Intl: "de", OG: "de_DE", Name: "Deutsch"},
	{ID: "fr", Lang: "fr", Dir: "ltr", Intl: "fr", OG: "fr_FR", Name: "Français"},
	{ID: "ja", Lang: "ja", Dir: "ltr", Intl: "ja", OG: "ja_JP", Name: "日本語"},
	{ID: "ar", Lang: "ar", Dir: "rtl", Intl: "ar-u-nu-latn", OG: "ar_AR", Name: "العربية"},
	{ID: "en-xa", Lang: "en-XA", Dir: "ltr", Intl: "en-GB", Name: "Pseudo (accented)", Pseudo: true},
	{ID: "ar-xb", Lang: "ar-XB", Dir: "rtl", Intl: "ar-u-nu-latn", Name: "Pseudo (RTL)", Pseudo: true},
}

// DefaultID is the default locale: unprefixed URLs, the fallback of every
// message, the x-default of hreflang.
const DefaultID = "en"

// formatters are the parsed CLDR data, by formatting locale.
var formatters = func() map[string]*formatter {
	out := map[string]*formatter{}
	for _, loc := range table {
		id := cldrID(loc.Intl)
		if out[id] != nil {
			continue
		}
		src, ok := cldr.Locales[id]
		if !ok {
			panic("i18n: no CLDR data for " + loc.Intl + "; add it to cldr/gen.go")
		}
		f, err := newFormatter(src)
		if err != nil {
			panic("i18n: CLDR data for " + id + ": " + err.Error())
		}
		out[id] = f
	}
	return out
}()

// cldrID drops the Unicode extension: "ar-u-nu-latn" is CLDR's "ar" with the
// Latin digits its tables already use.
func cldrID(intl string) string {
	id, _, _ := strings.Cut(intl, "-u-")
	return id
}

// Bundle is the shipped locales and their compiled catalogs.
type Bundle struct {
	locales []*Locale // enabled, the default first
	byID    map[string]*Locale
	// matcher negotiates among the non-pseudo locales, index-aligned with
	// matchable.
	matcher   language.Matcher
	matchable []*Locale
	// paths reports which paths take a locale prefix (LocalizePaths).
	paths atomic.Pointer[func(path string) bool]
}

// catalog is one locale's messages.
type catalog map[Key]*message

// message is one parsed message.
type message struct {
	nodes []node
	shape shape
	// static messages are rendered once, here.
	static bool
	plain  string
	html   template.HTML
}

// Load reads the ARB catalogs of the enabled locales from fsys, laid out as
// <locale ID>/<surface>.arb, and compiles them. The default locale is always
// enabled; pseudo adds en-xa and ar-xb, generated from English. Any error —
// an unknown locale, a missing catalog, a message that does not parse or
// does not fit its English source — fails the load.
func Load(fsys fs.FS, enabled []string, pseudo bool) (*Bundle, error) {
	ids := []string{DefaultID}
	for _, id := range enabled {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}

	b := &Bundle{byID: map[string]*Locale{}}
	var source catalog
	var problems []error
	for _, id := range ids {
		def, ok := definition(id)
		switch {
		case !ok:
			problems = append(problems, fmt.Errorf("locale %q is not one Timlee knows", id))
			continue
		case def.Pseudo:
			problems = append(problems, fmt.Errorf("locale %q is a pseudo-locale; enable it with TIMLEE_PSEUDO_LOCALES", id))
			continue
		}
		loc := newLocale(def)
		cat, err := readCatalog(fsys, loc, source)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if source == nil {
			source = cat
		}
		b.add(loc, cat, source)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("i18n: %w", errors.Join(problems...))
	}
	if pseudo {
		for _, id := range []string{"en-xa", "ar-xb"} {
			def, _ := definition(id)
			loc := newLocale(def)
			b.add(loc, pseudoCatalog(loc, source), source)
		}
	}

	tags := make([]language.Tag, 0, len(b.locales))
	for _, loc := range b.locales {
		if !loc.Pseudo {
			tags = append(tags, loc.Tag)
			b.matchable = append(b.matchable, loc)
		}
	}
	b.matcher = language.NewMatcher(tags)
	return b, nil
}

func definition(id string) (Locale, bool) {
	for _, loc := range table {
		if loc.ID == id {
			return loc, true
		}
	}
	return Locale{}, false
}

// KnownLocale reports whether id names a locale Timlee understands,
// including a pseudo-locale that may not be enabled in this process.
func KnownLocale(id string) bool {
	_, ok := definition(strings.ToLower(strings.TrimSpace(id)))
	return ok
}

func newLocale(def Locale) *Locale {
	loc := def
	loc.Tag = language.MustParse(def.Lang)
	return &loc
}

// add enables a locale. Its messages fall back to the source catalog.
func (b *Bundle) add(loc *Locale, cat, source catalog) {
	chain := []catalog{cat}
	if loc.ID != DefaultID {
		chain = append(chain, source)
	}
	loc.l = &Localizer{loc: loc, chain: chain, f: formatters[cldrID(loc.Intl)], b: b}
	b.locales = append(b.locales, loc)
	b.byID[loc.ID] = loc
}

// Default is the default locale.
func (b *Bundle) Default() *Locale { return b.locales[0] }

// ByID finds an enabled locale by its ID, which is lowercase.
func (b *Bundle) ByID(id string) (*Locale, bool) {
	loc, ok := b.byID[id]
	return loc, ok
}

// For returns the localizer of loc, or the default locale's when loc is nil
// or not enabled in this bundle.
func (b *Bundle) For(loc *Locale) *Localizer {
	if loc != nil && b.byID[loc.ID] == loc {
		return loc.l
	}
	return b.locales[0].l
}

/* --------------------------------------------------------------- catalogs -- */

var keyPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*)+$`)

// readCatalog reads and compiles every ARB file of a locale. Without a
// source it is the source (English); otherwise every message is checked
// against the source's.
func readCatalog(fsys fs.FS, loc *Locale, source catalog) (catalog, error) {
	files, err := fs.Glob(fsys, loc.ID+"/*.arb")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", loc.ID, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("locale %q has no catalog (looked for %s/*.arb)", loc.ID, loc.ID)
	}
	cat := catalog{}
	var problems []error
	for _, file := range files {
		raw, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		if err := compileARB(cat, raw, file, loc, source); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return cat, nil
}

// arbMeta is the part of an "@key" entry Load checks.
type arbMeta struct {
	Placeholders map[string]json.RawMessage `json:"placeholders"`
}

func compileARB(cat catalog, raw []byte, file string, loc *Locale, source catalog) error {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	surface := strings.TrimSuffix(path.Base(file), ".arb")
	var problems []error
	fail := func(format string, a ...any) {
		problems = append(problems, fmt.Errorf("%s: "+format, append([]any{file}, a...)...))
	}

	var declared string
	if v, ok := entries["@@locale"]; !ok || json.Unmarshal(v, &declared) != nil || declared != loc.ID {
		fail("@@locale is %s, want %q", v, loc.ID)
	}
	for name, v := range entries {
		switch {
		case strings.HasPrefix(name, "@@"):
			continue
		case strings.HasPrefix(name, "@"):
			if _, ok := entries[name[1:]]; !ok {
				fail("metadata %s has no message", name)
			}
			continue
		}
		key := Key(name)
		if !keyPattern.MatchString(name) || !strings.HasPrefix(name, surface+".") {
			fail("key %q must be %s.<area>.<item>, lower camel case", name, surface)
			continue
		}
		var text string
		if err := json.Unmarshal(v, &text); err != nil {
			fail("%s is not a string", name)
			continue
		}
		nodes, err := parseMessage(text)
		if err != nil {
			fail("%s: %v", name, err)
			continue
		}
		m := &message{nodes: nodes, shape: shapeOf(nodes)}
		if err := checkMessage(m, key, entries["@"+name], loc, source); err != nil {
			fail("%s: %v", name, err)
			continue
		}
		if m.shape.static() {
			m.static = true
			m.plain, m.html = staticText(formatters[cldrID(loc.Intl)], nodes)
		}
		cat[key] = m
	}
	return errors.Join(problems...)
}

// checkMessage holds a message to its English source and its locale's
// plural rules.
func checkMessage(m *message, key Key, meta json.RawMessage, loc *Locale, source catalog) error {
	rules := formatters[cldrID(loc.Intl)].plural
	for c := range m.shape.categories {
		if c != Other && !rules.has[c] {
			return fmt.Errorf("plural category %s does not exist in %s", c, loc.ID)
		}
	}
	if source == nil {
		if len(meta) == 0 {
			return nil
		}
		var md arbMeta
		if err := json.Unmarshal(meta, &md); err != nil {
			return fmt.Errorf("metadata: %w", err)
		}
		if md.Placeholders == nil {
			return nil
		}
		for name := range m.shape.args {
			if _, ok := md.Placeholders[name]; !ok {
				return fmt.Errorf("argument %q is not declared in placeholders", name)
			}
		}
		for name := range md.Placeholders {
			if _, ok := m.shape.args[name]; !ok {
				return fmt.Errorf("placeholder %q is not used", name)
			}
		}
		return nil
	}
	src, ok := source[key]
	if !ok {
		return errors.New("not in the English catalog")
	}
	return m.shape.compatible(src.shape)
}

/* -------------------------------------------------------------- Localizer -- */

// Localizer renders messages and formats values for one locale. It is safe
// for concurrent use and allocates nothing for a message without arguments.
type Localizer struct {
	loc   *Locale
	chain []catalog // the locale's catalog, then its fallbacks
	f     *formatter
	b     *Bundle // for Path: the other locales and the public paths
}

// Locale is the locale this localizer writes.
func (l *Localizer) Locale() *Locale { return l.loc }

func (l *Localizer) lookup(k Key) *message {
	for _, cat := range l.chain {
		if m, ok := cat[k]; ok {
			return m
		}
	}
	return nil
}

// T renders a message as plain text. args are name/value pairs: strings are
// wrapped in FSI…PDI so text of unknown direction cannot reorder the
// sentence, integers are formatted for the locale, Formatted values are
// printed as they are. A missing message renders as ⟦key⟧.
func (l *Localizer) T(k Key, args ...any) string {
	s, _ := l.text(k, nil, args)
	return s
}

// TN renders a message whose "count" argument is n, for plural messages.
func (l *Localizer) TN(k Key, n int64, args ...any) string {
	s, _ := l.text(k, &n, args)
	return s
}

// HTML renders a message as HTML: text escaped, tags rendered (<code> left
// to right, <link> as a link to its argument), string arguments in <bdi>.
func (l *Localizer) HTML(k Key, args ...any) template.HTML {
	s, _ := l.html(k, nil, args)
	return s
}

// text renders plain text, reporting misuse that T forgives.
func (l *Localizer) text(k Key, count *int64, pairs []any) (string, error) {
	m := l.lookup(k)
	switch {
	case m == nil:
		return missing(k), nil
	case m.static && len(pairs) == 0 && count == nil:
		return m.plain, nil
	}
	bp := buffers.Get().(*[]byte)
	b, err := l.render((*bp)[:0], m, count, pairs, false)
	s := string(b)
	release(bp, b)
	return s, err
}

func (l *Localizer) html(k Key, count *int64, pairs []any) (template.HTML, error) {
	m := l.lookup(k)
	switch {
	case m == nil:
		return template.HTML(appendEscaped(nil, missing(k))), nil //nolint:gosec // escaped
	case m.static && len(pairs) == 0 && count == nil:
		return m.html, nil
	}
	bp := buffers.Get().(*[]byte)
	b, err := l.render((*bp)[:0], m, count, pairs, true)
	h := template.HTML(b) //nolint:gosec // every byte comes from appendEscaped or a fixed tag
	release(bp, b)
	return h, err
}

// buffers are where messages with arguments are rendered, so the only
// allocation left is the result.
var buffers = sync.Pool{New: func() any { b := make([]byte, 0, 256); return &b }}

func release(bp *[]byte, b []byte) {
	if cap(b) <= 16<<10 {
		*bp = b[:0]
		buffers.Put(bp)
	}
}

func (l *Localizer) render(b []byte, m *message, count *int64, pairs []any, html bool) ([]byte, error) {
	r := renderer{f: l.f, html: html, args: args{pairs: pairs}}
	if count != nil {
		r.args.count, r.args.hasCount = *count, true
	}
	if err := r.args.check(); err != nil {
		return b, err
	}
	b = r.appendNodes(b, m.nodes)
	return b, r.err
}

// missing is what a message that no catalog holds renders as: impossible to
// miss on a page.
func missing(k Key) string { return "⟦" + string(k) + "⟧" }
