//nolint:testpackage // in-package test required: applyItemMovementStockDelta is package-private after Step 2.9.4.
package stock

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// TestApplyItemMovementStockDelta_NoDBDuringRecursion is the contract test
// for Phase 4.3: every executor caller must populate repoMap and
// priorStocks via loadAncestorStocks up-front, and the executor walk
// itself must not perform any database read during recursion.
//
// The previous walk took a *ent.Tx and called tx.Repository.Get plus
// tx.Stock.Query at every recursion step (FINDINGS section 3.5). After
// Phase 4.3 the parameter list no longer contains a transaction handle
// at all, so it is structurally impossible for the function to issue a
// query — there is nothing to issue against. This test exercises the
// guarantee end-to-end:
//
//  1. Build repoMap and priorStocks entirely in memory (no ent client,
//     no driver, no transaction).
//  2. Drive applyItemMovementStockDelta on a 5-deep ancestor chain that
//     includes the LCA of the FROM/TO endpoints. The recursion visits
//     every hop on both walks.
//  3. Assert the resulting stockMap matches the hand-computed expected
//     deltas. Wrong reads (or any read at all) would either crash on
//     the nil receiver or produce different numbers.
//
// In Go a compile-time assertion is the strongest "no DB query" check
// available for in-package code: the function signature is the
// authoritative contract. Pairing it with a behavioral assertion on a
// non-trivial ancestor closure makes the regression test concrete.
func TestApplyItemMovementStockDelta_NoDBDuringRecursion(t *testing.T) {
	t.Parallel()

	// Tree:
	//
	//   root
	//    └── grandparent  (LCA for from=A, to=B)
	//         ├── A (FROM)
	//         └── parentB
	//              └── B (TO)
	//
	// The walk visits A → grandparent (FROM), then B → parentB →
	// grandparent (TO). Above the LCA the walks must terminate — root
	// must NOT appear in stockMap. parentB (between B and the LCA) must
	// be visited.
	rootID := uuid.New()
	grandparentID := uuid.New()
	aID := uuid.New()
	parentBID := uuid.New()
	bID := uuid.New()
	itemID := uuid.New()

	repoMap := map[uuid.UUID]ent.Repository{
		rootID:        {ID: rootID, ParentID: uuid.Nil},
		grandparentID: {ID: grandparentID, ParentID: rootID},
		aID:           {ID: aID, ParentID: grandparentID},
		parentBID:     {ID: parentBID, ParentID: grandparentID},
		bID:           {ID: bID, ParentID: parentBID},
	}

	const seedQty int64 = 10
	// Seed prior stock at every repository in the closure: the
	// executor's "load latest stock per repo" baseline. We populate
	// non-zero IncomingStock at parentB and bID so the TO-walk's
	// "decrement reserved incoming on execute" semantic is observable.
	priorStocks := map[stockKey]ent.Stock{
		{RepositoryID: aID, ItemID: itemID}:           {Quantity: seedQty, OwnQuantity: seedQty},
		{RepositoryID: grandparentID, ItemID: itemID}: {Quantity: seedQty, OwnQuantity: seedQty},
		{RepositoryID: parentBID, ItemID: itemID}:     {IncomingStock: 4, OwnIncomingStock: 4},
		{RepositoryID: bID, ItemID: itemID}:           {IncomingStock: 4, OwnIncomingStock: 4},
	}

	const transferQty int64 = 4
	stockMap := make(map[uuid.UUID]ent.Stock)

	svc := &service{}

	// Sanity: the function signature does not accept a *ent.Tx. The
	// build only succeeds if repoMap and priorStocks are passed in
	// pre-loaded, so this call structurally cannot fire a DB query.
	require.NoError(
		t,
		svc.applyItemMovementStockDelta(itemID, aID, bID, transferQty, repoMap, priorStocks, stockMap, true),
	)

	// FROM-walk on A applies quantity = -transferQty. With seedQty=10
	// at A the new Quantity is 10-4 = 6. ownStock=true on the
	// originating repo, so OwnQuantity is also clamped at 6.
	gotA, ok := stockMap[aID]
	require.Truef(t, ok, "expected stockMap entry at FROM endpoint %s", aID)
	require.Equal(t, seedQty-transferQty, gotA.Quantity, "FROM endpoint Quantity")
	require.Equal(t, seedQty-transferQty, gotA.OwnQuantity, "FROM endpoint OwnQuantity")

	// TO-walk on B applies quantity = +transferQty. The pre-loaded
	// IncomingStock=4 represents a previously-reserved arrival; on
	// execute the executor decrements it to reflect that the
	// reservation is now realised. Final IncomingStock is max(4-4, 0).
	gotB, ok := stockMap[bID]
	require.Truef(t, ok, "expected stockMap entry at TO endpoint %s", bID)
	require.Equal(t, transferQty, gotB.Quantity, "TO endpoint Quantity")
	require.Equal(t, int64(0), gotB.IncomingStock, "TO endpoint IncomingStock cleared")
	require.Equal(t, transferQty, gotB.OwnQuantity, "TO endpoint OwnQuantity")

	// parentB sits strictly below the LCA on the TO chain, so the
	// TO-walk visits it (Quantity 0 + 4 = 4, IncomingStock 4-4 = 0)
	// and the FROM-walk does not. ownStock=false at non-leaf hops.
	gotParentB, ok := stockMap[parentBID]
	require.Truef(t, ok, "expected stockMap entry at parentB %s (between B and the LCA)", parentBID)
	require.Equal(t, transferQty, gotParentB.Quantity, "parentB Quantity")
	require.Equal(t, int64(0), gotParentB.IncomingStock, "parentB IncomingStock cleared")

	// LCA (grandparent): both walks process it. FROM-walk decrements
	// by 4, TO-walk increments by 4. Net Quantity at the LCA stays
	// at seedQty (FINDINGS section 3.4 — net-zero through the LCA
	// is exactly why the walk stops there).
	gotLCA, ok := stockMap[grandparentID]
	require.Truef(t, ok, "expected stockMap entry at LCA %s", grandparentID)
	require.Equal(t, seedQty, gotLCA.Quantity, "LCA Quantity unchanged (FROM + TO net to zero)")

	// Hard requirement: no entry at the root. The walk must terminate
	// at the LCA. Without the cutoff the FROM-walk would have climbed
	// from grandparent to root.
	_, atRoot := stockMap[rootID]
	require.Falsef(t, atRoot, "stockMap should not contain ancestor above LCA %s", rootID)
}

// TestApplyItemMovementStockDelta_MissingRepoIsHardError pins the
// no-nil-fallback rule: when a repository encountered during recursion
// is absent from the pre-loaded repoMap, the executor must return
// errAncestorRepoNotPreloaded rather than silently issuing a DB lookup.
// This guards the contract that every caller is responsible for seeding
// the loader — Phase 4.3 deliberately rejects the historical "lazy
// load" codepath.
func TestApplyItemMovementStockDelta_MissingRepoIsHardError(t *testing.T) {
	t.Parallel()

	// Tree the executor will try to walk:
	//
	//   missingRoot
	//        └── A (FROM)
	//
	// We populate A but deliberately omit missingRoot. The first
	// recursion step from A reads repoMap[missingRoot] and must fail
	// with errAncestorRepoNotPreloaded.
	missingRootID := uuid.New()
	aID := uuid.New()
	bID := uuid.New() // unrelated TO endpoint with no parents

	repoMap := map[uuid.UUID]ent.Repository{
		// missingRootID intentionally absent.
		aID: {ID: aID, ParentID: missingRootID},
		bID: {ID: bID, ParentID: uuid.Nil},
	}
	priorStocks := map[stockKey]ent.Stock{}
	stockMap := make(map[uuid.UUID]ent.Stock)

	svc := &service{}
	err := svc.applyItemMovementStockDelta(uuid.New(), aID, bID, 1, repoMap, priorStocks, stockMap, true)
	require.ErrorIsf(t, err, errAncestorRepoNotPreloaded,
		"expected errAncestorRepoNotPreloaded, got %v", err)
}
