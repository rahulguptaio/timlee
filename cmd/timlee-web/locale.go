package main

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/text/language"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
)

// Locale routing (docs/I18N.md §2).
//
// The default locale is unprefixed and every other one is a path prefix on
// the same origin: / and /de/, /models and /ar/models. Only public pages and
// flows take a prefix. The app, the form endpoints that only redirect and
// everything a machine reads keep one URL, and the app follows the reader:
// their saved preference, then the timlee.locale cookie, then English.
//
// Nothing redirects on Accept-Language, an address or a cookie. The only
// redirects here put a URL in its canonical form; a reader whose language is
// not the page's is offered theirs by a banner they can dismiss, never sent.

// localeCookie keeps the language a reader chose with POST /locale. A
// preference, not a secret, kept as long as the theme.
const (
	localeCookie         = "timlee.locale"
	localeCookieLifetime = 365 * 24 * time.Hour
)

// appRoots are the path trees where one URL serves every language and the
// answer follows the reader: the app, and the desktop app's sign-in relay,
// which sends the app's window to the sign-in page in the reader's language.
var appRoots = []string{"/apps", "/settings", "/sign-in/desktop"}

// unprefixedRoots are every path tree that keeps one URL in every language:
// the app's, the assets and the well-known files.
var unprefixedRoots = append(appRoots[:len(appRoots):len(appRoots)], "/assets", "/.well-known")

// under reports whether p is root or a path below it.
func under(p, root string) bool {
	return strings.HasPrefix(p, root) && (len(p) == len(root) || p[len(root)] == '/')
}

// localizedPath reports whether p, an unprefixed path, may carry a locale
// prefix: the public pages and flows, and any path no route claims, whose 404
// follows the prefix. Files never do (assets, root files, JSON, XML, text),
// nor the app or the form endpoints that only redirect. A form whose answer
// is a page posts to the localized path instead (the sign-in, invitation,
// cost-lint and search forms), so that page is in the reader's language.
func localizedPath(p string) bool {
	if strings.Contains(p[strings.LastIndexByte(p, '/')+1:], ".") {
		return false
	}
	for _, root := range unprefixedRoots {
		if under(p, root) {
			return false
		}
	}
	switch p {
	case "/sign-out", "/theme", "/locale", "/health", "/readyz":
		return false
	}
	return true
}

// followsReader reports whether r's page is in the reader's language rather
// than the URL's: an app page, whose one URL serves every language, or a
// page that does not exist.
func followsReader(r *http.Request) bool {
	if r.Pattern == "/" {
		return true
	}
	for _, root := range appRoots {
		if under(r.URL.Path, root) {
			return true
		}
	}
	return false
}

// splitLocale cuts a path into its first segment and the rest: "/de/models"
// is "de" and "/models", "/de" is "de" and "".
func splitLocale(path string) (seg, rest string) {
	if len(path) < 2 || path[0] != '/' {
		return "", path
	}
	seg = path[1:]
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		return seg[:i], seg[i:]
	}
	return seg, ""
}

// localeRoutes takes a locale prefix off a public page's URL and puts the
// locale on the request, so every language is served by the same route
// table:
//
//	/de/models            /models, in German
//	/de, /DE/models       301 to /de/, /de/models
//	/en/models            301 to /models: the default locale has one URL
//	/de/apps              301 to /apps: the app is never prefixed
//	/xx/models            no locale: the 404 it always was
//
// A request without a prefix passes through untouched, which is every request
// while English is the only locale.
func (s *server) localeRoutes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seg, rest := splitLocale(r.URL.Path)
		b := s.bundle()
		loc, ok := b.ByID(strings.ToLower(seg))
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case rest != "" && !localizedPath(rest):
			redirectLocale(w, r, seg, "")
		case seg != loc.ID || loc == b.Default() || rest == "":
			prefix := ""
			if loc != b.Default() {
				prefix = "/" + loc.ID
			}
			redirectLocale(w, r, seg, prefix)
		default:
			r2 := r.WithContext(i18n.WithLocale(r.Context(), loc, true))
			u := *r.URL
			u.Path, u.RawPath = rest, ""
			if raw := r.URL.RawPath; strings.HasPrefix(raw, "/"+seg+"/") {
				u.RawPath = raw[len(seg)+1:]
			}
			r2.URL = &u
			next.ServeHTTP(w, r2)
		}
	})
}

// redirectLocale answers with a permanent redirect to r's URL with its first
// segment, seg, replaced by prefix ("/de", or "" for none).
func redirectLocale(w http.ResponseWriter, r *http.Request, seg, prefix string) {
	esc := r.URL.EscapedPath()
	var rest string
	if strings.HasPrefix(esc, "/"+seg) && (len(esc) == len(seg)+1 || esc[len(seg)+1] == '/') {
		rest = esc[len(seg)+1:]
	} else {
		// The segment was percent-encoded: its decoded rest, escaped again.
		_, decoded := splitLocale(r.URL.Path)
		rest = (&url.URL{Path: decoded}).EscapedPath()
	}
	// Never a scheme-relative URL: "/en//example.com" goes to
	// /example.com on this site.
	rest = "/" + strings.TrimLeft(rest, `/\`)
	target := prefix + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

/* ------------------------------------------------- the request's locale -- */

// pageLocale is the language a page is written in. A public page's comes
// from its URL, where no prefix means the default; an app page and a page
// that does not exist follow the reader: user's saved preference, then the
// timlee.locale cookie, then the default.
func (s *server) pageLocale(r *http.Request, user *render.User) *i18n.Locale {
	b := s.bundle()
	if len(b.Locales()) == 1 {
		return b.Default()
	}
	if loc, fromURL := i18n.FromContext(r.Context()); fromURL {
		return b.For(loc).Locale()
	}
	if !followsReader(r) {
		return b.Default()
	}
	if user != nil {
		if loc, ok := b.ByID(user.Locale); ok {
			return loc
		}
	}
	if loc := s.cookieLocale(r); loc != nil {
		return loc
	}
	return b.Default()
}

// cookieLocale is the shipped locale the timlee.locale cookie names, or nil.
func (s *server) cookieLocale(r *http.Request) *i18n.Locale {
	c, err := r.Cookie(localeCookie)
	if err != nil {
		return nil
	}
	loc, _ := s.bundle().ByID(c.Value)
	return loc
}

func (s *server) setLocaleCookie(w http.ResponseWriter, id string) {
	maxAge := int(localeCookieLifetime.Seconds())
	if id == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     localeCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
}

// unprefixed is p, a path on this site, without its locale prefix:
// "/de/models?q=a" is "/models?q=a", "/de/" is "/".
func (s *server) unprefixed(p string) string {
	route, query := p, ""
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		route, query = p[:i], p[i:]
	}
	seg, rest := splitLocale(route)
	if _, ok := s.bundle().ByID(seg); !ok {
		return p
	}
	if rest == "" {
		rest = "/"
	}
	return rest + query
}

/* ------------------------------------------ the switcher and the banner -- */

// languages is what the language switcher and the suggestion banner show
// (partials/locale-switcher.gohtml, partials/locale-banner.gohtml) and the
// Language select of the profile page.
type languages struct {
	// Options are the shipped locales, each with this page in it.
	Options []languageOption
	// Links is set where the page has a URL in every language, so the
	// switcher is a list of links; an app page has one URL for all.
	Links bool
	// Saved is the language the reader chose, a locale ID, or empty.
	Saved string
	// ReturnTo is this page, for the forms that post to /locale.
	ReturnTo string
	// CSRF is the session's token for those forms, which then also save
	// the choice to the account; empty when signed out.
	CSRF template.HTML
	// Suggest is the suggestion banner, or nil.
	Suggest *languageOffer
}

// languageOption is one choice of the switcher.
type languageOption struct {
	*i18n.Locale
	// Href is this page in that language, where the page has one.
	Href string
	// Current marks the language the page is in.
	Current bool
}

// languageOffer is the suggestion banner: another language than the page's,
// written in the language it offers.
type languageOffer struct {
	*i18n.Locale
	L *i18n.Localizer
	// Keep is the page's own language, the banner's other choice.
	Keep *i18n.Locale
	// Continue words the offer for a reader who chose the language before,
	// rather than one whose browser asks for it.
	Continue bool
}

// localize fills what a page needs once more than one locale is shipped: its
// language versions, for hreflang and the switcher, and the suggestion. It
// also keeps the timlee.locale cookie in step with a signed-in reader's
// saved preference, so the pages that only see the cookie agree.
func (s *server) localize(w http.ResponseWriter, r *http.Request, p *render.Page) {
	b := s.bundle()
	if len(b.Locales()) < 2 || p.L == nil {
		return
	}
	here := p.L.Locale()
	cookie := s.cookieLocale(r)
	var choice *i18n.Locale
	if p.User != nil {
		if pref, ok := b.ByID(p.User.Locale); ok {
			choice = pref
			if pref != cookie {
				s.setLocaleCookie(w, pref.ID)
			}
		} else {
			// Until the account has one, the cookie stands in for it.
			choice = cookie
		}
	}

	links := localizedPath(r.URL.Path)
	langs := &languages{Links: links, ReturnTo: p.Here}
	switch {
	case choice != nil:
		langs.Saved = choice.ID
	case cookie != nil:
		langs.Saved = cookie.ID
	}
	if _, signedIn := sessionCookie(r); signedIn {
		langs.CSRF = csrfInput(csrfToken(r))
	}
	plain := s.unprefixed(p.Here)
	for _, loc := range b.Locales() {
		opt := languageOption{Locale: loc, Current: loc == here}
		if links {
			opt.Href = loc.Localizer().Path(plain)
		}
		langs.Options = append(langs.Options, opt)
	}

	// A public page, as opposed to a 404 or the answer to a form, also has
	// alternates and may offer the reader their language.
	if links && r.Pattern != "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		if p.Alternates == nil {
			// The route's path: a page's own Path may carry its query.
			origin, path := trimSlash(p.Origins.Public), r.URL.Path
			for _, loc := range b.Locales() {
				p.Alternates = append(p.Alternates, render.Alternate{
					Lang: loc.Lang,
					OG:   loc.OG,
					Href: origin + loc.Localizer().Path(path),
				})
			}
			p.Alternates = append(p.Alternates, render.Alternate{Lang: "x-default", Href: origin + path})
		}
		if p.Canonical != "" {
			p.Canonical = trimSlash(p.Origins.Public) + here.Localizer().Path(r.URL.Path)
		}
		w.Header().Add("Vary", "Accept-Language")
		switch {
		case choice != nil:
			if choice != here {
				langs.Suggest = &languageOffer{Locale: choice, L: choice.Localizer(), Keep: here, Continue: true}
			}
		case cookie == nil:
			// The only use of Accept-Language: an offer, never a redirect.
			if loc, confidence := b.Negotiate(r.Header.Get("Accept-Language")); confidence >= language.High && loc != here {
				langs.Suggest = &languageOffer{Locale: loc, L: loc.Localizer(), Keep: here}
			}
		}
	}
	p.Languages = langs
}

/* ----------------------------------------------------------- POST /locale -- */

// setLocale remembers the language a reader chose, in the cookie and, when
// they are signed in, in their account, and sends them back to the page they
// chose it on in that language: a public page's prefix is swapped, an app page
// stays where it is and follows the choice. An empty choice forgets it.
//
// A form, so it works without script; it changes nothing but a preference,
// and like the theme form only this site's pages may post it.
func (s *server) setLocale(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "That form could not be read.", "bad_request")
		return
	}
	b := s.bundle()
	id := r.PostFormValue("locale")
	loc, ok := b.ByID(id)
	if !ok {
		if id != "" {
			httpx.WriteError(w, http.StatusBadRequest, "Unknown language.", "bad_request")
			return
		}
		loc = b.Default()
	}
	s.setLocaleCookie(w, id)

	returnTo := safePath(r.PostFormValue("returnTo"), "/")
	saved := true
	if _, signedIn := sessionCookie(r); signedIn && validToken(r) {
		up := s.auth.upstream(r)
		if err := up.do(r.Context(), http.MethodPatch, "/api/v1/me", map[string]string{"locale": id}, nil); err != nil {
			s.log.Warn("could not save the language to the account", "error", err, "request_id", httpx.IDOf(r))
			saved = false
		}
		up.relay(w)
	}
	// The settings pages report what a form did; no other page reads it.
	if strings.HasPrefix(returnTo, "/settings") {
		if saved {
			s.setFlash(w, flash{Tone: "success", Message: loc.Localizer().T("common.locale.saved")})
		} else {
			s.setFlash(w, flash{Tone: "danger", Message: loc.Localizer().T("common.locale.notSaved")})
		}
	}
	http.Redirect(w, r, loc.Localizer().Path(s.unprefixed(returnTo)), http.StatusSeeOther)
}
