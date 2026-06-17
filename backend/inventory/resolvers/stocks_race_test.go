// stocks_race_test.go is a DELIBERATELY-RED regression test for the
// production bug documented in
// issue-stock-map-resets-own-quantity-to-zero-on-pending-pick-creation.md
// at the repository root.
//
// Read the issue first — this file is the executable form of that
// scenario. The matching fix lands in a SEPARATE commit and flips this
// test green; until that commit lands, the test FAILS with something
// like `own_quantity = 0, expected 14`, and that failure IS the bug.
//
// ─── Bug, in plain English ────────────────────────────────────────────
//
// The picking workflow turns one tour into a chain of pending picks via
// the `createInventoryCollectionMovement` GraphQL mutation. The
// collection wrapper sets `WithDeferredUnderflow` on the context, which
// makes `CreateItemMovement` (impl.go:475) route to the Go orchestration
// body `createItemMovementViaGo` (impl.go:497) instead of the Postgres
// fast-path stored procedure.
//
// `createItemMovementViaGo` reads the latest stocks snapshot TWICE,
// in two SEPARATE SQL statements within the same outer transaction:
//
//	1. impl.go:553  loadAncestorStocks         — fills the stockMap
//	                                              that the simulate walk
//	                                              and the eventual INSERT
//	                                              both consume.
//	2. impl.go:1620 loadLatestStockPerRepo     — feeds the per-(repo,item)
//	                                              version tracker so the
//	                                              new row picks
//	                                              `max(version)+1`.
//
// PostgreSQL's default isolation is READ COMMITTED. Under READ COMMITTED,
// each STATEMENT gets its own snapshot of committed data. If a third
// transaction commits a new stocks row between statements (1) and (2),
// the function ends up writing a row that:
//
//	• takes its `Quantity` AND `OwnQuantity` from the STALE snapshot of
//	  statement (1). simulateRepositoryStockMapWalk (impl.go:271) only
//	  mutates the In/Out counters on the in-memory stockMap; it never
//	  touches Quantity or OwnQuantity. So whatever stockMap was seeded
//	  with at loadAncestorStocks time gets persisted verbatim into the
//	  new row.
//	• takes its `version` from the FRESH snapshot of statement (2) via
//	  `SELECT MAX(version)+1`.
//
// The new row claims to be the latest by version but its Quantity and
// OwnQuantity reflect a state from BEFORE the row it just superseded.
// Two failure modes can follow:
//
//	A. CREATE-time surface — `consistencyCheckSourceRows` (deferred_
//	   underflow.go:76), which CreateCollectionMovement always runs at
//	   the end of its per-position loop, reads the LATEST stocks row by
//	   created_at — i.e. our just-inserted poison row — and computes
//	   `effective = Quantity + IncomingStock - OutgoingStock`. With the
//	   stale `Quantity = 0` plus the fresh `OutgoingStock = 1`
//	   reservation, effective = -1, the mutation returns
//	   "stock underflow: ... effective=-1", and gqltx rolls the tx back.
//	   The poisoned row is never committed. This is the surface THIS
//	   TEST pins.
//	B. EXECUTE-time surface — if the slot has accumulated enough
//	   IncomingStock from prior placements to mask the underflow at
//	   CREATE (as observed on the production tenant: incoming_stock=154
//	   from many placements), the consistency check PASSES and the
//	   poison row commits to the ledger. The next ExecuteItemMovement
//	   then reads it, computes `Quantity + (-1) = -1`, and
//	   validateStockMapNoUnderflow rolls THAT tx back with "stock
//	   underflow: quantity would be -1" (see the issue file for the
//	   full production timeline).
//
// Same bug, two surfaces. Pinning the CREATE-time surface (A) here is
// enough: a fix that prevents the poisoned row from being written
// closes both surfaces, and surface A is by far the easier one to
// reproduce deterministically because it does not depend on the slot's
// IncomingStock history.
//
// ─── How this test pins the race DETERMINISTICALLY ────────────────────
//
// A naive concurrent test would race two goroutines and hope the
// interleave happens. That is flaky. Instead we wrap the Ent driver in
// a `stocksReadGate` that counts SELECTs against the stocks table and
// can PAUSE execution at a chosen count, then release it on a signal.
//
// Timeline (V₀ is the first version PG would issue for (slot, item)
// once any row is INSERTed, i.e. V₀=0):
//
//	t=0   test:  build the tree (warehouse → {slot, box}) + an item.
//	             Do NOT seed any stock at slot. Stocks table is empty
//	             for (slot, item). This corresponds to the production
//	             moment BEFORE WF050's placement commit.
//
//	t=1   goA:   open a separate pgx connection, BEGIN tx, INSERT a
//	             stocks row for (slot, item) at version=V₀ with
//	             Quantity=14, OwnQuantity=14 (mimics what an
//	             ExecuteItemMovement of a virtual→slot placement would
//	             write). Leave the tx OPEN — do not commit yet.
//
//	t=2   goB:   send `createInventoryCollectionMovement(slot → box, 1)`
//	             via the GraphQL gateway. createItemMovementViaGo runs:
//	               • stocks SELECT #1 — availability check (impl.go:518).
//	                 goA's row is INVISIBLE (uncommitted, READ COMMITTED),
//	                 so the check sees no stock. Under WithDeferredUnderflow
//	                 the resulting "insufficient stock" error is suppressed
//	                 (impl.go:536-540) and the flow continues.
//	               • stocks SELECT #2 — loadAncestorStocks (impl.go:553).
//	                 Same snapshot, same invisible goA. Result:
//	                 stockMap[slot] has Quantity=0, OwnQuantity=0 (the
//	                 zero value of ent.Stock for a missing (repo, item)).
//
//	t=3   gate:  PAUSES goB right after stocks SELECT #2. goB's outer
//	             tx is open; stockMap is loaded with the stale (empty)
//	             snapshot; the movement INSERT and the version re-read
//	             have NOT happened yet.
//
//	t=4   test:  sees the pause, signals goA to commit.
//
//	t=5   goA:   COMMIT. Row at version=V₀, Quantity=14, OwnQuantity=14
//	             is now visible to subsequent statements.
//
//	t=6   test:  releases the gate. goB resumes.
//
//	t=7   goB:   simulate runs (in-memory), the movement row is INSERTed,
//	             then insertStockMapWithVersions fires:
//	               • stocks SELECT #3 — loadLatestStockPerRepo (impl.go:1620).
//	                 NEW statement, NEW snapshot. goA's row is now visible:
//	                 version=V₀ exists, so the version tracker emits V₀+1
//	                 as the next version.
//	               • stocks INSERT — writes a row at version=V₀+1, taking
//	                 Quantity / OwnQuantity from the STALE stockMap (both 0)
//	                 and the In/Out counters from the simulate
//	                 (OutgoingStock=1, OwnOutgoingStock=1).
//
//	t=8   goB:   CreateCollectionMovement runs consistencyCheckSourceRows
//	             over (slot, item). It loads the latest stocks row by
//	             created_at — i.e. our just-INSERTed poison row — and
//	             computes effective = 0 + 0 - 1 = -1. Underflow. The
//	             mutation returns "stock underflow: ... effective=-1"
//	             and gqltx rolls back.
//
// The test asserts goB's mutation succeeds. Pre-fix it FAILS with the
// underflow error message; post-fix it PASSES and the latest committed
// row carries Quantity=14, OwnQuantity=14 (the placement is preserved,
// the pending pick's reservation is recorded in OutgoingStock).
//
// ─── Why this is Postgres-only ────────────────────────────────────────
//
// The race is a READ COMMITTED snapshot-visibility anomaly. SQLite
// effectively serializes write transactions, so goB cannot read while
// goA is in-flight — the interleave is unreachable. There is intentionally
// no SQLite variant of this test.
//
// The embedded-postgres harness is already wired up by
// startEmbeddedPostgres / setupPostgres in resolver_test.go; we reuse
// startEmbeddedPostgres and provide our own setup variant
// (setupPostgresWithGate) that swaps the Ent driver for the gated one.

package resolvers_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commondb "github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/inventory/api"
	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entmigrate "github.com/pyck-ai/pyck/backend/inventory/ent/migrate"
	"github.com/pyck-ai/pyck/backend/inventory/model"
	"github.com/pyck-ai/pyck/backend/inventory/resolvers"
	"github.com/pyck-ai/pyck/backend/inventory/service/stock"
)

// =============================================================================
// stocksReadGate — Ent driver wrapper for deterministic interleaving
// =============================================================================
//
// stocksReadGate wraps an ent dialect.Driver and counts every SELECT
// statement that targets the `stocks` table (either bare or
// `inventory.stocks`). The test arms the gate with `Arm(N)`: the N-th
// matching SELECT will return its rows AS USUAL, then block on a
// channel before the calling goroutine is allowed to proceed. The test
// can observe the pause via the `paused` channel and release the
// goroutine via the `resume` channel.
//
// Concretely, the gate is invisible at construction time (armed=false).
// Setup queries (creating repositories, items, the seed placement)
// flow through without being counted or paused. The test arms the
// gate immediately before starting the goroutine that should be
// interleaved; once armed, every stocks SELECT is counted.
//
// The gate covers BOTH the `Driver.Query` path (used when ent runs a
// query without a transaction) and the `Tx.Query` path (used when ent
// runs a query inside a transaction). All other methods — Exec, Tx
// lifecycle, Dialect, Close — are passthrough.
//
// The gate is one-shot: it fires exactly once per Arm call, then ignores
// further matches until the next Arm. This matches the test's needs
// (we want exactly one pause point) and avoids accidental double-pauses
// if the gqltx retry middleware re-runs B's mutation.
type stocksReadGate struct {
	dialect.Driver

	mu         sync.Mutex
	armed      bool
	count      int                  // number of stocks SELECTs observed since Arm
	pauseAfter int                  // pause after this many stocks SELECTs
	fired      bool                 // whether we have already paused once this Arm
	paused     chan struct{}        // closed when we hit pauseAfter
	resume     chan struct{}        // wait on this to continue
	logf       func(string, ...any) // optional logger for diagnostics
}

// Arm activates the gate. The next `after` SELECTs against the stocks
// table will be allowed through; the one after that (i.e. the (after+1)-th)
// will NOT be intercepted — only the `after`-th read will fire the gate
// AFTER returning. Returns the channels the test should use:
//   - paused: closed exactly once, when the gate fires
//   - resume: close this channel to release the paused goroutine
//
// Must be called BEFORE the goroutine whose queries should be gated runs.
// Calling Arm again resets the counter and replaces the channels.
func (g *stocksReadGate) Arm(after int) (paused, resume chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = true
	g.count = 0
	g.pauseAfter = after
	g.fired = false
	g.paused = make(chan struct{})
	g.resume = make(chan struct{})
	return g.paused, g.resume
}

// Disarm turns the gate off and drops the channels. Useful in test
// cleanup if the test never armed or never released.
func (g *stocksReadGate) Disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = false
	g.paused = nil
	g.resume = nil
}

// Query is dialect.Driver.Query. We pass the call through, then check
// whether the executed statement matched and we should pause.
func (g *stocksReadGate) Query(ctx context.Context, query string, args, v any) error {
	err := g.Driver.Query(ctx, query, args, v)
	g.maybeGate(query)
	return err
}

// Tx wraps the per-call transaction in a gatedTx so that queries issued
// INSIDE the transaction also fire the counter. Ent uses Tx for
// short-lived transactions; we delegate to the inner driver and wrap
// the returned tx.
func (g *stocksReadGate) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := g.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &gatedTx{Tx: tx, gate: g}, nil
}

// BeginTx is the gqltx-middleware entry point. Every incoming GraphQL
// request opens a transaction via Client.BeginTx, which delegates to
// the driver via a type assertion (see ent/gen/client.go:199 — the
// generated `Client.BeginTx` type-asserts the driver to an
// `interface { BeginTx(...) (dialect.Tx, error) }`). The
// dialect.Driver interface itself does NOT expose BeginTx; we have to
// reach the method on the concrete entsql.Driver via the same type
// assertion. We wrap the returned tx so that queries inside the tx
// fire the gate.
func (g *stocksReadGate) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	beginTxer, ok := g.Driver.(interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("stocksReadGate: inner driver %T does not implement BeginTx", g.Driver)
	}
	tx, err := beginTxer.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &gatedTx{Tx: tx, gate: g}, nil
}

// maybeGate is the heart of the gate. It is called AFTER every SELECT
// (both inside and outside transactions). If the gate is armed, the
// query matches the stocks table, and we have not already fired this
// Arm cycle, the count is incremented and — when it matches the target —
// the calling goroutine is blocked on the resume channel.
//
// We pause AFTER returning the rows so the calling goroutine has fully
// observed the (stale) snapshot before being held. This matches the
// production scenario: loadAncestorStocks completes and returns to the
// caller before the would-be poison commit lands.
func (g *stocksReadGate) maybeGate(query string) {
	matched := isStocksSelect(query)
	g.mu.Lock()
	armed := g.armed
	if armed && g.logf != nil {
		g.logf("stocksReadGate: query (matched=%v, count=%d): %s", matched, g.count, snip(query))
	}
	if !matched || !armed || g.fired {
		g.mu.Unlock()
		return
	}
	g.count++
	hit := g.count == g.pauseAfter
	var (
		paused chan struct{}
		resume chan struct{}
	)
	if hit {
		g.fired = true
		paused = g.paused
		resume = g.resume
	}
	g.mu.Unlock()

	if hit {
		close(paused)
		<-resume
	}
}

// snip returns the first ~120 characters of a query for log readability.
func snip(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if len(q) > 120 {
		return q[:120] + "…"
	}
	return q
}

// isStocksSelect is a deliberately lenient pattern match. PostgreSQL
// produces SELECTs of the form
//
//	SELECT "stocks"."id", "stocks"."tenant_id", ... FROM "stocks" WHERE ...
//
// or, when search_path is not in effect for the rendered query,
//
//	SELECT ... FROM "inventory"."stocks" WHERE ...
//
// Both variants share the substring `from "stocks"` once lower-cased
// and whitespace-collapsed; the schema-qualified form additionally
// contains `from "inventory"."stocks"`. The test database is opened
// with `search_path=inventory`, so the bare form is what we actually
// observe — but we accept both to be robust.
//
// We also restrict matches to statements that START with SELECT, so
// INSERTs into stocks (which also reference the table name) are NOT
// counted. The bug is read-side; we only want to gate on reads.
func isStocksSelect(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if !strings.HasPrefix(q, "select") {
		return false
	}
	return strings.Contains(q, `from "stocks"`) ||
		strings.Contains(q, `from "inventory"."stocks"`)
}

// gatedTx wraps a dialect.Tx so queries issued inside the transaction
// fire the gate. Exec, Commit, and Rollback are passthroughs.
//
// Both `Query` and `QueryContext` are intercepted. The former is what
// ent's typed builders (e.g. `tx.Stock.Query()...All(ctx)`) end up
// calling on the driver. The latter is what code that runs raw SQL
// inside a tx uses — most notably `loadAncestorIDs`
// (ancestor_loader.go:227), which fires the recursive CTE that walks
// the repository parent_id graph. Without intercepting QueryContext
// here, ent's generated `txDriver.QueryContext` (see
// ent/gen/tx.go:258) type-asserts the inner dialect.Tx for a
// `QueryContext` method and returns "Tx.QueryContext is not supported"
// when the assertion fails — which would make every loadAncestorStocks
// call blow up. Same story for `ExecContext`, which the proc dispatch
// (impl.go:664) uses to call create_item_movement_proc. We forward both.
type gatedTx struct {
	dialect.Tx
	gate *stocksReadGate
}

// Query inside a transaction. Same shape as stocksReadGate.Query.
func (t *gatedTx) Query(ctx context.Context, query string, args, v any) error {
	err := t.Tx.Query(ctx, query, args, v)
	t.gate.maybeGate(query)
	return err
}

// QueryContext forwards a raw SQL query to the inner tx. The inner tx
// from entsql.OpenDB implements QueryContext; we type-assert and
// delegate. We still fire the gate so raw SQL SELECTs against stocks
// (none exist on this path today, but future code may add them) are
// counted just like ent-builder queries.
func (t *gatedTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q, ok := t.Tx.(interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	})
	if !ok {
		return nil, fmt.Errorf("gatedTx: inner tx %T does not implement QueryContext", t.Tx)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	t.gate.maybeGate(query)
	return rows, err
}

// ExecContext forwards a raw SQL exec to the inner tx. We do NOT fire
// the gate on Exec — the gate is read-side only by design.
func (t *gatedTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	x, ok := t.Tx.(interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	})
	if !ok {
		return nil, fmt.Errorf("gatedTx: inner tx %T does not implement ExecContext", t.Tx)
	}
	return x.ExecContext(ctx, query, args...)
}

// =============================================================================
// setupPostgresWithGate — adapter around setupPostgres that installs the gate
// =============================================================================
//
// This is mostly a copy of setupPostgres (resolver_test.go:235) with the
// driver wrapped. We can't reuse setupPostgres directly because it calls
// enttest.Open which constructs the driver internally and does not give
// us a hook to wrap it. Keeping the duplication local to this test file
// is the smaller change.
//
// Returns the test environment, the armed-on-demand gate, and the raw
// per-test DSN so the test can open its own pgx connection for the
// "concurrent committer" goroutine.
func setupPostgresWithGate(t *testing.T, pg pgHandle) (*testEnv, *stocksReadGate, string) {
	t.Helper()

	// Build a valid PostgreSQL identifier from the test name. Same
	// convention as setupPostgres; see that function for the reasoning.
	dbName := "t_" + pgIdentifierRe.ReplaceAllString(strings.ToLower(t.Name()), "_")
	if len(dbName) > 63 {
		dbName = dbName[:63]
	}

	adminDSN := pg.adminDSN()
	testDSN := pg.dsn(dbName)

	// 1. Create the per-test database in the admin connection.
	adminDB, err := sql.Open("pgx", adminDSN)
	require.NoError(t, err)
	_, err = adminDB.ExecContext(context.Background(), "CREATE DATABASE "+dbName)
	require.NoError(t, err)
	require.NoError(t, adminDB.Close())

	// 2. Apply all migrations against the per-test database. This is
	// what installs create_item_movement_proc and every other SQL
	// artefact the resolver under test depends on. See setupPostgres
	// for the rationale on search_path.
	migrationDB, err := sql.Open("pgx", testDSN)
	require.NoError(t, err)
	require.NoError(t, commondb.RunMigrations(context.Background(), migrationDB, "inventory", entmigrate.Migrations))
	require.NoError(t, migrationDB.Close())

	// 3. Drop the per-test database on cleanup.
	t.Cleanup(func() {
		drop, derr := sql.Open("pgx", adminDSN)
		if derr != nil {
			t.Logf("postgres drop db open: %v", derr)
			return
		}
		defer drop.Close()
		dropCtx := context.Background()
		_, _ = drop.ExecContext(dropCtx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", dbName)
		if _, derr := drop.ExecContext(dropCtx, "DROP DATABASE IF EXISTS "+dbName); derr != nil {
			t.Logf("postgres drop db: %v", derr)
		}
	})

	// 4. Open the *sql.DB the ent client will use, wrap it in an ent
	// driver, then wrap THAT in our gate. This is the only structural
	// difference from setupPostgres — enttest.Open hides driver
	// construction so we replicate it here.
	rawDB, err := sql.Open("pgx", testDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })

	innerDriver := entsql.OpenDB(dialect.Postgres, rawDB)
	gate := &stocksReadGate{Driver: innerDriver}
	if os.Getenv("PYCK_TEST_DEBUG") == "true" {
		gate.logf = t.Logf
	}
	client := ent.NewClient(ent.Driver(gate), ent.Log(t.Log))
	t.Cleanup(func() { _ = client.Close() })

	// 5. Wire up the same event hook the production code uses (mutations
	// emit outbox events). Without this, mutations work but emit no
	// outbox rows; that doesn't matter for this test but matches the
	// production wiring exactly, so behaviour is identical.
	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "inventory",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	// 6. Postgres dialect so the stock service uses the proc-aware
	// CreateItemMovement dispatch. CreateCollectionMovement wraps ctx
	// with WithDeferredUnderflow, which routes to the Go path
	// regardless, so the dialect only matters for the non-collection
	// path (which this test does not hit).
	inventoryStock, _ := stock.New(dialect.Postgres, nil)

	te := &testEnv{
		TestEnvironment: resolver.NewTestEnvironment[*ent.Client](t),
		t:               t,
		StockService:    inventoryStock,
	}

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("inventory", client, v, inventoryStock)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "inventory-test", 0))
	})

	te.loadDataTypes()

	return te, gate, testDSN
}

// =============================================================================
// The test
// =============================================================================

// TestCreateCollectionMovement_StaleBaselineUnderConcurrentExecute_Postgres
// pins the production "own_quantity collapses to 0" bug from the issue
// file. See the top-of-file package comment for the full narrative.
//
// Expected state on `main` (before the fix):  FAIL with own_quantity=0
// Expected state once the fix lands:           PASS with own_quantity=14
func TestCreateCollectionMovement_StaleBaselineUnderConcurrentExecute_Postgres(t *testing.T) {
	t.Parallel()

	pg := startEmbeddedPostgres(t)
	env, gate, testDSN := setupPostgresWithGate(t, pg)
	apiClient := setupAPIClient(t, env)
	ctx := env.ctx(userA)

	// ── Step 1: build the world ──────────────────────────────────────
	//
	// Tree:
	//
	//	warehouse (root)
	//	├─ slot   ← the test slot, mirrors prod slot 1-510-B-10h
	//	└─ box    ← destination of the pending pick
	//
	// Item: a single SKU with no DataType (the default plain-item type
	// is registered by loadDataTypes()).
	//
	// We do not create a virtual repository: the test never issues a
	// virtual→slot movement through the API; goroutine A INSERTs the
	// "placement" stocks row directly via pgx instead. The slot and box
	// share the warehouse parent so the FROM/TO walk has a real LCA
	// (otherwise the walk climbs to two separate roots and the test
	// scenario stops mirroring the production picking-tour layout).
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "warehouse", entrepository.TypeStatic, false, nil)
	slotID := stockTestCreateRepository(t, ctx, apiClient, "slot", entrepository.TypeStatic, false, &warehouseID)
	boxID := stockTestCreateRepository(t, ctx, apiClient, "box", entrepository.TypeStatic, false, &warehouseID)
	itemID := stockTestCreateItem(t, ctx, apiClient, "race-item")

	// ── Step 2: leave the slot EMPTY at baseline ─────────────────────
	//
	// We deliberately do NOT seed any stock at the slot. The baseline
	// stocks table contains no row for (slot, item) — equivalent to
	// own_quantity=0. This is the state goroutine B's
	// loadAncestorStocks will read while goroutine A's placement tx
	// is still uncommitted.
	//
	// In the issue file, the analogous state is the moment BEFORE
	// WF050 commits its placement of 14 units. The slot has yet to
	// "officially" receive the units.

	// Tenant is whatever userA carries. Used by goroutine A to scope
	// its raw SQL INSERT. (env.ctx(userA) sets this on ctx, but the
	// pgx-direct goroutine doesn't go through that context so we need
	// the bare UUID.)
	tenantID := userA.TenantID

	// Verify the slot has no stock for the item yet.
	verifyNoStockRow(t, ctx, testDSN, tenantID, slotID, itemID)

	// ── Step 3: start goroutine A — the slow EXECUTE ─────────────────
	//
	// Goroutine A simulates a concurrent EXECUTE that lifts the slot's
	// own_quantity from 0 → 14. We bypass the resolver/service and INSERT
	// the stocks row directly via pgx so that goroutine A's transaction
	// is fully under our control. In production, the analogous tx is
	// ExecuteItemMovement of the WF050 placement; the bug doesn't care
	// HOW the row appears, only that it appears AFTER goroutine B's
	// loadAncestorStocks and BEFORE goroutine B's loadLatestStockPerRepo.
	//
	// Synchronisation primitives:
	//   aReady   — A signals "I have INSERTed; tx is open"
	//   aCommit  — main signals "go ahead, commit"
	//   aDone    — A signals "committed and closed"
	aReady := make(chan struct{})
	aCommit := make(chan struct{})
	aDone := make(chan struct{})
	aErr := make(chan error, 1)

	go func() {
		defer close(aDone)

		connA, err := pgx.Connect(ctx, testDSN)
		if err != nil {
			aErr <- fmt.Errorf("A: pgx connect: %w", err)
			close(aReady)
			return
		}
		defer connA.Close(ctx)

		tx, err := connA.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			aErr <- fmt.Errorf("A: begin tx: %w", err)
			close(aReady)
			return
		}

		// The row mimics what `simulate + insertStockMapWithVersions`
		// would write for an EXECUTE of a virtual→slot placement of
		// qty=14: own_quantity becomes 14, the other counters reset.
		// Version 0 is the first row for this (tenant, repo, item) per
		// the OCC unique index.
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
				0
			)`,
			tenantID, userA.ID,
			uuid.MustParse(slotID), uuid.MustParse(itemID),
		)
		if err != nil {
			_ = tx.Rollback(ctx)
			aErr <- fmt.Errorf("A: insert stocks row: %w", err)
			close(aReady)
			return
		}

		// Signal that A has INSERTed (uncommitted) and is holding the
		// tx open. The test now coordinates B and the commit.
		close(aReady)

		// Wait for the test to release A. While we wait, B is running
		// its CreateCollectionMovement and the gate is pausing it
		// between loadAncestorStocks and loadLatestStockPerRepo.
		<-aCommit

		if cerr := tx.Commit(ctx); cerr != nil {
			aErr <- fmt.Errorf("A: commit: %w", cerr)
			return
		}
	}()

	// Wait for A to be holding its open tx with the row INSERTed.
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

	// ── Step 4: arm the gate for goroutine B ─────────────────────────
	//
	// We arm the gate to pause B AFTER its 2nd stocks SELECT. The Go
	// path in createItemMovementViaGo issues exactly three SELECTs
	// against stocks before its INSERT:
	//
	//   1. impl.go:518  Stock.Query().Order(Desc(created_at)).First(ctx)
	//                   — the FROM-availability check
	//   2. impl.go:553  loadAncestorStocks — fills stockMap (THE
	//                   STALE-READ STATEMENT)
	//   3. impl.go:1620 loadLatestStockPerRepo — fills the version
	//                   tracker (THE FRESH-READ STATEMENT)
	//
	// Pausing after #2 traps B with a stale stockMap and forces #3 to
	// happen AFTER A's commit, recreating the production race exactly.
	paused, resume := gate.Arm(2)
	t.Cleanup(gate.Disarm)

	// ── Step 5: start goroutine B — the pending pick CREATE ─────────
	//
	// B issues `createInventoryCollectionMovement(slot → box, qty=1)`
	// via the GraphQL gateway. The resolver wraps ctx with
	// WithDeferredUnderflow, which forces the Go path
	// (createItemMovementViaGo) — that is the buggy path. The gate
	// will pause B mid-flight.
	//
	// We pass a fresh ctx that does NOT carry env's span (env.ctx adds
	// one); the API client constructs its own. Tests like this one
	// nest goroutines, so using a derived ctx avoids span leakage.
	bDone := make(chan error, 1)
	go func() {
		_, err := callCreateCollectionPick(ctx, apiClient, itemID, slotID, boxID, 1)
		bDone <- err
	}()

	// ── Step 6: wait for the gate ────────────────────────────────────
	//
	// B's CreateCollectionMovement runs:
	//   • availability check (count=1)        — passes through
	//   • loadAncestorStocks (count=2)        — pauses HERE
	//
	// At this point, B is suspended inside its mutation. Its outer
	// transaction is open (gqltx middleware), stockMap is loaded with
	// own_quantity=0 (A's row invisible), but the INSERT and the version
	// re-read haven't run yet.
	select {
	case <-paused:
		t.Logf("gate fired: B is paused after the 2nd stocks SELECT")
	case <-time.After(30 * time.Second):
		t.Fatal("gate never fired (B did not reach the 2nd stocks SELECT within 30s)")
	}

	// ── Step 7: let A commit ─────────────────────────────────────────
	//
	// A's row at version=0, own_quantity=14 becomes visible to all
	// future statements. B's next stocks SELECT (loadLatestStockPerRepo)
	// will see it; B's INSERT will pick the NEXT version (1), which
	// does not conflict with A's row at version 0.
	//
	// Crucially, B's stockMap was loaded BEFORE A committed and is
	// already in-memory; the new visibility does not refresh it.
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

	// ── Step 8: release B ────────────────────────────────────────────
	//
	// B resumes inside maybeGate, the loadAncestorStocks call returns,
	// the in-memory simulate runs, the movement row is INSERTed, and
	// finally insertStockMapWithVersions:
	//   • loadLatestStockPerRepo SELECTs (count=3) — sees A's row,
	//     reports version=0, version tracker emits 1.
	//   • Builds the new row from stockMap (still stale: Quantity=0,
	//     OwnQuantity=0).
	//   • INSERT stocks at version=1 with Quantity=0, OwnQuantity=0,
	//     OutgoingStock=1, OwnOutgoingStock=1.
	// Then CreateCollectionMovement runs consistencyCheckSourceRows
	// over (slot, item), reads the latest stocks row by created_at
	// (the row B just wrote), computes effective = 0 + 0 - 1 = -1,
	// and returns "stock underflow: ... effective=-1". gqltx rolls
	// the tx back; the poison row never commits.
	close(resume)
	var bErr error
	select {
	case bErr = <-bDone:
	case <-time.After(30 * time.Second):
		t.Fatal("goroutine B never completed within 30s")
	}

	// ── Step 9: assert ───────────────────────────────────────────────
	//
	// Two branches:
	//
	//   PRE-FIX (the bug is present): bErr is the underflow error from
	//   consistencyCheckSourceRows. We fail the test with a long, walked-
	//   through message so the failure is self-explanatory — the next
	//   engineer to look at this should be able to read the failure
	//   output alone and understand exactly what is broken and where to
	//   look for the fix.
	//
	//   POST-FIX (the bug is gone): bErr is nil, the poison row was
	//   never written (because the fix re-reads the baseline before
	//   INSERT / serializes the txs / etc., depending on the fix shape).
	//   The latest committed stocks row is goA's placement (Quantity=14,
	//   OwnQuantity=14) plus whatever pending-pick row goB chose to
	//   write. Either way, the on-record own_quantity should be 14.
	if bErr != nil {
		// Surface the bug with maximum signal. We do not try to detect
		// the exact wording (it could change with fixes-in-progress) —
		// any error from B at this point indicates the race produced an
		// inconsistent state that the system caught.
		t.Fatalf(`BUG REPRO — TestCreateCollectionMovement_StaleBaselineUnderConcurrentExecute_Postgres

CreateCollectionMovement(slot → box, qty=1) failed with:

  %v

WHY: goroutine B's createItemMovementViaGo read its baseline (loadAncestorStocks
at impl.go:553) BEFORE goroutine A committed its placement row, so the in-memory
stockMap was seeded with Quantity=0, OwnQuantity=0 (the zero value for a missing
(repo,item) entry). The simulate walk left those fields alone — it only updates
the In/Out counters. By the time insertStockMapWithVersions ran its
loadLatestStockPerRepo (impl.go:1620), goA's row WAS visible, so the version
tracker assigned a fresh version=1, but the INSERT still used the stale
Quantity / OwnQuantity from the seeded stockMap. The result is a stocks row at
version=1 with Quantity=0 + OutgoingStock=1, which makes effective availability
= 0 + 0 - 1 = -1. consistencyCheckSourceRows (deferred_underflow.go:76) catches
this and aborts the mutation.

The same poisoned row, on a tenant with enough IncomingStock to mask the
underflow at consistencyCheckSourceRows, would commit and break the NEXT
ExecuteItemMovement instead — that is the production surface.

WHAT TO FIX: createItemMovementViaGo must either re-read its baseline
(Quantity / OwnQuantity) just before INSERT, escalate to SERIALIZABLE so PG
aborts the racing tx, or hold a row lock on the latest stock row for the
duration. See issue-stock-map-resets-own-quantity-to-zero-on-pending-pick-creation.md.

WHAT THIS TEST PROVES: the bug fires deterministically; any future patch must
keep this test green.
`, bErr)
	}

	// Post-fix path: assert the stocks state. The latest row by version
	// should be goB's pending-pick row, which under any correct fix
	// preserves goA's Quantity=14 and OwnQuantity=14.
	finalVer, finalOwn := readLatestStock(t, ctx, testDSN, tenantID, slotID, itemID)
	t.Logf("post-fix: latest stocks row for (slot, item): version=%d own_quantity=%d", finalVer, finalOwn)
	assert.Equal(t, int64(14), finalOwn,
		"post-fix sanity check: latest stocks row for (slot, item) "+
			"has own_quantity=%d at version=%d; expected 14 (preserved from "+
			"goroutine A's placement). If you got here without the BUG REPRO "+
			"firing, the per-call create succeeded but persisted the wrong "+
			"OwnQuantity — likely a partial fix that suppresses the "+
			"consistencyCheckSourceRows error without re-reading the baseline.",
		finalOwn, finalVer)
}

// =============================================================================
// helpers
// =============================================================================

// callCreateCollectionPick issues a single-position
// `createInventoryCollectionMovement` mutation and returns the ID of the
// created item movement. It is essentially stockTestCreateCollectionMovement
// but returns the error so a goroutine can choose how to handle it.
func callCreateCollectionPick(
	ctx context.Context,
	apiClient api.Client,
	itemID, fromID, toID string,
	quantity int,
) (string, error) {
	handler := testHandler
	qty := float64(quantity)

	parsedItemID, err := uuid.Parse(itemID)
	if err != nil {
		return "", fmt.Errorf("parse itemID: %w", err)
	}
	parsedFromID, err := uuid.Parse(fromID)
	if err != nil {
		return "", fmt.Errorf("parse fromID: %w", err)
	}
	parsedToID, err := uuid.Parse(toID)
	if err != nil {
		return "", fmt.Errorf("parse toID: %w", err)
	}

	result, err := apiClient.CreateInventoryCollectionMovement(ctx, api.CreateInventoryCollectionMovementArgs{
		Input: model.CreateCollectionMovementInput{
			Handler: &handler,
			Collection: []*model.CollectionMovementArrayInput{
				{
					Handler:  handler,
					FromID:   parsedFromID,
					ToID:     parsedToID,
					ItemID:   &parsedItemID,
					Quantity: &qty,
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("createInventoryCollectionMovement: %w", err)
	}
	movements := result.GetCreateInventoryCollectionMovement().GetMovements()
	if len(movements) == 0 {
		return "", fmt.Errorf("no movements returned")
	}
	return movements[0].GetID(), nil
}

// readLatestStock returns (version, own_quantity) of the latest stocks
// row for the given (tenant, repo, item) by version DESC. Used to
// validate the state AFTER the race.
func readLatestStock(t *testing.T, ctx context.Context, dsn string, tenantID uuid.UUID, repoID, itemID string) (int64, int64) {
	t.Helper()

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)

	var version, ownQty int64
	err = conn.QueryRow(ctx, `
		SELECT version, own_quantity
		FROM inventory.stocks
		WHERE tenant_id = $1
		  AND repository_id = $2
		  AND item_id = $3
		  AND deleted_at IS NULL
		ORDER BY version DESC
		LIMIT 1`,
		tenantID, uuid.MustParse(repoID), uuid.MustParse(itemID),
	).Scan(&version, &ownQty)
	require.NoError(t, err)

	return version, ownQty
}

// verifyNoStockRow asserts that no stocks row exists for (tenant, repo, item).
// Used at the start of the test to ensure we begin from a known-empty
// baseline (own_quantity = absent = 0).
func verifyNoStockRow(t *testing.T, ctx context.Context, dsn string, tenantID uuid.UUID, repoID, itemID string) {
	t.Helper()

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)

	var count int
	err = conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory.stocks
		WHERE tenant_id = $1 AND repository_id = $2 AND item_id = $3`,
		tenantID, uuid.MustParse(repoID), uuid.MustParse(itemID),
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count, "expected no pre-existing stocks rows for (slot, item)")
}
