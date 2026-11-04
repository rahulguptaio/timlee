package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/logx"
	"github.com/rahulguptaio/timlee/internal/render"
)

// The server, not the browser, decides who is reading.
//
// The console used to boot, ask the control plane who it was talking to, and
// only then draw a nav. That is one round trip after first paint during which
// the page is wrong, and it is the reason this layer exists: the session is
// resolved here, before any markup is produced, so the first response is
// already addressed to the right person.

// sessionCookieNames are the names the auth service may have issued. The
// __Host- prefixed one is what production uses; development runs over plain
// http, where a browser would refuse it.
var sessionCookieNames = []string{"__Host-timlee.sid", "timlee.sid"}

// authClient talks to the control plane on a page's behalf.
type authClient struct {
	origin string
	// publicOrigin is sent as Origin on forwarded writes, because the
	// control plane refuses state-changing requests from anywhere it does
	// not recognise.
	publicOrigin string
	http         *http.Client
	log          *slog.Logger
}

func newAuthClient(origin, publicOrigin string, logger *slog.Logger) *authClient {
	return &authClient{
		origin:       trimSlash(origin),
		publicOrigin: trimSlash(publicOrigin),
		// Short: a page render is blocked on this call, so a control plane
		// that has stopped answering must degrade to a signed-out page
		// quickly rather than hold every reader.
		http: &http.Client{Timeout: 3 * time.Second, Transport: forwardingTransport{}},
		log:  logger,
	}
}

// sessionCookie returns the request's session cookie, if it carries one.
func sessionCookie(r *http.Request) (*http.Cookie, bool) {
	for _, name := range sessionCookieNames {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c, true
		}
	}
	return nil, false
}

// sessionResponse mirrors the control plane's GET /auth/session.
type sessionResponse struct {
	User *struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatarUrl"`
		// Locale is the language saved in the account, if any.
		Locale string `json:"locale"`
		// Allowed is whether the account is on the prompt allowlist.
		Allowed bool `json:"allowed"`
	} `json:"user"`
	Org *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"role"`
	} `json:"org"`
}

// User resolves the signed-in visitor for a request, or nil.
//
// A control plane that is unreachable yields a signed-out page rather than an
// error page: the marketing site must still render, and every route that
// actually needs a session re-checks it before doing anything.
func (c *authClient) User(ctx context.Context, r *http.Request) *render.User {
	cookie, ok := sessionCookie(r)
	if !ok {
		// No credential, no call. Most readers are signed out, and this is
		// what keeps the landing page a pure template render.
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+"/auth/session", nil)
	if err != nil {
		c.log.Warn("could not build session request", "error", err)
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.AddCookie(cookie)
	if id := httpx.IDOf(r); id != "" {
		req.Header.Set("X-Request-Id", id)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn("control plane unreachable; rendering signed out",
			"error", err, "request_id", httpx.IDOf(r))
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.log.Warn("session lookup failed", "status", resp.StatusCode, "request_id", httpx.IDOf(r))
		return nil
	}

	var body sessionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		c.log.Warn("session response was not readable", "error", err)
		return nil
	}
	if body.User == nil {
		return nil
	}

	user := &render.User{
		ID:        body.User.ID,
		Email:     body.User.Email,
		Name:      body.User.Name,
		AvatarURL: body.User.AvatarURL,
		Locale:    body.User.Locale,
		Allowed:   body.User.Allowed,
	}
	ids := logx.IDs{UserID: body.User.ID}
	if body.Org != nil {
		user.Org = body.Org.Name
		user.Role = body.Org.Role
		ids.OrgID = body.Org.ID
	}
	// Ids only: the address and names above are for the page, not events.
	logx.Identify(r.Context(), ids)
	return user
}

// providers is what the sign-in page renders its buttons from.
type providers struct {
	Names      []string `json:"providers"`
	MagicLink  bool     `json:"magicLink"`
	DevLogin   bool     `json:"devLogin"`
	MailOutbox bool     `json:"mailOutbox"`
}

// Providers asks the control plane which ways in it can honour.
func (c *authClient) Providers(ctx context.Context) (providers, error) {
	var out providers

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+"/auth/providers", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("control plane returned %d for /auth/providers", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

// forward relays a form the browser submitted to this server as the JSON the
// control plane expects, and hands back whatever it said. A successful answer
// is decoded into out when out is not nil.
//
// This is what lets sign-in work with scripting switched off: the control
// plane's API is unchanged, and the translation from <form> to JSON happens
// on a server that is already in the request path.
func (c *authClient) forward(ctx context.Context, r *http.Request, path string, payload, out any) (int, []*http.Cookie, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.publicOrigin)
	if id := httpx.IDOf(r); id != "" {
		req.Header.Set("X-Request-Id", id)
	}
	if cookie, ok := sessionCookie(r); ok {
		req.AddCookie(cookie)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	cookies := resp.Cookies()
	if resp.StatusCode < 400 {
		if out != nil {
			if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
				return resp.StatusCode, cookies, fmt.Errorf("that did not work. Please try again")
			}
			return resp.StatusCode, cookies, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, cookies, nil
	}

	// The control plane's message is written for a person, so it is the one
	// worth showing; only its shape is this layer's business.
	var failure httpx.ErrorBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&failure); err != nil ||
		strings.TrimSpace(failure.Error.Message) == "" {
		return resp.StatusCode, cookies, fmt.Errorf("that did not work. Please try again")
	}
	return resp.StatusCode, cookies, fmt.Errorf("%s", failure.Error.Message)
}
