package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/rahulguptaio/timlee/internal/i18n"
)

// Theme is the colour scheme a reader chose.
type Theme string

// The two themes a reader can choose, and System for no choice: the page then
// follows the reader's system setting.
const (
	System Theme = ""
	Light  Theme = "light"
	Dark   Theme = "dark"
)

// ThemeCookie is where a reader's choice is kept.
//
// A cookie rather than localStorage, because only a cookie reaches the server
// in time to put data-theme on the first response. localStorage can only be
// read after the document exists, which is exactly when a flash of the wrong
// theme has already happened.
const ThemeCookie = "timlee.theme"

// ThemeColor is the browser-chrome colour matching a theme.
func (t Theme) ThemeColor() string {
	if t == Dark {
		return "#0b1424"
	}
	return "#ffffff"
}

// Other returns the theme a toggle would switch to.
func (t Theme) Other() Theme {
	if t == Dark {
		return Light
	}
	return Dark
}

// Valid reports whether t is a theme a reader can choose.
func (t Theme) Valid() bool { return t == Light || t == Dark }

// ThemeOf is the theme the reader chose, or System when they have not chosen.
//
// No choice is not guessed at. The stylesheet follows the system setting
// through color-scheme in every engine, before any script runs; the server
// cannot, because WebKit and Gecko never send Sec-CH-Prefers-Color-Scheme
// and Chromium sends it only after a first response. Guessing is what used
// to pin Safari, the macOS app's WKWebView and Firefox to one theme.
func ThemeOf(r *http.Request) Theme {
	if c, err := r.Cookie(ThemeCookie); err == nil {
		if t := Theme(c.Value); t.Valid() {
			return t
		}
	}
	return System
}

// User is the signed-in visitor, in the shape a page needs them.
type User struct {
	ID        string
	Email     string
	Name      string
	AvatarURL string
	// Org and Role describe the membership the session currently has
	// selected; both are empty for a user who belongs to no organisation.
	Org  string
	Role string
	// Locale is the language the reader saved in their account, a locale
	// ID, or empty when they have not chosen one.
	Locale string
	// Allowed is the control plane's answer to "is this account on the
	// TIMLEE_ALLOWED_EMAILS allowlist?". Only allowed users may submit
	// prompts; handlers must check it server-side before accepting one.
	Allowed bool
}

// Display is the name to show, falling back to the local part of the address
// so a user who never set a name is still addressed by something human.
func (u *User) Display() string {
	if u == nil {
		return ""
	}
	if name := strings.TrimSpace(u.Name); name != "" {
		return name
	}
	local, _, _ := strings.Cut(u.Email, "@")
	return local
}

// Initials are the avatar fallback: at most two letters from the display name.
func (u *User) Initials() string {
	if u == nil {
		return ""
	}
	var out []rune
	for _, field := range strings.Fields(u.Display()) {
		for _, r := range field {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// Origins are the three addresses a browser may be sent to. Pages need them
// for links and the server needs them for the policy that allows those links.
type Origins struct {
	Public  string
	Control string
}

// Link is one navigation target.
type Link struct {
	Label string
	Href  string
	// External opens in a new tab and is marked as such.
	External bool
}

// LinkGroup is a titled column of links in the footer.
type LinkGroup struct {
	Title string
	Links []Link
}

// Site is the chrome shared by every page: the links in the header and the
// footer. Supplied per request so the same partials serve a page that lives
// at "/" and one that has to link back to it.
type Site struct {
	Nav    []Link
	Footer []LinkGroup
	// Tagline is the one-line description under the footer logo.
	Tagline string
	// Legal is the copyright line.
	Legal string
}

// Page is what every handler fills and every template receives.
//
// One struct rather than a per-page type because the document shell asks the
// same questions on every route — who is reading, in which theme, under which
// policy — and a shared answer is what keeps the nav honest across pages.
type Page struct {
	// Layout names the template in layouts/ that wraps this page. Empty
	// means DefaultLayout.
	Layout string

	Title       string
	Description string
	// Canonical is the absolute URL of this page.
	Canonical string
	// Robots is the robots meta value; empty omits the tag entirely.
	Robots string
	// OGImage is the absolute URL of the social preview image.
	OGImage string

	// Theme is the reader's choice, or System: only a choice is written to
	// the document, so without one the stylesheet follows the system.
	Theme Theme
	// Nav is the section of the site the page belongs to.
	Nav string
	// BodyClass is set on <body> for surfaces the stylesheet treats
	// differently, e.g. "auth-page".
	BodyClass string

	// User is the signed-in visitor, or nil. The server decides this; no
	// page may decide it in the browser.
	User *User

	// Nonce authorises this response's inline style and JSON-LD under the CSP.
	Nonce     string
	RequestID string

	Origins Origins
	// Site is the header and footer chrome.
	Site Site
	// Path is the request path, used as the return target of the forms that
	// make the page work without JavaScript.
	Path string
	Year int

	// L writes the page's messages and formats its values in the reader's
	// locale: {{ t .L "common.nav.signIn" }}.
	L *i18n.Localizer
	// Here is the path and query the reader asked for, with the locale
	// prefix the URL carried: the return target that keeps the language.
	Here string
	// Alternates are the page's other language versions, for hreflang.
	Alternates []Alternate
	// Languages is what the language switcher and the suggestion banner
	// show (cmd/timlee-web/locale.go); nil while one locale is shipped.
	Languages any

	// JSONLD is pre-encoded structured data. Encoded in Go rather than
	// written into the template so the bytes are guaranteed valid JSON with
	// every HTML-significant character escaped.
	JSONLD template.JS
	// ClientMessages is the CSP-safe #timlee-i18n data block for islands and
	// components loaded by this page.
	ClientMessages template.JS

	// Data is the page's own content.
	Data any
}

// Alternate is one language version of a page.
type Alternate struct {
	// Lang is the hreflang value: a language tag, or "x-default".
	Lang string
	// OG is the Open Graph locale identifier; empty for x-default and
	// pseudo-locales, which have no registered Open Graph locale.
	OG string
	// Href is the absolute URL of that version.
	Href string
}

func (p *Page) layout() string {
	if p.Layout == "" {
		return DefaultLayout
	}
	return p.Layout
}

// SignedIn reports whether a session resolved on the server.
func (p *Page) SignedIn() bool { return p != nil && p.User != nil }

// Allowed reports whether the reader is signed in and on the prompt allowlist.
func (p *Page) Allowed() bool { return p.SignedIn() && p.User.Allowed }

// IsDark reports whether the reader chose the dark theme.
func (p *Page) IsDark() bool { return p != nil && p.Theme == Dark }

// ThemeColor is the browser-chrome colour while the system is in scheme
// ("light" or "dark"): the chosen theme's, or scheme's own without a choice.
func (p *Page) ThemeColor(scheme string) string {
	if p.Theme.Valid() {
		return p.Theme.ThemeColor()
	}
	return Theme(scheme).ThemeColor()
}

// ThemeToggle is what the theme toggle chooses for a reader whose system is
// in scheme ("light" or "dark"): the opposite of the theme they see, or
// "system" when that opposite is the system's own scheme, so that undoing a
// choice follows the system again rather than pinning one.
func (p *Page) ThemeToggle(scheme string) string {
	shown := Theme(scheme)
	if p.Theme.Valid() {
		shown = p.Theme
	}
	if next := shown.Other(); next != Theme(scheme) {
		return string(next)
	}
	return "system"
}

// NextTheme is the opposite of the chosen theme, dark when there is none.
// The site's own toggle uses ThemeToggle, which knows the system's scheme.
func (p *Page) NextTheme() Theme { return p.Theme.Other() }

// ThemeLabel names NextTheme for a screen reader.
func (p *Page) ThemeLabel() string {
	key := i18n.Key("common.theme.switchDark")
	if p.NextTheme() == Light {
		key = "common.theme.switchLight"
	}
	return p.L.T(key)
}

// SignInURL is where a signed-out reader is sent, carrying the page they were
// trying to reach so they come back to it.
func (p *Page) SignInURL() string { return SignInPath(p.Path) }

// SignInPath builds the sign-in URL for a return target.
func SignInPath(returnTo string) string {
	if returnTo == "" || returnTo == "/" {
		return "/sign-in"
	}
	return "/sign-in?returnTo=" + url.QueryEscape(returnTo)
}

// Defaults fills the fields every page shares but no handler should have to
// remember: the request's identity, its nonce, its theme and its origin.
func (p *Page) Defaults(r *http.Request, nonce, requestID string, origins Origins) *Page {
	p.Nonce = nonce
	p.RequestID = requestID
	p.Origins = origins
	p.Path = r.URL.Path
	p.Here = r.URL.RequestURI()
	loc, fromURL := i18n.FromContext(r.Context())
	if fromURL {
		p.Here = "/" + loc.ID + p.Here
	}
	if p.L == nil {
		p.L = loc.Localizer()
	}
	p.Theme = ThemeOf(r)
	p.Year = time.Now().Year()
	if p.Canonical == "" && origins.Public != "" {
		p.Canonical = strings.TrimSuffix(origins.Public, "/") + r.URL.Path
	}
	return p
}

// JSON encodes structured data for a <script type="application/ld+json">
// block.
//
// encoding/json escapes <, > and & by default, so the result cannot close the
// script element that carries it.
func JSON(v any) (template.JS, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("render: encode json-ld: %w", err)
	}
	return template.JS(raw), nil
}
