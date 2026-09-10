// Package storage persists trips, routes and their derived measurements in
// SQLite.
//
// The database holds one writer at a time, which suits a single-user tracker
// and keeps the deployment to one file that can be backed up by copying it.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// timeLayout is how instants are written. RFC 3339 in UTC sorts correctly as
// text, so ordinary string comparison orders rows by time.
const timeLayout = time.RFC3339Nano

// Store is a handle on the database.
type Store struct {
	db *sql.DB
}

// Open connects to the SQLite database at path, applying any outstanding
// migrations. The caller must Close the returned Store.
func Open(ctx context.Context, path string) (*Store, error) {
	// Write-ahead logging lets the reporting pages read while an import is in
	// progress; the busy timeout absorbs the brief contention that remains.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite permits a single writer. Confining the pool to one connection
	// trades a little read concurrency for the absence of lock contention,
	// which is the right trade for one user.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for the few callers that need to run their
// own statements, such as tests.
func (s *Store) DB() *sql.DB { return s.db }

// migrate applies every embedded migration that has not yet run, in name order.
func (s *Store) migrate(ctx context.Context) error {
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`
	if _, err := s.db.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)

	for _, name := range entries {
		var applied int
		err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
				name, formatTime(time.Now().UTC()))
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// inTx runs fn inside a transaction, rolling back if it returns an error.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// nullTime renders an instant for storage, writing NULL for the zero time.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func timeFrom(v sql.NullString) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return parseTime(v.String)
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// InitialisedAt reports when this database was first created, taken from when
// the earliest migration was applied.
//
// It is the plainest available answer to "is my data surviving a deployment?":
// if this keeps moving forward, the database is being recreated each time rather
// than persisted.
func (s *Store) InitialisedAt(ctx context.Context) (time.Time, error) {
	var at sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(applied_at) FROM schema_migrations`).Scan(&at)
	if err != nil {
		return time.Time{}, fmt.Errorf("query schema age: %w", err)
	}
	if !at.Valid {
		return time.Time{}, nil
	}
	return parseTime(at.String), nil
}
