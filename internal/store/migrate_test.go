package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dreulavelle/azir/internal/store"
)

// Every container start calls Migrate, so it must be safe to run repeatedly.
func TestMigrateIsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	for range 3 {
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("repeat migrate: %v", err)
		}
	}

	applied, err := db.AppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("ledger is empty after migrating")
	}
	for _, m := range applied {
		if m.Checksum == "" {
			t.Errorf("migration %s recorded without a checksum", m.Version)
		}
	}
}

// Replicas start together. Concurrent migration must serialise on the advisory
// lock rather than racing to apply the same DDL.
func TestMigrateIsSafeConcurrently(t *testing.T) {
	db := testDB(t)

	const racers = 6
	errs := make([]error, racers)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = db.Migrate(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d failed: %v", i, err)
		}
	}

	// Exactly one ledger row per migration, not one per racer.
	applied, err := db.AppliedMigrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, m := range applied {
		seen[m.Version]++
	}
	for version, n := range seen {
		if n != 1 {
			t.Errorf("migration %s recorded %d times", version, n)
		}
	}
}

// Editing an applied migration is how environments silently diverge. It must
// be fatal, not a warning and not a no-op.
func TestMigrateRejectsEditedMigration(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	applied, err := db.AppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("nothing applied")
	}

	// Simulate a file edit by corrupting the recorded checksum: the next run
	// sees ledger and file disagree, which is the same condition.
	if _, err := db.Pool().Exec(ctx,
		`UPDATE schema_migrations SET checksum = 'deadbeef' WHERE version = $1`,
		applied[0].Version); err != nil {
		t.Fatal(err)
	}

	err = db.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate accepted a modified migration; environments can now diverge silently")
	}
	if !errors.Is(err, store.ErrMigrationDrift) {
		t.Fatalf("want ErrMigrationDrift, got %v", err)
	}
	if !strings.Contains(err.Error(), applied[0].Version) {
		t.Errorf("drift error does not name the offending migration: %v", err)
	}
}

// A database migrated by a newer build must not be silently accepted by an
// older one — that is a rollback quietly running against a future schema.
func TestMigrateRejectsUnknownAppliedMigration(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx,
		`INSERT INTO schema_migrations (version, checksum) VALUES ('9999_from_the_future', 'x')`,
	); err != nil {
		t.Fatal(err)
	}

	err := db.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate accepted a database newer than this binary")
	}
	if !strings.Contains(err.Error(), "9999_from_the_future") {
		t.Errorf("error does not name the unknown migration: %v", err)
	}
}

// pgvector is the reason this is Postgres. If the extension is missing the
// deployment is wrong, and that should fail here rather than in phase 5.
func TestVectorExtensionIsUsable(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	var distance float64
	if err := db.Pool().QueryRow(ctx,
		`SELECT '[1,2,3]'::vector <=> '[1,2,4]'::vector`).Scan(&distance); err != nil {
		t.Fatalf("pgvector is not usable: %v", err)
	}
	if distance <= 0 || distance > 1 {
		t.Fatalf("implausible cosine distance %v", distance)
	}

	// An HNSW index must be creatable, since that is the whole reason for
	// choosing Postgres over SQLite.
	if _, err := db.Pool().Exec(ctx, `
		CREATE TABLE vector_smoke (id INT PRIMARY KEY, embedding vector(3));
		CREATE INDEX vector_smoke_hnsw ON vector_smoke
			USING hnsw (embedding vector_cosine_ops);
		DROP TABLE vector_smoke;`); err != nil {
		t.Fatalf("cannot build an HNSW index: %v", err)
	}
}

// Trigram search backs fuzzy customer lookup by name.
func TestTrigramExtensionIsUsable(t *testing.T) {
	db := testDB(t)
	var sim float64
	if err := db.Pool().QueryRow(context.Background(),
		`SELECT similarity('Acme Dental', 'acme dentl')`).Scan(&sim); err != nil {
		t.Fatalf("pg_trgm is not usable: %v", err)
	}
	if sim <= 0 {
		t.Fatalf("implausible similarity %v", sim)
	}
}

// Per-table autovacuum settings exist because the global config is a
// compromise the high-churn tables cannot live with. A migration that silently
// failed to apply them would show up much later as a table that never vacuums.
func TestPerTableAutovacuumApplied(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	want := map[string]string{
		"audit_log":    "autovacuum_vacuum_scale_factor=0.01",
		"capabilities": "autovacuum_vacuum_scale_factor=0.05",
		"credentials":  "autovacuum_vacuum_threshold=25",
		"customers":    "autovacuum_vacuum_threshold=50",
	}

	for table, setting := range want {
		var options []string
		if err := db.Pool().QueryRow(ctx,
			`SELECT COALESCE(reloptions, '{}') FROM pg_class WHERE relname = $1`,
			table).Scan(&options); err != nil {
			t.Fatalf("read reloptions for %s: %v", table, err)
		}
		if !slices.Contains(options, setting) {
			t.Errorf("%s is missing %q; got %v", table, setting, options)
		}
	}
}

// Autovacuum must not be able to claim maintenance_work_mem per worker. With
// maintenance_work_mem raised for HNSW builds, inheriting it across several
// workers is how a container gets OOM-killed during routine maintenance.
func TestAutovacuumMemoryIsBounded(t *testing.T) {
	db := testDB(t)

	var setting string
	if err := db.Pool().QueryRow(context.Background(),
		`SELECT current_setting('autovacuum_work_mem')`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting == "-1" {
		t.Error("autovacuum_work_mem inherits maintenance_work_mem; " +
			"each worker can then claim the full HNSW build budget")
	}
}
