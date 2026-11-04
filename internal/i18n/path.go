package i18n

import (
	"net/url"
	"strings"
)

// A locale other than the default lives under a path prefix on the same
// origin: /de/ and /de/models, while English keeps / and /models (docs/I18N.md
// §2). Only public pages take a prefix. The app, the form endpoints that only
// redirect and everything a machine reads keep a single unprefixed URL, which
// the server tells the bundle with LocalizePaths.

// Locales are the enabled locales, the default first. The slice is the
// bundle's own: read it, do not change it.
func (b *Bundle) Locales() []*Locale { return b.locales }

// LocalizePaths tells the bundle which unprefixed paths are public pages, the
// only ones a locale prefix may carry; Path and SignInPath leave every other
// path as it is. The server calls it before it serves. Until then every path
// counts as a page.
func (b *Bundle) LocalizePaths(localized func(path string) bool) {
	b.paths.Store(&localized)
}

// localized reports whether path, without a prefix, query or fragment, may
// carry a locale prefix.
func (b *Bundle) localized(path string) bool {
	fn := b.paths.Load()
	return fn == nil || (*fn)(path)
}

// prefixed reports whether path already starts with an enabled locale's
// segment: /de, /de/ or /de/models.
func (b *Bundle) prefixed(path string) bool {
	seg := path[1:]
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	_, ok := b.byID[seg]
	return ok
}

// Path is where this locale's reader finds p, a path on this site with an
// optional query and fragment: "/models" is "/de/models" in German and stays
// "/models" in English, and "/" is "/de/". A path that already names a
// locale, a path that is not a public page, a URL on another site and a bare
// fragment come back as they are.
func (l *Localizer) Path(p string) string {
	b := l.b
	if b == nil || l.loc == b.locales[0] ||
		!strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) {
		return p
	}
	route, rest := p, ""
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		route, rest = p[:i], p[i:]
	}
	if b.prefixed(route) || !b.localized(route) {
		return p
	}
	return "/" + l.loc.ID + route + rest
}

// SignInPath is the sign-in page in this locale, which sends the reader on to
// returnTo once they are in.
func (l *Localizer) SignInPath(returnTo string) string {
	signIn := l.Path("/sign-in")
	if returnTo == "" || returnTo == "/" {
		return signIn
	}
	return signIn + "?returnTo=" + url.QueryEscape(returnTo)
}
