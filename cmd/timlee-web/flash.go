package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Messages that survive one redirect.
//
// Every settings form answers with a redirect, so a refresh never repeats
// the action. What the action achieved has to cross that redirect, and a
// cookie read once by the next page and cleared in the same response is the
// only carrier that needs no server-side state. A query string would put the
// message in history and let a link fake it.

// flashLifetime bounds how long an unread message waits. The next page load
// reads it; anything older was abandoned.
const flashLifetime = 60 * time.Second

// revealLifetime bounds how long a new key's secret waits to be shown. It is
// read by the very next request, so this is generous.
const revealLifetime = 2 * time.Minute

// flash is one message about what just happened.
type flash struct {
	// Tone is a timlee-alert/timlee-toast tone: success, info, warning or
	// danger.
	Tone    string `json:"t"`
	Message string `json:"m"`
}

// cookieName applies the __Host- prefix in production, which binds a cookie
// to this exact origin over HTTPS so a sibling subdomain cannot plant one.
// Development runs over plain http, where a browser rejects the prefix.
func (s *server) cookieName(base string) string {
	if s.cfg.IsProduction() {
		return "__Host-" + base
	}
	return base
}

func (s *server) setFlash(w http.ResponseWriter, f flash) {
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	s.setShortCookie(w, s.cookieName("timlee.flash"), base64.RawURLEncoding.EncodeToString(raw),
		flashLifetime, http.SameSiteLaxMode)
}

// takeFlash reads the pending message and clears it in the same response.
func (s *server) takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	name := s.cookieName("timlee.flash")
	c, err := r.Cookie(name)
	if err != nil {
		return nil
	}
	s.clearCookie(w, name, http.SameSiteLaxMode)

	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var f flash
	if json.Unmarshal(raw, &f) != nil || f.Message == "" {
		return nil
	}
	switch f.Tone {
	case "success", "info", "warning", "danger":
	default:
		f.Tone = "info"
	}
	return &f
}

func (s *server) setShortCookie(w http.ResponseWriter, name, value string, life time.Duration, site http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(life.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: site,
	})
}

func (s *server) clearCookie(w http.ResponseWriter, name string, site http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: site,
	})
}

/* ------------------------------------------------------- one-time secret -- */

// revealedKey is a new API key on its way to the one page that shows it.
//
// The control plane returns a key's secret exactly once and keeps only its
// hash. The form that asked for it redirects, so the secret has to reach the
// next page without landing in a URL, a log or a server-side store. It rides
// in a cookie sealed with AES-GCM under a key derived from the session
// cookie: unreadable on its own, useless to any other session, and cleared by
// the response that displays it — so a refresh or the back button finds it
// gone, which is what "shown once" means.
type revealedKey struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Prefix string `json:"prefix"`
	Secret string `json:"secret"`
}

func revealCipher(r *http.Request) (cipher.AEAD, error) {
	session, ok := sessionCookie(r)
	if !ok {
		return nil, errors.New("no session to bind the secret to")
	}
	key := sha256.Sum256([]byte("timlee-web/reveal/v1\x00" + session.Value))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealReveal stores a new key for the next page of the same session.
func (s *server) sealReveal(w http.ResponseWriter, r *http.Request, key revealedKey) error {
	aead, err := revealCipher(r)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(key) //nolint:gosec // G117: sealed with AES-GCM (revealCipher) before it leaves the server
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nonce, nonce, plain, []byte("reveal"))
	// Strict: the only request that should ever carry it is this site's own
	// redirect, never a navigation from somewhere else.
	s.setShortCookie(w, s.cookieName("timlee.reveal"), base64.RawURLEncoding.EncodeToString(sealed),
		revealLifetime, http.SameSiteStrictMode)
	return nil
}

// takeReveal opens and clears a pending secret. A cookie from another
// session, or a tampered one, simply fails to open.
func (s *server) takeReveal(w http.ResponseWriter, r *http.Request) *revealedKey {
	name := s.cookieName("timlee.reveal")
	c, err := r.Cookie(name)
	if err != nil {
		return nil
	}
	s.clearCookie(w, name, http.SameSiteStrictMode)

	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	aead, err := revealCipher(r)
	if err != nil || len(sealed) < aead.NonceSize() {
		return nil
	}
	nonce, body := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, body, []byte("reveal"))
	if err != nil {
		return nil
	}
	var key revealedKey
	if json.Unmarshal(plain, &key) != nil || key.Secret == "" {
		return nil
	}
	return &key
}
