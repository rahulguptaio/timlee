package store

import (
	"context"
	"database/sql"
	"sync"
)

// handle is a database handle that prepares each distinct query once and
// then reuses it.
//
// A one-shot query makes SQLite parse and plan its SQL on every call, which
// was more than a quarter of an API key verification. A prepared statement is
// parsed once per connection and afterwards only re-bound. Every query in
// this package is a constant, so the cache holds a fixed set of statements.
//
// Statements inside a write transaction run on the *sql.Tx and are not
// cached: a write transaction is rare. A read-only snapshot uses the cache
// (see snapshot).
type handle struct {
	*sql.DB
	stmts sync.Map // query -> *sql.Stmt
}

// statement is a cached statement as callers see it: borrowed, so it has no
// Close. The handle owns it, and database/sql closes it with the handle.
type statement interface {
	ExecContext(ctx context.Context, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, args ...any) *sql.Row
}

func (h *handle) prepared(ctx context.Context, query string) (statement, error) {
	stmt, err := h.stmt(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmt, nil
}

func (h *handle) stmt(ctx context.Context, query string) (*sql.Stmt, error) {
	if stmt, ok := h.stmts.Load(query); ok {
		return stmt.(*sql.Stmt), nil
	}
	stmt, err := h.DB.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	if first, raced := h.stmts.LoadOrStore(query, stmt); raced {
		// Another caller cached the same query first. This copy was never
		// used, so nothing depends on how its close goes.
		_ = stmt.Close()
		return first.(*sql.Stmt), nil
	}
	return stmt, nil
}

// ExecContext, QueryContext and QueryRowContext shadow *sql.DB's own, so
// every statement run on a handle goes through the cache.

func (h *handle) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	stmt, err := h.prepared(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmt.ExecContext(ctx, args...)
}

func (h *handle) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	stmt, err := h.prepared(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmt.QueryContext(ctx, args...)
}

func (h *handle) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	stmt, err := h.prepared(ctx, query)
	if err != nil {
		// A *sql.Row cannot carry an error made outside database/sql. The
		// unprepared query fails the same way and reports it through Scan.
		return h.DB.QueryRowContext(ctx, query, args...)
	}
	return stmt.QueryRowContext(ctx, args...)
}

// snapshot runs fn's reads in one read-only transaction: one snapshot of the
// database, so what they read agrees, and one acquisition of SQLite's read
// lock, which separate statements take and release once each. The
// statements still come from the cache: each is re-bound to the
// transaction's connection (tx.StmtContext), where it has already been
// prepared once the pool is warm, rather than parsed again.
func (h *handle) snapshot(ctx context.Context, fn func(q queryer) error) error {
	tx, err := h.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	var missed []string
	if err = fn(snapshotQueryer{tx: tx, h: h, missed: &missed}); err != nil {
		_ = tx.Rollback()
	} else {
		err = tx.Commit()
	}
	// Cached only now. Preparing on the handle means waiting for a free
	// connection while the transaction holds one, and an in-memory database
	// has only the one.
	for _, query := range missed {
		_, _ = h.stmt(ctx, query) //nolint:sqlclosecheck // cached: the handle owns the statement
	}
	return err
}

// snapshotQueryer is a snapshot's transaction seen through the handle's
// statement cache. A statement not cached yet runs unprepared on the
// transaction, and is cached when the snapshot ends.
type snapshotQueryer struct {
	tx     *sql.Tx
	h      *handle
	missed *[]string
}

// bound is query's cached statement re-bound to the transaction, or nil
// when it is not cached yet. Borrowed, like every cached statement:
// database/sql closes it when the transaction ends.
func (q snapshotQueryer) bound(ctx context.Context, query string) statement {
	cached, ok := q.h.stmts.Load(query)
	if !ok {
		*q.missed = append(*q.missed, query)
		return nil
	}
	return q.tx.StmtContext(ctx, cached.(*sql.Stmt))
}

func (q snapshotQueryer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if stmt := q.bound(ctx, query); stmt != nil {
		return stmt.ExecContext(ctx, args...)
	}
	return q.tx.ExecContext(ctx, query, args...)
}

func (q snapshotQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if stmt := q.bound(ctx, query); stmt != nil {
		return stmt.QueryContext(ctx, args...)
	}
	return q.tx.QueryContext(ctx, query, args...)
}

func (q snapshotQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if stmt := q.bound(ctx, query); stmt != nil {
		return stmt.QueryRowContext(ctx, args...)
	}
	return q.tx.QueryRowContext(ctx, query, args...)
}
