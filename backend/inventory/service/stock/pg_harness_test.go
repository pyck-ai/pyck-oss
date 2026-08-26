// Real-Postgres harness for all tests in the service/stock package, built on
// the shared backend/common/test/pgtest container harness: one postgres
// container per test binary, an isolated migrated database per test. See the
// pgtest package doc for boot behaviour (SKIP_PG_TESTS opt-out, panic when
// Docker is unavailable).
//
// To run the full suite (from backend/inventory):
//
//	go test ./service/stock/... -count=1 -v -timeout 10m

//nolint:testpackage // in-package test: accesses package-private types.
package stock

import (
	"context"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/pgtest"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entmigrate "github.com/pyck-ai/pyck/backend/inventory/ent/migrate"
)

var (
	pkgPG          *pgtest.Handle
	pkgPGTerminate func()
)

// TestMain starts the shared Postgres container (once per test binary) before
// running any tests, then tears it down on exit.
func TestMain(m *testing.M) {
	pkgPG, pkgPGTerminate = pgtest.Start("service/stock")
	code := m.Run()
	pkgPGTerminate()
	os.Exit(code)
}

// openPGEntClient allocates an isolated per-test Postgres database, applies
// all inventory migrations (which installs the stocks unique index that the
// OCC tests depend on), opens an *ent.Client against it, and registers
// cleanup. The caller receives a ready-to-use, fully-migrated ent client.
//
// enttest.Open's Schema.Create call is harmless on a fully-migrated database:
// Atlas detects existing structures and treats them as no-ops.
func openPGEntClient(t *testing.T) *ent.Client {
	t.Helper()
	return openPGEntClientWithLogger(t, t.Log)
}

// openPGEntClientWithLogger is like openPGEntClient but accepts a custom log
// function. Use this when a test needs to intercept query strings (e.g. to
// assert on LIMIT 1 existence probes emitted by the debug driver).
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

// newPGTestEnv creates an ancestorTestEnv backed by a real Postgres database.
// All helper methods on ancestorTestEnv (mkRepo, mkItem, mkStock, withTx, etc.)
// work identically against Postgres.
func newPGTestEnv(t *testing.T) *ancestorTestEnv {
	t.Helper()
	client := openPGEntClient(t)
	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)
	return &ancestorTestEnv{t: t, client: client, ctx: ctx, tenantID: tenantID}
}
