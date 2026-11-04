package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/appfactory"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/render"
)

// server holds everything a page needs that does not change between requests.
type server struct {
	cfg     config.Config
	log     *slog.Logger
	engine  *render.Engine
	auth    *authClient
	origins render.Origins
	// rootFiles are the unhashed files served at fixed root paths.
	rootFiles map[string]rootFile
	// i18n is the shipped locales and their catalogs; nil serves English.
	i18n *i18n.Bundle
	// apps builds and edits generated apps; nil when the workspace is absent.
	apps *appfactory.Factory
	// previews issues the capability URLs the preview server accepts.
	previews *previewSigner
}

// routes builds the mux. Readiness is registered by the caller, which is the
// only thing that knows when this process is draining.
func (s *server) routes(mux *http.ServeMux, assetHandler http.Handler) {
	mux.Handle("GET /assets/", assetHandler)
	handleRootFiles(mux, s.rootFiles)

	// {$} so this matches the root exactly and leaves everything else to the
	// catch-all below, which answers with a page rather than a bare 404.
	mux.HandleFunc("GET /{$}", s.landing)
	mux.HandleFunc("GET /sign-in", s.signIn)
	mux.HandleFunc("POST /sign-in", s.signInSubmit)
	mux.HandleFunc("GET /sign-in/sent", s.signInSent)
	mux.HandleFunc("GET /sign-in/desktop/start", s.desktopStart)
	mux.HandleFunc("GET /sign-in/desktop/complete", s.desktopComplete)
	mux.HandleFunc("POST /sign-out", s.signOut)
	mux.HandleFunc("POST /theme", s.setTheme)
	mux.HandleFunc("POST /locale", s.setLocale)
	mux.HandleFunc("GET /health", s.health)
	s.settingsRoutes(mux)
	s.appsRoutes(mux)
	mux.HandleFunc("/", s.notFound)
}

// handler wraps the routes in the middleware every response needs. The mux
// comes back with it because readiness is the one route that depends on the
// server which will own it, and that server needs this handler first.
func (s *server) handler(assetHandler http.Handler) (*http.ServeMux, http.Handler) {
	mux := http.NewServeMux()
	s.routes(mux, assetHandler)
	s.bundle().LocalizePaths(localizedPath)

	return mux, httpx.Chain(mux,
		httpx.RequestID(),
		httpx.Logging(s.log),
		httpx.Recover(s.log),
		httpx.SecurityHeaders(httpx.SecurityOptions{
			Production: s.cfg.IsProduction(),
			CSP:        s.pageCSP,
		}),
		httpx.OriginGuard(httpx.OriginGuardOptions{
			Allowed: s.cfg.AllowedOrigins(),
			// Only the forms this site serves change anything, and all of
			// them are cookie-authenticated posts.
			Protect: func(string) bool { return true },
			Logger:  s.log,
		}),
		// Calls to the control plane made while serving a reader carry
		// their address.
		withClientIP,
		// Last, so the logs and the guards above see the URL as requested.
		s.localeRoutes,
	)
}

/* ----------------------------------------------------------- page setup -- */

// page starts a page with everything the shell needs, including the session.
// Resolving it here means no handler can forget to, and no template has to
// ask the browser.
func (s *server) page(r *http.Request) *render.Page {
	var user *render.User
	if s.auth != nil {
		user = s.auth.User(r.Context(), r)
	}
	l := s.pageLocale(r, user).Localizer()
	p := &render.Page{
		User: user,
		L:    l,
		Site: site(l),
	}
	return p.Defaults(r, httpx.NonceOf(r), httpx.IDOf(r), s.origins)
}

// render writes a page, turning a template failure into a plain 500 rather
// than a half-written document.
func (s *server) render(w http.ResponseWriter, r *http.Request, status int, name string, p *render.Page) {
	if p.L == nil {
		p.L = s.pageLocale(r, p.User).Localizer()
	}
	if p.ClientMessages == "" {
		clientMessages, err := render.JSON(p.L.Block(componentClientKeys...))
		if err != nil {
			s.log.Error("could not encode client messages", "page", name, "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, p.L.T("common.error.internal"), "server_error")
			return
		}
		p.ClientMessages = clientMessages
	}
	s.localize(w, r, p)
	w.Header().Set("Content-Language", p.L.Locale().Lang)
	if err := s.engine.Render(w, status, name, p); err != nil {
		s.log.Error("render failed", "page", name, "error", err, "request_id", httpx.IDOf(r))
		logx.Fail(r, err)
		httpx.WriteError(w, http.StatusInternalServerError, p.L.T("common.error.internal"), "server_error")
	}
}

/* -------------------------------------------------------------- landing -- */

// landing is the public home page: what Timlee does and the one way in.
func (s *server) landing(w http.ResponseWriter, r *http.Request) {
	p := s.page(r)
	p.Title = p.L.T("marketing.meta.title")
	p.Description = p.L.T("marketing.meta.description")
	p.Canonical = trimSlash(s.cfg.PublicOrigin) + "/"
	s.render(w, r, http.StatusOK, "landing", p)
}

/* -------------------------------------------------------------- sign-in -- */

type signInData struct {
	Providers  []providerButton
	MagicLink  bool
	DevLogin   bool
	MailOutbox bool
	ReturnTo   string
	Email      string
	Sent       bool
	SentEmail  string
	Error      string
	// EmailError is the problem with the address, shown on the field.
	EmailError string
	// SelfHref returns to the empty form, keeping the return target.
	SelfHref string
	// Desktop is set when the desktop app opened this page; every way in
	// then ends by handing the sign-in to the app (desktop.go).
	Desktop *desktopSignIn
}

type providerButton struct {
	ID    string
	Label string
	Href  string
}

var providerLabels = map[string]string{
	"github":    "GitHub",
	"google":    "Google",
	"microsoft": "Microsoft",
}

func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	returnTo := safePath(r.URL.Query().Get("returnTo"), "/apps")

	p := s.page(r)
	desktop := desktopFromQuery(r.URL.Query())
	// A reader who already has a session has nothing to do here — unless the
	// desktop app sent them, which needs a sign-in of its own.
	if p.SignedIn() && desktop == nil {
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}

	data := signInData{
		ReturnTo: returnTo,
		Desktop:  desktop,
	}
	if key := signInError(r.URL.Query().Get("error")); key != "" {
		data.Error = p.L.T(key)
	}
	s.renderSignIn(w, r, p, data, http.StatusOK)
}

// signInErrors maps the codes the control plane redirects with onto sentences
// a reader can act on.
var signInErrors = map[string]i18n.Key{
	"link_expired":     "auth.callback.linkExpired",
	"access_denied":    "auth.callback.accessDenied",
	"provider_failed":  "auth.callback.providerFailed",
	"mail_unavailable": "auth.failure.mailUnavailable",
	"desktop_flow":     "auth.callback.desktopFlow",
}

func signInError(code string) i18n.Key {
	if code == "" {
		return ""
	}
	if key, ok := signInErrors[code]; ok {
		return key
	}
	return "auth.callback.default"
}

func (s *server) renderSignIn(w http.ResponseWriter, r *http.Request, p *render.Page, data signInData, status int) {
	available, err := s.auth.Providers(r.Context())
	if err != nil {
		s.log.Warn("could not list sign-in providers", "error", err, "request_id", httpx.IDOf(r))
		if data.Error == "" {
			data.Error = p.L.T("auth.failure.unreachable")
		}
	}

	control := trimSlash(s.cfg.ControlOrigin)
	for _, name := range available.Names {
		label := providerLabels[name]
		if label == "" {
			label = strings.ToUpper(name[:1]) + name[1:]
		}
		start := url.Values{"returnTo": {data.ReturnTo}}
		if loc := p.L.Locale(); loc != s.bundle().Default() {
			// The control plane writes its email and its error
			// redirects in the language the reader signed in in.
			start.Set("locale", loc.ID)
		}
		if data.Desktop != nil {
			for key, values := range data.Desktop.query() {
				start[key] = values
			}
		}
		data.Providers = append(data.Providers, providerButton{
			ID:    name,
			Label: label,
			Href:  control + "/auth/" + url.PathEscape(name) + "/start?" + start.Encode(),
		})
	}
	data.MagicLink = available.MagicLink
	data.DevLogin = available.DevLogin
	data.MailOutbox = available.MailOutbox
	data.SelfHref = p.L.SignInPath(data.ReturnTo)
	if data.Desktop != nil {
		self := data.Desktop.query()
		self.Set("returnTo", data.ReturnTo)
		data.SelfHref = p.L.Path("/sign-in") + "?" + self.Encode()
	}

	p.Layout = "auth"
	p.BodyClass = "auth-page"
	p.Robots = "noindex,nofollow"
	p.Title = p.L.T("auth.signIn.documentTitle")
	if data.Sent {
		p.Title = p.L.T("auth.sent.title")
	}
	// A screen reader announces the title first, so it says something failed.
	if data.Error != "" || data.EmailError != "" {
		p.Title = p.L.T("auth.signIn.errorTitle", "title", p.Title)
	}
	p.Description = p.L.T("auth.signIn.description")
	p.Data = data

	s.render(w, r, status, "sign-in", p)
}

// signInSubmit forwards a posted form to the control plane as JSON.
//
// The browser never speaks to the auth API directly here, so sign-in works
// with scripting switched off — and the response's cookies are relayed
// verbatim, so the session the control plane issued is the session the
// browser keeps.
func (s *server) signInSubmit(w http.ResponseWriter, r *http.Request) {
	l := s.localizer(r)
	if err := r.ParseForm(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, l.T("common.form.unreadable"), "bad_request")
		return
	}

	returnTo := safePath(r.PostFormValue("returnTo"), "/apps")
	email := strings.TrimSpace(r.PostFormValue("email"))
	desktop := desktopFromForm(r)
	intent := r.PostFormValue("intent")

	var (
		path    string
		payload map[string]any
	)
	switch intent {
	case "dev":
		path, payload = "/auth/dev", map[string]any{"email": email}
	case "magic-link":
		if problem := emailProblem(email); problem != "" {
			p := s.page(r)
			s.renderSignIn(w, r, p, signInData{
				ReturnTo:   returnTo,
				Email:      email,
				EmailError: p.L.T(problem),
				Desktop:    desktop,
			}, http.StatusUnprocessableEntity)
			return
		}
		path, payload = "/auth/magic-link", map[string]any{"email": email, "returnTo": returnTo}
	default:
		httpx.WriteError(w, http.StatusBadRequest, l.T("auth.signIn.unknownMethod"), "bad_request")
		return
	}
	if desktop != nil {
		payload["desktop"] = desktop.payload()
	}
	if loc := l.Locale(); loc != s.bundle().Default() {
		payload["locale"] = loc.ID
	}

	var answer struct {
		DesktopRedirect string `json:"desktopRedirect"`
	}
	status, cookies, err := s.auth.forward(r.Context(), r, path, payload, &answer)
	for _, c := range cookies {
		http.SetCookie(w, c)
	}

	if err != nil {
		code, message := signInFailure(intent, status)
		if code >= http.StatusInternalServerError {
			s.log.Warn("sign-in failed upstream", "intent", intent, "status", status, "error", err, "request_id", httpx.IDOf(r))
		}
		p := s.page(r)
		s.renderSignIn(w, r, p, signInData{
			ReturnTo: returnTo,
			Email:    email,
			Error:    p.L.T(message),
			Desktop:  desktop,
		}, code)
		return
	}

	if intent == "dev" {
		// For the desktop app, the browser carries the app's code to the
		// app's listener; otherwise the session was issued inline.
		if desktop != nil {
			if !isLoopbackDelivery(answer.DesktopRedirect, desktop) {
				s.log.Error("control plane answered a desktop sign-in without a loopback delivery", "request_id", httpx.IDOf(r))
				httpx.WriteError(w, http.StatusBadGateway, l.T("auth.failure.desktopComplete"), "bad_gateway")
				return
			}
			http.Redirect(w, r, answer.DesktopRedirect, http.StatusFound)
			return
		}
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}

	// Redirect rather than render, so a refresh of the confirmation page
	// does not send a second mail. The address goes with it sealed in a
	// cookie, never in the URL.
	if err := s.sealSent(w, email); err != nil {
		s.log.Warn("could not keep the address for the sent page", "error", err, "request_id", httpx.IDOf(r))
	}
	http.Redirect(w, r, l.Path(sentPath(returnTo, desktop)), http.StatusSeeOther)
}

func (s *server) signOut(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, s.localizer(r).T("common.form.unreadable"), "bad_request")
		return
	}

	_, cookies, err := s.auth.forward(r.Context(), r, "/auth/logout", map[string]string{}, nil)
	for _, c := range cookies {
		http.SetCookie(w, c)
	}
	if err != nil {
		s.log.Warn("sign-out failed upstream", "error", err, "request_id", httpx.IDOf(r))
	}

	http.Redirect(w, r, safePath(r.PostFormValue("returnTo"), "/"), http.StatusSeeOther)
}

/* ---------------------------------------------------------------- theme -- */

// themeCookieLifetime is how long a reader's theme choice is remembered.
const themeCookieLifetime = 365 * 24 * time.Hour

// setTheme records a reader's choice where the server can see it.
//
// A cookie, not localStorage: only a cookie arrives in time to put data-theme
// on the first response, which is what removes the flash the old inline
// script existed to paper over. "system" forgets the choice, so the pages
// follow the system setting again.
func (s *server) setTheme(w http.ResponseWriter, r *http.Request) {
	l := s.localizer(r)
	if err := r.ParseForm(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, l.T("common.form.unreadable"), "bad_request")
		return
	}

	theme := render.Theme(r.PostFormValue("theme"))
	if !theme.Valid() && theme != "system" {
		httpx.WriteError(w, http.StatusBadRequest, l.T("common.theme.unknown"), "bad_request")
		return
	}

	// A preference, not a session.
	maxAge := int(themeCookieLifetime.Seconds())
	if !theme.Valid() {
		theme, maxAge = render.System, -1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     render.ThemeCookie,
		Value:    string(theme),
		Path:     "/",
		MaxAge:   maxAge,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.IsProduction(),
		// Readable by script on purpose: web/assets/ts/theme.ts writes the
		// same cookie when it applies a choice without a round trip.
		HttpOnly: false,
	})

	http.Redirect(w, r, safePath(r.PostFormValue("returnTo"), "/"), http.StatusSeeOther)
}

/* ------------------------------------------------------- health and 404 -- */

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "timlee-web",
		"env":     s.cfg.Env,
		"pages":   s.engine.Pages(),
	})
}

func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	p := s.page(r)
	p.Title = p.L.T("common.error.notFoundDocumentTitle")
	p.Description = p.L.T("common.error.notFoundDescription")
	p.Robots = "noindex,nofollow"
	p.Canonical = ""
	p.Data = map[string]string{
		"Status": "404",
		"Title":  p.L.T("common.error.notFoundTitle"),
		"Body":   p.L.T("common.error.notFoundBody"),
	}
	s.render(w, r, http.StatusNotFound, "error", p)
}

/* ---------------------------------------------------------------- CSP --- */

// pageCSP is the policy for an HTML surface.
//
// Written out rather than inherited so each allowance is justified: scripts
// and styles come from this origin or carry this response's nonce, there is
// no 'unsafe-inline' anywhere, and the only cross-origin destinations are the
// control plane, which sign-in forms and OAuth starts are sent to.
func (s *server) pageCSP(r *http.Request) string {
	nonce := "'nonce-" + httpx.NonceOf(r) + "'"
	control := trimSlash(s.cfg.ControlOrigin)

	directives := []string{
		"default-src 'none'",
		// No base element may retarget this document's relative URLs.
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"object-src 'none'",
		// Sign-in posts here and OAuth starts at the control plane.
		"form-action 'self' " + control,
		"script-src 'self' " + nonce,
		"style-src 'self' " + nonce,
		// data: for the inline document icon; https: for provider avatars,
		// which are served from whichever CDN the identity provider uses.
		"img-src 'self' data: https:",
		"font-src 'self'",
		"connect-src 'self' " + control,
		"manifest-src 'self'",
		// Only the sandboxed preview of a generated app is framed.
		"frame-src " + trimSlash(s.cfg.PreviewOrigin),
	}
	return strings.Join(directives, "; ")
}

/* ------------------------------------------------------------- helpers -- */

// safePath keeps a redirect target inside this site.
//
// Anything absolute, scheme-relative or backslash-prefixed is discarded:
// those are the shapes an open redirect takes, and a sign-in page is exactly
// where one is worth the most to an attacker.
func safePath(candidate, fallback string) string {
	if candidate == "" {
		return fallback
	}
	if !strings.HasPrefix(candidate, "/") ||
		strings.HasPrefix(candidate, "//") ||
		strings.HasPrefix(candidate, "/\\") {
		return fallback
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return fallback
	}
	return parsed.RequestURI()
}
