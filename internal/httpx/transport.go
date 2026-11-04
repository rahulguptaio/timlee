package httpx

import "net/http"

// serviceIdleConnsPerHost is how many idle keep-alive connections a process
// keeps to each host it calls. net/http's default of two suits a browser-like
// client. A page that calls other services several times per view, at 50
// concurrent views, opened a new connection for one
// call in nine, discarded more that it had dialled and never used, and left
// 10,000-14,000 sockets in TIME_WAIT within five seconds — enough, run twice
// in a row, to exhaust the ephemeral ports ("can't assign requested address")
// and serve readers the signed-out page.
const serviceIdleConnsPerHost = 64

// TuneDefaultTransport sizes http.DefaultTransport's idle pool for calls
// between Timlee's services, which go to a handful of hosts many times over.
//
// The default transport is tuned rather than replaced so every client built
// on it — including the reverse proxy and those with no Transport of their
// own — shares one pool, and so tests that install their own round tripper
// as the default keep answering those clients. Call it once at startup,
// before the first request; a default that is not an *http.Transport is left
// alone.
//
// After a burst the pool holds up to serviceIdleConnsPerHost connections per
// host until they have idled for the transport's IdleConnTimeout (90 s), so
// a descriptor count taken sooner than that includes them.
func TuneDefaultTransport() {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	t.MaxIdleConnsPerHost = serviceIdleConnsPerHost
	// The total must not undercut the per-host figure across the few
	// services a process calls.
	t.MaxIdleConns = max(t.MaxIdleConns, 4*serviceIdleConnsPerHost)
}
