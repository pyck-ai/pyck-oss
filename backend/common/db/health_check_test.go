package db_test

import (
	"context"
	"database/sql"
	"net/url"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// openTestDb returns an in-memory SQLite database. SQLite stands in for
// PostgreSQL here because the health check only needs a driver that can
// answer SELECT 1; this keeps the tests runnable without a Postgres server.
func openTestDb(t *testing.T) *sql.DB {
	t.Helper()

	testDb, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { _ = testDb.Close() })
	return testDb
}

// TestDbHealthChecker_Healthy verifies the happy path: a reachable database
// passes the check. Because SQLite rejects the Postgres-only SHOW statement
// used by the informational isolation-level probe, a green result here also
// proves that a failing isolation lookup never fails the health check.
func TestDbHealthChecker_Healthy(t *testing.T) {
	t.Parallel()

	checker := db.NewDbHealthChecker(openTestDb(t))

	if err := checker.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck on healthy database returned error: %v", err)
	}
}

// TestDbHealthChecker_ClosedDatabase verifies that an unreachable database
// fails the check, which is what turns the /health/ready endpoint into a 503.
func TestDbHealthChecker_ClosedDatabase(t *testing.T) {
	t.Parallel()

	closedDb, err := sql.Open("sqlite3", "file::memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}
	if err := closedDb.Close(); err != nil {
		t.Fatalf("closing sqlite: %v", err)
	}

	checker := db.NewDbHealthChecker(closedDb)

	if err := checker.HealthCheck(context.Background()); err == nil {
		t.Fatal("HealthCheck on closed database returned nil, want error")
	}
}

// TestDbHealthChecker_CancelledContext verifies the check respects the
// probe's deadline: kubelet probes carry a ~1s timeout, and a check that
// ignores cancellation would report stale results.
func TestDbHealthChecker_CancelledContext(t *testing.T) {
	t.Parallel()

	checker := db.NewDbHealthChecker(openTestDb(t))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := checker.HealthCheck(ctx); err == nil {
		t.Fatal("HealthCheck with cancelled context returned nil, want error")
	}
}

// TestClose_ClosesHealthPool pins the driver invariant that the health pool
// is always initialized: Close closes it unconditionally (no nil guard), so
// every constructor — including the test-only one — must populate it. A nil
// healthDb would panic here.
func TestClose_ClosesHealthPool(t *testing.T) {
	t.Parallel()

	driver := db.NewMultiDriverWithDrivers(&fakeDriver{}, &fakeDriver{})

	if driver.HealthDB() == nil {
		t.Fatal("HealthDB() returned nil; the health pool must always be initialized")
	}
	if err := driver.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

// TestBuildHealthUri verifies the health pool's connection string keeps the
// writer URI's settings and tags the connection with a service-scoped
// application_name so probe connections are identifiable in
// pg_stat_activity.
func TestBuildHealthUri(t *testing.T) {
	t.Parallel()

	writerUri, err := db.BuildPoolUri(
		"postgres://user:pass@host:5432/dbname",
		"inventory",
		"serializable",
	)
	if err != nil {
		t.Fatalf("buildPoolUri returned error: %v", err)
	}

	parsed, err := url.Parse(db.BuildHealthUri(writerUri, "inventory"))
	if err != nil {
		t.Fatalf("parsing health URI: %v", err)
	}

	query := parsed.Query()
	if got, want := query.Get("application_name"), "inventory-health"; got != want {
		t.Errorf("application_name = %q, want %q", got, want)
	}
	if got, want := query.Get("search_path"), "inventory"; got != want {
		t.Errorf("search_path = %q, want %q", got, want)
	}
	if got, want := query.Get("default_transaction_isolation"), "serializable"; got != want {
		t.Errorf("default_transaction_isolation = %q, want %q", got, want)
	}
}
