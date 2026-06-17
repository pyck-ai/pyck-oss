//nolint:testpackage // in-package test required: loadAncestorStocks is package-private.
package stock

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/inventory/ent/gen/privacy"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// ancestorTestEnv bundles the ent client + tenant-scoped context that
// every test in this file builds on. Using SQLite in-memory keeps the
// whole suite hermetic — the recursive CTE the loader emits is plain
// SQL that both PG (production) and SQLite (test) accept verbatim.
// The env carries the tenant-scoped ctx that every helper passes
// through; threading ctx through every helper signature would just
// shuffle the pointer without changing scope.
//
//nolint:containedctx // intentional: test scaffolding owns the ctx.
type ancestorTestEnv struct {
	t        *testing.T
	client   *ent.Client
	ctx      context.Context
	tenantID uuid.UUID
}

func newAncestorTestEnv(t *testing.T) *ancestorTestEnv {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, testresolver.DatabaseURI(t))
	t.Cleanup(func() { _ = client.Close() })

	tenantID := uuid.New()
	user := &authn.User{ID: uuid.New(), TenantID: tenantID}
	ctx := request.Context(context.Background(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)

	return &ancestorTestEnv{t: t, client: client, ctx: ctx, tenantID: tenantID}
}

// mkRepo creates a repository owned by the test tenant. Pass uuid.Nil
// for a root.
func (e *ancestorTestEnv) mkRepo(name string, parent uuid.UUID) uuid.UUID {
	e.t.Helper()
	b := e.client.Repository.Create().
		SetTenantID(e.tenantID).
		SetName(name).
		SetType(entrepository.TypeStatic).
		SetVirtualRepo(false)
	if parent != uuid.Nil {
		b.SetParentID(parent)
	}
	r, err := b.Save(e.ctx)
	require.NoError(e.t, err)
	return r.ID
}

// mkChain creates a parent_id chain root -> n_1 -> ... -> n_{depth-1}
// of the given depth, returns the IDs in walk order (index 0 = root,
// last index = leaf). depth==1 yields a single root.
func (e *ancestorTestEnv) mkChain(prefix string, depth int) []uuid.UUID {
	e.t.Helper()
	ids := make([]uuid.UUID, depth)
	parent := uuid.Nil
	for i := range depth {
		id := e.mkRepo(fmt.Sprintf("%s-%02d", prefix, i), parent)
		ids[i] = id
		parent = id
	}
	return ids
}

// mkItem creates an item owned by the test tenant.
func (e *ancestorTestEnv) mkItem(sku string) uuid.UUID {
	e.t.Helper()
	it, err := e.client.Item.Create().
		SetTenantID(e.tenantID).
		SetSku(sku).
		Save(e.ctx)
	require.NoError(e.t, err)
	return it.ID
}

// mkStock seeds a single stock row at (repo, item) with the given
// quantity. Used to pin the latest-row selection in tests that load
// stocks alongside repos. Picks the next available version so successive
// calls for the same (repo, item) (used by stale-vs-fresh tests) don't
// trip the Phase 6.1 unique index.
func (e *ancestorTestEnv) mkStock(repoID, itemID uuid.UUID, qty int64) uuid.UUID {
	e.t.Helper()
	var nextVersion int64
	latest, qerr := e.client.Stock.Query().
		Where(
			entstock.TenantID(e.tenantID),
			entstock.RepositoryID(repoID),
			entstock.ItemID(itemID),
		).
		Order(ent.Desc(entstock.FieldVersion)).
		First(e.ctx)
	if qerr == nil && latest != nil {
		nextVersion = latest.Version + 1
	} else if qerr != nil && !ent.IsNotFound(qerr) {
		require.NoError(e.t, qerr)
	}
	row, err := e.client.Stock.Create().
		SetTenantID(e.tenantID).
		SetRepositoryID(repoID).
		SetItemID(itemID).
		SetQuantity(qty).
		SetOwnQuantity(qty).
		SetVersion(nextVersion).
		Save(e.ctx)
	require.NoError(e.t, err)
	return row.ID
}

// withTx opens a transaction on the env's tenant context and rolls it
// back on cleanup, so tests don't leak partial state across cases.
func (e *ancestorTestEnv) withTx() *ent.Tx {
	e.t.Helper()
	tx, err := e.client.Tx(e.ctx)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// keysOfRepos sorts a repo map's keys for stable comparison output.
func keysOfRepos(m map[uuid.UUID]ent.Repository) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestLoadAncestorStocks_ChainDepth1 covers the trivial single-seed
// case: a repo at the root has only itself as the ancestor closure.
func TestLoadAncestorStocks_ChainDepth1(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)
	rootID := e.mkRepo("root", uuid.Nil)

	tx := e.withTx()
	svc := &service{}

	repos, stocks, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID, []uuid.UUID{rootID}, nil, false)
	require.NoError(t, err)
	require.Len(t, repos, 1, "depth-1 chain should yield exactly the seed repo")
	require.Contains(t, repos, rootID)
	require.Empty(t, stocks, "no seeded stock means an empty stock map")
}

// TestLoadAncestorStocks_ChainDepth5 walks a 5-deep chain from the
// leaf and verifies every level (leaf included) shows up exactly once.
func TestLoadAncestorStocks_ChainDepth5(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)
	chain := e.mkChain("c5", 5)
	leaf := chain[len(chain)-1]

	tx := e.withTx()
	svc := &service{}

	repos, _, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID, []uuid.UUID{leaf}, nil, false)
	require.NoError(t, err)
	require.Len(t, repos, 5, "chain of 5 should yield all 5 ancestors+leaf")
	for _, id := range chain {
		require.Contains(t, repos, id, "missing %s from repo map (keys=%v)", id, keysOfRepos(repos))
	}
}

// TestLoadAncestorStocks_ChainDepth10 exercises a deeper chain still
// well within the depth cap (20). Every level must be returned and the
// repository structs must hydrate with their parent_id intact, so the
// caller can reuse them for LCA computations downstream.
func TestLoadAncestorStocks_ChainDepth10(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)
	chain := e.mkChain("c10", 10)
	leaf := chain[len(chain)-1]

	tx := e.withTx()
	svc := &service{}

	repos, _, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID, []uuid.UUID{leaf}, nil, false)
	require.NoError(t, err)
	require.Len(t, repos, 10)

	// Walk the chain via the loaded structs and verify parent linkage.
	cur := leaf
	for i := len(chain) - 1; i >= 0; i-- {
		repo, ok := repos[cur]
		require.Truef(t, ok, "expected %s at chain index %d", cur, i)
		if i == 0 {
			require.Equal(t, uuid.Nil, repo.ParentID, "root parent must be Nil")
		} else {
			require.Equal(t, chain[i-1], repo.ParentID, "broken chain at index %d", i)
			cur = repo.ParentID
		}
	}
}

// TestLoadAncestorStocks_SharedAncestorsDeduped feeds two seeds whose
// ancestor chains overlap above their LCA, and asserts the result has
// no duplicates (DISTINCT in the CTE) while still containing every
// distinct ancestor.
func TestLoadAncestorStocks_SharedAncestorsDeduped(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	// Tree:
	//
	//   root
	//   └── shared
	//        ├── leftBranch
	//        │     └── leftLeaf
	//        └── rightBranch
	//              └── rightLeaf
	rootID := e.mkRepo("root", uuid.Nil)
	sharedID := e.mkRepo("shared", rootID)
	leftBranchID := e.mkRepo("leftBranch", sharedID)
	rightBranchID := e.mkRepo("rightBranch", sharedID)
	leftLeafID := e.mkRepo("leftLeaf", leftBranchID)
	rightLeafID := e.mkRepo("rightLeaf", rightBranchID)

	tx := e.withTx()
	svc := &service{}

	repos, _, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID,
		[]uuid.UUID{leftLeafID, rightLeafID}, nil, false)
	require.NoError(t, err)

	// Six distinct repos in the closure: leftLeaf, rightLeaf, leftBranch,
	// rightBranch, shared, root. Map invariant guarantees no duplicates;
	// length check pins the dedupe contract regardless.
	want := []uuid.UUID{rootID, sharedID, leftBranchID, rightBranchID, leftLeafID, rightLeafID}
	require.Len(t, repos, len(want))
	for _, id := range want {
		require.Contains(t, repos, id)
	}
}

// TestLoadAncestorStocks_StocksHydratedAndFiltered seeds stock rows on
// some seeds (with a stale and a fresh row to verify "latest only"),
// then verifies:
//   - the latest stock row is the one returned for that (repo, item),
//   - repos without any stock are absent from the stock map,
//   - filtering by the items list narrows the result accordingly.
func TestLoadAncestorStocks_StocksHydratedAndFiltered(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)
	chain := e.mkChain("stk", 3)
	root, mid, leaf := chain[0], chain[1], chain[2]

	itemA := e.mkItem("itemA")
	itemB := e.mkItem("itemB")

	// Seed a stale row at the leaf for itemA, then a fresher one. The
	// loader's NOT EXISTS-on-self predicate must pick the fresher row.
	staleID := e.mkStock(leaf, itemA, 5)
	// Force a strictly later created_at by sleeping a millisecond; SQLite's
	// CURRENT_TIMESTAMP resolution is sufficient for ent's default and the
	// stale-vs-fresh ordering needs to be unambiguous.
	time.Sleep(2 * time.Millisecond)
	freshID := e.mkStock(leaf, itemA, 9)
	require.NotEqual(t, staleID, freshID)

	// Seed stock at mid for itemB. Root has no stock at all.
	midID := e.mkStock(mid, itemB, 7)
	require.NotEqual(t, uuid.Nil, midID)

	tx := e.withTx()
	svc := &service{}

	// Without an item filter, both the leaf/itemA and mid/itemB rows
	// should show up. Root has no stock, so it must NOT appear.
	repos, stocks, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID, []uuid.UUID{leaf}, nil, false)
	require.NoError(t, err)
	require.Len(t, repos, 3)
	require.Len(t, stocks, 2)
	require.Equal(t, int64(9), stocks[stockKey{RepositoryID: leaf, ItemID: itemA}].Quantity,
		"latest row for (leaf, itemA) should win, not stale=5")
	require.Equal(t, int64(7), stocks[stockKey{RepositoryID: mid, ItemID: itemB}].Quantity)
	require.NotContains(t, stocks, stockKey{RepositoryID: root, ItemID: itemA})
	require.NotContains(t, stocks, stockKey{RepositoryID: root, ItemID: itemB})

	// With items=[itemA] only the leaf/itemA pair survives.
	_, stocksA, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID,
		[]uuid.UUID{leaf}, []uuid.UUID{itemA}, false)
	require.NoError(t, err)
	require.Len(t, stocksA, 1)
	require.Contains(t, stocksA, stockKey{RepositoryID: leaf, ItemID: itemA})
	require.NotContains(t, stocksA, stockKey{RepositoryID: mid, ItemID: itemB})
}

// TestLoadAncestorStocks_IncludeDeletedToggle verifies the
// includeDeleted toggle: a soft-deleted ancestor must be invisible in
// the default mode and present when includeDeleted=true. The toggle
// must propagate to both the recursive walk (so the chain isn't
// truncated at the deleted ancestor when includeDeleted=true) and to
// the stock hydration (a soft-deleted stock row must be hidden by
// default and visible when included).
func TestLoadAncestorStocks_IncludeDeletedToggle(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	// Tree: root -> middle -> leaf. We will soft-delete `middle`.
	rootID := e.mkRepo("root", uuid.Nil)
	middleID := e.mkRepo("middle", rootID)
	leafID := e.mkRepo("leaf", middleID)
	itemID := e.mkItem("delItem")
	_ = e.mkStock(leafID, itemID, 3)

	// Soft-delete middle. We bypass the HistoryMixin's filter via the
	// FEATURE_SHOW_DELETED-marked context so the UpdateOne is allowed
	// to address the row at all.
	deleteCtx := feature.Context(e.ctx, feature.FEATURE_SHOW_DELETED)
	_, err := e.client.Repository.UpdateOneID(middleID).
		SetDeletedAt(time.Now().UTC()).
		Save(deleteCtx)
	require.NoError(t, err)

	tx := e.withTx()
	svc := &service{}

	// Default (includeDeleted=false): the recursive walk filters out
	// `middle`, so the leaf's anchor row matches but the recursion
	// can't follow parent_id through a deleted node. Result: only the
	// leaf is reachable. The stock for leaf is non-deleted so it stays.
	repos, stocks, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID,
		[]uuid.UUID{leafID}, nil, false)
	require.NoError(t, err)
	require.Contains(t, repos, leafID)
	require.NotContains(t, repos, middleID,
		"soft-deleted middle must be excluded when includeDeleted=false")
	require.NotContains(t, repos, rootID,
		"root unreachable through soft-deleted middle when includeDeleted=false")
	require.Contains(t, stocks, stockKey{RepositoryID: leafID, ItemID: itemID})

	// includeDeleted=true: the walk crosses the soft-deleted middle,
	// so root, middle, and leaf are all returned.
	repos2, _, err := svc.loadAncestorStocks(deleteCtx, tx, e.tenantID,
		[]uuid.UUID{leafID}, nil, true)
	require.NoError(t, err)
	require.Contains(t, repos2, leafID)
	require.Contains(t, repos2, middleID)
	require.Contains(t, repos2, rootID)
}

// TestLoadAncestorStocks_DepthCapTruncates pins the §0.4 depth cap:
// a chain deeper than ancestorWalkDepthCap+1, seeded at the leaf, must
// yield exactly ancestorWalkDepthCap+1 repos (depths 0 through cap
// inclusive). Any ancestor beyond the cap is truncated and absent
// from the returned map. Phase 4.3 raised the cap to 100 to
// accommodate the deep-nesting-50-levels regression fixture; the
// truncation behaviour itself is unchanged.
func TestLoadAncestorStocks_DepthCapTruncates(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)
	const overflow = 5
	chain := e.mkChain("deep", ancestorWalkDepthCap+overflow)
	leaf := chain[len(chain)-1]

	tx := e.withTx()
	svc := &service{}

	repos, _, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID, []uuid.UUID{leaf}, nil, false)
	require.NoError(t, err)

	// Depth cap admits cap+1 levels: the anchor (depth 0) plus cap
	// recursive expansions. Seeded at the leaf, the deepest cap+1
	// entries must appear and the remaining `overflow` closest-to-root
	// entries must not.
	wantLevels := ancestorWalkDepthCap + 1
	require.Len(t, repos, wantLevels,
		"with depth cap %d, a %d-deep chain seeded at the leaf must yield %d levels",
		ancestorWalkDepthCap, len(chain), wantLevels)
	for i := len(chain) - wantLevels; i < len(chain); i++ {
		require.Contains(t, repos, chain[i],
			"depth %d (id=%s) within cap should be present", len(chain)-1-i, chain[i])
	}
	for i := range len(chain) - wantLevels {
		require.NotContains(t, repos, chain[i],
			"depth %d (id=%s) beyond cap must be truncated", len(chain)-1-i, chain[i])
	}
}

// TestLoadAncestorStocks_EmptySeeds returns empty maps without making
// any DB call. This is the cheap-path the resolvers rely on: a
// movement with no FROM/TO seeds (or a collection batch where the
// position list is empty) must not spend a round trip.
func TestLoadAncestorStocks_EmptySeeds(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	tx := e.withTx()
	svc := &service{}

	repos, stocks, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID, nil, nil, false)
	require.NoError(t, err)
	require.NotNil(t, repos)
	require.NotNil(t, stocks)
	require.Empty(t, repos)
	require.Empty(t, stocks)
}
