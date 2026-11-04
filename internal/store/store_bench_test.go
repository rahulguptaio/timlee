package store_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/store"
)

// benchRows is how many keys and sessions the tables hold, so a lookup that
// loses its index shows up as a slowdown instead of hiding behind one row.
const benchRows = 1000

func openBenchStore(b *testing.B, path string) (*store.Store, string, string) {
	b.Helper()
	st, err := store.Open(b.Context(), path)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	user, _, err := st.UpsertFromProvider(b.Context(), store.Profile{
		Provider: "email", Subject: "bench@timlee.test", Email: "bench@timlee.test",
		EmailVerified: true, Name: "Bench",
	})
	if err != nil {
		b.Fatalf("user: %v", err)
	}
	orgID, err := st.CreateOrg(b.Context(), "Bench", user.ID)
	if err != nil {
		b.Fatalf("org: %v", err)
	}
	return st, user.ID, orgID
}

// BenchmarkVerifyAPIKey is the data plane's key check: hash the presented
// secret, look the hash up, and record the use.
func BenchmarkVerifyAPIKey(b *testing.B) {
	st, userID, orgID := openBenchStore(b, ":memory:")
	var secret string
	for i := range benchRows {
		key, err := st.CreateAPIKey(b.Context(), orgID, userID, fmt.Sprintf("key %d", i), nil)
		if err != nil {
			b.Fatalf("create key: %v", err)
		}
		secret = key.Secret
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := st.VerifyAPIKey(b.Context(), secret); err != nil {
			b.Fatalf("verify: %v", err)
		}
	}
}

// BenchmarkGetSession is the lookup behind every cookie-authenticated
// request: hash the session id and read the live row.
func BenchmarkGetSession(b *testing.B) {
	st, userID, orgID := openBenchStore(b, ":memory:")
	document := []byte(fmt.Sprintf(`{"userId":%q,"orgId":%q}`, userID, orgID))
	expires := time.Now().Add(time.Hour).UnixMilli()
	var id string
	for range benchRows {
		id = store.NewSessionID()
		if err := st.PutSession(b.Context(), id, document, expires); err != nil {
			b.Fatalf("put session: %v", err)
		}
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := st.GetSession(b.Context(), id); err != nil {
			b.Fatalf("get session: %v", err)
		}
	}
}

// benchDatabaseFile is a database file in a scratch directory inside the
// package, so the parallel benchmarks run on WAL with a writer and a pool of
// readers, as production does, rather than on one in-memory connection.
func benchDatabaseFile(b *testing.B) string {
	b.Helper()
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

// BenchmarkVerifyAPIKeyParallel is the data plane checking keys for several
// requests at once, which is what it does under load.
func BenchmarkVerifyAPIKeyParallel(b *testing.B) {
	st, userID, orgID := openBenchStore(b, benchDatabaseFile(b))
	ctx := b.Context()
	key, err := st.CreateAPIKey(ctx, orgID, userID, "parallel", nil)
	if err != nil {
		b.Fatalf("create key: %v", err)
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := st.VerifyAPIKey(ctx, key.Secret); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkGetSessionParallel is cookie lookups for several signed-in
// requests at once.
func BenchmarkGetSessionParallel(b *testing.B) {
	st, userID, orgID := openBenchStore(b, benchDatabaseFile(b))
	ctx := b.Context()
	id := store.NewSessionID()
	document := []byte(fmt.Sprintf(`{"userId":%q,"orgId":%q}`, userID, orgID))
	if err := st.PutSession(ctx, id, document, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		b.Fatalf("put session: %v", err)
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := st.GetSession(ctx, id); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
