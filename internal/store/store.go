// Package store is Azir's own persistence: the customer spine, the credential
// vault's storage, the capability approval gate, and the audit mirror.
//
// It holds no authoritative copy of ticket state. Syncro owns that.
//
// SQLite via modernc.org/sqlite — pure Go, so the binary stays static and the
// image stays small. For a handful of technicians the write volume is trivial,
// and a backup is a file copy.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a lookup matches nothing.
var ErrNotFound = errors.New("store: not found")

// timeFormat matches the ISO-8601 the schema stores.
const timeFormat = "2006-01-02T15:04:05.000Z"

// DB holds two pools. SQLite permits exactly one writer at a time, so the
// write pool is capped at a single connection and every mutation serialises
// through it. Reads run concurrently against WAL snapshots, which keeps a slow
// scan — a brute-force vector search, later — from blocking writes.
type DB struct {
	read  *sql.DB
	write *sql.DB
	path  string
}

// Open prepares the database file and applies pragmas.
func Open(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("store: database path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("store: create data directory: %w", err)
		}
	}

	write, err := open(ctx, path, 1)
	if err != nil {
		return nil, err
	}
	read, err := open(ctx, path, 8)
	if err != nil {
		write.Close()
		return nil, err
	}
	return &DB{read: read, write: write, path: path}, nil
}

func open(ctx context.Context, path string, maxConns int) (*sql.DB, error) {
	// WAL for concurrent readers, busy_timeout so a contended write waits
	// instead of failing, foreign_keys because the schema relies on cascades.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"+
		"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", url.PathEscape(path))

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return db, nil
}

// Close releases both pools.
func (db *DB) Close() {
	db.read.Close()
	db.write.Close()
}

// Path is the database file location.
func (db *DB) Path() string { return db.path }

// Ping reports reachability.
func (db *DB) Ping(ctx context.Context) error { return db.read.PingContext(ctx) }

// Reader exposes the read pool for packages building their own queries.
func (db *DB) Reader() *sql.DB { return db.read }

// Writer exposes the single-connection write pool.
func (db *DB) Writer() *sql.DB { return db.write }

// Migrate applies any migration not yet recorded, in filename order, each in
// its own transaction. A hand-rolled migrator rather than a dependency: the
// requirement is "run these files once, in order", and that is forty lines.
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.write.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)

	for _, path := range entries {
		version := strings.TrimSuffix(strings.TrimPrefix(path, "migrations/"), ".sql")

		var seen int
		err := db.write.QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&seen)
		if err != nil {
			return fmt.Errorf("store: check migration %s: %w", version, err)
		}
		if seen > 0 {
			continue
		}

		body, err := migrationFS.ReadFile(path)
		if err != nil {
			return err
		}

		tx, err := db.write.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback() //nolint:errcheck // the original error is what matters
			return fmt.Errorf("store: apply %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("store: record %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit %s: %w", version, err)
		}
	}
	return nil
}

func nowString() string { return time.Now().UTC().Format(timeFormat) }

// parseTime tolerates the handful of ISO-8601 shapes SQLite's strftime and Go
// can produce, so a row written by a default expression reads back the same as
// one written by Go.
func parseTime(s string) time.Time {
	for _, layout := range []string{timeFormat, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
