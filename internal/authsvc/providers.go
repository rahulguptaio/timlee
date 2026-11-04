package authsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/store"
)

// Identity providers.
//
// Google and Microsoft Entra are OpenID Connect providers, so they go through
// go-oidc, which discovers the issuer and then validates the ID token
// properly: JWKS signature, iss, aud, exp and nonce, with PKCE S256 on the
// authorisation request. GitHub is OAuth 2.0 only and issues no ID token, so
// there is nothing to validate and the profile is read from its API instead.
//
// A provider is registered only when its credentials are configured and, for
// OIDC, discovery succeeds. A provider that is unreachable at boot should
// cost one sign-in button, not the whole service.

// msPersonalTenant is the Entra directory holding personal Microsoft
// accounts. Any other tenant is a managed work or school directory, which is
// the only case where Entra's implicit assertion about an address is worth
// anything.
const msPersonalTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

type provider struct {
	name     string
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	// pkce marks providers that accept an S256 challenge. GitHub does not
	// issue an ID token, and sending it a verifier buys nothing.
	pkce bool
}

// registerProviders discovers and configures every provider whose
// credentials are present. Discovery is network I/O, so the providers are
// prepared in parallel and the service waits for them before listening.
func registerProviders(ctx context.Context, cfg config.Config, logger *slog.Logger) map[string]*provider {
	type result struct {
		name string
		p    *provider
	}
	results := make(chan result, len(cfg.Providers))
	pending := 0

	for _, name := range []string{"github", "google", "microsoft"} {
		credentials := cfg.Providers[name]
		if !credentials.Configured() {
			continue
		}
		pending++
		go func(name string, credentials config.Provider) {
			p, err := buildProvider(ctx, name, credentials, cfg.ControlOrigin)
			if err != nil {
				logger.Error("provider discovery failed; sign-in disabled for it",
					"provider", name, "error", err)
				results <- result{name: name}
				return
			}
			logger.Info("auth provider enabled", "provider", name, "validation", validationOf(p))
			results <- result{name: name, p: p}
		}(name, credentials)
	}

	registered := map[string]*provider{}
	for i := 0; i < pending; i++ {
		got := <-results
		if got.p != nil {
			registered[got.name] = got.p
		}
	}
	if len(registered) == 0 {
		logger.Warn("no OAuth providers registered", "dev_login", cfg.AllowDevLogin)
	}
	return registered
}

func validationOf(p *provider) string {
	if p.verifier != nil {
		return "oidc"
	}
	return "oauth2"
}

func buildProvider(ctx context.Context, name string, credentials config.Provider, controlOrigin string) (*provider, error) {
	redirect := controlOrigin + "/auth/" + name + "/callback"

	if name == "github" {
		return &provider{
			name: name,
			oauth: &oauth2.Config{
				ClientID:     credentials.ClientID,
				ClientSecret: credentials.ClientSecret,
				RedirectURL:  redirect,
				// user:email is needed because a GitHub profile often has no
				// public address at all.
				Scopes: []string{"read:user", "user:email"},
				Endpoint: oauth2.Endpoint{
					AuthURL:  "https://github.com/login/oauth/authorize",
					TokenURL: "https://github.com/login/oauth/access_token",
				},
			},
		}, nil
	}

	issuer := "https://accounts.google.com"
	if name == "microsoft" {
		tenant := credentials.Tenant
		if tenant == "" {
			tenant = "common"
		}
		issuer = "https://login.microsoftonline.com/" + tenant + "/v2.0"
	}

	// Bounded: a hanging discovery must not hold up the whole boot.
	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	issued, err := oidc.NewProvider(discoveryCtx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", issuer, err)
	}

	return &provider{
		name: name,
		oauth: &oauth2.Config{
			ClientID:     credentials.ClientID,
			ClientSecret: credentials.ClientSecret,
			RedirectURL:  redirect,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
			Endpoint:     issued.Endpoint(),
		},
		verifier: issued.Verifier(&oidc.Config{ClientID: credentials.ClientID}),
		pkce:     true,
	}, nil
}

// authCodeURL builds the authorisation request for one sign-in attempt.
func (p *provider) authCodeURL(flow *oauthFlow) string {
	options := []oauth2.AuthCodeOption{}
	if p.pkce {
		options = append(options,
			oauth2.S256ChallengeOption(flow.Verifier),
			oauth2.SetAuthURLParam("nonce", flow.Nonce),
		)
	}
	return p.oauth.AuthCodeURL(flow.State, options...)
}

// exchange trades the authorisation code for a profile, validating whatever
// the provider gives us the means to validate.
func (p *provider) exchange(ctx context.Context, code string, flow *oauthFlow) (store.Profile, error) {
	options := []oauth2.AuthCodeOption{}
	if p.pkce {
		options = append(options, oauth2.VerifierOption(flow.Verifier))
	}

	token, err := p.oauth.Exchange(ctx, code, options...)
	if err != nil {
		return store.Profile{}, fmt.Errorf("exchange code: %w", err)
	}

	if p.verifier == nil {
		return p.githubProfile(ctx, token)
	}
	return p.oidcProfile(ctx, token, flow)
}

func (p *provider) oidcProfile(ctx context.Context, token *oauth2.Token, flow *oauthFlow) (store.Profile, error) {
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return store.Profile{}, errors.New("no id_token in the token response")
	}
	// Signature against the issuer's JWKS, plus iss, aud and exp.
	idToken, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return store.Profile{}, fmt.Errorf("verify id_token: %w", err)
	}
	// The nonce ties this token to the authorisation request this browser
	// started, which is what stops a token minted elsewhere being replayed.
	if idToken.Nonce != flow.Nonce {
		return store.Profile{}, errors.New("id_token nonce does not match the authorisation request")
	}

	var claims struct {
		Subject           string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     any    `json:"email_verified"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Picture           string `json:"picture"`
		Tenant            string `json:"tid"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return store.Profile{}, fmt.Errorf("read id_token claims: %w", err)
	}

	profile := store.Profile{
		// sub is pairwise per application and stable. oid and
		// preferred_username are not safe identifiers to key on.
		Subject:   claims.Subject,
		Email:     claims.Email,
		Name:      claims.Name,
		AvatarURL: claims.Picture,
		Provider:  p.name,
	}

	switch p.name {
	case "microsoft":
		if profile.Email == "" {
			profile.Email = claims.PreferredUsername
		}
		// Entra does not return email_verified. A managed work or school
		// tenant has verified its users' addresses as a matter of course; the
		// personal-accounts tenant has not, so addresses from it are never
		// allowed to link to an existing account.
		profile.EmailVerified = claims.Tenant != "" && claims.Tenant != msPersonalTenant
	default:
		// Google states verification explicitly, and only an explicit true
		// is good enough to merge this identity into an existing account.
		verified, _ := claims.EmailVerified.(bool)
		profile.EmailVerified = verified
	}

	if profile.Subject == "" {
		return store.Profile{}, errors.New("id_token carries no subject")
	}
	return profile, nil
}

func (p *provider) githubProfile(ctx context.Context, token *oauth2.Token) (store.Profile, error) {
	client := p.oauth.Client(ctx, token)

	var account struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := githubGet(ctx, client, "https://api.github.com/user", &account); err != nil {
		return store.Profile{}, err
	}
	if account.ID == 0 {
		return store.Profile{}, errors.New("github returned no account id")
	}

	// GitHub returns every address with its own verified flag; the primary
	// one is the only candidate for linking.
	var addresses []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := githubGet(ctx, client, "https://api.github.com/user/emails", &addresses); err != nil {
		return store.Profile{}, err
	}

	profile := store.Profile{
		Provider:  "github",
		Subject:   fmt.Sprintf("%d", account.ID),
		Name:      account.Name,
		AvatarURL: account.AvatarURL,
	}
	if profile.Name == "" {
		profile.Name = account.Login
	}
	for _, address := range addresses {
		if address.Primary {
			profile.Email, profile.EmailVerified = address.Email, address.Verified
			break
		}
	}
	if profile.Email == "" && len(addresses) > 0 {
		profile.Email, profile.EmailVerified = addresses[0].Email, addresses[0].Verified
	}
	return profile, nil
}

func githubGet(ctx context.Context, client *http.Client, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("github %s: unexpected status %d", url, res.StatusCode)
	}
	// Bounded so a hostile or broken upstream cannot exhaust memory here.
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(into)
}

// providerNames lists registered providers in a stable order, so /health and
// the sign-in screen do not reshuffle between calls.
func providerNames(registered map[string]*provider) []string {
	names := make([]string, 0, len(registered))
	for name := range registered {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
