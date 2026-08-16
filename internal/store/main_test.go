package store_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testDSN is resolved once for the package.
var testDSN string

// usingOwnPostgres reports whether this run started its own container with the
// deployment's tuned configuration. An externally supplied database has
// whatever configuration its operator gave it, so assertions about tuning must
// not run against it.
var usingOwnPostgres bool

// TestMain provides a database for the whole package.
//
// Two paths, in order of preference:
//
//   - AZIR_TEST_DATABASE_URL, if set. CI supplies a service container this
//     way, and a developer running the suite repeatedly wants the fast loop
//     that `make test-db` gives.
//   - Otherwise a container started here, using the same pgvector image the
//     deployment runs.
//
// The point of the second path is that a skipped test looks exactly like a
// passing one. Requiring manual setup means `go test ./...` reports success
// while every SQL path went unexercised, which is precisely the code most
// likely to be wrong.
func TestMain(m *testing.M) {
	if dsn := os.Getenv("AZIR_TEST_DATABASE_URL"); dsn != "" {
		testDSN = dsn
		os.Exit(m.Run())
	}
	usingOwnPostgres = true

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx,
		// The production image, so a missing extension fails here rather than
		// in a deployment.
		"pgvector/pgvector:pg18",
		tcpostgres.WithDatabase("azir_test"),
		tcpostgres.WithUsername("azir"),
		tcpostgres.WithPassword("azir_test"),
		// The deployment's own tuning, so a typo in it fails the suite rather
		// than surfacing as mysterious production behaviour. This is the
		// difference between testing Postgres and testing our Postgres.
		tcpostgres.WithConfigFile(filepath.Join("..", "..", "deploy", "postgres", "postgresql.conf")),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		// Docker being unavailable is a real situation — a restricted CI
		// runner, a machine without it installed. Say so precisely rather
		// than failing with a connection error thirty frames deep.
		fmt.Fprintf(os.Stderr,
			"store tests need a database: set AZIR_TEST_DATABASE_URL "+
				"(see `make test-db`) or make Docker available.\nunderlying error: %v\n", err)
		os.Exit(1)
	}

	testDSN, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("connection string: %v", err)
	}

	code := m.Run()

	// Terminate explicitly: os.Exit skips deferred functions.
	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "could not terminate test container: %v\n", err)
	}
	os.Exit(code)
}
