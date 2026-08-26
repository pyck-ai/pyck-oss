// Package pgtest provides the shared Postgres testcontainer harness for
// DB-backed test binaries. A package's TestMain calls Start once to boot a
// postgres container for the whole binary; each test then calls
// CreateMigratedDB to allocate an isolated, fully-migrated database inside
// that container.
//
// Boot behaviour:
//   - SKIP_PG_TESTS=1 → no container is started; Start returns a nil handle
//     and Require skips every DB-backed test individually.
//   - Docker unavailable (and SKIP_PG_TESTS unset) → Start panics so CI
//     fails loudly instead of silently passing as a no-op.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for sql.Open

	commondb "github.com/pyck-ai/pyck/backend/common/db"
)

const (
	// Image is the postgres Docker image the harness starts. Pinned to a
	// specific minor version so test runs are reproducible across dev
	// machines and CI; bump deliberately rather than relying on :latest.
	Image = "postgres:18-alpine"

	// maxConnections raises the postgres default of 100: every parallel test
	// in a binary holds its own pooled connections against the one shared
	// container, and race tests open extra raw connections on top, so the
	// default can be exhausted on high-core runners ("FATAL: sorry, too many
	// clients already").
	maxConnections = 300
)

// init disables the testcontainers-go "ryuk" reaper for every binary that
// links this package.
//
// Ryuk is the sidecar container testcontainers normally spawns to clean up
// orphaned test containers if the parent process crashes hard (SIGKILL, OOM).
// It must reach the host's Docker socket from inside its own container, which
// fails on rootless-Docker dev machines (socket permission denied) and
// surfaces as the postgres container start failing with `unexpected container
// status "removing"`. We terminate the container ourselves via the Start
// terminate func, so ryuk's only added value (cleanup after a forcibly-killed
// test process) is traded for not hitting the socket-permission gotcha;
// stray containers from killed runs are found with
// `docker ps -a | grep postgres:18-alpine`.
//
//nolint:gochecknoinits // env must be set before testcontainers caches its config on first use.
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true"); err != nil {
			panic(fmt.Sprintf("pgtest: set TESTCONTAINERS_RYUK_DISABLED: %v", err))
		}
	}
}

// Handle carries the coordinates of the shared Postgres test container.
type Handle struct {
	host     string
	port     uint32
	user     string
	password string
}

// Start boots the shared postgres container for a test binary and returns a
// handle plus a terminate func the caller must invoke after m.Run() (the
// terminate func is always safe to call). label prefixes failure messages,
// conventionally the package under test.
//
// When SKIP_PG_TESTS=1 no container is started and the returned handle is
// nil; Require then skips each DB-backed test. Any other failure to start
// panics so the binary fails loudly rather than silently skipping.
func Start(label string) (*Handle, func()) {
	if os.Getenv("SKIP_PG_TESTS") == "1" {
		return nil, func() {}
	}

	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, Image,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
		// The official image's entrypoint treats a flag-shaped first arg as
		// arguments to the postgres server binary.
		testcontainers.WithCmdArgs("-c", fmt.Sprintf("max_connections=%d", maxConnections)),
	)
	if err != nil {
		panic(fmt.Sprintf("%s: failed to start postgres test container: %v", label, err))
	}

	host, err := c.Host(ctx)
	if err != nil {
		panicTerminated(c, fmt.Sprintf("%s: failed to get postgres container host: %v", label, err))
	}
	mapped, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		panicTerminated(c, fmt.Sprintf("%s: failed to get postgres container port: %v", label, err))
	}

	h := &Handle{
		host:     host,
		port:     uint32(mapped.Num()),
		user:     "postgres",
		password: "postgres",
	}
	terminate := func() {
		termCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if terr := c.Terminate(termCtx); terr != nil {
			fmt.Fprintf(os.Stderr, "%s: terminate postgres test container: %v\n", label, terr)
		}
	}
	return h, terminate
}

// panicTerminated terminates the half-started container, then panics with msg
// (annotated with the terminate error, if any).
func panicTerminated(c *tcpostgres.PostgresContainer, msg string) {
	if terr := c.Terminate(context.Background()); terr != nil {
		msg += fmt.Sprintf("; terminate: %v", terr)
	}
	panic(msg)
}

// Require skips t when the shared container was not started (SKIP_PG_TESTS=1
// was set, so Start returned a nil handle).
func Require(t *testing.T, h *Handle) {
	t.Helper()
	if h == nil {
		t.Skip("Postgres not available (SKIP_PG_TESTS=1)")
	}
}

// AdminDSN returns a DSN that targets the bootstrap "postgres" database.
// Used to CREATE / DROP the per-test databases.
func (h *Handle) AdminDSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable",
		h.host, h.port, h.user, h.password)
}

// DSN returns a DSN for the named per-test database. searchPath (the service
// schema, e.g. "inventory") routes bare-table-name SQL to that schema,
// matching how the production pool URI is shaped in backend/common/db.
func (h *Handle) DSN(dbName, searchPath string) string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable search_path=%s",
		h.host, h.port, h.user, h.password, dbName, searchPath)
}

var identRe = regexp.MustCompile(`[^a-z0-9_]`)

// dbSeq disambiguates per-test database names whose test names collide after
// truncation to postgres' 63-char identifier limit.
var dbSeq atomic.Int64

// UniqueDBName derives a unique, valid Postgres identifier (≤63 chars) for
// the calling test's database from t.Name(), appending a monotonically
// increasing suffix so repeated calls and truncation-colliding subtests still
// get distinct names.
func UniqueDBName(t *testing.T) string {
	t.Helper()
	suffix := fmt.Sprintf("_%d", dbSeq.Add(1))
	base := "t_" + identRe.ReplaceAllString(strings.ToLower(t.Name()), "_")
	if len(base)+len(suffix) > 63 {
		base = base[:63-len(suffix)]
	}
	return base + suffix
}

// CreateMigratedDB creates an isolated database for t inside the shared
// container, applies the service's embedded SQL migrations to it, and
// registers cleanup that drops the database again. It returns the DSN to
// open clients against. Skips t when the container was not started
// (SKIP_PG_TESTS=1).
func CreateMigratedDB(t *testing.T, h *Handle, service string, migrations fs.FS) string {
	t.Helper()
	Require(t, h)

	dbName := UniqueDBName(t)
	adminDSN := h.AdminDSN()
	dsn := h.DSN(dbName, service)

	adminDB, err := sql.Open("pgx", adminDSN)
	require.NoError(t, err)
	_, err = adminDB.ExecContext(context.Background(), "CREATE DATABASE "+dbName)
	require.NoError(t, err)
	require.NoError(t, adminDB.Close())

	migDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, commondb.RunMigrations(context.Background(), migDB, service, migrations))
	require.NoError(t, migDB.Close())

	t.Cleanup(func() {
		drop, derr := sql.Open("pgx", adminDSN)
		if derr != nil {
			t.Logf("pg drop db open: %v", derr)
			return
		}
		defer func() {
			if cerr := drop.Close(); cerr != nil {
				t.Logf("pg drop db close: %v", cerr)
			}
		}()
		dropCtx := context.Background()
		// Terminate lingering connections first or DROP fails with
		// "is being accessed by other users".
		if _, terr := drop.ExecContext(dropCtx,
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", dbName); terr != nil {
			t.Logf("pg terminate backends for %s: %v", dbName, terr)
		}
		if _, derr := drop.ExecContext(dropCtx, "DROP DATABASE IF EXISTS "+dbName); derr != nil {
			t.Logf("pg drop db %s: %v", dbName, derr)
		}
	})

	return dsn
}
