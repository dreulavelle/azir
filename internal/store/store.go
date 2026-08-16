// Package store is Azir's own persistence: the customer spine, the credential
// vault's storage, the capability approval gate, and the audit mirror.
//
// It holds no authoritative copy of ticket state. Syncro owns that.
//
// Postgres rather than SQLite because semantic recall needs an ANN index over
// embeddings — brute-force search does not survive the corpus that a few years
// of call records and ticket history produce — and because ingest, polling and
// conversation writes are genuinely concurrent.
package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a lookup matches nothing.
var ErrNotFound = errors.New("store: not found")

// ErrMigrationDrift means an already-applied migration file has been edited.
// Editing applied migrations is how environments silently diverge, so this is
// fatal rather than a warning: write a new migration instead.
var ErrMigrationDrift = errors.New("store: applied migration has been modified")

// migrationLockID namespaces the advisory lock migrations take. Two replicas
// starting together must not both attempt to migrate.
const migrationLockID int64 = 0x415A4952 // "AZIR"

// noTxMarker opts a migration out of running inside a transaction, which some
// statements require — CREATE INDEX CONCURRENTLY most notably.
const noTxMarker = "-- azir:no-transaction"

// DB wraps a connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// Open connects and verifies reachability.
func Open(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// The DSN carries a password; never include it in the error.
		return nil, errors.New("store: invalid DATABASE_URL")
	}
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close releases the pool.
func (db *DB) Close() { db.pool.Close() }

// Pool exposes the underlying pool for packages that need it.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Ping reports database reachability.
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

// Migration is one versioned schema change.
type Migration struct {
	Version  string
	Checksum string
	Body     string
	InTx     bool
}

// AppliedMigration is a row from the ledger.
type AppliedMigration struct {
	Version   string
	Checksum  string
	AppliedAt time.Time
	DurationM int64
}

// loadMigrations reads and orders the embedded migrations.
func loadMigrations() ([]Migration, error) {
	paths, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	out := make([]Migration, 0, len(paths))
	for _, path := range paths {
		body, err := migrationFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{
			Version:  strings.TrimSuffix(strings.TrimPrefix(path, "migrations/"), ".sql"),
			Checksum: hex.EncodeToString(sum[:]),
			Body:     string(body),
			InTx:     !strings.Contains(string(body), noTxMarker),
		})
	}
	return out, nil
}

// Migrate applies pending migrations under an advisory lock.
//
// Properties this guarantees, each of which exists because its absence is a
// way production quietly diverges from development:
//
//   - Exactly one process migrates at a time, so concurrent replica starts are
//     safe.
//   - Every migration is checksummed, and editing an applied one is a fatal
//     error rather than a silent no-op.
//   - Each migration runs in its own transaction unless it opts out, so a
//     failure leaves no half-applied schema.
//   - The ledger records checksum and duration, so drift is diagnosable after
//     the fact rather than only at the moment it happens.
func (db *DB) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return errors.New("store: no migrations embedded")
	}

	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire migration connection: %w", err)
	}
	defer conn.Release()

	// Serialise migrations across every process touching this database. The
	// lock is released when the session ends, so a crashed migrator does not
	// wedge the next start.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("store: take migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     TEXT PRIMARY KEY,
			checksum    TEXT        NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			duration_ms BIGINT      NOT NULL DEFAULT 0
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied := map[string]string{}
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read migration ledger: %w", err)
	}
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return err
		}
		applied[version] = checksum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Drift check runs over everything before anything is applied, so a bad
	// tree is rejected whole rather than half-migrated.
	known := map[string]struct{}{}
	for _, m := range migrations {
		known[m.Version] = struct{}{}
		if sum, ok := applied[m.Version]; ok && sum != m.Checksum {
			return fmt.Errorf("%w: %s (ledger %s, file %s) — write a new migration instead of editing an applied one",
				ErrMigrationDrift, m.Version, short(sum), short(m.Checksum))
		}
	}
	for version := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf("store: database has migration %q that this binary does not know about; it is newer than this build", version)
		}
	}

	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		started := time.Now()

		if m.InTx {
			err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.Body); err != nil {
					return err
				}
				_, err := tx.Exec(ctx,
					`INSERT INTO schema_migrations (version, checksum, duration_ms) VALUES ($1, $2, $3)`,
					m.Version, m.Checksum, time.Since(started).Milliseconds())
				return err
			})
		} else {
			// Opted out of a transaction: the statement cannot be rolled back,
			// so the ledger row is written only after it succeeds. A crash
			// between the two re-runs the migration, which is why no-tx
			// migrations must be written idempotently.
			if _, err = conn.Exec(ctx, m.Body); err == nil {
				_, err = conn.Exec(ctx,
					`INSERT INTO schema_migrations (version, checksum, duration_ms) VALUES ($1, $2, $3)`,
					m.Version, m.Checksum, time.Since(started).Milliseconds())
			}
		}
		if err != nil {
			return fmt.Errorf("store: migration %s failed: %w", m.Version, err)
		}
	}
	return nil
}

// AppliedMigrations returns the ledger, newest first. Exposed so the admin
// console can show schema state without a psql session.
func (db *DB) AppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT version, checksum, applied_at, duration_ms
		FROM schema_migrations ORDER BY version DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: read migration ledger: %w", err)
	}
	defer rows.Close()

	out := []AppliedMigration{}
	for rows.Next() {
		var m AppliedMigration
		if err := rows.Scan(&m.Version, &m.Checksum, &m.AppliedAt, &m.DurationM); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}
