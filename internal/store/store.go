// Package store is the control plane's database: identity, tenancy,
// credentials, browser sessions and the audit trail.
//
// Tenancy rule: every tenant-scoped read takes an org id and filters on it in
// SQL. A caller that forgets to pass one gets no rows rather than another
// tenant's rows — the failure mode is a visible bug, not a silent leak.
//
// The driver is modernc.org/sqlite, a pure-Go translation of SQLite. cgo
// would force a C toolchain on every build and cross-compile, which is the
// one thing a single-binary deployment is meant to avoid.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store owns the database handles. It is safe for concurrent use.
type Store struct {
	// db is the one writer. Every write and every transaction runs on its
	// single connection, so writers in this process never contend for
	// SQLite's lock.
	db *handle
	// read serves reads outside transactions. On a file it is a small pool of
	// read-only connections, which WAL lets run while the writer writes; an
	// in-memory database cannot be shared like that, so it reads on db.
	read *handle
}

// readConns is the size of the read pool: enough for the handful of
// concurrent reads a control plane sees, each one a short indexed lookup.
const readConns = 4

// queryer is the subset of database/sql both *sql.DB and *sql.Tx implement,
// so a repository function can run inside or outside a transaction without
// being written twice. Only the context-taking methods are in it: a request
// that is cancelled or times out must stop its statement, in a transaction
// or not, rather than leave it running against the database.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrNotFound is returned when a lookup matched nothing.
var ErrNotFound = errors.New("not found")

// Open connects to the SQLite file at path, creating it and applying
// migrations if needed. The path ":memory:" gives a fresh private database
// that lives only as long as the returned Store. ctx bounds the connection
// check and the migrations, not the Store's lifetime.
func Open(ctx context.Context, path string) (*Store, error) {
	var dsn string
	if path == ":memory:" {
		// A shared cache is needed for the handle to survive a pooled
		// connection being recycled, and a unique name keeps two in-memory
		// databases in one process from becoming the same database.
		dsn = "file:" + token(9) + "?mode=memory&cache=shared&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	} else {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
		// WAL lets readers run concurrently with a writer, which matters as
		// soon as two requests are in flight. NORMAL is the right durability
		// trade-off under WAL: a crash can lose the last transaction, not the
		// file. busy_timeout fails slow instead of hanging forever.
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Writes get one connection. SQLite serialises writers anyway, and a
	// single writer removes the SQLITE_BUSY retry loop entirely; reads get a
	// pool of their own below, so they do not queue behind it.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	s := &Store{db: &handle{DB: db}}
	s.read = s.db
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		read, err := openReader(ctx, path)
		if err != nil {
			db.Close()
			return nil, err
		}
		s.read = &handle{DB: read}
	}
	return s, nil
}

// openReader opens the read pool on a file database, after migrations. WAL is
// a property of the file, already set by the writer, so readers need only a
// busy timeout of their own; query_only turns an accidental write here into
// an error rather than a second writer contending for the lock.
func openReader(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	db.SetMaxOpenConns(readConns)
	db.SetMaxIdleConns(readConns)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	return db, nil
}

// Close releases the database handles.
func (s *Store) Close() error {
	var readErr error
	if s.read != s.db {
		readErr = s.read.Close()
	}
	return errors.Join(readErr, s.db.Close())
}

// Ping checks that the database is reachable, for the readiness probe. It
// takes a context because a readiness check that can hang is worse than one
// that fails: an orchestrator waiting on it will neither route traffic nor
// restart the instance.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if s.read != s.db {
		return s.read.PingContext(ctx)
	}
	return nil
}

// DB exposes the writer for tests that need to assert on raw rows.
func (s *Store) DB() *sql.DB { return s.db.DB }

// migrate applies any embedded migration that has not run yet, each in its
// own transaction so a failure leaves the database on the last good version.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migration (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_migration`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		applied[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	// Lexical order is version order: migrations are numbered.
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migration (name, applied_at) VALUES (?, ?)`, name, nowMillis()); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
	}
	return nil
}

// transact runs fn inside a transaction, rolling back if it returns an error
// or panics. fn must issue its statements under the same ctx.
func (s *Store) transact(ctx context.Context, fn func(q queryer) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if v := recover(); v != nil {
			tx.Rollback()
			panic(v)
		}
	}()
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// nullString converts a nullable column into a plain string; the distinction
// between NULL and "" is never load-bearing for the text columns here.
func nullString(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

// nullable turns "" back into SQL NULL so an absent value is stored as one.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullTime(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	ms := v.Int64
	return &ms
}
