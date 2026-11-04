package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/appfactory"
)

// The preview server.
//
// Generated apps are arbitrary code, so they are never served from the site's
// own origin: here they get a separate listener on a separate site
// (TIMLEE_PREVIEW_*), which config.Validate enforces in production. The
// workspace frames them sandboxed (no top navigation). Apps share this
// origin, and so share storage; per-app hostnames would separate them once
// there are multiple owners.
//
// Every preview URL carries a capability, /<expiry>.<signature>/<slug>/,
// issued only to signed-in, allowlisted readers of the workspace page. The
// app's assets use relative URLs (Vite base './'), so they inherit it. No
// cookie is involved: the frame is cross-site, where browsers block them.

// previewTTL bounds how long a preview link works after the page that issued it.
const previewTTL = 12 * time.Hour

type previewSigner struct {
	key    []byte
	origin string
}

func newPreviewSigner(secret, origin string) *previewSigner {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("timlee-web/preview/v1"))
	return &previewSigner{key: mac.Sum(nil), origin: trimSlash(origin)}
}

func (p *previewSigner) sign(slug string, expiry int64) string {
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(slug + "\x00" + strconv.FormatInt(expiry, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}

// URL is the app's preview, valid for previewTTL from now.
func (p *previewSigner) URL(slug string, now time.Time) string {
	expiry := now.Add(previewTTL).Unix()
	return p.origin + "/" + strconv.FormatInt(expiry, 10) + "." + p.sign(slug, expiry) + "/" + slug + "/"
}

// verify checks a capability segment for slug.
func (p *previewSigner) verify(capability, slug string, now time.Time) bool {
	rawExpiry, sig, ok := strings.Cut(capability, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil || now.Unix() > expiry {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(p.sign(slug, expiry)))
}

// previewHandler serves an app's published build to holders of a valid capability.
func previewHandler(apps *appfactory.Factory, signer *previewSigner, publicOrigin string) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		// The color-scheme script in each app's index.html runs before paint.
		// The app's own scripts are generated code either way, so a hash would
		// add brittleness without a boundary.
		"script-src 'self' 'unsafe-inline'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob: https:",
		"font-src 'self' data:",
		// data: for Web Awesome's built-in icons, which it fetches as data URLs.
		"connect-src 'self' https: data:",
		"form-action 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"frame-ancestors " + trimSlash(publicOrigin),
	}, "; ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		capability, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		slug, file, hasSlash := strings.Cut(rest, "/")
		if !appfactory.ValidSlug(slug) || !signer.verify(capability, slug, time.Now()) {
			http.NotFound(w, r)
			return
		}
		dir := apps.PreviewDir(slug)
		if dir == "" {
			http.NotFound(w, r)
			return
		}
		if !hasSlash {
			http.Redirect(w, r, "/"+capability+"/"+slug+"/", http.StatusMovedPermanently)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// Work in progress, behind an expiring link: never index it.
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Cache-Control", "no-cache")

		root, err := os.OpenRoot(dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		name := path.Clean("/" + file)[1:]
		if info, err := root.Stat(name); name == "" || err != nil || info.IsDir() {
			// Client-side routes fall back to the app shell.
			name = "index.html"
		}
		f, err := root.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, info.ModTime(), f)
	})
}
