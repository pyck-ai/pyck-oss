// Real-Postgres harness for TestPG_* tests in the service/stock package.
//
// The harness starts a postgres:18-alpine container once per test-binary
// invocation via TestMain, applies all inventory migrations to a per-test
// database, and exposes helpers (newPGTestEnv, openPGEntClient) that
// return a fully-migrated *ent.Client backed by real Postgres. The
// SQLite-backed tests that form the bulk of this package are completely
// unaffected: they do not touch the PG harness.
//
// Harness boot:
//   - If Docker is unavailable the container start silently fails; pkgPG
//     stays nil and every TestPG_ caller skips with t.Skip.
//   - If SKIP_PG_TESTS=1 is set in the environment the container is never
//     started, which is useful on machines where Docker exists but the test
//     suite is intentionally kept lightweight.
//
// To run only the PG-backed tests (from backend/inventory):
//
//	GOWORK=off go test ./service/stock/ -run 'TestPG_' -count=1 -v -timeout 5m
//

//nolint:testpackage // in-package test: accesses package-private types.
package stock

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" driver for sql.Open

	"github.com/pyck-ai/pyck/backend/common/authn"
	commondb "github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/request"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entmigrate "github.com/pyck-ai/pyck/backend/inventory/ent/migrate"
)

const pgTestImage = "postgres:18-alpine"

var (
	pkgPG          *pgHandle
	pkgPGTerminate func()
)

var pgIdentRe = regexp.MustCompile(`[^a-z0-9_]`)

// init disables testcontainers-go's ryuk reaper for this test binary.
// Ryuk is a sidecar container that cleans up orphaned test containers; it
// needs to reach the Docker daemon socket, which is permission-denied in
// sandboxed environments. We register t.Cleanup and call Terminate ourselves
// so ryuk is unnecessary. See resolvers/resolver_test.go for the full rationale.
//
//nolint:gochecknoinits
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}

// TestMain starts the shared Postgres container (once per test binary) before
// running any tests, then tears it down on exit. SQLite-backed tests are not
// affected by this TestMain: they open their own in-process databases via
// enttest.Open.
func TestMain(m *testing.M) {
	if os.Getenv("SKIP_PG_TESTS") != "1" {
		startPGForPackage()
	}
	code := m.Run()
	if pkgPGTerminate != nil {
		pkgPGTerminate()
	}
	os.Exit(code)
}

// startPGForPackage starts the postgres:18-alpine testcontainer and populates
// pkgPG. Startup errors are silently swallowed; callers use requirePG to skip
// individual tests when pkgPG is nil.
func startPGForPackage() {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, pgTestImage,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return
	}

	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(context.Background())
		return
	}
	mapped, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = c.Terminate(context.Background())
		return
	}

	pkgPG = &pgHandle{
		host:     host,
		port:     uint32(mapped.Num()),
		user:     "postgres",
		password: "postgres",
	}
	pkgPGTerminate = func() {
		termCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Terminate(termCtx)
	}
}

// pgHandle carries the coordinates of the shared Postgres test container.
type pgHandle struct {
	host     string
	port     uint32
	user     string
	password string
}

// adminDSN returns a DSN that targets the bootstrap "postgres" database.
// Used to CREATE / DROP per-test databases.
func (p pgHandle) adminDSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable",
		p.host, p.port, p.user, p.password)
}

// dsn returns a DSN for the named per-test database. search_path=inventory
// ensures bare-table-name SQL (e.g. SELECT ... FROM "stocks") lands in the
// inventory schema, matching production behaviour.
func (p pgHandle) dsn(dbName string) string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable search_path=inventory",
		p.host, p.port, p.user, p.password, dbName)
}

// requirePG skips t when the package-level Postgres harness is unavailable.
func requirePG(t *testing.T) {
	t.Helper()
	if pkgPG == nil {
		t.Skip("Postgres not available (Docker not running or SKIP_PG_TESTS=1)")
	}
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
	requirePG(t)

	dbName := "t_" + pgIdentRe.ReplaceAllString(strings.ToLower(t.Name()), "_")
	if len(dbName) > 63 {
		dbName = dbName[:63]
	}

	adminDSN := pkgPG.adminDSN()
	testDSN := pkgPG.dsn(dbName)

	adminDB, err := sql.Open("pgx", adminDSN)
	require.NoError(t, err)
	_, err = adminDB.ExecContext(context.Background(), "CREATE DATABASE "+dbName)
	require.NoError(t, err)
	require.NoError(t, adminDB.Close())

	migDB, err := sql.Open("pgx", testDSN)
	require.NoError(t, err)
	require.NoError(t, commondb.RunMigrations(context.Background(), migDB, "inventory", entmigrate.Migrations))
	require.NoError(t, migDB.Close())

	t.Cleanup(func() {
		drop, derr := sql.Open("pgx", adminDSN)
		if derr != nil {
			t.Logf("pg drop db open: %v", derr)
			return
		}
		defer drop.Close()
		dropCtx := context.Background()
		_, _ = drop.ExecContext(dropCtx,
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", dbName)
		if _, derr := drop.ExecContext(dropCtx, "DROP DATABASE IF EXISTS "+dbName); derr != nil {
			t.Logf("pg drop db %s: %v", dbName, derr)
		}
	})

	client := enttest.Open(t, dialect.Postgres, testDSN,
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// newPGTestEnv creates an ancestorTestEnv backed by a real Postgres database.
// It is the PG equivalent of newAncestorTestEnv (which uses SQLite).
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
