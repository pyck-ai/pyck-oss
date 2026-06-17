//nolint:testpackage // in-package test required: ExecuteRepositoryMovement orchestration is exercised against a private *service receiver, and the read-narrowing assertion inspects package-private loader behaviour.
package stock

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// fatAncestorTopology builds the topology shared by both
// ExecuteRepositoryMovement regression tests and seeds a consistent
// pre-move stock baseline plus a configurable amount of unrelated
// "noise" stock at the FROM ancestor:
//
//		FROM (root)        TO (root)
//		  |
//		MOVING (the repo being delivered/moved)
//
//	  - The trolley item I_move sits at MOVING (own qty = trolleyQty) and
//	    is rolled up at FROM (qty = trolleyQty), mirroring the consistent
//	    parent/child baseline the resolver-level execute test relies on
//	    (resolvers/executerepositorymovement_test.go). Seeding FROM avoids
//	    a spurious underflow when ExecuteRepositoryMovement subtracts the
//	    moved quantity from FROM's rolled-up total.
//	  - `noise` unrelated items each hold a stock row at FROM only. FROM is
//	    an ancestor of MOVING and therefore part of the {MOVING, FROM, TO}
//	    closure ExecuteRepositoryMovement loads. These rows stand in for the
//	    ~12k accumulated rolled-up rows the WF102 RCA observed at the
//	    PACKING-ZONE / warehouse-root shared ancestors on the dev tenant.
//
// Returns the repo IDs, the trolley item ID, and the IDs of the noise
// items so callers can assert they were left untouched.
type fatAncestorFixture struct {
	fromID, toID, movingID uuid.UUID
	trolleyItemID          uuid.UUID
	noiseItemIDs           []uuid.UUID
}

func buildFatAncestorTopology(e *ancestorTestEnv, trolleyQty int64, noise int) fatAncestorFixture {
	e.t.Helper()

	fromID := e.mkRepo("FROM", uuid.Nil)
	toID := e.mkRepo("TO", uuid.Nil)
	movingID := e.mkRepo("MOVING", fromID)

	trolleyItemID := e.mkItem("WF102-TROLLEY")
	// Own stock physically on the moving repository plus the matching
	// rolled-up total at its parent FROM. Both rows are required for the
	// post-move arithmetic to land on FROM->0 / TO->trolleyQty without an
	// intermediate underflow (see resolver execute test for the same
	// parent+child seeding).
	e.mkStock(movingID, trolleyItemID, trolleyQty)
	e.mkStock(fromID, trolleyItemID, trolleyQty)

	noiseItemIDs := make([]uuid.UUID, 0, noise)
	for i := range noise {
		itemID := e.mkItem("WF102-NOISE-" + strconv.Itoa(i))
		// Unrelated rolled-up history at the shared FROM ancestor. Not on
		// MOVING, so the move can never change its rolled-up stock; the
		// only question this stock raises is whether the closure read
		// hydrates it (the bug) or skips it (the fix).
		e.mkStock(fromID, itemID, 7)
		noiseItemIDs = append(noiseItemIDs, itemID)
	}

	return fatAncestorFixture{
		fromID:        fromID,
		toID:          toID,
		movingID:      movingID,
		trolleyItemID: trolleyItemID,
		noiseItemIDs:  noiseItemIDs,
	}
}

// TestExecuteRepositoryMovement_NarrowsClosureReadToMovingRepoItems is the
// WF102 regression guard. It pins that ExecuteRepositoryMovement loads its
// ancestor-closure stock baseline narrowed to the items physically on the
// moving repository, NOT every (repo, item) pair in the closure.
//
// This is the read-side parallel of the write-side
// TestCreateRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems /
// TestDeleteRepositoryMovement_DoesNotRewriteUnaffectedAncestorItems. Those
// guard the fan-out *write* set; the Execute fan-out is already constrained
// to the moving repo's items by construction (it only iterates
// nestStockKeyMap(priorStocks)[RepositoryID]), so the bug ExecuteRepository-
// Movement actually carried was purely a *read* amplification: with
// items=nil, loadAncestorStocks hydrated the latest row for every item that
// has ever rolled up at a shared ancestor. On the dev tenant that was ~12k
// rows walked across 61 LIMIT/OFFSET pages, ~180-260s, past the ~117s
// gateway timeout — the DeliverTrolleyToPackingZone hang.
//
// SQLite has no gateway timeout and a few hundred rows fit in a single
// AllPages page, so we cannot reproduce the wall-clock blow-up. Instead we
// count, via an ent query interceptor, how many stock ROWS are hydrated
// while ExecuteRepositoryMovement runs and assert the count stays bounded
// by the moving repo's item set rather than scaling with the unrelated
// ancestor noise.
//
// Before the fix (items=nil at impl.go:1186) this hydrates all `noise`
// unrelated FROM rows and trips the bound; after the fix (narrowed to
// loadItemIDsAtRepo(RepositoryID)) only the trolley item's handful of rows
// across the closure are read.
func TestExecuteRepositoryMovement_NarrowsClosureReadToMovingRepoItems(t *testing.T) {
	t.Parallel()

	e := newAncestorTestEnv(t)

	const noise = 80
	const readBound = 40 // narrowed read is ~4 rows; nil read is noise+~3 (>=83)

	fx := buildFatAncestorTopology(e, 10, noise)

	// Count stock rows hydrated by every Stock query. Registered on the
	// client before the transaction is opened so it propagates into the
	// tx's StockClient (Client.Tx copies config.inters), exactly the way
	// the LimitMixin interceptor reaches in-transaction reads in
	// production. The closure runs on the test goroutine (Execute is
	// synchronous), so the plain int counter needs no synchronisation.
	hydrated := 0
	e.client.Stock.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			res, err := next.Query(ctx, q)
			if err == nil {
				if rv := reflect.ValueOf(res); rv.Kind() == reflect.Slice {
					hydrated += rv.Len()
				}
			}
			return res, err
		})
	}))

	tx := e.withTx()
	svc := &service{}

	// Create the movement first. This narrows by item already (the Create
	// path was fixed earlier), and its reads happen before the measurement
	// window below, so they do not pollute the Execute row count.
	movement, err := svc.CreateRepositoryMovement(e.ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         fx.toID,
			RepositoryID: fx.movingID,
		},
		TenantID: e.tenantID,
	})
	require.NoError(t, err)
	require.NotNil(t, movement)

	// Measurement window: only the stock rows hydrated by
	// ExecuteRepositoryMovement itself.
	before := hydrated
	executed, err := svc.ExecuteRepositoryMovement(e.ctx, tx, ExecuteRepositoryMovementInput{
		ID:       movement.ID,
		TenantID: e.tenantID,
	})
	require.NoError(t, err)
	require.True(t, executed.Executed)

	readDuringExecute := hydrated - before

	require.Positive(t, readDuringExecute,
		"sanity: the interceptor must observe the moving repo's own stock read; "+
			"a zero count means the interceptor was not wired into the transaction")
	require.Less(t, readDuringExecute, readBound,
		"ExecuteRepositoryMovement must narrow its closure stock read to the moving "+
			"repository's items; it hydrated %d rows with %d unrelated noise items at the "+
			"shared FROM ancestor, which means it read the whole closure (items=nil) instead "+
			"of loadItemIDsAtRepo(RepositoryID) — the WF102 DeliverTrolleyToPackingZone hang",
		readDuringExecute, noise)
}

// TestExecuteRepositoryMovement_RollupCorrectWithFatSharedAncestor is the
// safety net for the WF102 narrowing: it proves the read narrowing is
// behavior-preserving. With the same fat shared ancestor present, the
// stock rollup must still be exactly correct and the unrelated ancestor
// items must be left untouched.
//
// The danger of the surgical fix is narrowing by the wrong key or dropping
// a row the FROM/TO walk needs. This test would go red on any such mistake
// (e.g. narrowing by FromID instead of RepositoryID, which would fail to
// hydrate the moving repo's own stock and zero out the move).
//
// Arithmetic mirrors the resolver-level execute test
// (resolvers/executerepositorymovement_test.go): moving a repo holding
// qty 10 from FROM to TO leaves FROM at 0, TO at 10, and the moving repo
// keeping its own 10.
func TestExecuteRepositoryMovement_RollupCorrectWithFatSharedAncestor(t *testing.T) {
	t.Parallel()

	e := newAncestorTestEnv(t)

	const trolleyQty int64 = 10
	const noise = 80

	fx := buildFatAncestorTopology(e, trolleyQty, noise)

	tx := e.withTx()
	svc := &service{}

	movement, err := svc.CreateRepositoryMovement(e.ctx, tx, CreateRepositoryMovementInput{
		Input: ent.CreateRepositoryMovementInput{
			Handler:      "test",
			ToID:         fx.toID,
			RepositoryID: fx.movingID,
		},
		TenantID: e.tenantID,
	})
	require.NoError(t, err)
	require.NotNil(t, movement)

	executed, err := svc.ExecuteRepositoryMovement(e.ctx, tx, ExecuteRepositoryMovementInput{
		ID:       movement.ID,
		TenantID: e.tenantID,
	})
	require.NoError(t, err)
	require.True(t, executed.Executed)

	// latestQty returns the current rolled-up quantity for (repo, item):
	// the highest-version non-deleted row, which is the freshest write the
	// per-(repo,item) version tracker produced.
	latestQty := func(repoID, itemID uuid.UUID) int64 {
		t.Helper()
		row, qerr := tx.Stock.Query().
			Where(
				entstock.TenantID(e.tenantID),
				entstock.RepositoryID(repoID),
				entstock.ItemID(itemID),
				entstock.DeletedAtIsNil(),
			).
			Order(ent.Desc(entstock.FieldVersion)).
			First(e.ctx)
		require.NoError(t, qerr)
		return row.Quantity
	}

	// The moving repository is now parented under TO.
	movingRepo, err := tx.Repository.Get(e.ctx, fx.movingID)
	require.NoError(t, err)
	require.Equal(t, fx.toID, movingRepo.ParentID, "moving repository must be re-parented under TO")

	// Rollup conservation: the trolley quantity left FROM and landed on TO,
	// while the moving repository kept its own stock — unchanged by the
	// presence of the fat unrelated history at FROM.
	require.Equal(t, int64(0), latestQty(fx.fromID, fx.trolleyItemID), "FROM must drop the moved quantity")
	require.Equal(t, trolleyQty, latestQty(fx.toID, fx.trolleyItemID), "TO must receive the moved quantity")
	require.Equal(t, trolleyQty, latestQty(fx.movingID, fx.trolleyItemID), "moving repo keeps its own quantity")

	// The unrelated ancestor items must be untouched: still at their
	// seeded quantity and never re-stamped with this movement's ID.
	for _, noiseItemID := range fx.noiseItemIDs {
		require.Equal(t, int64(7), latestQty(fx.fromID, noiseItemID),
			"unrelated ancestor item %s must keep its rolled-up quantity", noiseItemID)
	}

	movementItemIDStrs, err := tx.Stock.Query().
		Where(
			entstock.TenantID(e.tenantID),
			entstock.MovementID(movement.ID),
		).
		GroupBy(entstock.FieldItemID).
		Strings(e.ctx)
	require.NoError(t, err)
	require.ElementsMatch(t,
		[]string{fx.trolleyItemID.String()},
		movementItemIDStrs,
		"only the moving repository's item may be written across the Create+Execute fan-out; "+
			"unrelated ancestor items must not be re-stamped",
	)
}
