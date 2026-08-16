// Package testsupport provides the real dependencies tests need: a Postgres
// with Azir's schema and tuning, and an in-process NATS with JetStream.
//
// Nothing here is a mock. A mocked store proves nothing about the SQL and a
// mocked broker proves nothing about the protocol, and those are the parts
// that actually break.
package testsupport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/dreulavelle/azir/internal/natsd"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/vault"
)

// repoRoot locates the checkout, so helpers can find deployment files
// regardless of which package's directory the test runs in.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// PostgresDSN starts a Postgres for the calling package and returns its DSN.
//
// AZIR_TEST_DATABASE_URL short-circuits this for a fast local loop. Otherwise
// a container runs the deployment's own image and configuration, so a typo in
// our tuning fails the suite rather than surfacing later in production.
//
// Call this once per package from TestMain: container startup is seconds, and
// per-test isolation comes from resetting the schema instead.
func PostgresDSN(ctx context.Context) (dsn string, ownContainer bool, stop func(), err error) {
	if existing := os.Getenv("AZIR_TEST_DATABASE_URL"); existing != "" {
		return existing, false, func() {}, nil
	}

	container, err := tcpostgres.Run(ctx,
		"pgvector/pgvector:pg18",
		tcpostgres.WithDatabase("azir_test"),
		tcpostgres.WithUsername("azir"),
		tcpostgres.WithPassword("azir_test"),
		tcpostgres.WithConfigFile(filepath.Join(repoRoot(), "deploy", "postgres", "postgresql.conf")),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		return "", false, nil, fmt.Errorf(
			"tests need a database: set AZIR_TEST_DATABASE_URL (see `make test-db`) "+
				"or make Docker available: %w", err)
	}

	dsn, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		return "", false, nil, err
	}
	return dsn, true, func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "could not terminate test container: %v\n", err)
		}
	}, nil
}

// DB opens the package's database and resets it to a migrated, empty state.
func DB(t *testing.T, dsn string) *store.DB {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(db.Close)

	// Extensions live in the shared schema; objects do not.
	if _, err := db.Pool().Exec(ctx,
		`DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// Vault returns a deterministic vault for tests. The key is fixed so a failure
// is reproducible; it is obviously not a secret.
func Vault(t *testing.T) *vault.Vault {
	t.Helper()
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i * 7)
	}
	v, err := vault.New(map[int][]byte{1: key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// NATS starts an in-process server with JetStream and returns a connection.
//
// In-process rather than a container: nats-server is a Go library, so this is
// the real protocol in milliseconds. A container would be slower and no more
// faithful.
func NATS(t *testing.T) (*nats.Conn, string) {
	t.Helper()

	srv, err := natsd.Start(natsd.Options{
		StoreDir: t.TempDir(),
		Host:     "127.0.0.1",
		Port:     -1, // any free port
	})
	if err != nil {
		t.Fatalf("start nats: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.URL())
	if err != nil {
		t.Fatalf("connect to nats: %v", err)
	}
	t.Cleanup(nc.Close)

	return nc, srv.URL()
}
