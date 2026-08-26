package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCounter keeps each test's schema name distinct within a run.
var testCounter atomic.Int64

// These tests exercise the SQL the tool's correctness rests on — the rollup
// invariant and the repair loop — against a real Postgres. They are gated on
// STOCKFIX_TEST_DB_URL so the module keeps its build-and-scp dependency set
// (pgx + testify) instead of pulling a container runtime in.
//
//	STOCKFIX_TEST_DB_URL=postgres://user:pass@localhost:5432/db?sslmode=disable \
//	  go test ./... -run TestPG
//
// Each test builds its own schema from scratch and drops it afterwards, so it
// needs nothing but CREATE privileges — no pyck database, no migrations.
const testDBURLEnv = "STOCKFIX_TEST_DB_URL"

const (
	tenantA = "aaaaaaaa-0000-4000-8000-000000000001"
	itemA   = "11111111-0000-4000-8000-000000000001"
)

// openTestSchema connects, creates a disposable schema holding the four tables
// the queries touch, and returns it with a cleanup that drops it.
func openTestSchema(t *testing.T) (*sql.DB, string) {
	t.Helper()

	url := os.Getenv(testDBURLEnv)
	if url == "" {
		t.Skipf("set %s to run the database-backed tests", testDBURLEnv)
	}

	ctx := context.Background()
	db, err := openDB(ctx, url)
	require.NoError(t, err)

	// A schema per test keeps parallel runs from colliding; the name has to
	// clear validateSchema, so it stays inside [a-z0-9_].
	schema := fmt.Sprintf("stockfix_t%d", os.Getpid()+int(testCounter.Add(1)))
	require.NoError(t, validateSchema(schema))

	ddl := []string{
		fmt.Sprintf(`CREATE SCHEMA %s`, schema),
		fmt.Sprintf(`CREATE TABLE %s.repositories (
			id uuid PRIMARY KEY, tenant_id uuid NOT NULL, name text NOT NULL,
			parent_id uuid, deleted_at timestamptz)`, schema),
		fmt.Sprintf(`CREATE TABLE %s.items (
			id uuid PRIMARY KEY, tenant_id uuid NOT NULL, sku text NOT NULL)`, schema),
		fmt.Sprintf(`CREATE TABLE %s.stocks (
			id uuid PRIMARY KEY, tenant_id uuid NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(), created_by uuid NOT NULL,
			deleted_at timestamptz,
			quantity bigint NOT NULL, incoming_stock bigint NOT NULL DEFAULT 0,
			outgoing_stock bigint NOT NULL DEFAULT 0, own_quantity bigint NOT NULL DEFAULT 0,
			own_incoming_stock bigint NOT NULL DEFAULT 0, own_outgoing_stock bigint NOT NULL DEFAULT 0,
			item_id uuid NOT NULL, repository_id uuid NOT NULL, movement_id uuid,
			version bigint NOT NULL,
			UNIQUE (tenant_id, repository_id, item_id, version))`, schema),
		fmt.Sprintf(`CREATE TABLE %s.item_movements (
			id uuid PRIMARY KEY, created_at timestamptz, executed_at timestamptz)`, schema),
		fmt.Sprintf(`CREATE TABLE %s.repository_movements (
			id uuid PRIMARY KEY, created_at timestamptz, executed_at timestamptz)`, schema),
	}
	for _, stmt := range ddl {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA %s CASCADE`, schema))
		db.Close()
	})

	return db, schema
}

// mkRepo inserts a repository; parent "" means root, deleted marks it soft-deleted.
func mkRepo(t *testing.T, db *sql.DB, schema, id, name, parent string, deleted bool) {
	t.Helper()
	var parentArg, deletedArg any
	if parent != "" {
		parentArg = parent
	}
	if deleted {
		deletedArg = time.Now()
	}
	_, err := db.ExecContext(context.Background(), fmt.Sprintf(
		`INSERT INTO %s.repositories (id, tenant_id, name, parent_id, deleted_at)
		 VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5)`, schema),
		id, tenantA, name, parentArg, deletedArg)
	require.NoError(t, err)
}

// mkStock inserts one stock row at the given version.
func mkStock(t *testing.T, db *sql.DB, schema, repo string, version, quantity, own int64) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), fmt.Sprintf(
		`INSERT INTO %s.stocks (id, tenant_id, created_by, quantity, own_quantity, item_id, repository_id, version)
		 VALUES (gen_random_uuid(), $1::uuid, $2::uuid, $3, $4, $5::uuid, $6::uuid, $7)`, schema),
		tenantA, tenantA, quantity, own, itemA, repo, version)
	require.NoError(t, err)
}

// currentQuantity returns the highest-version live quantity for a repository.
func currentQuantity(t *testing.T, db *sql.DB, schema, repo string) int64 {
	t.Helper()
	var q int64
	err := db.QueryRowContext(context.Background(), fmt.Sprintf(
		`SELECT quantity FROM %s.stocks
		 WHERE repository_id = $1::uuid AND item_id = $2::uuid AND deleted_at IS NULL
		 ORDER BY version DESC LIMIT 1`, schema), repo, itemA).Scan(&q)
	require.NoError(t, err)
	return q
}

// runRepair applies the repair in a committed transaction, as cmdFix does.
func runRepair(t *testing.T, db *sql.DB, schema string) *repairPlan {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, err)

	plan, err := repairTenant(ctx, tx, schema, tenantA, io.Discard)
	require.NoError(t, err)

	remaining, err := verifyAfterFix(ctx, tx, schema, tenantA, plan.Unfixable)
	require.NoError(t, err)
	require.Empty(t, remaining, "repair left the tenant violated")

	require.NoError(t, tx.Commit())
	return plan
}

const (
	repoRoot  = "22222222-0000-4000-8000-000000000001"
	repoMid   = "22222222-0000-4000-8000-000000000002"
	repoLeaf  = "22222222-0000-4000-8000-000000000003"
	repoOther = "22222222-0000-4000-8000-000000000004"
)

// Correcting a child moves its parent's expected sum. A parent that was
// consistent before the repair must be repriced by it, not left violated with
// the run reported as successful.
func TestPG_Repair_ParentRepricedAfterChild(t *testing.T) {
	db, schema := openTestSchema(t)

	mkRepo(t, db, schema, repoRoot, "root", "", false)
	mkRepo(t, db, schema, repoLeaf, "leaf", repoRoot, false)
	// leaf: stored 5 but owns 10 — violated. root: 0 own + leaf's stored 5 = 5 — consistent.
	mkStock(t, db, schema, repoLeaf, 0, 5, 10)
	mkStock(t, db, schema, repoRoot, 0, 5, 0)

	violations, err := rollupViolations(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	require.Len(t, violations, 1, "only the leaf starts out violated")

	plan := runRepair(t, db, schema)

	require.Len(t, plan.Corrective, 2, "the parent must be corrected along with the child")
	assert.Equal(t, repoLeaf, plan.Corrective[0].RepoID, "children are corrected first")
	assert.Equal(t, repoRoot, plan.Corrective[1].RepoID)
	assert.Equal(t, int64(10), currentQuantity(t, db, schema, repoLeaf))
	assert.Equal(t, int64(10), currentQuantity(t, db, schema, repoRoot))

	after, err := rollupViolations(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	assert.Empty(t, after)
}

// A parent and its child violated at once must converge in one run: the
// parent's correction has to be priced off the child's corrected quantity, not
// the stale one it carried at the start.
func TestPG_Repair_MultiLevelChainConverges(t *testing.T) {
	db, schema := openTestSchema(t)

	mkRepo(t, db, schema, repoRoot, "grand", "", false)
	mkRepo(t, db, schema, repoMid, "mid", repoRoot, false)
	mkRepo(t, db, schema, repoLeaf, "leaf", repoMid, false)
	mkStock(t, db, schema, repoLeaf, 0, 10, 10) // consistent
	mkStock(t, db, schema, repoMid, 0, 3, 0)    // violated: expected 10
	mkStock(t, db, schema, repoRoot, 0, 0, 0)   // violated: expected 3, then 10

	violations, err := rollupViolations(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	require.Len(t, violations, 2)

	plan := runRepair(t, db, schema)

	require.Len(t, plan.Corrective, 2)
	assert.Equal(t, repoMid, plan.Corrective[0].RepoID, "mid is corrected before grand")
	assert.Equal(t, repoRoot, plan.Corrective[1].RepoID)
	assert.Equal(t, int64(10), currentQuantity(t, db, schema, repoMid))
	assert.Equal(t, int64(10), currentQuantity(t, db, schema, repoRoot),
		"grand must be priced off mid's corrected quantity, not its stale 3")

	after, err := rollupViolations(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	assert.Empty(t, after)
}

// create_item_movement_proc's ancestor walk skips soft-deleted repositories, so
// their stock is frozen and must not count towards a parent's expected sum.
func TestPG_Rollup_IgnoresSoftDeletedRepositories(t *testing.T) {
	db, schema := openTestSchema(t)

	mkRepo(t, db, schema, repoRoot, "root", "", false)
	mkRepo(t, db, schema, repoLeaf, "deleted-child", repoRoot, true)
	mkStock(t, db, schema, repoLeaf, 0, 7, 7) // live stock under a dead repo
	mkStock(t, db, schema, repoRoot, 0, 0, 0)

	violations, err := rollupViolations(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	assert.Empty(t, violations, "a soft-deleted child must not invent a violation on its parent")
}

func TestPG_Quiescence_ReadsFingerprint(t *testing.T) {
	db, schema := openTestSchema(t)

	mkRepo(t, db, schema, repoRoot, "root", "", false)
	mkStock(t, db, schema, repoRoot, 0, 1, 1)
	mkStock(t, db, schema, repoRoot, 1, 2, 2)

	before, err := quiescence(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	assert.Equal(t, quiescenceMark{Rows: 2, VersionS: 1}, before)

	// An append moves both halves, which is what the guard watches for.
	mkStock(t, db, schema, repoRoot, 2, 3, 3)
	after, err := quiescence(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	assert.Equal(t, quiescenceMark{Rows: 3, VersionS: 3}, after)
	assert.NotEqual(t, before, after)
}

// A parent must wait for every violation below it, not just its direct
// children: an intermediate node that is consistent today becomes violated once
// its own child is corrected, and correcting the top too early writes a row
// against a value that is about to move.
func TestPG_Repair_GrandparentWaitsForDeepDescendant(t *testing.T) {
	db, schema := openTestSchema(t)

	mkRepo(t, db, schema, repoRoot, "grand", "", false)
	mkRepo(t, db, schema, repoMid, "mid", repoRoot, false)
	mkRepo(t, db, schema, repoLeaf, "leaf", repoMid, false)
	mkStock(t, db, schema, repoLeaf, 0, 5, 10) // violated: expected 10
	mkStock(t, db, schema, repoMid, 0, 5, 0)   // consistent with leaf's stored 5
	mkStock(t, db, schema, repoRoot, 0, 9, 0)  // violated: expected 5, then 10

	plan := runRepair(t, db, schema)

	assert.Len(t, plan.Corrective, 3, "each pair is corrected exactly once")
	assert.Equal(t, int64(10), currentQuantity(t, db, schema, repoRoot))

	after, err := rollupViolations(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	assert.Empty(t, after)
}

// Siblings are independent: correcting one must not disturb the other, and the
// parent is repriced once off both.
func TestPG_Repair_SiblingsAndParent(t *testing.T) {
	db, schema := openTestSchema(t)

	mkRepo(t, db, schema, repoRoot, "root", "", false)
	mkRepo(t, db, schema, repoLeaf, "leaf-a", repoRoot, false)
	mkRepo(t, db, schema, repoOther, "leaf-b", repoRoot, false)
	mkStock(t, db, schema, repoLeaf, 0, 1, 4)  // violated: expected 4
	mkStock(t, db, schema, repoOther, 0, 2, 6) // violated: expected 6
	mkStock(t, db, schema, repoRoot, 0, 3, 0)  // expected 1+2=3 now, 10 after

	plan := runRepair(t, db, schema)

	require.Len(t, plan.Corrective, 3)
	assert.Equal(t, repoRoot, plan.Corrective[2].RepoID, "the parent goes last")
	assert.Equal(t, int64(10), currentQuantity(t, db, schema, repoRoot))

	after, err := rollupViolations(context.Background(), db, schema, tenantA)
	require.NoError(t, err)
	assert.Empty(t, after)
}
