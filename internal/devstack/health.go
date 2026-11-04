package devstack

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Readiness checks.
//
// "Started" and "ready" are different events, and the gap between them is
// where a dependency-ordered stack goes wrong: the inference endpoint
// verifies credentials against the control plane on its first request, so
// starting it before the control plane answers produces a confusing 503
// rather than a clear startup failure. Each service is therefore gated on its
// own health endpoint before the next one is launched.

// Probe reports whether a service is ready yet.
type Probe func(ctx context.Context) error

// HTTPProbe builds a Probe that GETs url and accepts the status codes accept
// approves of.
func HTTPProbe(url string, accept func(status int) bool) Probe {
	if accept == nil {
		accept = func(status int) bool { return status == http.StatusOK }
	}
	// A dedicated client with connection reuse disabled: a service that is
	// still booting may accept a connection and then close it, and a pooled
	// half-open connection would make the next probe fail for the wrong
	// reason.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()

		if !accept(res.StatusCode) {
			return fmt.Errorf("HTTP %d", res.StatusCode)
		}
		return nil
	}
}

// WaitForHealth polls until the probe succeeds, the deadline passes, or ctx is
// cancelled.
//
// The interval backs off: a service that is ready in 40ms should not wait a
// full second, and one that takes 20s should not be probed 200 times.
func WaitForHealth(ctx context.Context, probe Probe, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	delay := 50 * time.Millisecond
	var last error

	for {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := probe(attempt)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		last = err

		if time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("not healthy after %s: %w", timeout, last)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay = min(delay*3/2, time.Second)
	}
}

// PortFree reports whether a port can be taken on host.
//
// Both halves matter. Binding proves no exclusive listener holds it; dialling
// catches a wildcard listener on the same port, which some platforms let a
// specific bind coexist with — the exact case where a stale service survives
// a previous run and the new one appears to start but is never reached.
func PortFree(host string, port int) bool {
	address := net.JoinHostPort(host, fmt.Sprint(port))

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	_ = listener.Close()

	conn, err := net.DialTimeout("tcp", address, 300*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}
