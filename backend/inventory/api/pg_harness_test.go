// Real-Postgres harness for all tests in the api package, built on the shared
// backend/common/test/pgtest container harness: one postgres container per
// test binary, an isolated migrated database per test. See the pgtest package
// doc for boot behaviour (SKIP_PG_TESTS opt-out, panic when Docker is
// unavailable).
//
// To run (from backend/inventory):
//
//	go test ./api/... -count=1 -v -timeout 10m
package api_test

import (
	"os"
	"testing"

	"entgo.io/ent/dialect"

	"github.com/pyck-ai/pyck/backend/common/test/pgtest"

	"github.com/pyck-ai/pyck/backend/inventory/core"
	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entmigrate "github.com/pyck-ai/pyck/backend/inventory/ent/migrate"
)

var (
	pkgPG          *pgtest.Handle
	pkgPGTerminate func()
)

// TestMain starts the shared Postgres container (once per test binary) before
// running any tests, then tears it down on exit.
func TestMain(m *testing.M) {
	pkgPG, pkgPGTerminate = pgtest.Start("api")
	// The resolver layer reads core.Config.DbDriver for dialect-conditional
	// SQL (uniqueness validation, proc dispatch); leaving it empty would make
	// it emit SQLite syntax against the real Postgres database.
	core.Config.DbDriver = dialect.Postgres
	code := m.Run()
	pkgPGTerminate()
	os.Exit(code)
}

// openPGEntClientWithLogger allocates an isolated per-test Postgres database,
// applies all inventory migrations, opens an *ent.Client against it with the
// given log function, and registers cleanup.
func openPGEntClientWithLogger(t *testing.T, logFn func(...any)) *ent.Client {
	t.Helper()

	dsn := pgtest.CreateMigratedDB(t, pkgPG, "inventory", entmigrate.Migrations)
	client := enttest.Open(t, dialect.Postgres, dsn,
		enttest.WithOptions(ent.Log(logFn)),
	)
	t.Cleanup(func() {
		if cerr := client.Close(); cerr != nil {
			t.Logf("close ent client: %v", cerr)
		}
	})
	return client
}
