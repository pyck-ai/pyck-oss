//nolint:testpackage // in-package test required: applyItemMovementStockDelta is package-private after Step 2.9.4.
package stock

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// TestApplyItemMovementStockDelta_SiblingMoveStopsAtLCA mirrors the simulate-
// path test from Step 3.2 (TestSimulateRepositoryStockMap_SiblingMoveStopsAtLCA)
// but exercises the executor walk: applyItemMovementStockDelta drives
// applyRepositoryStockDelta for both the FROM and the TO endpoints. With FROM
// and TO siblings under a shared grandparent, the executor must update entries
// at FROM, TO, and the grandparent (the LCA), but it must NOT visit any
// ancestor above the grandparent. Above the LCA the +q from the TO-walk and
// the -q from the FROM-walk cancel exactly, so visiting any further ancestor
// would only emit no-op snapshot rows.
//
// The contract pinned here is what RebuildStockTable's regression suite
// exercises end-to-end through the rebuild pipeline; this unit test makes the
// LCA cutoff explicit at the executor layer.
func TestApplyItemMovementStockDelta_SiblingMoveStopsAtLCA(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	// Tree:
	//
	//   root
	//    └── greatGrandparent
	//         └── grandparent (= LCA for from=A, to=B)
	//              ├── A (FROM)
	//              └── B (TO)
	mkRepo := func(name string, parent uuid.UUID) uuid.UUID {
		t.Helper()
		b := client.Repository.Create().
			SetTenantID(tenantID).
			SetName(name).
			SetType(entrepository.TypeStatic).
			SetVirtualRepo(false)
		if parent != uuid.Nil {
			b.SetParentID(parent)
		}
		repo, err := b.Save(ctx)
		require.NoError(t, err)
		return repo.ID
	}

	rootID := mkRepo("root", uuid.Nil)
	greatGrandparentID := mkRepo("greatGrandparent", rootID)
	grandparentID := mkRepo("grandparent", greatGrandparentID)
	aID := mkRepo("A", grandparentID)
	bID := mkRepo("B", grandparentID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("LCA-EXEC-SIBLING").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID
	const qty int64 = 7

	// Seed enough stock at A so the FROM-walk does not underflow when we
	// transfer qty from A to B. We seed only at A; the test's contract is
	// about which repos appear in the resulting stockMap, not about the
	// numerical content of the rows.
	_, err = client.Stock.Create().
		SetTenantID(tenantID).
		SetRepositoryID(aID).
		SetItemID(itemID).
		SetQuantity(qty).
		SetOwnQuantity(qty).
		Save(ctx)
	require.NoError(t, err)

	// Run the executor walk inside a real ent.Tx so the implementation's
	// tx.Repository.Get / tx.Stock.Query calls hit the same code path as the
	// production executor.
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	stockMap := make(map[uuid.UUID]ent.Stock)

	svc := &service{}

	// Pre-load the ancestor closure exactly the way Phase 4.3 requires
	// every executor caller to do — the function itself performs zero
	// DB reads.
	repoMap, priorStocks, err := svc.loadAncestorStocks(ctx, tx, tenantID, []uuid.UUID{aID, bID}, []uuid.UUID{itemID}, false)
	require.NoError(t, err)
	if err := svc.applyItemMovementStockDelta(itemID, aID, bID, qty, repoMap, priorStocks, stockMap, true); err != nil {
		t.Fatalf("applyItemMovementStockDelta returned error: %v", err)
	}

	// Expect entries at A, B, and the grandparent (the LCA). Both walks
	// process the LCA itself once each (FROM-walk decrements, TO-walk
	// increments, netting zero), so the LCA *is* present in the map.
	for _, want := range []uuid.UUID{aID, bID, grandparentID} {
		if _, ok := stockMap[want]; !ok {
			t.Errorf("expected stockMap entry at %s, got none", want)
		}
	}

	// Hard requirement: no entry at any ancestor above the LCA. Without the
	// cutoff the walk would climb to greatGrandparent and root and emit
	// no-op snapshot rows for them.
	for _, forbidden := range []uuid.UUID{greatGrandparentID, rootID} {
		if entry, ok := stockMap[forbidden]; ok {
			t.Errorf("stockMap should not contain ancestor above LCA %s, got entry %+v", forbidden, entry)
		}
	}
}

// TestApplyItemMovementStockDelta_DisjointRootsWalksToRoot pins the fallback
// behavior for the LCA == uuid.Nil case in the executor path (mirrors
// TestSimulateRepositoryStockMap_DisjointRootsWalksToRoot). Two disjoint trees
// share no ancestor, so each walk must continue to its own root.
func TestApplyItemMovementStockDelta_DisjointRootsWalksToRoot(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	mkRepo := func(name string, parent uuid.UUID) uuid.UUID {
		t.Helper()
		b := client.Repository.Create().
			SetTenantID(tenantID).
			SetName(name).
			SetType(entrepository.TypeStatic).
			SetVirtualRepo(false)
		if parent != uuid.Nil {
			b.SetParentID(parent)
		}
		repo, err := b.Save(ctx)
		require.NoError(t, err)
		return repo.ID
	}

	// Two disjoint trees: rootX -> A, rootY -> B.
	rootXID := mkRepo("rootX", uuid.Nil)
	rootYID := mkRepo("rootY", uuid.Nil)
	aID := mkRepo("A", rootXID)
	bID := mkRepo("B", rootYID)

	item, err := client.Item.Create().
		SetTenantID(tenantID).
		SetSku("LCA-EXEC-DISJOINT").
		Save(ctx)
	require.NoError(t, err)
	itemID := item.ID
	const qty int64 = 3

	// Seed enough stock at A and rootX so the FROM-walk's underflow guard is
	// satisfied when we walk from A all the way up to rootX.
	for _, repoID := range []uuid.UUID{aID, rootXID} {
		_, err := client.Stock.Create().
			SetTenantID(tenantID).
			SetRepositoryID(repoID).
			SetItemID(itemID).
			SetQuantity(qty).
			SetOwnQuantity(qty).
			Save(ctx)
		require.NoError(t, err)
	}

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	stockMap := make(map[uuid.UUID]ent.Stock)

	svc := &service{}

	// Pre-load the ancestor closure for both endpoints. With two
	// disjoint trees the loader walks each chain to its own root, so
	// the closure includes A, rootX, B and rootY.
	repoMap, priorStocks, err := svc.loadAncestorStocks(ctx, tx, tenantID, []uuid.UUID{aID, bID}, []uuid.UUID{itemID}, false)
	require.NoError(t, err)
	if err := svc.applyItemMovementStockDelta(itemID, aID, bID, qty, repoMap, priorStocks, stockMap, true); err != nil {
		t.Fatalf("applyItemMovementStockDelta returned error: %v", err)
	}

	// Both endpoints AND both tree roots must appear: with no shared
	// ancestor, the LCA cutoff cannot fire and each walk runs all the way
	// to its respective root.
	for _, want := range []uuid.UUID{aID, bID, rootXID, rootYID} {
		if _, ok := stockMap[want]; !ok {
			t.Errorf("expected stockMap entry at %s (LCA == Nil should walk to root), got none", want)
		}
	}
}
