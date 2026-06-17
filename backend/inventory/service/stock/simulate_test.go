//nolint:testpackage // in-package test required: simulateRepositoryStockMap is package-private after Step 2.9.4.
package stock

import (
	"testing"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// TestSimulateRepositoryStockMap_SiblingMoveStopsAtLCA exercises the LCA
// cutoff added in Step 3.2: when FROM and TO are siblings under a shared
// grandparent, the simulate walks must update entries at FROM, TO, and the
// grandparent (the LCA), but they must NOT visit any ancestor above the
// grandparent. Above the LCA the +q and -q deltas cancel exactly, so
// previously emitted no-op snapshot rows up to the tree root.
func TestSimulateRepositoryStockMap_SiblingMoveStopsAtLCA(t *testing.T) {
	t.Parallel()

	// Tree:
	//
	//   root
	//    └── greatGrandparent
	//         └── grandparent (= LCA for from=A, to=B)
	//              ├── A (FROM)
	//              └── B (TO)
	root := uuid.New()
	greatGrandparent := uuid.New()
	grandparent := uuid.New()
	a := uuid.New()
	b := uuid.New()

	repoMap := map[uuid.UUID]ent.Repository{
		root:             {ID: root, ParentID: uuid.Nil},
		greatGrandparent: {ID: greatGrandparent, ParentID: root},
		grandparent:      {ID: grandparent, ParentID: greatGrandparent},
		a:                {ID: a, ParentID: grandparent},
		b:                {ID: b, ParentID: grandparent},
	}

	itemID := uuid.New()
	const qty int64 = 7

	stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}

	svc := &service{}

	// FROM-walk: push -qty up A's chain.
	if err := svc.simulateRepositoryStockMap(itemID, a, b, -qty, stockMap, repoMap, true); err != nil {
		t.Fatalf("simulateRepositoryStockMap(FROM-walk) returned error: %v", err)
	}
	// TO-walk: push +qty up B's chain.
	if err := svc.simulateRepositoryStockMap(itemID, b, a, qty, stockMap, repoMap, true); err != nil {
		t.Fatalf("simulateRepositoryStockMap(TO-walk) returned error: %v", err)
	}

	// Expect entries at A, B, and the grandparent (the LCA). Both walks
	// process the LCA itself once each, contributing exactly the delta the
	// implementation would otherwise have skipped if we cut off below the
	// LCA. The point of this test is the absence of entries strictly above.
	for _, want := range []uuid.UUID{a, b, grandparent} {
		if _, ok := stockMap[want]; !ok {
			t.Errorf("expected stockMap entry at %s, got none", want)
		}
	}

	// Hard requirement: no entry at any ancestor above the LCA.
	for _, forbidden := range []uuid.UUID{greatGrandparent, root} {
		if entry, ok := stockMap[forbidden]; ok {
			t.Errorf("stockMap should not contain ancestor above LCA %s, got entry %+v", forbidden, entry)
		}
	}
}

// TestSimulateRepositoryStockMap_DisjointRootsWalksToRoot pins the fallback
// behavior for the LCA == uuid.Nil case (e.g., virtual vs non-virtual trees
// that share no ancestor): both walks must continue to their respective tree
// roots, just as they did before the cutoff was introduced.
func TestSimulateRepositoryStockMap_DisjointRootsWalksToRoot(t *testing.T) {
	t.Parallel()

	// Two disjoint trees: rootX -> a, rootY -> b.
	rootX := uuid.New()
	rootY := uuid.New()
	a := uuid.New()
	b := uuid.New()

	repoMap := map[uuid.UUID]ent.Repository{
		rootX: {ID: rootX, ParentID: uuid.Nil},
		rootY: {ID: rootY, ParentID: uuid.Nil},
		a:     {ID: a, ParentID: rootX},
		b:     {ID: b, ParentID: rootY},
	}

	itemID := uuid.New()
	const qty int64 = 3

	stockMap := map[uuid.UUID]map[uuid.UUID]ent.Stock{}
	svc := &service{}

	if err := svc.simulateRepositoryStockMap(itemID, a, b, -qty, stockMap, repoMap, true); err != nil {
		t.Fatalf("simulateRepositoryStockMap(FROM-walk) returned error: %v", err)
	}
	if err := svc.simulateRepositoryStockMap(itemID, b, a, qty, stockMap, repoMap, true); err != nil {
		t.Fatalf("simulateRepositoryStockMap(TO-walk) returned error: %v", err)
	}

	// Both endpoints AND both tree roots must appear: no shared ancestor
	// means the cutoff cannot fire.
	for _, want := range []uuid.UUID{a, b, rootX, rootY} {
		if _, ok := stockMap[want]; !ok {
			t.Errorf("expected stockMap entry at %s (LCA == Nil should walk to root), got none", want)
		}
	}
}
