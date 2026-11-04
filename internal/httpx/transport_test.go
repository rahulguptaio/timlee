package httpx_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rahulguptaio/timlee/internal/httpx"
)

// A page view calls the control plane while other page views are doing the
// same. With net/http's default of two idle connections per host, a second
// burst of calls dials afresh for nearly every request.
func TestTuneDefaultTransportKeepsConnectionsForConcurrentCalls(t *testing.T) {
	saved := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = saved })
	http.DefaultTransport = saved.(*http.Transport).Clone()
	httpx.TuneDefaultTransport()

	const burst = 16
	var (
		dialled atomic.Int64
		mu      sync.Mutex
		arrived int
		release = make(chan struct{})
	)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold every call until the whole burst is in flight, so the burst
		// really needs burst connections at once.
		mu.Lock()
		arrived++
		if arrived == burst {
			close(release)
		}
		wait := release
		mu.Unlock()
		<-wait
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			dialled.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	client := &http.Client{} // no Transport: uses the default, as the services' clients do
	fire := func() {
		mu.Lock()
		arrived, release = 0, make(chan struct{})
		mu.Unlock()
		var wg sync.WaitGroup
		for range burst {
			wg.Go(func() {
				resp, err := client.Get(srv.URL)
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
			})
		}
		wg.Wait()
	}

	fire()
	if got := dialled.Load(); got != burst {
		t.Fatalf("first burst dialled %d connections, want %d", got, burst)
	}
	fire()
	if got := dialled.Load() - burst; got != 0 {
		t.Errorf("second burst dialled %d new connections; want all %d reused", got, burst)
	}
}

type fixedRoundTripper struct{}

func (fixedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

// Tests install their own round tripper as the default to answer outbound
// calls in-process; tuning must leave it in place.
func TestTuneDefaultTransportLeavesAReplacedDefaultAlone(t *testing.T) {
	saved := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = saved })

	http.DefaultTransport = fixedRoundTripper{}
	httpx.TuneDefaultTransport()
	if _, ok := http.DefaultTransport.(fixedRoundTripper); !ok {
		t.Fatalf("default transport replaced with %T", http.DefaultTransport)
	}
}
