package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
)

// The account and organisation settings: /settings and its sections.
//
// Every page is complete in its first response and every change is a form
// that posts here; settings_actions.go answers those. What a reader may do is
// decided in this file, from the role the control plane reports, before any
// markup exists — a control that would be refused is not rendered as one.
// The control plane still enforces every rule; this layer only declines to
// offer what it would refuse.

// settingsRoutes registers the account surfaces and the invitation page.
func (s *server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", s.settingsOverview)
	mux.HandleFunc("GET /settings/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/settings", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /settings/profile", s.settingsProfile)
	mux.HandleFunc("GET /settings/security", s.settingsSecurity)
	mux.HandleFunc("GET /settings/organisation", s.settingsOrganisation)
	mux.HandleFunc("GET /settings/members", s.settingsMembers)
	mux.HandleFunc("GET /settings/keys", s.settingsKeys)
	mux.HandleFunc("GET /settings/audit", s.settingsAudit)
	mux.HandleFunc("GET /settings/audit/export.csv", s.settingsAuditExport("csv"))
	mux.HandleFunc("GET /settings/audit/export.json", s.settingsAuditExport("json"))
	mux.HandleFunc("GET /settings/{section}/{rest...}", s.settingsLeftover)

	mux.Handle("POST /settings/org", s.settingsAction("/settings", s.switchOrg))
	mux.Handle("POST /settings/profile", s.settingsAction("/settings/profile", s.updateProfile))
	mux.Handle("POST /settings/security/identities/{id}/unlink", s.settingsAction("/settings/security", s.unlinkIdentity))
	// Where the form lived before sign-in methods moved to Security.
	mux.Handle("POST /settings/profile/identities/{id}/unlink", s.settingsAction("/settings/security", s.unlinkIdentity))
	mux.Handle("POST /settings/security/sessions/end-others", s.settingsAction("/settings/security", s.endOtherSessions))
	mux.Handle("POST /settings/organisation", s.settingsAction("/settings/organisation", s.renameOrg))
	mux.Handle("POST /settings/organisation/leave", s.settingsAction("/settings/organisation", s.leaveOrg))
	mux.Handle("POST /settings/organisation/delete", s.settingsAction("/settings/organisation", s.deleteOrg))
	mux.Handle("POST /settings/keys", s.settingsAction("/settings/keys", s.createKey))
	mux.Handle("POST /settings/keys/{id}/rotate", s.settingsAction("/settings/keys", s.rotateKey))
	mux.Handle("POST /settings/keys/{id}/revoke", s.settingsAction("/settings/keys", s.revokeKey))
	mux.Handle("POST /settings/members/invite", s.settingsAction("/settings/members", s.invite))
	mux.Handle("POST /settings/members/invitations/{id}/revoke", s.settingsAction("/settings/members", s.revokeInvitation))
	mux.Handle("POST /settings/members/{userId}/role", s.settingsAction("/settings/members", s.setRole))
	mux.Handle("POST /settings/members/{userId}/remove", s.settingsAction("/settings/members", s.removeMember))

	mux.HandleFunc("GET /invite.html", s.inviteLegacy)
	mux.HandleFunc("GET /invite", s.invitePage)
	mux.HandleFunc("POST /invite", s.acceptInvite)
}

// settingsSection is one entry in the settings navigation: the reader's own
// account first, then the organisation's.
type settingsSection struct {
	ID    string
	Label i18n.Key
	Href  string
}

var settingsSections = []settingsSection{
	{ID: "overview", Label: "settings.nav.overview", Href: "/settings"},
	{ID: "profile", Label: "settings.nav.profile", Href: "/settings/profile"},
	{ID: "security", Label: "settings.nav.security", Href: "/settings/security"},
	{ID: "organisation", Label: "settings.nav.organisation", Href: "/settings/organisation"},
	{ID: "members", Label: "settings.nav.members", Href: "/settings/members"},
	{ID: "keys", Label: "settings.nav.keys", Href: "/settings/keys"},
	{ID: "audit", Label: "settings.nav.audit", Href: "/settings/audit"},
}

// settingsLink is a section as the navigation renders it.
type settingsLink struct {
	ID      string
	Label   string
	Href    string
	Current bool
}

// settingsShell is what the settings layout needs on every page.
type settingsShell struct {
	Section string
	Nav     []settingsLink
	User    sessionUser
	// Org is the organisation the session acts on, nil for an account that
	// belongs to none.
	Org  *membership
	Orgs []membership
	// Role is the reader's role in Org as the control plane resolved it for
	// this request; the empty role when there is no Org.
	Role      role
	RoleLabel string
	RoleBlurb string
	// CSRF is the hidden input every form on the page carries.
	CSRF  template.HTML
	Flash *flash
	// Problems are what was wrong with the form this page answers, in the
	// order its fields appear; the error summary lists them.
	Problems []problem
	// Path is this page, which the organisation switcher returns to.
	Path           string
	ClientMessages template.JS
}

// CanManage is the one question most of the settings pages ask.
func (s *settingsShell) CanManage() bool { return canManage(s.Role) }

// problem is one thing wrong with a submitted form: the message, and the id
// of the control the error summary links to.
type problem struct {
	Field   string
	Message string
}

// ErrorFor is the summary's message for a field, for the field to repeat.
func (s *settingsShell) ErrorFor(field string) string {
	for _, p := range s.Problems {
		if p.Field == field {
			return p.Message
		}
	}
	return ""
}

// problem adds a field's message to the error summary, if it has one.
func (s *settingsShell) problem(field, message string) {
	if message != "" {
		s.Problems = append(s.Problems, problem{Field: field, Message: message})
	}
}

// contact is someone a reader without access can ask.
type contact struct {
	Name  string
	Email string
}

// settingsReq is one settings request in progress.
type settingsReq struct {
	page    *render.Page
	shell   *settingsShell
	up      *upstream
	session *accountSession
	// org is the active organisation's detail, nil when the account has none
	// or it could not be read; orgErr says which.
	org    *orgDetail
	orgErr error
}

// beginSettings resolves the reader, their organisation and their role, or
// answers the request itself (a redirect to sign in, or an outage page) and
// reports false.
func (s *server) beginSettings(w http.ResponseWriter, r *http.Request, section string) (*settingsReq, bool) {
	// Account pages carry secrets and personal data; nothing may cache them.
	w.Header().Set("Cache-Control", "no-store")

	if _, ok := sessionCookie(r); !ok {
		http.Redirect(w, r, render.SignInPath(r.URL.RequestURI()), http.StatusSeeOther)
		return nil, false
	}

	up := s.auth.upstream(r)
	ctx := r.Context()

	var (
		session *accountSession
		sessErr error
		org     *orgDetail
		orgErr  error
		wg      sync.WaitGroup
	)
	wg.Go(func() { session, sessErr = up.Session(ctx) })
	wg.Go(func() { org, orgErr = up.Org(ctx) })
	wg.Wait()

	if sessErr != nil {
		s.settingsUnavailable(w, r, sessErr)
		return nil, false
	}
	if session.User == nil {
		up.relay(w)
		http.Redirect(w, r, render.SignInPath(r.URL.RequestURI()), http.StatusSeeOther)
		return nil, false
	}

	// The session may still point at an organisation the reader has since
	// been removed from. The control plane's session view already falls back
	// to a live membership, but its org-scoped API keeps refusing the stale
	// one, so pin the session to what the reader can actually see.
	if statusOf(orgErr) == http.StatusForbidden && session.Org != nil {
		if up.SwitchOrg(ctx, session.Org.ID) == nil {
			org, orgErr = up.Org(ctx)
		}
	}

	shell := &settingsShell{
		Section: section,
		User:    *session.User,
		Org:     session.Org,
		Orgs:    session.Orgs,
		CSRF:    csrfInput(csrfToken(r)),
		Flash:   s.takeFlash(w, r),
		Path:    r.URL.Path,
	}
	if org != nil {
		shell.Role = org.Role
		if shell.Org != nil && shell.Org.ID != org.Org.ID {
			// The API is authoritative about which organisation it acted on.
			for i := range session.Orgs {
				if session.Orgs[i].ID == org.Org.ID {
					shell.Org = &session.Orgs[i]
				}
			}
		}
	} else if session.Org != nil && orgErr != nil && statusOf(orgErr) != http.StatusForbidden {
		// The organisation could not be read just now; the session's view
		// of the role is the best available, and every action is still
		// checked by the control plane when it is attempted.
		shell.Role = session.Org.Role
	}
	l := s.localizer(r)
	clientMessages, err := render.JSON(l.Block(joinClientKeys(
		[]i18n.Key{"errors.settings.network"},
		componentClientKeys,
	)...))
	if err != nil {
		s.log.Error("could not encode settings client messages", "error", err)
	}
	shell.ClientMessages = clientMessages
	if shell.Role.valid() {
		shell.RoleLabel = shell.Role.Label(l)
		shell.RoleBlurb = shell.Role.Blurb(l)
	}
	for _, sec := range settingsSections {
		shell.Nav = append(shell.Nav, settingsLink{ID: sec.ID, Label: l.T(sec.Label), Href: sec.Href, Current: sec.ID == section})
	}

	p := &render.Page{
		Layout: "settings",
		Robots: "noindex,nofollow,noarchive",
		Nav:    section,
		L:      l,
		User: &render.User{
			ID:        session.User.ID,
			Email:     session.User.Email,
			Name:      session.User.Name,
			AvatarURL: session.User.AvatarURL,
		},
	}
	if shell.Org != nil {
		p.User.Org = shell.Org.Name
		p.User.Role = string(shell.Role)
	}
	p.Defaults(r, httpx.NonceOf(r), httpx.IDOf(r), s.origins)
	p.Canonical = ""

	return &settingsReq{page: p, shell: shell, up: up, session: session, org: org, orgErr: orgErr}, true
}

// finish relays the control plane's cookies and renders a settings page.
func (s *server) finish(w http.ResponseWriter, r *http.Request, sr *settingsReq, status int, name string, title i18n.Key, data any) {
	sr.page.Title = sr.page.L.T(
		"settings.meta.title",
		"page", i18n.Formatted(sr.page.L.T(title)),
	)
	failed := status == http.StatusUnprocessableEntity || len(sr.shell.Problems) > 0
	if f := sr.shell.Flash; f != nil && f.Tone == "danger" {
		failed = true
	}
	if failed {
		// A reader using a screen reader without scripting hears the title
		// first on a page load; the failure belongs in it.
		sr.page.Title = sr.page.L.T(
			"errors.page.title",
			"title", i18n.Formatted(sr.page.Title),
		)
	}
	sr.page.Description = sr.page.L.T("settings.meta.description")
	sr.page.Data = data
	sr.up.relay(w)
	s.render(w, r, status, name, sr.page)
}

// settingsUnavailable is the page for a control plane that did not answer.
// It is not a sign-in redirect: the reader may well be signed in, and sending
// them to a sign-in page that cannot work either would only mislead.
func (s *server) settingsUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Warn("settings unavailable", "error", err, "request_id", httpx.IDOf(r))
	w.Header().Set("Retry-After", "30")
	n := notice{
		Title: s.localizer(r).T("errors.settings.unavailableTitle"),
		Body:  s.localizer(r).T("errors.settings.unavailableBody"),
		Href:  r.URL.RequestURI(),
		Label: s.localizer(r).T("errors.action.retry"),
	}
	if statusOf(err) == http.StatusTooManyRequests {
		// The control plane answered, just not now: saying it is down would
		// send someone looking for an outage that does not exist.
		w.Header().Set("Retry-After", "60")
		n.Body = s.localizer(r).T("errors.settings.busyBody")
	}
	s.notice(w, r, http.StatusServiceUnavailable, n)
}

// notice is a standalone message page: an outage, or a form that could not
// be verified.
type notice struct {
	Title string
	Body  string
	Href  string
	Label string
}

func (s *server) notice(w http.ResponseWriter, r *http.Request, status int, n notice) {
	w.Header().Set("Cache-Control", "no-store")
	p := &render.Page{
		Layout: "settings-solo",
		Robots: "noindex,nofollow",
		Title:  n.Title + " — Timlee",
		L:      s.localizer(r),
		Data:   n,
	}
	p.Defaults(r, httpx.NonceOf(r), httpx.IDOf(r), s.origins)
	p.Canonical = ""
	s.render(w, r, status, "settings-notice", p)
}

/* ------------------------------------------------------------- shared -- */

/* -------------------------------------------------------------- tables -- */

// tablePageSize is how many rows a settings table lists before it pages.
const tablePageSize = 50

// sortColumn is a column a table can be sorted by. First is the direction
// the first sort goes in: names A to Z, dates newest first.
type sortColumn struct {
	ID    string
	Label i18n.Key
	First string
}

// sortHeader is a sortable column's header: a link that sorts the table by
// it, and the aria-sort it carries when the table is sorted by it. Action
// says, to a screen reader, what following the link does.
type sortHeader struct {
	Label  string
	Href   string
	Sort   string
	Action string
}

// tableSort reads ?sort= and ?dir= against a table's columns: an unknown
// column is no sort, and an unknown direction is the column's first.
func tableSort(query url.Values, columns []sortColumn) (column, dir string) {
	for _, c := range columns {
		if c.ID != query.Get("sort") {
			continue
		}
		dir = query.Get("dir")
		if dir != "asc" && dir != "desc" {
			dir = c.First
		}
		return c.ID, dir
	}
	return "", ""
}

// sortHeaders are a table's sortable headers by column. href is the table
// sorted by a column in a direction, with its filters kept.
func sortHeaders(l *i18n.Localizer, columns []sortColumn, column, dir string, href func(column, dir string) string) map[string]sortHeader {
	headers := map[string]sortHeader{}
	for _, c := range columns {
		h := sortHeader{Label: l.T(c.Label)}
		next := c.First
		if c.ID == column {
			h.Sort, next = "ascending", "desc"
			if dir == "desc" {
				h.Sort, next = "descending", "asc"
			}
		}
		h.Href = href(c.ID, next)
		h.Action = l.T("settings.table.sortAscending")
		if next == "desc" {
			h.Action = l.T("settings.table.sortDescending")
		}
		headers[c.ID] = h
	}
	return headers
}

// paging is one page of a table: the rows shown, numbered from 1, and links
// to the others.
type paging struct {
	From, To   int
	Pages      []pageLink
	Prev, Next string
}

// pageOf cuts n rows to the page ?page= asks for, tablePageSize at a time.
// start and end index the rows to show; href is the table at a page.
func pageOf(query url.Values, n int, href func(page int) string) (start, end int, p paging) {
	pages := max(1, (n+tablePageSize-1)/tablePageSize)
	page, _ := strconv.Atoi(query.Get("page"))
	page = min(max(page, 1), pages)
	start = (page - 1) * tablePageSize
	end = min(start+tablePageSize, n)
	if start < end {
		p.From, p.To = start+1, end
	}
	if pages > 1 {
		p.Pages = pageLinks(page, pages, href)
		if page > 1 {
			p.Prev = href(page - 1)
		}
		if page < pages {
			p.Next = href(page + 1)
		}
	}
	return start, end, p
}

// stamp is one moment, in every form a template may want it.
type stamp struct {
	ISO  string
	Date i18n.Formatted
	Full i18n.Formatted
	Ago  i18n.Formatted
}

func stampOf(l *i18n.Localizer, ms int64, now time.Time) stamp {
	t := time.UnixMilli(ms).UTC()
	return stamp{
		ISO:  t.Format(time.RFC3339),
		Date: i18n.Formatted(l.Date(t, i18n.DateMedium)),
		Full: i18n.Formatted(l.DateTime(t, i18n.DateMedium)),
		Ago:  i18n.Formatted(l.Relative(t, now)),
	}
}

// initialsOf matches the avatar component's own fallback — first letters of
// the first and last word — so a server-rendered avatar and an upgraded one
// never disagree.
func initialsOf(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return "?"
	}
	first := []rune(words[0])
	out := string(unicode.ToUpper(first[0]))
	if len(words) > 1 {
		last := []rune(words[len(words)-1])
		out += string(unicode.ToUpper(last[0]))
	}
	return out
}

// roleOption is one choice in a role picker.
type roleOption struct {
	Value    role
	Label    string
	Blurb    string
	Selected bool
}

// grantable lists the roles an actor may hand out, weakest first.
func grantable(l *i18n.Localizer, actor, selected role) []roleOption {
	var out []roleOption
	for _, candidate := range roles {
		if !canGrant(actor, candidate) {
			continue
		}
		out = append(out, roleOption{
			Value: candidate, Label: candidate.Label(l), Blurb: candidate.Blurb(l),
			Selected: candidate == selected,
		})
	}
	return out
}

// adminsOf lists who can manage an organisation, for a reader who cannot.
func adminsOf(org *orgDetail) []contact {
	if org == nil {
		return nil
	}
	var out []contact
	for _, m := range org.Members {
		if canManage(m.Role) {
			out = append(out, contact{Name: m.display(), Email: m.Email})
		}
	}
	return out
}

// loadFailure turns a failed read into a sentence for the page.
func loadFailure(l *i18n.Localizer, what i18n.Key, err error) string {
	if errors.Is(err, errUnreachable) {
		return l.T(
			"errors.settings.loadUnreachable",
			"what", i18n.Formatted(l.T(what)),
		)
	}
	return l.T(
		"errors.settings.loadFailed",
		"what", i18n.Formatted(l.T(what)),
	)
}

/* ------------------------------------------------------------ overview -- */

type overviewData struct {
	Shell       *settingsShell
	Initials    string
	MemberCount int
	// ActiveKeys still work; ExpiringKeys of them expire within
	// expiringWithin.
	ActiveKeys   int
	ExpiringKeys int
	KeysKnown    bool
	Pending      int
	Admins       []contact
}

func (s *server) settingsOverview(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.beginSettings(w, r, "overview")
	if !ok {
		return
	}
	data := overviewData{
		Shell:    sr.shell,
		Initials: initialsOf(displayName(sr.shell.User)),
		Admins:   adminsOf(sr.org),
	}
	if sr.org != nil {
		data.MemberCount = len(sr.org.Members)
	}

	if sr.shell.CanManage() {
		ctx := r.Context()
		var wg sync.WaitGroup
		wg.Go(func() {
			keys, err := sr.up.Keys(ctx)
			if err != nil {
				return
			}
			data.KeysKnown = true
			now := time.Now()
			for _, k := range keys {
				switch keyState(k, now) {
				case "revoked", "expired":
				case "expiring":
					data.ActiveKeys++
					data.ExpiringKeys++
				default:
					data.ActiveKeys++
				}
			}
		})
		wg.Go(func() {
			if invites, err := sr.up.Invitations(ctx); err == nil {
				data.Pending = len(invites)
			}
		})
		wg.Wait()
	}

	s.finish(w, r, sr, http.StatusOK, "settings", "settings.overview.title", data)
}

func displayName(u sessionUser) string {
	if name := strings.TrimSpace(u.Name); name != "" {
		return name
	}
	local, _, _ := strings.Cut(u.Email, "@")
	return local
}

/* ------------------------------------------------------------- profile -- */

type profileForm struct {
	Name  string
	Error string
}

type identityRow struct {
	ID         string
	Provider   string
	Email      string
	Linked     stamp
	LastUsed   *stamp
	Confirming bool
}

type profileData struct {
	Shell *settingsShell
	Form  profileForm
}

type accountSecurityData struct {
	Shell      *settingsShell
	Identities []identityRow
	// OnlyOne means unlinking is impossible: it would lock the account out.
	OnlyOne bool
	// Others counts the account's other sessions; SessionsKnown is false
	// when they could not be counted.
	Others        int
	SessionsKnown bool
}

// providerLabel names a sign-in method. "email" covers both the mailed link
// and development sign-in, which the control plane records the same way.
func providerLabel(l *i18n.Localizer, name string) string {
	if name == "email" {
		return l.T("settings.security.providerEmail")
	}
	if label, ok := providerLabels[name]; ok {
		return label
	}
	if name == "" {
		return l.T("settings.security.providerUnknown")
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func (s *server) settingsProfile(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.beginSettings(w, r, "profile")
	if !ok {
		return
	}
	s.renderProfile(w, r, sr, profileForm{Name: sr.shell.User.Name}, http.StatusOK)
}

func (s *server) renderProfile(w http.ResponseWriter, r *http.Request, sr *settingsReq, form profileForm, status int) {
	sr.shell.problem("pf-name-input", form.Error)
	s.finish(w, r, sr, status, "settings-profile", "settings.profile.title", profileData{Shell: sr.shell, Form: form})
}

/* ------------------------------------------------------------ security -- */

func (s *server) settingsSecurity(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.beginSettings(w, r, "security")
	if !ok {
		return
	}
	now := time.Now()
	confirm := r.URL.Query().Get("unlink")

	data := accountSecurityData{Shell: sr.shell, OnlyOne: len(sr.session.Identities) <= 1}
	for _, id := range sr.session.Identities {
		row := identityRow{
			ID:         id.ID,
			Provider:   providerLabel(sr.page.L, id.Provider),
			Email:      id.Email,
			Linked:     stampOf(sr.page.L, id.CreatedAt, now),
			Confirming: !data.OnlyOne && id.ID == confirm,
		}
		if id.LastUsedAt != nil {
			used := stampOf(sr.page.L, *id.LastUsedAt, now)
			row.LastUsed = &used
		}
		data.Identities = append(data.Identities, row)
	}
	others, err := sr.up.OtherSessions(r.Context())
	if err != nil {
		s.log.Warn("could not count sessions", "error", err, "request_id", httpx.IDOf(r))
	}
	data.Others, data.SessionsKnown = others, err == nil

	s.finish(w, r, sr, http.StatusOK, "settings-security", "settings.security.title", data)
}

/* -------------------------------------------------------- organisation -- */

// maxOrgName is the control plane's limit on an organisation's name, in
// characters.
const maxOrgName = 80

type orgForm struct {
	Name, NameError       string
	Confirm, ConfirmError string
}

type organisationData struct {
	Shell *settingsShell
	Form  orgForm
	// SoleOwner is true when the reader is the organisation's only owner,
	// who cannot leave it.
	SoleOwner bool
	IsOwner   bool
	Leaving   bool
	// Keys are the reader's own working keys, which leaving revokes;
	// KeysKnown is false when they could not be read, or the reader may not
	// list keys.
	Keys      []memberKey
	KeysKnown bool
	// Members and LiveKeys are what deleting the organisation takes away.
	Members   int
	LiveKeys  int
	LoadError string
	Admins    []contact
}

func (s *server) settingsOrganisation(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.beginSettings(w, r, "organisation")
	if !ok {
		return
	}
	s.renderOrganisation(w, r, sr, orgForm{}, http.StatusOK)
}

func (s *server) renderOrganisation(w http.ResponseWriter, r *http.Request, sr *settingsReq, form orgForm, status int) {
	data := organisationData{
		Shell:   sr.shell,
		Form:    form,
		IsOwner: sr.shell.Role == roleOwner,
		Leaving: r.URL.Query().Get("leave") == "1",
		Admins:  adminsOf(sr.org),
	}
	if data.Form.Name == "" && sr.shell.Org != nil {
		data.Form.Name = sr.shell.Org.Name
	}
	sr.shell.problem("org-name", form.NameError)
	sr.shell.problem("org-delete-confirm", form.ConfirmError)
	if sr.org == nil && sr.shell.Org != nil {
		data.LoadError = sr.page.L.T("settings.organisation.loadFailed")
		if errors.Is(sr.orgErr, errUnreachable) {
			data.LoadError = sr.page.L.T("settings.organisation.loadUnreachable")
		}
	}
	if sr.org != nil {
		owners := 0
		for _, m := range sr.org.Members {
			if m.Role == roleOwner {
				owners++
			}
		}
		data.Members = len(sr.org.Members)
		data.SoleOwner = data.IsOwner && owners == 1
	}

	if sr.shell.CanManage() {
		now := time.Now()
		keys, err := sr.up.Keys(r.Context())
		data.KeysKnown = err == nil
		for _, k := range keys {
			if state := keyState(k, now); state == "revoked" || state == "expired" {
				continue
			}
			data.LiveKeys++
			if k.CreatedBy != sr.shell.User.ID {
				continue
			}
			mk := memberKey{Label: k.Label, Prefix: k.Prefix}
			if k.LastUsedAt != nil {
				used := stampOf(sr.page.L, *k.LastUsedAt, now)
				mk.LastUsed = &used
			}
			data.Keys = append(data.Keys, mk)
		}
	}

	s.finish(w, r, sr, status, "settings-organisation", "settings.organisation.title", data)
}

/* ---------------------------------------------------------------- keys -- */

// keyLifetimes are the lifetimes a new key can be given, in days. The form
// offers "never" beside them, as a choice that has to be made on purpose.
var keyLifetimes = []struct {
	Days  int
	Label i18n.Key
}{
	{7, "settings.keys.lifetime.week"},
	{30, "settings.keys.lifetime.month"},
	{90, "settings.keys.lifetime.quarter"},
	{365, "settings.keys.lifetime.year"},
}

// defaultKeyLifetime is the choice a new key starts with: time to set up and
// deploy, short enough that a forgotten key does not outlive its purpose.
const defaultKeyLifetime = "30"

// keyOverlaps are how long a rotated key keeps working beside its
// replacement, in hours. 0 revokes it at once.
var keyOverlaps = []struct {
	Hours int
	Label i18n.Key
}{
	{1, "settings.keys.overlap.hour"},
	{24, "settings.keys.overlap.day"},
	{168, "settings.keys.overlap.week"},
	{0, "settings.keys.overlap.none"},
}

const defaultKeyOverlap = "24"

// expiringWithin is how close to its expiry a key is flagged for attention.
const expiringWithin = 7 * 24 * time.Hour

// keyLifetime reads the form's choice of lifetime: days, or nil for a key
// that never expires. ok is false for anything the form does not offer.
func keyLifetime(value string) (days *int, ok bool) {
	if value == "never" {
		return nil, true
	}
	for _, l := range keyLifetimes {
		if strconv.Itoa(l.Days) == value {
			d := l.Days
			return &d, true
		}
	}
	return nil, false
}

type keyForm struct {
	Label        string
	Expires      string
	Error        string
	ExpiresError string
}

// choice is one radio in a group the server renders.
type choice struct {
	Value    string
	Label    string
	Hint     string
	Selected bool
}

// keyRow is one key as the table shows it. State is where the key is in its
// life: active, expiring (within expiringWithin), replaced (rotated, and
// working until its overlap ends), expired or revoked.
type keyRow struct {
	ID       string
	Label    string
	Prefix   string
	Creator  string
	Created  stamp
	LastUsed *stamp
	Expires  *stamp
	Revoked  *stamp
	State    string
	// Replaces is the prefix of the key this one was rotated from.
	Replaces   string
	Confirming bool
	Rotating   bool

	// What the table sorts by: epoch milliseconds, 0 for never used, and
	// an expiry of 0 for a key that never expires.
	createdAt, usedAt, expiresAt int64
}

// keyColumns are the keys table's sortable columns.
var keyColumns = []sortColumn{
	{"label", "settings.keys.col.label", "asc"},
	{"created", "settings.keys.col.created", "desc"},
	{"used", "settings.keys.col.used", "desc"},
	{"expires", "settings.keys.col.expires", "asc"},
}

// sortKeys orders key rows by a column. Ties, and the unsorted table, go
// newest first; a key that never expires sorts after every date.
func sortKeys(rows []keyRow, column, dir string) {
	by := func(a, b keyRow) int {
		switch column {
		case "label":
			return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
		case "used":
			return cmp.Compare(a.usedAt, b.usedAt)
		case "expires":
			never := func(k keyRow) int64 {
				if k.expiresAt == 0 {
					return math.MaxInt64
				}
				return k.expiresAt
			}
			return cmp.Compare(never(a), never(b))
		}
		return cmp.Compare(a.createdAt, b.createdAt)
	}
	slices.SortStableFunc(rows, func(a, b keyRow) int {
		order := by(a, b)
		if dir == "desc" {
			order = -order
		}
		return cmp.Or(order, cmp.Compare(b.createdAt, a.createdAt))
	})
}

// Live reports whether the key is still accepted.
func (k keyRow) Live() bool { return k.State != "expired" && k.State != "revoked" }

// Rotatable reports whether the key can be handed over to a replacement:
// live, and not already replaced.
func (k keyRow) Rotatable() bool { return k.State == "active" || k.State == "expiring" }

func keyState(k apiKey, now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return "revoked"
	case k.ExpiresAt != nil && *k.ExpiresAt <= now.UnixMilli():
		return "expired"
	case k.ReplacedBy != "":
		return "replaced"
	case k.ExpiresAt != nil && time.UnixMilli(*k.ExpiresAt).Sub(now) < expiringWithin:
		return "expiring"
	}
	return "active"
}

// keyStatuses are the table's filters, in the order offered. "active" means
// every key still accepted.
var keyStatuses = []struct {
	ID    string
	Label i18n.Key
}{
	{"", "settings.keys.filter.all"},
	{"active", "settings.keys.filter.active"},
	{"expiring", "settings.keys.filter.expiring"},
	{"expired", "settings.keys.filter.expired"},
	{"revoked", "settings.keys.filter.revoked"},
}

func (k keyRow) is(status string) bool {
	switch status {
	case "":
		return true
	case "active":
		return k.Live()
	}
	return k.State == status
}

// filterLink is one filter of a list, as a link to the list filtered.
type filterLink struct {
	Label   string
	Count   int
	Href    string
	Current bool
}

type keysData struct {
	Shell     *settingsShell
	Reveal    *revealedKey
	Form      keyForm
	Lifetimes []choice
	Overlaps  []choice
	Keys      []keyRow
	Filters   []filterLink
	// Total is how many keys the organisation has, of which Live still
	// work; Keys holds those the filters let through. Creator names whose
	// keys a createdBy filter shows.
	Total     int
	Live      int
	Expired   int
	Revoked   int
	Filtered  bool
	Creator   string
	LoadError string
	Admins    []contact
	// Headers are the sortable columns' headers; Shown counts the keys the
	// filters let through, of which the page lists Paging's.
	Headers map[string]sortHeader
	Shown   int
	Paging  paging
}

func (s *server) settingsKeys(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.beginSettings(w, r, "keys")
	if !ok {
		return
	}
	s.renderKeys(w, r, sr, keyForm{}, http.StatusOK)
}

func (s *server) renderKeys(w http.ResponseWriter, r *http.Request, sr *settingsReq, form keyForm, status int) {
	now := time.Now()
	l := sr.page.L
	if form.Expires == "" {
		form.Expires = defaultKeyLifetime
	}
	data := keysData{
		Shell:  sr.shell,
		Form:   form,
		Admins: adminsOf(sr.org),
	}
	for _, lifetime := range keyLifetimes {
		value := strconv.Itoa(lifetime.Days)
		data.Lifetimes = append(data.Lifetimes, choice{
			Value: value, Label: l.T(lifetime.Label), Selected: value == form.Expires,
			Hint: l.Date(now.Add(time.Duration(lifetime.Days)*24*time.Hour).UTC(), i18n.DateMedium),
		})
	}
	data.Lifetimes = append(data.Lifetimes, choice{
		Value: "never", Label: l.T("settings.keys.lifetime.never"), Selected: form.Expires == "never",
		Hint: l.T("settings.keys.lifetimeNeverHint"),
	})
	for _, o := range keyOverlaps {
		value := strconv.Itoa(o.Hours)
		data.Overlaps = append(data.Overlaps, choice{Value: value, Label: l.T(o.Label), Selected: value == defaultKeyOverlap})
	}
	sr.shell.problem("key-label", form.Error)
	sr.shell.problem("key-expires-input-"+data.Lifetimes[0].Value, form.ExpiresError)

	// Read before anything else can fail: the secret is cleared by this
	// response, so it must be shown by this response.
	data.Reveal = s.takeReveal(w, r)

	if sr.shell.CanManage() {
		keys, err := sr.up.Keys(r.Context())
		if err != nil {
			s.log.Warn("could not list keys", "error", err, "request_id", httpx.IDOf(r))
			data.LoadError = loadFailure(l, "settings.keys.loadSubject", err)
		}
		fillKeyRows(&data, l, r.URL.Query(), keys, memberNames(sr.org), now)
	}

	s.finish(w, r, sr, status, "settings-keys", "settings.keys.title", data)
}

// fillKeyRows fills in the table: every key's row and the filters' counts,
// then the rows the query's filters let through, live keys first.
func fillKeyRows(data *keysData, l *i18n.Localizer, query url.Values, keys []apiKey, names map[string]string, now time.Time) {
	replaces := map[string]string{}
	for _, k := range keys {
		if k.ReplacedBy != "" {
			replaces[k.ReplacedBy] = k.Prefix
		}
	}

	creator := query.Get("createdBy")
	if _, known := names[creator]; creator != "" && !known {
		creator = ""
	}
	statusFilter := query.Get("status")
	known := false
	for _, st := range keyStatuses {
		known = known || st.ID == statusFilter
	}
	if !known {
		statusFilter = ""
	}

	var rows []keyRow
	for _, k := range keys {
		if creator != "" && k.CreatedBy != creator {
			continue
		}
		row := keyRow{
			ID:        k.ID,
			Label:     k.Label,
			Prefix:    k.Prefix,
			Creator:   names[k.CreatedBy],
			Created:   stampOf(l, k.CreatedAt, now),
			State:     keyState(k, now),
			Replaces:  replaces[k.ID],
			createdAt: k.CreatedAt,
		}
		if k.LastUsedAt != nil {
			row.usedAt = *k.LastUsedAt
		}
		if k.ExpiresAt != nil {
			row.expiresAt = *k.ExpiresAt
		}
		if row.Creator == "" {
			row.Creator = l.T("settings.keys.formerMember")
		}
		if k.LastUsedAt != nil {
			used := stampOf(l, *k.LastUsedAt, now)
			row.LastUsed = &used
		}
		if k.ExpiresAt != nil {
			expires := stampOf(l, *k.ExpiresAt, now)
			row.Expires = &expires
		}
		if k.RevokedAt != nil {
			revoked := stampOf(l, *k.RevokedAt, now)
			row.Revoked = &revoked
		}
		row.Confirming = row.Live() && query.Get("revoke") == k.ID
		row.Rotating = row.Rotatable() && query.Get("rotate") == k.ID
		rows = append(rows, row)
	}

	column, dir := tableSort(query, keyColumns)
	keysHref := func(status, column, dir string, page int) string {
		q := url.Values{}
		for name, value := range map[string]string{"status": status, "createdBy": creator, "sort": column, "dir": dir} {
			if value != "" {
				q.Set(name, value)
			}
		}
		if page > 1 {
			q.Set("page", strconv.Itoa(page))
		}
		return auditHref("/settings/keys", q)
	}
	link := func(st string) string { return keysHref(st, column, dir, 1) }
	for _, st := range keyStatuses {
		count := 0
		for _, row := range rows {
			if row.is(st.ID) {
				count++
			}
		}
		// A filter that would show nothing is not offered, unless it is the
		// one in force.
		if count == 0 && st.ID != "" && st.ID != statusFilter {
			continue
		}
		data.Filters = append(data.Filters, filterLink{Label: l.T(st.Label), Count: count, Href: link(st.ID), Current: st.ID == statusFilter})
	}

	data.Total = len(rows)
	for _, row := range rows {
		switch {
		case row.Live():
			data.Live++
		case row.State == "expired":
			data.Expired++
		default:
			data.Revoked++
		}
		if row.is(statusFilter) {
			data.Keys = append(data.Keys, row)
		}
	}
	data.Filtered = statusFilter != "" || creator != ""
	if creator != "" {
		data.Creator = names[creator]
	}
	if column != "" {
		sortKeys(data.Keys, column, dir)
	} else {
		// Live keys first; each group stays newest first, as listed.
		sort.SliceStable(data.Keys, func(i, j int) bool {
			return data.Keys[i].Live() && !data.Keys[j].Live()
		})
	}
	data.Headers = sortHeaders(l, keyColumns, column, dir, func(c, d string) string { return keysHref(statusFilter, c, d, 1) })
	data.Shown = len(data.Keys)
	start, end, page := pageOf(query, len(data.Keys), func(n int) string { return keysHref(statusFilter, column, dir, n) })
	data.Keys, data.Paging = data.Keys[start:end], page
}

// memberNames maps each member's id to the name the pages show for them.
func memberNames(org *orgDetail) map[string]string {
	names := map[string]string{}
	if org != nil {
		for _, m := range org.Members {
			names[m.ID] = m.display()
		}
	}
	return names
}

/* ------------------------------------------------------------- members -- */

type inviteForm struct {
	Email      string
	Role       role
	EmailError string
	RoleError  string
}

type memberRow struct {
	ID        string
	Name      string
	Email     string
	Initials  string
	AvatarURL string
	Role      role
	RoleLabel string
	Joined    stamp
	IsSelf    bool
	// Options is non-empty exactly when the reader may change this role.
	Options []roleOption
	// CanRemove is whether the reader may remove this member.
	CanRemove bool
	// SoleOwner marks the one owner an organisation cannot lose.
	SoleOwner  bool
	Confirming bool
	// Keys are the API keys they created that still work: removing them
	// revokes these. KeysKnown is false when the keys could not be read, and
	// the confirmation can only say so in general.
	Keys       []memberKey
	KeysKnown  bool
	RecentKeys int

	joinedAt int64
}

// memberColumns are the members table's sortable columns. Role sorts the
// strongest first.
var memberColumns = []sortColumn{
	{"name", "settings.members.col.name", "asc"},
	{"role", "settings.members.col.role", "desc"},
	{"joined", "settings.members.col.joined", "desc"},
}

func sortMembers(rows []memberRow, column, dir string) {
	slices.SortStableFunc(rows, func(a, b memberRow) int {
		var order int
		switch column {
		case "name":
			order = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case "role":
			order = cmp.Compare(roleRank[a.Role], roleRank[b.Role])
		default:
			order = cmp.Compare(a.joinedAt, b.joinedAt)
		}
		if dir == "desc" {
			order = -order
		}
		return cmp.Or(order, strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
}

// roleConfirm is the question a role change asks first, when it is one a
// person cannot undo alone: a reader giving up their own management, a new
// owner, or an owner demoted. It is asked on the members page, as an open
// confirmation the server renders.
type roleConfirm struct {
	ID, Name      string
	To            role
	Self          bool
	Title, Body   string
	Confirm, Keep string
	Danger        bool
}

// needsConfirm reports whether changing a role from one to the other asks
// first.
func needsConfirm(self bool, from, to role) bool {
	switch {
	case from == to:
		return false
	case self && !canManage(to):
		return true
	case to == roleOwner, from == roleOwner:
		return true
	}
	return false
}

// confirmRole words the question for one change.
func confirmRole(l *i18n.Localizer, org string, m memberRow, to role) roleConfirm {
	c := roleConfirm{ID: m.ID, Name: m.Name, To: to, Self: m.IsSelf, Keep: l.T("settings.security.cancel")}
	switch {
	case m.IsSelf && !canManage(to):
		c.Title = l.T("settings.members.confirmSelfTitle", "role", i18n.Formatted(to.Label(l)))
		c.Body = l.T("settings.members.confirmSelfBody", "org", org)
		c.Confirm = l.T("settings.members.confirmSelfButton", "role", i18n.Formatted(to.Label(l)))
		c.Keep, c.Danger = l.T("settings.members.keepRole"), true
	case to == roleOwner:
		c.Title = l.T("settings.members.confirmOwnerTitle", "name", m.Name)
		c.Body = l.T("settings.members.confirmOwnerBody", "name", m.Name, "org", org)
		c.Confirm = l.T("settings.members.confirmOwnerButton", "name", m.Name)
	case m.IsSelf:
		c.Title = l.T("settings.members.confirmSelfTitle", "role", i18n.Formatted(to.Label(l)))
		c.Body = l.T("settings.members.confirmSelfOwnerBody", "org", org)
		c.Confirm = l.T("settings.members.confirmSelfButton", "role", i18n.Formatted(to.Label(l)))
		c.Keep, c.Danger = l.T("settings.members.keepRole"), true
	default:
		c.Title = l.T(
			"settings.members.confirmDemoteTitle",
			"name", m.Name,
			"role", i18n.Formatted(to.Label(l)),
		)
		c.Body = l.T("settings.members.confirmDemoteBody", "name", m.Name)
		c.Confirm = l.T("settings.members.confirmDemoteButton", "role", i18n.Formatted(to.Label(l)))
		c.Danger = true
	}
	return c
}

// memberKey is one of a member's working API keys, as the removal
// confirmation lists it.
type memberKey struct {
	Label    string
	Prefix   string
	LastUsed *stamp
}

type invitationRow struct {
	ID        string
	Email     string
	Role      role
	RoleLabel string
	InvitedBy string
	Sent      stamp
	Expires   stamp
	Expired   bool
	// CanResend is whether the reader may grant this invitation's role,
	// which re-sending it does, and the server can send mail to do it.
	CanResend  bool
	Confirming bool
}

type membersData struct {
	Shell       *settingsShell
	Members     []memberRow
	Owners      int
	LoadError   string
	Form        inviteForm
	RoleChoices []roleOption
	// MailOff is true when the control plane reported it cannot send mail,
	// which is the only way an invitation reaches anyone.
	MailOff     bool
	Invitations []invitationRow
	InviteError string
	Admins      []contact
	// Headers are the members table's sortable headers; Paging is the page
	// of members listed, of len(all).
	Headers map[string]sortHeader
	Paging  paging
	Count   int
	// Confirm is a role change waiting for its confirmation, if any.
	Confirm *roleConfirm
}

func (s *server) settingsMembers(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.beginSettings(w, r, "members")
	if !ok {
		return
	}
	s.renderMembers(w, r, sr, inviteForm{Role: roleMember}, http.StatusOK)
}

func (s *server) renderMembers(w http.ResponseWriter, r *http.Request, sr *settingsReq, form inviteForm, status int) {
	actor := sr.shell.Role
	data := membersData{
		Shell:  sr.shell,
		Form:   form,
		Admins: adminsOf(sr.org),
	}
	if !form.Role.valid() {
		data.Form.Role = roleMember
	}

	if sr.org == nil && sr.shell.Org != nil {
		data.LoadError = loadFailure(sr.page.L, "settings.members.loadSubject", sr.orgErr)
	}

	var (
		invites   []invitation
		keys      []apiKey
		keysKnown bool
	)
	if canManage(actor) {
		ctx := r.Context()
		var (
			invErr    error
			mail, ask bool
			wg        sync.WaitGroup
		)
		wg.Go(func() { invites, invErr = sr.up.Invitations(ctx) })
		wg.Go(func() { mail, ask = sr.up.MailAvailable(ctx) })
		// Removing someone revokes the keys they created, and the
		// confirmation names them.
		wg.Go(func() {
			var err error
			keys, err = sr.up.Keys(ctx)
			keysKnown = err == nil
		})
		wg.Wait()
		if invErr != nil {
			data.InviteError = loadFailure(sr.page.L, "settings.members.invitesLoadSubject", invErr)
		}
		// Only a definite "no" disables the form; a control plane that could
		// not be asked gets the benefit of the doubt, and the attempt itself
		// reports the outcome.
		data.MailOff = ask && !mail
		data.RoleChoices = grantable(sr.page.L, actor, data.Form.Role)
		sr.shell.problem("invite-email", form.EmailError)
		if len(data.RoleChoices) > 0 {
			sr.shell.problem("invite-role-input-"+string(data.RoleChoices[0].Value), form.RoleError)
		}
	}

	now := time.Now()
	query := r.URL.Query()
	names := map[string]string{}
	working := map[string][]memberKey{}
	recent := map[string]int{}
	for _, k := range keys {
		if state := keyState(k, now); state == "revoked" || state == "expired" || k.CreatedBy == "" {
			continue
		}
		mk := memberKey{Label: k.Label, Prefix: k.Prefix}
		if k.LastUsedAt != nil {
			used := stampOf(sr.page.L, *k.LastUsedAt, now)
			mk.LastUsed = &used
			if now.Sub(time.UnixMilli(*k.LastUsedAt)) < 24*time.Hour {
				recent[k.CreatedBy]++
			}
		}
		working[k.CreatedBy] = append(working[k.CreatedBy], mk)
	}
	if sr.org != nil {
		for _, m := range sr.org.Members {
			names[m.ID] = m.display()
			if m.Role == roleOwner {
				data.Owners++
			}
		}
		for _, m := range sr.org.Members {
			row := memberRow{
				ID:        m.ID,
				Name:      m.display(),
				Email:     m.Email,
				Initials:  initialsOf(m.display()),
				AvatarURL: m.AvatarURL,
				Role:      m.Role,
				RoleLabel: m.Role.Label(sr.page.L),
				Joined:    stampOf(sr.page.L, m.JoinedAt, now),
				IsSelf:    m.ID == sr.shell.User.ID,
				SoleOwner: m.Role == roleOwner && data.Owners == 1,
				joinedAt:  m.JoinedAt,
			}
			if canActOn(actor, m.Role) {
				row.Options = grantable(sr.page.L, actor, m.Role)
				// Leaving is not offered here; removing yourself is a
				// different decision from removing a colleague.
				row.CanRemove = !row.IsSelf
				row.Confirming = row.CanRemove && query.Get("remove") == m.ID
				row.KeysKnown = keysKnown
				row.Keys, row.RecentKeys = working[m.ID], recent[m.ID]
			}
			data.Members = append(data.Members, row)
		}
	}

	// A role change that asks first comes back here with the question.
	if id, to := query.Get("role-confirm"), role(query.Get("role")); id != "" && sr.shell.Org != nil {
		for _, row := range data.Members {
			if row.ID != id || !slices.ContainsFunc(row.Options, func(o roleOption) bool { return o.Value == to }) {
				continue
			}
			if from := row.Role; needsConfirm(row.IsSelf, from, to) {
				c := confirmRole(sr.page.L, sr.shell.Org.Name, row, to)
				data.Confirm = &c
			}
		}
	}

	column, dir := tableSort(query, memberColumns)
	membersHref := func(column, dir string, page int) string {
		q := url.Values{}
		if column != "" {
			q.Set("sort", column)
			q.Set("dir", dir)
		}
		if page > 1 {
			q.Set("page", strconv.Itoa(page))
		}
		return auditHref("/settings/members", q)
	}
	if column != "" {
		sortMembers(data.Members, column, dir)
	}
	data.Headers = sortHeaders(sr.page.L, memberColumns, column, dir, func(c, d string) string { return membersHref(c, d, 1) })
	data.Count = len(data.Members)
	start, end, page := pageOf(query, len(data.Members), func(n int) string { return membersHref(column, dir, n) })
	data.Members, data.Paging = data.Members[start:end], page

	confirm := query.Get("revoke")
	for _, inv := range invites {
		row := invitationRow{
			ID:         inv.ID,
			Email:      inv.Email,
			Role:       inv.Role,
			RoleLabel:  inv.Role.Label(sr.page.L),
			InvitedBy:  names[inv.InvitedBy],
			Sent:       stampOf(sr.page.L, inv.CreatedAt, now),
			Expires:    stampOf(sr.page.L, inv.ExpiresAt, now),
			Expired:    inv.ExpiresAt <= now.UnixMilli(),
			CanResend:  canGrant(actor, inv.Role) && !data.MailOff,
			Confirming: inv.ID == confirm,
		}
		if row.InvitedBy == "" {
			row.InvitedBy = sr.page.L.T("settings.members.formerMember")
		}
		data.Invitations = append(data.Invitations, row)
	}

	s.finish(w, r, sr, status, "settings-members", "settings.members.title", data)
}

/* --------------------------------------------------------------- audit -- */

// auditPageSize is how many events a page of the log lists. The table is
// compact, so a page can be long.
const auditPageSize = 50

// auditExportCap bounds one export to the newest this many events a filter
// matches. The page says so when a filter matches more.
const auditExportCap = 50_000

// auditFetchSize is how many events an export asks the control plane for at
// a time: its largest page.
const auditFetchSize = 500

type auditCategory struct {
	ID    string
	Label i18n.Key
}

// auditCategories are the activity filter's choices. The IDs are the
// control plane's categories (store.AuditCategories).
var auditCategories = []auditCategory{
	{ID: "", Label: "settings.audit.category.all"},
	{ID: "keys", Label: "settings.audit.category.keys"},
	{ID: "members", Label: "settings.audit.category.members"},
	{ID: "invitations", Label: "settings.audit.category.invitations"},
	{ID: "organisation", Label: "settings.audit.category.organisation"},
}

// auditPeriod is a preset window of the log, offered as a link.
type auditPeriod struct {
	ID    string
	Label i18n.Key
	Span  time.Duration
}

var auditPeriods = []auditPeriod{
	{"24h", "settings.audit.period.day", 24 * time.Hour},
	{"7d", "settings.audit.period.week", 7 * 24 * time.Hour},
	{"30d", "settings.audit.period.month", 30 * 24 * time.Hour},
	{"", "settings.audit.period.all", 0},
}

// dateLayout is how a custom range's dates arrive: what <input type="date">
// submits, and what a reader without one can type.
const dateLayout = "2006-01-02"

// auditFilters is the log's filter as the page's query string carries it.
type auditFilters struct {
	Category, Actor, Target string
	// Period is a preset's ID; From and To, a custom range of UTC dates,
	// replace it when either is given.
	Period, From, To string
}

func auditFiltersOf(query url.Values) auditFilters {
	f := auditFilters{
		Category: query.Get("category"),
		Actor:    query.Get("actor"),
		Target:   strings.TrimSpace(query.Get("target")),
		Period:   query.Get("period"),
		From:     strings.TrimSpace(query.Get("from")),
		To:       strings.TrimSpace(query.Get("to")),
	}
	if !slices.ContainsFunc(auditCategories, func(c auditCategory) bool { return c.ID == f.Category }) {
		f.Category = ""
	}
	if !slices.ContainsFunc(auditPeriods, func(p auditPeriod) bool { return p.ID == f.Period }) {
		f.Period = ""
	}
	if runes := []rune(f.Target); len(runes) > 200 {
		f.Target = string(runes[:200])
	}
	return f
}

// Custom reports whether a custom range replaces the preset period.
func (f auditFilters) Custom() bool { return f.From != "" || f.To != "" }

// values is the query string for these filters, without a page.
func (f auditFilters) values() url.Values {
	v := url.Values{}
	for _, kv := range [][2]string{
		{"category", f.Category}, {"actor", f.Actor}, {"target", f.Target},
		{"period", f.Period}, {"from", f.From}, {"to", f.To},
	} {
		if kv[1] != "" {
			v.Set(kv[0], kv[1])
		}
	}
	if f.Custom() {
		v.Del("period")
	}
	return v
}

// window is the filters' time bounds in epoch milliseconds (since
// inclusive, until exclusive; zero for none). A custom range that cannot be
// used adds its problems to shell and reports false.
func (f auditFilters) window(now time.Time, l *i18n.Localizer, shell *settingsShell) (since, until int64, ok bool) {
	if !f.Custom() {
		for _, p := range auditPeriods {
			if p.ID == f.Period && p.Span > 0 {
				since = now.Add(-p.Span).UnixMilli()
			}
		}
		return since, 0, true
	}
	ok = true
	var from, to time.Time
	if f.From != "" {
		var err error
		if from, err = time.Parse(dateLayout, f.From); err != nil {
			shell.problem("audit-from", l.T("settings.audit.fromInvalid"))
			ok = false
		}
	}
	if f.To != "" {
		var err error
		if to, err = time.Parse(dateLayout, f.To); err != nil {
			shell.problem("audit-to", l.T("settings.audit.toInvalid"))
			ok = false
		}
	}
	if ok && !from.IsZero() && !to.IsZero() && from.After(to) {
		shell.problem("audit-from", l.T("settings.audit.rangeBackwards"))
		ok = false
	}
	if !ok {
		return 0, 0, false
	}
	if !from.IsZero() {
		since = from.UnixMilli()
	}
	if !to.IsZero() {
		// The whole of the last day.
		until = to.AddDate(0, 0, 1).UnixMilli()
	}
	return since, until, true
}

type option struct {
	Value    string
	Label    string
	Selected bool
}

type auditRow struct {
	When    stamp
	Actor   string
	Email   string
	Action  string
	Target  string
	Details string
}

type pageLink struct {
	N       int
	Href    string
	Current bool
	Gap     bool
}

type auditData struct {
	Shell      *settingsShell
	Filters    auditFilters
	Categories []option
	Actors     []option
	Periods    []filterLink
	// Today and Earliest bound the date inputs; Earliest is empty when
	// nothing has been recorded.
	Today, Earliest string
	Filtered        bool
	Rows            []auditRow
	Total           int
	From, To        int
	Pages           []pageLink
	Prev, Next      string
	// Kept is when the oldest kept event was recorded.
	Kept         *stamp
	ExportCSV    string
	ExportJSON   string
	ExportCapped bool
	ExportCap    int
	LoadError    string
	Admins       []contact
}

func (s *server) settingsAudit(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.beginSettings(w, r, "audit")
	if !ok {
		return
	}
	data := auditData{Shell: sr.shell, Admins: adminsOf(sr.org), ExportCap: auditExportCap}
	if !sr.shell.CanManage() {
		s.finish(w, r, sr, http.StatusOK, "settings-audit", "settings.audit.title", data)
		return
	}

	now := time.Now()
	l := sr.page.L
	query := r.URL.Query()
	filters := auditFiltersOf(query)
	data.Filters = filters
	data.Today = now.UTC().Format(dateLayout)
	data.Filtered = len(filters.values()) > 0
	for _, c := range auditCategories {
		data.Categories = append(data.Categories, option{Value: c.ID, Label: l.T(c.Label), Selected: c.ID == filters.Category})
	}
	for _, p := range auditPeriods {
		f := filters
		f.Period, f.From, f.To = p.ID, "", ""
		data.Periods = append(data.Periods, filterLink{
			Label:   l.T(p.Label),
			Href:    auditHref("/settings/audit", f.values()),
			Current: !filters.Custom() && p.ID == filters.Period,
		})
	}

	since, until, valid := filters.window(now, l, sr.shell)
	status := http.StatusOK
	if !valid {
		status = http.StatusUnprocessableEntity
	}

	members := map[string]member{}
	if sr.org != nil {
		for _, m := range sr.org.Members {
			members[m.ID] = m
		}
	}

	page, _ := strconv.Atoi(query.Get("page"))
	page = max(page, 1)
	want := auditQuery{
		Category: filters.Category, Actor: filters.Actor, Target: filters.Target,
		Since: since, Until: until, Offset: (page - 1) * auditPageSize, Limit: auditPageSize, Actors: true,
	}
	var found *auditPage
	var err error
	if valid {
		found, err = sr.up.Audit(r.Context(), want)
		if err == nil && len(found.Events) == 0 && found.Total > 0 && page > 1 {
			// Past the last page, as after the log was filtered down:
			// show the last page there is.
			page = (found.Total + auditPageSize - 1) / auditPageSize
			want.Offset = (page - 1) * auditPageSize
			found, err = sr.up.Audit(r.Context(), want)
		}
	} else {
		// The range is unusable, but the other filters and the people to
		// choose from still are.
		found, err = sr.up.Audit(r.Context(), auditQuery{Limit: 1, Actors: true})
		if found != nil {
			found.Events, found.Total = nil, 0
		}
	}
	if err != nil {
		s.log.Warn("could not read audit log", "error", err, "request_id", httpx.IDOf(r))
		data.LoadError = loadFailure(l, "settings.audit.loadSubject", err)
		found = &auditPage{}
	}

	// People are offered from the log itself, so the list holds everyone
	// who has done something, including those who have since left.
	var actors []option
	known := false
	for _, a := range found.Actors {
		name := a.Name
		if strings.TrimSpace(name) == "" {
			name = a.Email
		}
		if m, ok := members[a.ID]; ok {
			name = m.display()
		}
		if name == "" {
			name = l.T("settings.members.formerMember")
		}
		actors = append(actors, option{Value: a.ID, Label: name, Selected: a.ID == filters.Actor})
		known = known || a.ID == filters.Actor
	}
	sort.Slice(actors, func(i, j int) bool { return strings.ToLower(actors[i].Label) < strings.ToLower(actors[j].Label) })
	data.Actors = append([]option{{
		Value:    "",
		Label:    l.T("settings.audit.anyone"),
		Selected: filters.Actor == "" || !known,
	}}, actors...)

	if found.Earliest > 0 {
		kept := stampOf(l, found.Earliest, now)
		data.Kept = &kept
		data.Earliest = time.UnixMilli(found.Earliest).UTC().Format(dateLayout)
	}
	data.Total = found.Total
	if n := len(found.Events); n > 0 {
		data.From = want.Offset + 1
		data.To = want.Offset + n
	}
	for _, e := range found.Events {
		data.Rows = append(data.Rows, describe(e, members, l, now))
	}

	pages := max(1, (found.Total+auditPageSize-1)/auditPageSize)
	link := func(n int) string {
		q := filters.values()
		if n > 1 {
			q.Set("page", strconv.Itoa(n))
		}
		return auditHref("/settings/audit", q)
	}
	if pages > 1 {
		data.Pages = pageLinks(page, pages, link)
		if page > 1 {
			data.Prev = link(page - 1)
		}
		if page < pages {
			data.Next = link(page + 1)
		}
	}
	data.ExportCSV = auditHref("/settings/audit/export.csv", filters.values())
	data.ExportJSON = auditHref("/settings/audit/export.json", filters.values())
	data.ExportCapped = found.Total > auditExportCap

	s.finish(w, r, sr, status, "settings-audit", "settings.audit.title", data)
}

func auditHref(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// pageLinks numbers the pages around the current one, eliding the rest.
func pageLinks(current, total int, href func(int) string) []pageLink {
	var out []pageLink
	gap := false
	for n := 1; n <= total; n++ {
		if n == 1 || n == total || (n >= current-1 && n <= current+1) {
			out = append(out, pageLink{N: n, Href: href(n), Current: n == current})
			gap = false
			continue
		}
		if !gap {
			out = append(out, pageLink{Gap: true})
			gap = true
		}
	}
	return out
}

// actorOf names who did something: as they were named when they did it,
// or as the organisation knows them now for an event older than that.
func actorOf(l *i18n.Localizer, e auditEvent, members map[string]member) (name, email string) {
	switch {
	case strings.TrimSpace(e.ActorName) != "":
		name, email = e.ActorName, e.ActorEmail
	case e.ActorEmail != "":
		name, email = e.ActorEmail, e.ActorEmail
	default:
		m, ok := members[e.ActorID]
		if !ok {
			return l.T("settings.members.formerMember"), ""
		}
		name, email = m.display(), m.Email
	}
	if email == name {
		email = ""
	}
	return name, email
}

func metadataOf(e auditEvent) map[string]string {
	out := map[string]string{}
	if e.Metadata == "" {
		return out
	}
	var raw map[string]any
	if json.Unmarshal([]byte(e.Metadata), &raw) != nil {
		return out
	}
	for k, v := range raw {
		switch value := v.(type) {
		case string:
			out[k] = value
		case float64:
			out[k] = strconv.FormatFloat(value, 'f', -1, 64)
		}
	}
	return out
}

// targetType is the kind of thing an action is about, as an export names it.
func targetType(action string) string {
	switch {
	case strings.HasPrefix(action, "api_key."):
		return "api_key"
	case action == "member.invited", strings.HasPrefix(action, "invitation."):
		return "invitation"
	case strings.HasPrefix(action, "member."):
		return "user"
	case strings.HasPrefix(action, "org."):
		return "organisation"
	}
	return ""
}

// targetName is what an event's target was called when it was recorded, or
// for an older event what the organisation knows of it now; "" when
// nothing is known.
func targetName(e auditEvent, members map[string]member, meta map[string]string) string {
	if e.TargetLabel != "" {
		return e.TargetLabel
	}
	switch targetType(e.Action) {
	case "user":
		if m, ok := members[e.Target]; ok {
			return m.display()
		}
	case "invitation":
		return meta["email"]
	}
	return ""
}

// describe turns one event into a sentence a person can audit. The target is
// named as it was when the event was recorded, falling back for older
// events to what is known of it now.
func describe(e auditEvent, members map[string]member, l *i18n.Localizer, now time.Time) auditRow {
	meta := metadataOf(e)
	row := auditRow{
		When:   stampOf(l, e.CreatedAt, now),
		Actor:  l.T("settings.audit.system"),
		Action: e.Action,
		Target: targetName(e, members, meta),
	}
	if e.ActorID != "" {
		row.Actor, row.Email = actorOf(l, e, members)
	}
	if row.Target == "" {
		switch targetType(e.Action) {
		case "user":
			row.Target = l.T("settings.members.formerMember")
		case "invitation":
			row.Target = l.T("settings.audit.earlierInvitation")
		case "api_key":
			row.Target = e.Target
		}
	}

	switch e.Action {
	case "api_key.created":
		row.Action = l.T("settings.audit.action.keyCreated")
	case "api_key.revoked":
		row.Action = l.T("settings.audit.action.keyRevoked")
	case "api_key.rotated":
		row.Action = l.T("settings.audit.rotated")
		if ms, err := strconv.ParseInt(meta["stopsAt"], 10, 64); err == nil && ms > 0 {
			row.Details = l.T("settings.audit.rotatedUntil", "when", stampOf(l, ms, now).Full)
		}
	case "member.invited":
		row.Action = l.T("settings.audit.action.memberInvited")
		if r := role(meta["role"]); r.valid() {
			row.Details = l.T("settings.audit.detail.asRole", "role", i18n.Formatted(r.Label(l)))
		}
	case "invitation.revoked":
		row.Action = l.T("settings.audit.action.invitationRevoked")
	case "invitation.accepted":
		row.Action = l.T("settings.audit.action.invitationAccepted")
		if r := role(meta["role"]); r.valid() {
			row.Details = l.T("settings.audit.detail.joinedAs", "role", i18n.Formatted(r.Label(l)))
		}
	case "member.role_changed":
		row.Action = l.T("settings.audit.action.roleChanged")
		from, to := role(meta["from"]), role(meta["to"])
		if from.valid() && to.valid() {
			row.Details = l.T(
				"settings.audit.detail.roleChanged",
				"from", i18n.Formatted(from.Label(l)),
				"to", i18n.Formatted(to.Label(l)),
			)
		}
	case "member.removed":
		row.Action = l.T("settings.audit.action.memberRemoved")
		var parts []string
		if r := role(meta["role"]); r.valid() {
			parts = append(parts, l.T(
				"settings.audit.detail.wasRole",
				"role", i18n.Formatted(r.Label(l)),
			))
		}
		if n, err := strconv.Atoi(meta["revokedKeys"]); err == nil && n > 0 {
			parts = append(parts, l.T("settings.audit.detail.keysRevoked", "count", n))
		}
		row.Details = strings.Join(parts, l.T("settings.audit.detail.separator"))
	case "org.created":
		row.Action = l.T("settings.audit.action.orgCreated")
	}
	if row.Target == "" {
		row.Target = l.T("settings.audit.unknownTarget")
	}
	return row
}
