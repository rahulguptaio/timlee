package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/appfactory"
	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
)

// The apps list and each app's workspace.
//
// Every route here needs a signed-in, allowlisted account: generated code
// runs on this machine. Every form works without scripts (post, redirect,
// re-render); web/assets/ts/workspace.ts layers live progress, a code editor,
// and saving without a page load on top, asking for JSON with Accept.

// maxFileFormBytes leaves room for a full editor file once form-encoded (up to
// three bytes per byte for punctuation and UTF-8) plus the other fields.
const maxFileFormBytes = 3*appfactory.MaxFileSize + 16<<10

// appsRoutes registers the workspace. The factory is nil when the workspace
// is missing, in which case the routes are not served at all.
func (s *server) appsRoutes(mux *http.ServeMux) {
	if s.apps == nil {
		return
	}
	mux.HandleFunc("GET /apps", s.appsList)
	mux.HandleFunc("POST /apps", s.appsAction(maxFormBytes, s.appsCreate))
	mux.HandleFunc("GET /apps/{slug}", s.appWorkspace)
	mux.HandleFunc("POST /apps/{slug}/prompts", s.appsAction(maxFormBytes, s.appPrompt))
	mux.HandleFunc("GET /apps/{slug}/jobs/{job}/events", s.appJobEvents)
	mux.HandleFunc("GET /apps/{slug}/files", s.appFile)
	mux.HandleFunc("POST /apps/{slug}/files", s.appsAction(maxFileFormBytes, s.appSaveFile))
	mux.HandleFunc("POST /apps/{slug}/theme", s.appsAction(maxFormBytes, s.appSaveTheme))
}

/* ---------------------------------------------------------------- guards -- */

// appsPage resolves the page and refuses anyone who may not build apps. It
// returns nil when it has already answered.
func (s *server) appsPage(w http.ResponseWriter, r *http.Request) *render.Page {
	w.Header().Set("Cache-Control", "no-store")
	p := s.page(r)
	if !p.SignedIn() {
		http.Redirect(w, r, p.L.SignInPath(r.URL.RequestURI()), http.StatusSeeOther)
		return nil
	}
	if !p.Allowed() {
		p.Title = p.L.T("apps.denied.title")
		p.Robots = "noindex,nofollow"
		p.Canonical = ""
		p.Data = map[string]string{
			"Status": "403",
			"Title":  p.L.T("apps.denied.title"),
			"Body":   p.L.T("apps.denied.body"),
		}
		s.render(w, r, http.StatusForbidden, "error", p)
		return nil
	}
	p.Robots = "noindex,nofollow"
	p.Canonical = ""
	return p
}

// appsAction is the POST wrapper: same-site origin, CSRF token, then the
// allowlist, in that order, before the handler sees the form.
func (s *server) appsAction(limit int64, h func(http.ResponseWriter, *http.Request, *render.Page)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		back := "/apps"
		if slug := r.PathValue("slug"); appfactory.ValidSlug(slug) {
			back = "/apps/" + slug
		}
		if _, ok := sessionCookie(r); !ok {
			http.Redirect(w, r, render.SignInPath(back), http.StatusSeeOther)
			return
		}
		if !s.fromThisSite(r) {
			s.refuse(w, r, back, "origin")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if err := r.ParseForm(); err != nil {
			s.appError(w, r, s.localizer(r), back, fmt.Errorf("%w: %s", appfactory.ErrInvalid, s.localizer(r).T("common.form.unreadable")))
			return
		}
		if !validToken(r) {
			s.refuse(w, r, back, "token")
			return
		}
		p := s.appsPage(w, r)
		if p == nil {
			return
		}
		h(w, r, p)
	}
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// appError answers a failed action: JSON for the enhanced page, a flash and a
// redirect back for a plain form post.
func (s *server) appError(w http.ResponseWriter, r *http.Request, l *i18n.Localizer, back string, err error) {
	status, key := http.StatusInternalServerError, i18n.Key("common.error.internal")
	message := ""
	switch {
	case errors.Is(err, appfactory.ErrNotFound):
		status, key = http.StatusNotFound, "apps.error.notFound"
	case errors.Is(err, appfactory.ErrBusy):
		status, key = http.StatusConflict, "apps.error.busy"
	case errors.Is(err, appfactory.ErrDirty):
		// Shown as is: only the server's owner can act on it, and the paths
		// tell them what to commit.
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, appfactory.ErrGenerated):
		status, key = http.StatusBadRequest, "apps.error.generated"
	case errors.Is(err, appfactory.ErrInvalid):
		// Validation messages are written for people.
		status, message = http.StatusBadRequest, strings.TrimPrefix(err.Error(), appfactory.ErrInvalid.Error()+": ")
	default:
		s.log.Error("apps action failed", "path", r.URL.Path, "error", err, "request_id", httpx.IDOf(r))
	}
	if message == "" {
		message = l.T(key)
	}
	if wantsJSON(r) {
		writeJSON(w, status, map[string]string{"error": message})
		return
	}
	s.setFlash(w, flash{Tone: "danger", Message: message})
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// noWriteDeadline lifts the server-wide write timeout for responses that
// wait on a build or stream progress.
func noWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
}

/* ------------------------------------------------------------------ list -- */

type appsListData struct {
	Apps  []appfactory.App
	CSRF  template.HTML
	Flash *flash
}

func (s *server) appsList(w http.ResponseWriter, r *http.Request) {
	p := s.appsPage(w, r)
	if p == nil {
		return
	}
	apps, err := s.apps.List()
	if err != nil {
		s.appError(w, r, p.L, "/", err)
		return
	}
	p.Title = p.L.T("apps.meta.title")
	p.Nav = "apps"
	p.Data = appsListData{Apps: apps, CSRF: csrfInput(csrfToken(r)), Flash: s.takeFlash(w, r)}
	s.render(w, r, http.StatusOK, "apps", p)
}

func (s *server) appsCreate(w http.ResponseWriter, r *http.Request, p *render.Page) {
	app, err := s.apps.Create(r.Context(), r.PostFormValue("name"), r.PostFormValue("description"))
	if err != nil {
		s.appError(w, r, p.L, "/apps", err)
		return
	}
	if prompt := strings.TrimSpace(r.PostFormValue("prompt")); prompt != "" {
		if _, err := s.apps.StartPrompt(app.Slug, prompt); err != nil {
			s.appError(w, r, p.L, "/apps/"+app.Slug, err)
			return
		}
	}
	http.Redirect(w, r, "/apps/"+app.Slug, http.StatusSeeOther)
}

/* ------------------------------------------------------------- workspace -- */

type workspaceData struct {
	App        appfactory.App
	View       string
	CSRF       template.HTML
	Flash      *flash
	PreviewURL string
	Busy       bool

	// Prompt view.
	Job      *appfactory.Job
	JobURL   string
	Events   []appfactory.Event
	JobDone  bool
	History  []string
	Messages map[string]string

	// Code view.
	Files     []appfactory.FileEntry
	File      string
	Content   string
	Generated bool
	Exists    bool

	// Theme view.
	Schemes   []string
	Fonts     []appfactory.Font
	ThemeRows []themeRow
	ShapeRows []themeRow
}

// themeRow is one token's control: its current value in each scheme, which is
// the app's override when it has one and the design system's default otherwise.
type themeRow struct {
	Token appfactory.Token
	Light string
	Dark  string
}

func themeRows(c appfactory.Catalog, app appfactory.App) (colors, shape []themeRow) {
	for _, t := range c.Tokens {
		row := themeRow{Token: t, Light: t.Light, Dark: t.Dark}
		if v, ok := app.Tokens.Light[t.Name]; ok {
			row.Light = v
		}
		if v, ok := app.Tokens.Dark[t.Name]; ok {
			row.Dark = v
		}
		if t.Type == "color" {
			colors = append(colors, row)
		} else {
			shape = append(shape, row)
		}
	}
	return colors, shape
}

var workspaceViews = []string{"prompt", "code", "theme"}

func (s *server) appWorkspace(w http.ResponseWriter, r *http.Request) {
	p := s.appsPage(w, r)
	if p == nil {
		return
	}
	slug := r.PathValue("slug")
	app, err := s.apps.Get(slug)
	if err != nil {
		if errors.Is(err, appfactory.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		s.appError(w, r, p.L, "/apps", err)
		return
	}
	view := r.URL.Query().Get("view")
	if !slices.Contains(workspaceViews, view) {
		view = "prompt"
	}
	d := workspaceData{
		App:   app,
		View:  view,
		CSRF:  csrfInput(csrfToken(r)),
		Flash: s.takeFlash(w, r),
		Busy:  s.apps.Busy(slug),
		Messages: map[string]string{
			"saving":         p.L.T("apps.status.saving"),
			"saved":          p.L.T("apps.status.saved"),
			"buildFailed":    p.L.T("apps.status.buildFailed"),
			"copilotChanged": p.L.T("apps.status.copilotChanged"),
			"requestFailed":  p.L.T("apps.status.requestFailed"),
			"finished":       p.L.T("apps.status.finished"),
			"failed":         p.L.T("apps.status.failed"),
		},
	}
	if s.apps.PreviewDir(slug) != "" {
		d.PreviewURL = s.previews.URL(slug, time.Now())
	}

	switch view {
	case "prompt":
		if job := s.apps.Jobs.Latest(slug); job != nil {
			d.Job = job
			d.JobURL = "/apps/" + slug + "/jobs/" + job.ID
			ctx, cancel := context.WithCancel(r.Context())
			cancel() // only what is already there; never wait while rendering
			d.Events, d.JobDone = job.Next(ctx, 0)
		}
		d.History, _ = s.apps.History(r.Context(), slug, 10)
	case "code":
		d.Files, err = s.apps.ListFiles(slug)
		if err != nil {
			s.appError(w, r, p.L, "/apps/"+slug, err)
			return
		}
		d.File = r.URL.Query().Get("file")
		if d.File == "" {
			d.File = "src/App.svelte"
		}
		d.Generated = appfactory.IsGenerated(d.File)
		content, err := s.apps.ReadFile(slug, d.File)
		switch {
		case err == nil:
			d.Content, d.Exists = content, true
		case errors.Is(err, appfactory.ErrNotFound):
			// Opening a path that does not exist yet is how a file is created.
		default:
			d.File = ""
		}
	case "theme":
		catalog, err := s.apps.Catalog()
		if err != nil {
			s.appError(w, r, p.L, "/apps/"+slug, err)
			return
		}
		d.Fonts = catalog.Fonts
		d.Schemes = []string{"light", "dark"}
		d.ThemeRows, d.ShapeRows = themeRows(catalog, app)
	}

	p.Title = app.Name
	p.Nav = "apps"
	p.BodyClass = "workspace-app"
	p.Data = d
	s.render(w, r, http.StatusOK, "app", p)
}

/* ---------------------------------------------------------------- prompt -- */

func (s *server) appPrompt(w http.ResponseWriter, r *http.Request, p *render.Page) {
	slug := r.PathValue("slug")
	job, err := s.apps.StartPrompt(slug, r.PostFormValue("prompt"))
	if err != nil {
		s.appError(w, r, p.L, "/apps/"+slug, err)
		return
	}
	jobURL := "/apps/" + slug + "/jobs/" + job.ID
	if wantsJSON(r) {
		writeJSON(w, http.StatusAccepted, map[string]string{"jobUrl": jobURL})
		return
	}
	s.setFlash(w, flash{Tone: "info", Message: p.L.T("apps.prompt.started")})
	http.Redirect(w, r, "/apps/"+slug, http.StatusSeeOther)
}

// appJobEvents streams a job's progress as Server-Sent Events, starting
// after the index the client already has (?after= or Last-Event-ID), so a
// reconnect never repeats or skips an event.
func (s *server) appJobEvents(w http.ResponseWriter, r *http.Request) {
	p := s.appsPage(w, r)
	if p == nil {
		return
	}
	job := s.apps.Jobs.Get(r.PathValue("slug"), r.PathValue("job"))
	if job == nil {
		http.Error(w, p.L.T("apps.error.notFound"), http.StatusNotFound)
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	if id, err := strconv.Atoi(r.Header.Get("Last-Event-ID")); err == nil {
		after = id + 1
	}
	after = max(after, 0)

	noWriteDeadline(w)
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Send the headers now, so the browser sees the stream open even before
	// the first event.
	if err := rc.Flush(); err != nil {
		return
	}

	ctx := r.Context()
	for {
		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		events, done := job.Next(waitCtx, after)
		cancel()
		if ctx.Err() != nil {
			return
		}
		for _, e := range events {
			raw, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\nevent: progress\ndata: %s\n\n", e.Seq, raw)
			after = e.Seq + 1
		}
		if len(events) == 0 {
			fmt.Fprint(w, ": keepalive\n\n")
		}
		if done {
			fmt.Fprint(w, "event: end\ndata: {}\n\n")
			_ = rc.Flush()
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

/* ------------------------------------------------------------------ code -- */

// appFile returns one file's text for the editor to reload after a prompt.
func (s *server) appFile(w http.ResponseWriter, r *http.Request) {
	p := s.appsPage(w, r)
	if p == nil {
		return
	}
	content, err := s.apps.ReadFile(r.PathValue("slug"), r.URL.Query().Get("path"))
	if err != nil {
		r.Header.Set("Accept", "application/json")
		s.appError(w, r, p.L, "", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

func (s *server) appSaveFile(w http.ResponseWriter, r *http.Request, p *render.Page) {
	slug := r.PathValue("slug")
	path := r.PostFormValue("path")
	back := "/apps/" + slug + "?view=code&file=" + url.QueryEscape(path)
	noWriteDeadline(w)
	// Normalize the editor's line endings; browsers submit textareas with CRLF.
	content := strings.ReplaceAll(r.PostFormValue("content"), "\r\n", "\n")
	build, err := s.apps.SaveFile(r.Context(), slug, path, content)
	s.answerBuild(w, r, p, back, build, err)
}

func (s *server) answerBuild(w http.ResponseWriter, r *http.Request, p *render.Page, back string, build appfactory.BuildResult, err error) {
	if err != nil {
		s.appError(w, r, p.L, back, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"build": build})
		return
	}
	f := flash{Tone: "success", Message: p.L.T("apps.status.saved")}
	if !build.OK {
		f = flash{Tone: "danger", Message: p.L.T("apps.status.buildFailed")}
	}
	s.setFlash(w, f)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

/* ----------------------------------------------------------------- theme -- */

func (s *server) appSaveTheme(w http.ResponseWriter, r *http.Request, p *render.Page) {
	slug := r.PathValue("slug")
	back := "/apps/" + slug + "?view=theme"
	catalog, err := s.apps.Catalog()
	if err != nil {
		s.appError(w, r, p.L, back, err)
		return
	}
	tokens := appfactory.Tokens{Light: map[string]string{}, Dark: map[string]string{}}
	for _, t := range catalog.Tokens {
		// Shape tokens have one control that applies to both schemes.
		light := strings.TrimSpace(r.PostFormValue("light:" + t.Name))
		dark := strings.TrimSpace(r.PostFormValue("dark:" + t.Name))
		if t.Type != "color" {
			dark = light
		}
		if light != "" {
			tokens.Light[t.Name] = light
		}
		if dark != "" {
			tokens.Dark[t.Name] = dark
		}
	}
	noWriteDeadline(w)
	build, err := s.apps.SaveTheme(r.Context(), slug, r.PostFormValue("font"), tokens)
	s.answerBuild(w, r, p, back, build, err)
}
