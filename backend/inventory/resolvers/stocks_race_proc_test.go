// stocks_race_proc_test.go is the second DELIBERATELY-RED regression for
// the production bug from
// issue-stock-map-resets-own-quantity-to-zero-on-pending-pick-creation.md.
//
// Where stocks_race_test.go pins the bug in the Go orchestration path
// (createItemMovementViaGo, used when WithDeferredUnderflow is set —
// i.e. the collection-movement entry point used by the picking
// workflow), this file pins the SAME architectural bug in the
// independent Postgres fast-path:
// `inventory.create_item_movement_proc` (the PL/pgSQL function
// installed by migration 20260430070249_create_item_movement_proc.up.sql).
//
// The Go path and the proc path are independent code with the same
// shape: each reads a snapshot of the stocks table, then later inserts
// a new row that combines the stale snapshot (for Quantity / OwnQuantity)
// with a fresh per-(repo, item) version. Any fix must cover BOTH
// surfaces; we therefore pin both with separate tests so a fix that
// only addresses one is immediately visible.
//
// ─── Bug in the proc, in plain English ────────────────────────────────
//
// The proc's body is roughly:
//
//	1. Build pg_temp.tmp_stock_deltas: ONE SELECT per (repo, item) on
//	   the ancestor closure, taking the latest stocks row by
//	   `ORDER BY s2.version DESC LIMIT 1` (migration line 196-207). This
//	   captures base_quantity, base_own_quantity, and friends.
//	2. LOOP { BEGIN ... savepoint ... }:
//	     a. INSERT into item_movements (uses v_movement_id, idempotent
//	        across retries because the savepoint rolls back the failed
//	        attempt).
//	     b. INSERT into stocks. The new row's `version` is computed
//	        inline by `SELECT MAX(version)+1` over `stocks`. The new
//	        row's `quantity` / `own_quantity` come from
//	        pg_temp.tmp_stock_deltas, which was populated BEFORE the
//	        LOOP and is NOT refreshed on retry.
//	     c. EXCEPTION WHEN unique_violation: sleep+retry, up to 50 times.
//
// Step 1 runs ONCE; step 2 can run multiple times due to the OCC
// retry. If another transaction commits a new stocks row for the same
// (repo, item) DURING the proc's LOOP, the proc's first INSERT attempt
// will collide on the unique index, the savepoint rolls back, the
// EXCEPTION handler bumps the retry counter, sleeps, and re-enters
// the BEGIN block. The next INSERT picks `MAX(version)+1` over the
// NOW-VISIBLE rows and succeeds at the new version. But the
// `base_quantity` / `base_own_quantity` that fed the INSERT's CASE
// expressions are still the ORIGINAL stale values from tmp_stock_deltas.
//
// Result: a stocks row at a fresh version, with stale Quantity /
// OwnQuantity that overrides the value committed by the racing tx.
//
// This is the proc-side analogue of the Go path's stale-stockMap bug.
// Same architectural defect, two implementations.
//
// ─── How this test pins the proc race DETERMINISTICALLY ──────────────
//
// Unlike the Go path test, we do NOT need a custom driver wrapper.
// The proc has the OCC retry loop built in, and Postgres' unique-index
// blocking gives us a natural synchronisation primitive:
//
//	t=0  test:  set up tree {warehouse, slot, box} + item; place 1
//	            unit at slot (executed, committed). The placement
//	            writes two stocks rows for slot — version=0 from the
//	            CREATE step (own_q=0) and version=1 from the EXECUTE
//	            step (own_q=1). Latest by version: v=1, own_q=1.
//
//	t=1  goA:   open a separate pgx connection (application_name
//	            "race-A"), BEGIN tx, INSERT a stocks row for
//	            (slot, item) at version=2 with own_quantity=14
//	            (mimics a concurrent EXECUTE of a placement that
//	            lifted the count from 1 → 14). Leave the tx OPEN.
//
//	t=2  goB:   open another pgx connection (application_name "race-B"),
//	            call inventory.create_item_movement_proc(...) to create
//	            a pending pick (slot → box, qty=1). The proc runs:
//	              • Availability check sees the LATEST visible row
//	                (v=1, own_q=1) — goA's v=2 is invisible. Passes
//	                (1 ≥ 1).
//	              • tmp_stock_deltas captures base_own_quantity = 1
//	                from v=1 (the highest visible version). THIS IS
//	                THE STALE SNAPSHOT.
//	              • Enters LOOP, tries INSERT at MAX(version)+1 = 2.
//	                BLOCKS on goA's unique-index reservation for v=2.
//
//	t=3  test:  poll pg_stat_activity for goB to be in `wait_event_type=
//	            'Lock'`. Once we see B blocked, commit goA.
//
//	t=4  goA:   COMMIT. goB's INSERT wakes up, fails with
//	            unique_violation 23505 (goA now owns v=2).
//
//	t=5  goB:   proc's EXCEPTION handler runs, sleeps a few ms, retries.
//	            On retry, MAX(version)+1 = 3 (sees goA's row at v=2).
//	            INSERT at v=3 with base_own_quantity = 1 (from the
//	            STALE tmp_stock_deltas — NOT refreshed on retry).
//	            SUCCESS.
//
//	t=6  test:  read latest stocks row by version. With the bug:
//	            version=3, own_quantity=1 (stale, masks goA's 14).
//	            With the fix: version=3, own_quantity=14 (preserved).
//
// The test asserts own_quantity = 14. Pre-fix it FAILS with
// own_quantity=1; post-fix it PASSES.
//
// ─── Why Postgres-only ────────────────────────────────────────────────
//
// The proc is PL/pgSQL — it does not exist on SQLite at all. There is
// no SQLite reproducer because there is no SQLite code path to test.

package resolvers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// TestCreateItemMovementProc_StaleBaselineUnderConcurrentExecute_Postgres
// reproduces the proc-side surface of the production bug. See the
// top-of-file comment for the narrative.
//
// Expected state on `main` (before the fix):  FAIL with own_quantity=1
// Expected state once the fix lands:           PASS with own_quantity=14
func TestCreateItemMovementProc_StaleBaselineUnderConcurrentExecute_Postgres(t *testing.T) {
	t.Parallel()

	pg := startEmbeddedPostgres(t)
	// We reuse setupPostgresWithGate purely for the DSN it returns; the
	// gate is never armed in this test. The proc-side bug surfaces
	// through the proc's own retry loop and PG's unique-index lock
	// waiting, no driver-level intervention required.
	env, _, testDSN := setupPostgresWithGate(t, pg)
	apiClient := setupAPIClient(t, env)
	ctx := env.ctx(userA)

	// ── Step 1: build the world via the normal API ───────────────────
	//
	// Tree:
	//
	//	virtual  (virtual)
	//	warehouse (root)
	//	├─ slot   ← FROM of the pending pick
	//	└─ box    ← TO of the pending pick
	//
	// We seed via the API (not via raw pgx) so the items / repositories
	// land with correctly-populated audit columns, tenant scoping, and
	// all the JSON validation the resolver enforces.
	virtualID := stockTestCreateRepository(t, ctx, apiClient, "virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	slotID := stockTestCreateRepository(t, ctx, apiClient, "slot", entrepository.TypeStatic, false, &warehouseID)
	boxID := stockTestCreateRepository(t, ctx, apiClient, "box", entrepository.TypeStatic, false, &warehouseID)
	itemID := stockTestCreateItem(t, ctx, apiClient, "race-proc-item")

	// Place ONE unit at slot so the proc's availability check at
	// migration line 95-114 has stock to allow our subsequent pick.
	// We deliberately place ONLY 1, not 14, so the post-race assertion
	// can distinguish "own_q=1 (stale baseline, BUG)" from "own_q=14
	// (preserved from goA's racing row, FIX)".
	placeMvID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, slotID, 1)
	stockTestExecuteItemMovement(t, ctx, apiClient, placeMvID)

	tenantID := userA.TenantID
	createdBy := userA.ID

	// Sanity: the seed leaves TWO stocks rows for slot — version=0 from
	// the placement CREATE step (writes own_q=0, the baseline at that
	// instant) and version=1 from the placement EXECUTE step (applies
	// the +1 delta, own_q=1). Latest by version is therefore v=1, own_q=1.
	seedVer, seedOwn := readLatestStock(t, ctx, testDSN, tenantID, slotID, itemID)
	require.Equal(t, int64(1), seedVer, "seed stocks row (latest by version) should be version=1")
	require.Equal(t, int64(1), seedOwn, "seed stocks row (latest by version) should have own_quantity=1")

	// ── Step 2: start goroutine A — the racing EXECUTE ───────────────
	//
	// goA holds a pgx connection with a long-lived tx that has
	// INSERTed a stocks row at version=2 with own_quantity=14. The
	// version-2 row is what the proc will collide with on its first
	// INSERT attempt (the proc would also pick MAX(version)+1 = 2),
	// and the unique-index conflict is our synchronisation primitive
	// (it forces the proc to wait until A commits).
	//
	// We tag the connection with `application_name=race-A` so the test
	// can identify it in pg_stat_activity while we poll for goB's lock
	// wait (we filter goA OUT to avoid false positives on goA's own
	// inactive backend).
	aReady := make(chan struct{})
	aCommit := make(chan struct{})
	aDone := make(chan struct{})
	aErr := make(chan error, 1)

	go func() {
		defer close(aDone)

		connA, err := pgxConnectWithAppName(ctx, testDSN, "race-A")
		if err != nil {
			aErr <- fmt.Errorf("A: connect: %w", err)
			close(aReady)
			return
		}
		defer connA.Close(ctx)

		tx, err := connA.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			aErr <- fmt.Errorf("A: begin: %w", err)
			close(aReady)
			return
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO inventory.stocks (
				id, tenant_id, created_at, created_by,
				repository_id, item_id, movement_id,
				quantity, incoming_stock, outgoing_stock,
				own_quantity, own_incoming_stock, own_outgoing_stock,
				version
			) VALUES (
				gen_random_uuid(), $1, now(), $2,
				$3, $4, gen_random_uuid(),
				14, 0, 0,
				14, 0, 0,
				2
			)`,
			tenantID, createdBy,
			uuid.MustParse(slotID), uuid.MustParse(itemID),
		)
		if err != nil {
			_ = tx.Rollback(ctx)
			aErr <- fmt.Errorf("A: insert: %w", err)
			close(aReady)
			return
		}

		close(aReady)
		<-aCommit

		if cerr := tx.Commit(ctx); cerr != nil {
			aErr <- fmt.Errorf("A: commit: %w", cerr)
		}
	}()

	select {
	case <-aReady:
	case <-time.After(10 * time.Second):
		t.Fatal("goroutine A never reached the INSERT/READY state within 10s")
	}
	select {
	case err := <-aErr:
		t.Fatalf("goroutine A failed before pause: %v", err)
	default:
	}

	// ── Step 3: start goroutine B — the proc call ────────────────────
	//
	// B invokes inventory.create_item_movement_proc to create a pending
	// pick (slot → box, qty=1). The proc:
	//
	//   1. Resolves FROM/TO virtuality        — neither is virtual.
	//   2. Availability check                  — sees seed v=1
	//                                             (own_q=1, in=0, out=0
	//                                             → effective=1 ≥ 1).
	//                                             Passes.
	//   3. Builds tmp_stock_deltas             — for slot, picks the
	//                                             ORDER BY version DESC
	//                                             LIMIT 1 row, which is
	//                                             v=1 (own_q=1) because
	//                                             goA's v=2 is
	//                                             uncommitted. THIS IS
	//                                             THE STALE SNAPSHOT.
	//   4. LOOP → INSERT movement + stocks     — stocks INSERT computes
	//                                             MAX(version)+1 = 2
	//                                             inline, tries to INSERT
	//                                             at v=2. BLOCKS on
	//                                             goA's unique-index
	//                                             reservation.
	//
	// We don't get B's result until goA commits (which unblocks B's
	// retry path). The result channel decouples timing.
	bResult := make(chan error, 1)
	bMovementID := uuid.New() // pre-generated so we can also assert on the movement row
	go func() {
		connB, err := pgxConnectWithAppName(ctx, testDSN, "race-B")
		if err != nil {
			bResult <- fmt.Errorf("B: connect: %w", err)
			return
		}
		defer connB.Close(ctx)

		// Single statement: the proc runs in its own implicit tx.
		// We capture the returned movement_id but already know it
		// (we passed it in as p_movement_id) — useful for diagnostics.
		var returnedID uuid.UUID
		err = connB.QueryRow(ctx, `SELECT inventory.create_item_movement_proc(
			$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::bigint, $6::text,
			$7::uuid, $8::uuid, $9::int, $10::uuid, $11::text, $12::jsonb,
			$13::uuid, $14::uuid
		)`,
			tenantID,
			uuid.MustParse(itemID),
			uuid.MustParse(slotID), // p_from_id
			uuid.MustParse(boxID),  // p_to_id
			1,                      // p_quantity
			"race-test",            // p_handler
			uuid.Nil,               // p_collection_id (zero UUID = direct, no collection)
			nil,                    // p_order_id
			nil,                    // p_position
			nil,                    // p_data_type_id
			nil,                    // p_data_type_slug
			nil,                    // p_data
			createdBy,              // p_created_by
			bMovementID,            // p_movement_id (pre-generated)
		).Scan(&returnedID)
		if err != nil {
			bResult <- fmt.Errorf("B: proc call: %w", err)
			return
		}
		bResult <- nil
	}()

	// ── Step 4: wait for goB to block on the unique-index lock ───────
	//
	// We poll pg_stat_activity until we see the race-B backend in the
	// `wait_event_type = 'Lock'` state. This is the deterministic
	// "B has attempted its INSERT and is waiting on A" signal.
	//
	// Use a SHORT poll interval (10ms) to keep the test fast. A 10s
	// timeout is generous — the proc reaches its INSERT in
	// milliseconds on a stock embedded postgres.
	if err := waitForBackendBlocked(ctx, testDSN, "race-B", 10*time.Second); err != nil {
		// If we got here, B may have errored out before reaching the
		// INSERT. Try to surface that error before failing.
		select {
		case bErr := <-bResult:
			t.Fatalf("goroutine B failed before blocking on the lock: %v (waitForBackendBlocked: %v)", bErr, err)
		default:
			t.Fatalf("waitForBackendBlocked: %v", err)
		}
	}

	// ── Step 5: commit goA — release goB's unique-index wait ─────────
	close(aCommit)
	select {
	case <-aDone:
	case <-time.After(10 * time.Second):
		t.Fatal("goroutine A never committed within 10s")
	}
	select {
	case err := <-aErr:
		require.NoError(t, err, "goroutine A failed")
	default:
	}

	// ── Step 6: wait for goB to finish ───────────────────────────────
	//
	// On the unique_violation, the proc's EXCEPTION handler bumps the
	// retry counter, sleeps a few ms, and re-enters BEGIN. The next
	// INSERT sees MAX(version)+1 = 2 and succeeds. tmp_stock_deltas
	// (built before the LOOP, line 178-207 of the migration) is NOT
	// re-read, so the new stocks row carries the stale own_quantity.
	//
	// We allow up to 30s — the proc's backoff caps at ~10ms per retry
	// and the budget is 50 retries, so a worst-case total is well
	// under a second.
	select {
	case err := <-bResult:
		require.NoError(t, err, "create_item_movement_proc returned an error after the race")
	case <-time.After(30 * time.Second):
		t.Fatal("goroutine B (proc call) never completed within 30s")
	}

	// ── Step 7: assert the bug ───────────────────────────────────────
	//
	// Read the latest stocks row by version DESC. The fix-preserving
	// answer is version=2 with own_quantity=14 (the racing tx's value
	// is preserved; the proc reflects the FRESH baseline observed at
	// INSERT time). The bug answer is version=2 with own_quantity=1
	// (the stale baseline from tmp_stock_deltas wins, masking goA's
	// committed value of 14).
	finalVer, finalOwn := readLatestStock(t, ctx, testDSN, tenantID, slotID, itemID)
	t.Logf("post-race latest stocks row for (slot, item): version=%d own_quantity=%d", finalVer, finalOwn)

	require.Equal(t, int64(3), finalVer,
		"sanity: post-race latest stocks row should be at version=3 "+
			"(goA wrote version=2 directly; the proc wrote version=3 after a "+
			"unique_violation retry). Got version=%d — the race did not "+
			"interleave as expected; investigate the synchronisation.",
		finalVer)

	assert.Equal(t, int64(14), finalOwn,
		`BUG REPRO — TestCreateItemMovementProc_StaleBaselineUnderConcurrentExecute_Postgres

Latest stocks row at version=%d has own_quantity=%d.
Expected own_quantity=14 (preserved from goA's racing INSERT at version=2).

WHY: inventory.create_item_movement_proc builds pg_temp.tmp_stock_deltas
ONCE before its OCC retry LOOP (migration 20260430070249, line 178-207).
On a unique_violation retry, the INSERT inside the LOOP recomputes its
`+"`version`"+` inline via SELECT MAX(version)+1, but the `+"`own_quantity`"+`
column comes from base_own_quantity in tmp_stock_deltas — which is NEVER
re-read. When a concurrent tx (goA) commits a new row between the proc's
tmp_stock_deltas snapshot and the proc's INSERT, the new row picks a
fresh version (correctly) but a stale own_quantity (incorrectly). The
poisoned row masks goA's value as the latest by version, and the next
read of "latest stocks for (slot, item) by version DESC" returns the
stale value.

This is the proc-side surface of the same architectural bug
stocks_race_test.go pins on the Go path (createItemMovementViaGo). Any
fix must close both.

WHAT TO FIX: move the tmp_stock_deltas build INSIDE the LOOP so each
retry observes a fresh snapshot, OR change the INSERT to source
own_quantity from a fresh subquery alongside the version subquery.
See issue-stock-map-resets-own-quantity-to-zero-on-pending-pick-creation.md.

WHAT THIS TEST PROVES: the proc-side bug is deterministic; any future
patch must keep this test green.`,
		finalVer, finalOwn)
}

// pgxConnectWithAppName opens a pgx connection that advertises the
// given application_name in pg_stat_activity. The test uses this to
// identify which backend is goroutine A vs goroutine B when polling
// for lock waits — the per-test database is otherwise busy with the
// ent client's pool too, so filtering by application_name is the
// most precise way to single out our race actors.
func pgxConnectWithAppName(ctx context.Context, dsn, appName string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = appName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return conn, nil
}

// waitForBackendBlocked polls pg_stat_activity until a backend with the
// given application_name is observed in `wait_event_type = 'Lock'`,
// indicating it is waiting on a database-level lock (in our test, the
// unique-index lock held by goroutine A). Returns nil when observed,
// or an error after `timeout` elapses.
//
// We poll on a fresh pgx connection so the observation does not perturb
// the race actors.
func waitForBackendBlocked(ctx context.Context, dsn, appName string, timeout time.Duration) error {
	probe, err := pgxConnectWithAppName(ctx, dsn, "race-probe")
	if err != nil {
		return fmt.Errorf("probe connect: %w", err)
	}
	defer probe.Close(ctx)

	const pollEvery = 10 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var blocked int
		err := probe.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM pg_stat_activity
			WHERE application_name = $1
			  AND wait_event_type = 'Lock'`,
			appName,
		).Scan(&blocked)
		if err != nil {
			return fmt.Errorf("probe query: %w", err)
		}
		if blocked > 0 {
			return nil
		}
		time.Sleep(pollEvery)
	}
	return fmt.Errorf("backend %q never entered a Lock wait within %s", appName, timeout)
}
