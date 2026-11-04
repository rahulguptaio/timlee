package main

import (
	"context"
	"net/http"

	"github.com/rahulguptaio/timlee/internal/httpx"
)

// The control plane rate-limits per client address. A call this
// site makes on a reader's behalf arrives from this process, so without help
// every reader would share one budget and the whole site would degrade
// together once it ran out. These carry the reader's address instead.

type clientIPKey struct{}

// withClientIP records the reader's address — as httpx.ClientIP resolves it,
// believing our own proxy only — for the calls made while serving r.
func withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientIPKey{}, httpx.ClientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// forwardingTransport stamps X-Forwarded-For with the recorded address. It
// replaces any value already there: the planes believe the rightmost entry,
// and only this process may decide what that is.
type forwardingTransport struct{ base http.RoundTripper }

func (t forwardingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	ip, _ := req.Context().Value(clientIPKey{}).(string)
	if ip == "" {
		return base.RoundTrip(req)
	}
	// A RoundTripper must not modify the request it was given.
	req = req.Clone(req.Context())
	req.Header.Set("X-Forwarded-For", ip)
	return base.RoundTrip(req)
}
