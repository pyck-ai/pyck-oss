package resolvers_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/contrib/entgql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/resolvers"
)

// TestCurrentStocksIndexPlanPostgres validates the #1346 acceptance criterion —
// a scoped current-stock read served by an index (not a full table scan), fast,
// and never silently truncated — against real Postgres.
//
// It seeds a hot item across many repositories, each with many stock versions
// (approximating the eu-prod hot item: ~7,900 rows / ~216 repos), then:
//
//   - proves the head-selection query the resolver emits can be served by an
//     index, forcing enable_seqscan=off so a small test table cannot mask the
//     available index behind a legitimately-cheaper sequential scan (at prod
//     scale the scoped predicate must index-fetch only the ~thousands of scoped
//     rows rather than scan the whole ledger);
//   - proves currentStocks paginates over a scope larger than the platform's
//     200-row default limit without dropping repositories — the regression the
//     LimitMixin cap would otherwise cause;
//   - logs the natural plans and execution times for the NOT EXISTS (what the
//     resolver emits) and DISTINCT ON (the criterion's named shape) queries.
//     Single-digit ms is a prod-replica figure; embedded Postgres differs, so
//     these are reported, not asserted.
//
//nolint:tparallel,paralleltest // subtests share one seeded database and run sequentially
func TestCurrentStocksIndexPlanPostgres(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	const (
		repos           = 216
		versionsPerRepo = 37
	)

	item := te.newItem(ctx, userA).Create()
	repoIDs := make([]uuid.UUID, repos)
	for i := range repoIDs {
		repoIDs[i] = te.newRepository(ctx, userA).Create().ID
	}

	seedStockLedgerRaw(t, ctx, te, userA.TenantID, userA.ID, item.ID, repoIDs, versionsPerRepo)
	pgExec(t, ctx, te, "ANALYZE stocks")

	inList := uuidInList(repoIDs)

	// The head-selection query the resolver actually emits (DistinctOnExists:
	// scope predicates on both the outer scan and the correlated NOT EXISTS
	// subquery, superseding by version).
	notExistsSQL := fmt.Sprintf(`
		SELECT s.id FROM stocks s
		WHERE s.tenant_id = $1 AND s.item_id = $2 AND s.repository_id IN (%s)
		AND NOT EXISTS (
			SELECT 1 FROM stocks s2
			WHERE s2.repository_id = s.repository_id
			  AND s2.item_id = s.item_id
			  AND s2.version > s.version
			  AND s2.tenant_id = $1 AND s2.item_id = $2 AND s2.repository_id IN (%s)
		)`, inList, inList)

	// The shape named in the acceptance criterion, for comparison.
	distinctOnSQL := fmt.Sprintf(`
		SELECT DISTINCT ON (s.repository_id, s.item_id) s.id
		FROM stocks s
		WHERE s.tenant_id = $1 AND s.item_id = $2 AND s.repository_id IN (%s) AND s.deleted_at IS NULL
		ORDER BY s.repository_id, s.item_id, s.version DESC`, inList)

	t.Run("head-selection is servable by an index, not a full table scan", func(t *testing.T) {
		forcedPlan, _ := pgExplain(t, ctx, te, true, notExistsSQL, userA.TenantID, item.ID)
		t.Logf("NOT EXISTS plan (enable_seqscan=off):\n%s", forcedPlan)

		// With sequential scans disabled Postgres still produces a plan: an
		// index-backed one exists for the scoped fetch. (It uses the
		// (repository_id, item_id, created_at) index rather than the version
		// unique index — equivalent for narrowing to the scoped rows.)
		assert.Contains(t, forcedPlan, "Index Scan",
			"the scoped head-selection must have an index-based access path")
	})

	t.Run("currentStocks paginates over the 200-row cap without dropping repositories", func(t *testing.T) {
		where := &ent.StockWhereInput{ItemID: &item.ID, RepositoryIDIn: repoIDs}
		r := resolvers.NewResolver("inventory", te.Ent, nil, te.StockService)

		// 200 is the platform-wide LimitMixin cap (first+1 == 201 is the max
		// accepted), so a scope of 216 needs two pages. If head-row selection
		// were itself capped at 200, the second page would be empty and the tail
		// 16 repositories would vanish — the silent-truncation regression.
		const pageSize = 200
		first := pageSize
		seen := make(map[uuid.UUID]int64, repos)
		var after *entgql.Cursor[uuid.UUID]
		for range 5 {
			conn, err := r.Query().CurrentStocks(ctx, after, &first, nil, nil, nil, where)
			require.NoError(t, err)
			if len(conn.Edges) == 0 {
				break
			}
			for _, e := range conn.Edges {
				seen[e.Node.RepositoryID] = e.Node.Quantity
			}
			after = &conn.Edges[len(conn.Edges)-1].Cursor
			if len(conn.Edges) < pageSize {
				break
			}
		}

		require.Len(t, seen, repos, "pagination must cover every repository in the scope")
		for _, q := range seen {
			// quantity was seeded equal to version, so the current (highest
			// version) row carries quantity == versionsPerRepo-1.
			assert.EqualValues(t, versionsPerRepo-1, q)
		}
	})

	// #1572: inventoryItems(where: {hasCurrentStockWith: [{quantityGT: 0}]}) as
	// itemHasCurrentStockPredicate emits it (see item_current_stock_test.go).
	// The correlated EXISTS must reach the item's rows through the
	// (tenant_id, item_id, repository_id, version DESC) index, not a scan of
	// the tenant's ledger.
	t.Run("items with current stock are served by the (tenant, item) index", func(t *testing.T) {
		itemsSQL := `
			SELECT i.id FROM items i
			WHERE EXISTS (
				SELECT 1 FROM stocks cs
				WHERE cs.item_id = i.id AND cs.tenant_id = i.tenant_id
				  AND cs.tenant_id IN ($1)
				  AND (cs.deleted_at IS NULL OR cs.deleted_at = '0001-01-01 00:00:00')
				  AND NOT EXISTS (
					SELECT 1 FROM stocks s2
					WHERE s2.repository_id = cs.repository_id AND s2.item_id = cs.item_id
					  AND s2.version > cs.version AND s2.tenant_id = i.tenant_id)
				  AND (cs.quantity <> 0 OR cs.incoming_stock <> 0 OR cs.outgoing_stock <> 0
					OR cs.own_quantity <> 0 OR cs.own_incoming_stock <> 0 OR cs.own_outgoing_stock <> 0)
				  AND cs.quantity > 0)
			AND i.tenant_id IN ($1) AND i.deleted_at IS NULL
			ORDER BY i.id LIMIT 51`

		// Besides the hot item, clone 2,000 items with 3 repositories x 4
		// versions each, so item_id is selective like in prod, then ANALYZE.
		pgExec(t, ctx, te, `INSERT INTO items
			SELECT (jsonb_populate_record(i, jsonb_build_object('id', gen_random_uuid(), 'sku', 'plan-' || g))).*
			FROM items i, generate_series(1, 2000) g WHERE i.id = $1`, item.ID)
		pgExec(t, ctx, te, `INSERT INTO stocks (id, tenant_id, created_at, created_by, quantity, item_id, repository_id, version)
			SELECT gen_random_uuid(), i.tenant_id, now(), $1::uuid, v, i.id, r, v
			FROM items i, unnest($2::uuid[]) r, generate_series(0, 3) v
			WHERE i.sku LIKE 'plan-%'`, userA.ID, "{"+strings.ReplaceAll(uuidInList(repoIDs[:3]), "'", "")+"}")
		pgExec(t, ctx, te, "ANALYZE items")
		pgExec(t, ctx, te, "ANALYZE stocks")

		// Forbid hash/merge joins and bitmap scans: the item side is a page of
		// 51 rows, so the plan to check is the nested loop that probes stocks
		// per item.
		plan, ms := pgExplainWith(t, ctx, te,
			[]string{"enable_seqscan = off", "enable_hashjoin = off", "enable_mergejoin = off", "enable_bitmapscan = off"},
			itemsSQL, userA.TenantID)
		t.Logf("items with current stock plan (nested loop forced), %.3f ms:\n%s", ms, plan)

		const wantIndex = "stock_tenant_id_item_id_repository_id_version"
		assert.Contains(t, plan, "using "+wantIndex+" on stocks cs",
			"the current-row lookup must use the (tenant_id, item_id, repository_id, version DESC) index")
		// The superseding-row probe is correlated on tenant, repository and
		// item, so either tenant-led index serves it (the planner picks the
		// created_at one here: 4 rows per pair); it must not scan.
		assert.Regexp(t, `Index (Only )?Scan using stock_tenant_id_\w+ on stocks s2`, plan,
			"the superseding-row lookup must use a tenant-led index")
		assert.NotContains(t, plan, "Seq Scan on stocks")
	})

	t.Run("report natural plans and execution times", func(t *testing.T) {
		nePlan, neMs := pgExplain(t, ctx, te, false, notExistsSQL, userA.TenantID, item.ID)
		doPlan, doMs := pgExplain(t, ctx, te, false, distinctOnSQL, userA.TenantID, item.ID)
		t.Logf("NOT EXISTS (natural) — %.3f ms execution\n%s", neMs, nePlan)
		t.Logf("DISTINCT ON (natural) — %.3f ms execution\n%s", doMs, doPlan)
		t.Logf("head-selection latency over %d rows: NOT EXISTS %.3f ms vs DISTINCT ON %.3f ms",
			repos*versionsPerRepo, neMs, doMs)
	})
}

// seedStockLedgerRaw bulk-inserts versionsPerRepo stock rows per repository via
// raw SQL, bypassing the ent mutation hooks. quantity is set equal to version so
// the current (highest-version) row is identifiable. All rows are live.
func seedStockLedgerRaw(t *testing.T, ctx context.Context, te *testEnv, tenantID, createdBy, itemID uuid.UUID, repoIDs []uuid.UUID, versionsPerRepo int) {
	t.Helper()

	const cols = 8
	now := time.Now().UTC()

	rows := make([][]any, 0, len(repoIDs)*versionsPerRepo)
	for _, repoID := range repoIDs {
		for v := range versionsPerRepo {
			rows = append(rows, []any{
				uuid.New(), tenantID, now, createdBy, itemID, repoID, int64(v), int64(v),
			})
		}
	}

	const chunk = 1000
	for start := 0; start < len(rows); start += chunk {
		end := min(start+chunk, len(rows))
		batch := rows[start:end]

		var sb strings.Builder
		sb.WriteString(`INSERT INTO stocks (id, tenant_id, created_at, created_by, item_id, repository_id, quantity, version) VALUES `)
		args := make([]any, 0, len(batch)*cols)
		for i, r := range batch {
			if i > 0 {
				sb.WriteByte(',')
			}
			base := i * cols
			fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)
			args = append(args, r...)
		}
		pgExec(t, ctx, te, sb.String(), args...)
	}
}

func uuidInList(ids []uuid.UUID) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "'" + id.String() + "'"
	}
	return strings.Join(quoted, ", ")
}

// rollbackAfterCommit unwinds a probe transaction. sql.ErrTxDone is expected
// when the body already committed; anything else is worth seeing in the log.
func rollbackAfterCommit(t *testing.T, tx *ent.Tx) {
	t.Helper()
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		t.Logf("rollback probe tx: %v", err)
	}
}

// pgExec runs a statement in its own transaction via the ent tx driver (raw SQL,
// no ent mutation hooks).
func pgExec(t *testing.T, ctx context.Context, te *testEnv, query string, args ...any) {
	t.Helper()
	tx, err := te.Ent.Tx(ctx)
	require.NoError(t, err)
	defer rollbackAfterCommit(t, tx)
	_, err = tx.ExecContext(ctx, query, args...)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

// pgExplain runs EXPLAIN (ANALYZE, BUFFERS) and returns the plan text and the
// reported execution time in milliseconds. When forceIndex is set it disables
// sequential scans for the statement so an available index path is revealed even
// on a small table where the planner would legitimately prefer a seq scan.
func pgExplain(t *testing.T, ctx context.Context, te *testEnv, forceIndex bool, query string, args ...any) (plan string, execMs float64) {
	t.Helper()
	var settings []string
	if forceIndex {
		settings = append(settings, "enable_seqscan = off")
	}
	return pgExplainWith(t, ctx, te, settings, query, args...)
}

// pgExplainWith is pgExplain with arbitrary SET LOCAL settings, e.g.
// "enable_seqscan = off".
func pgExplainWith(t *testing.T, ctx context.Context, te *testEnv, settings []string, query string, args ...any) (plan string, execMs float64) {
	t.Helper()
	tx, err := te.Ent.Tx(ctx)
	require.NoError(t, err)
	defer rollbackAfterCommit(t, tx)

	for _, setting := range settings {
		_, err = tx.ExecContext(ctx, "SET LOCAL "+setting)
		require.NoError(t, err)
	}

	rows, err := tx.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query, args...)
	require.NoError(t, err)
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("close explain rows: %v", cerr)
		}
	}()

	var b strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		b.WriteString(line)
		b.WriteByte('\n')
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "Execution Time:") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 {
				parsed, perr := strconv.ParseFloat(fields[2], 64)
				require.NoError(t, perr)
				execMs = parsed
			}
		}
	}
	require.NoError(t, rows.Err())
	return b.String(), execMs
}
