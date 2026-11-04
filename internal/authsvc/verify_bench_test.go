package authsvc_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/authsvc"
	"github.com/rahulguptaio/timlee/internal/config"
	"github.com/rahulguptaio/timlee/internal/store"
)

const benchInternalToken = "bench-internal-token-0123456789abcdef"

// The service rate-limits by caller address, and a benchmark sends far more
// than any one caller may. Requests rotate over this many addresses so every
// one is served rather than refused with a 429.
const benchCallers = 4096

// BenchmarkVerify measures the three ways a credential is checked: an API key
// and a relayed session cookie for the data plane, and the browser's own
// session lookup that every signed-in page view makes.
func BenchmarkVerify(b *testing.B) {
	service, cases := verifyCases(b, ":memory:")
	callers := benchAddresses()

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			checkAccepted(b, service, tc)

			w := &benchWriter{header: http.Header{}}
			i := 0
			b.ReportAllocs()
			for b.Loop() {
				r := tc.request()
				r.RemoteAddr = callers[i%len(callers)]
				clear(w.header)
				service.ServeHTTP(w, r)
				i++
			}

			if w.status != http.StatusOK {
				b.Fatalf("status %d: the benchmark is no longer measuring an accepted credential", w.status)
			}
		})
	}
}

// BenchmarkVerifyParallel is the same checks several at a time on a database
// file, as production runs them: WAL, one writer and a pool of readers.
func BenchmarkVerifyParallel(b *testing.B) {
	service, cases := verifyCases(b, benchDatabaseFile(b))
	callers := benchAddresses()

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			checkAccepted(b, service, tc)

			var next atomic.Int64
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				w := &benchWriter{header: http.Header{}}
				for pb.Next() {
					r := tc.request()
					r.RemoteAddr = callers[next.Add(1)%int64(len(callers))]
					clear(w.header)
					service.ServeHTTP(w, r)
					if w.status != http.StatusOK {
						b.Errorf("status %d: the benchmark is no longer measuring an accepted credential", w.status)
						return
					}
				}
			})
		})
	}
}

// TestVerifyUnderConcurrencyNeverHangs sends the benchmark's three checks at
// once from more callers than the store's read pool has connections, on a
// statement cache that has not seen the session queries yet. A session
// read holds one connection for its whole snapshot; anything in it that
// waited for a second connection would starve here: on ":memory:" the pool
// is that one connection. Every request carries a deadline, so a wait for a
// connection fails the test instead of hanging it.
func TestVerifyUnderConcurrencyNeverHangs(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"memory", ":memory:"},
		{"file", benchDatabaseFile(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, cases := verifyCases(t, tc.path)
			callers := benchAddresses()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			const workers, calls = 16, 30
			var (
				wg      sync.WaitGroup
				next    atomic.Int64
				refused = make(chan string, workers)
				start   = make(chan struct{})
			)
			for range workers {
				wg.Go(func() {
					<-start
					for range calls {
						n := next.Add(1)
						check := cases[n%int64(len(cases))]
						r := check.request().WithContext(ctx)
						r.RemoteAddr = callers[n%int64(len(callers))]
						rec := httptest.NewRecorder()
						service.ServeHTTP(rec, r)
						if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), check.want) {
							refused <- fmt.Sprintf("%s: status %d, %.120s (context: %v)", check.name, rec.Code, rec.Body, ctx.Err())
							return
						}
					}
				})
			}
			close(start)

			finished := make(chan struct{})
			go func() { wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-time.After(20 * time.Second):
				// Past the deadline as well: a wait no context reaches.
				var dump strings.Builder
				_ = pprof.Lookup("goroutine").WriteTo(&dump, 1)
				t.Fatalf("verification hung past its deadline:\n%s", dump.String())
			}
			close(refused)
			for failure := range refused {
				t.Error(failure)
			}
		})
	}
}

type verifyCase struct {
	name string
	// want is what only an accepted verdict contains.
	want    string
	request func() *http.Request
}

// verifyCases signs a user in on a store at path and returns the service and
// a request for each kind of check.
func verifyCases(b testing.TB, path string) (*authsvc.Service, []verifyCase) {
	st, err := store.Open(b.Context(), path)
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	service := authsvc.New(context.Background(), config.Config{
		Env:           "development",
		PublicOrigin:  "https://timlee.example",
		ControlOrigin: "https://auth.timlee.example",
		SessionSecret: "bench-session-secret-0123456789abcdef",
		InternalToken: benchInternalToken,
		SessionTTL:    time.Hour,
		AllowDevLogin: true,
		Providers:     map[string]config.Provider{},
	}, st, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	// Sign in through the development route, which issues a real session.
	rec := httptest.NewRecorder()
	login := httptest.NewRequest(http.MethodPost, "/auth/dev", strings.NewReader(`{"email":"bench@timlee.test"}`))
	login.Header.Set("Content-Type", "application/json")
	service.ServeHTTP(rec, login)
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "timlee.sid" {
			session = c
		}
	}
	if rec.Code != http.StatusOK || session == nil {
		b.Fatalf("dev sign-in: status %d, no session cookie", rec.Code)
	}

	user, err := st.UserByEmail(b.Context(), "bench@timlee.test")
	if err != nil {
		b.Fatalf("user: %v", err)
	}
	memberships, err := st.OrgsForUser(b.Context(), user.ID)
	if err != nil || len(memberships) == 0 {
		b.Fatalf("org: %v (%d memberships)", err, len(memberships))
	}
	key, err := st.CreateAPIKey(b.Context(), memberships[0].ID, user.ID, "bench", nil)
	if err != nil {
		b.Fatalf("key: %v", err)
	}

	return service, []verifyCase{
		{"api-key", key.ID, func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/internal/verify-key", strings.NewReader(`{"key":"`+key.Secret+`"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Internal-Token", benchInternalToken)
			return r
		}},
		{"relayed-session", user.ID, func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/internal/verify-session", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Internal-Token", benchInternalToken)
			r.AddCookie(session)
			return r
		}},
		{"browser-session", user.ID, func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
			r.AddCookie(session)
			return r
		}},
	}
}

// checkAccepted fails the benchmark unless tc's credential is accepted: a
// verdict is only worth timing if it is the positive one.
func checkAccepted(b *testing.B, service *authsvc.Service, tc verifyCase) {
	check := httptest.NewRecorder()
	service.ServeHTTP(check, tc.request())
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), tc.want) {
		b.Fatalf("status %d, body %.200s: the credential was not accepted", check.Code, check.Body)
	}
}

func benchAddresses() []string {
	callers := make([]string, benchCallers)
	for i := range callers {
		callers[i] = fmt.Sprintf("10.0.%d.%d:52100", i>>8, i&255)
	}
	return callers
}

// benchDatabaseFile is a database file in a scratch directory inside the
// package, removed afterwards.
func benchDatabaseFile(b testing.TB) string {
	if err := os.MkdirAll(".scratch-probe", 0o700); err != nil {
		b.Fatalf("scratch root: %v", err)
	}
	dir, err := os.MkdirTemp(".scratch-probe", "bench-")
	if err != nil {
		b.Fatalf("scratch dir: %v", err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "auth.db")
}

// benchWriter keeps the status and headers and drops the body.
type benchWriter struct {
	header http.Header
	status int
}

func (w *benchWriter) Header() http.Header         { return w.header }
func (w *benchWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *benchWriter) WriteHeader(status int)      { w.status = status }
