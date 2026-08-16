package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/dreulavelle/azir/internal/testsupport"
)

// testDSN and usingOwnPostgres are resolved once for the package.
var (
	testDSN          string
	usingOwnPostgres bool
)

// TestMain provides a database for the whole package.
//
// A skipped test looks exactly like a passing one, so this never skips: it
// uses AZIR_TEST_DATABASE_URL when set, and otherwise starts the deployment's
// own Postgres image with the deployment's own configuration.
func TestMain(m *testing.M) {
	ctx := context.Background()

	dsn, own, stop, err := testsupport.PostgresDSN(ctx)
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	testDSN, usingOwnPostgres = dsn, own

	code := m.Run()
	stop()
	os.Exit(code)
}
