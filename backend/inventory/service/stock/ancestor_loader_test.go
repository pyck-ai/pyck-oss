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

	"github.com/pyck-ai/pyck/backend/common/feature"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// ancestorTestEnv bundles the ent client + tenant-scoped context that
// every test in this file builds on. The recursive CTE the loader emits
// is standard SQL accepted by Postgres.
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
	return newPGTestEnv(t)
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

// mkVirtualRepo is mkRepo but with virtual_repo=true, so a test can assert the
// loader projects the flag callers read (impl.go's LCA classification) rather
// than only ever seeing the false mkRepo hardcodes.
func (e *ancestorTestEnv) mkVirtualRepo(name string, parent uuid.UUID) uuid.UUID {
	e.t.Helper()
	b := e.client.Repository.Create().
		SetTenantID(e.tenantID).
		SetName(name).
		SetType(entrepository.TypeStatic).
		SetVirtualRepo(true)
	if parent != uuid.Nil {
		b.SetParentID(parent)
	}
	r, err := b.Save(e.ctx)
	require.NoError(e.t, err)
	return r.ID
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

// assertLoaderParity runs the same query through both loadAncestorStocks paths —
// the zero-value &service{} Go fallback and a Postgres-dialect service that
// routes to the inventory.load_ancestor_stocks proc — and asserts they agree on
// every field callers actually read (repo parent_id/virtual_repo, stock version
// and quantities). The other tests in this file all build &service{}, so only
// this helper exercises the proc that runs in production. It returns the proc
// result so a caller can pin case-specific expectations on top.
// The caller supplies ctx: with includeDeleted=true it must be a
// FEATURE_SHOW_DELETED context, or the Go path's HistoryMixin interceptor hides
// soft-deleted repos during hydration (the proc, raw SQL, honours only the
// param) and the two paths would legitimately diverge.
func (e *ancestorTestEnv) assertLoaderParity(
	ctx context.Context, seeds, items []uuid.UUID, includeDeleted bool,
) (map[uuid.UUID]ent.Repository, map[stockKey]ent.Stock) {
	e.t.Helper()

	goRepos, goStocks, err := (&service{}).
		loadAncestorStocks(ctx, e.withTx(), e.tenantID, seeds, items, includeDeleted)
	require.NoError(e.t, err)

	procRepos, procStocks, err := (&service{dbDialect: dialect.Postgres}).
		loadAncestorStocks(ctx, e.withTx(), e.tenantID, seeds, items, includeDeleted)
	require.NoError(e.t, err)

	require.Len(e.t, procRepos, len(goRepos), "proc and Go must return the same repo set")
	for id, gr := range goRepos {
		pr, ok := procRepos[id]
		require.True(e.t, ok, "repo %s from Go path missing in proc path", id)
		require.Equal(e.t, gr.ParentID, pr.ParentID, "repo %s parent_id mismatch", id)
		require.Equal(e.t, gr.VirtualRepo, pr.VirtualRepo, "repo %s virtual_repo mismatch", id)
	}

	require.Len(e.t, procStocks, len(goStocks), "proc and Go must return the same stock set")
	for k, gs := range goStocks {
		ps, ok := procStocks[k]
		require.True(e.t, ok, "stock %v from Go path missing in proc path", k)
		require.Equal(e.t, gs.Version, ps.Version, "stock %v version mismatch", k)
		require.Equal(e.t, gs.Quantity, ps.Quantity, "stock %v quantity mismatch", k)
		require.Equal(e.t, gs.OwnQuantity, ps.OwnQuantity, "stock %v own_quantity mismatch", k)
		require.Equal(e.t, gs.IncomingStock, ps.IncomingStock, "stock %v incoming mismatch", k)
		require.Equal(e.t, gs.OutgoingStock, ps.OutgoingStock, "stock %v outgoing mismatch", k)
		require.Equal(e.t, gs.OwnIncomingStock, ps.OwnIncomingStock, "stock %v own_incoming mismatch", k)
		require.Equal(e.t, gs.OwnOutgoingStock, ps.OwnOutgoingStock, "stock %v own_outgoing mismatch", k)
	}
	return procRepos, procStocks
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

	// Filtering by both items returns the leaf/itemA and mid/itemB rows.
	// Root has no stock, so it must NOT appear.
	repos, stocks, err := svc.loadAncestorStocks(e.ctx, tx, e.tenantID, []uuid.UUID{leaf}, []uuid.UUID{itemA, itemB}, false)
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

// TestLoadAncestorStocks_EmptyItemsSkipsStock pins the empty-item-list
// contract: with no items the ancestor repos are still returned, but the stock
// map comes back empty — the loader skips the closure stock scan instead of
// hydrating every item. This is the fast path for a movement whose repository
// holds no stock (loadItemIDsAtRepo returns none): under a bulk assign it
// avoids a full-catalog read per empty box.
//
// The case runs through assertLoaderParity because the proc expresses "no
// items" structurally (an empty jsonb array yields an empty pair set) while
// the Go path expresses it as an early return — the two must be pinned to
// agree, or a caller would see dialect-dependent results for an empty list.
func TestLoadAncestorStocks_EmptyItemsSkipsStock(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)
	chain := e.mkChain("empty-items", 3)
	root, mid, leaf := chain[0], chain[1], chain[2]

	// Seed stock at the ancestors: the old "empty means all" behaviour would
	// hydrate these, the new contract must not.
	item := e.mkItem("empty-items-sku")
	e.mkStock(mid, item, 7)
	e.mkStock(root, item, 3)

	repos, stocks := e.assertLoaderParity(e.ctx, []uuid.UUID{leaf}, []uuid.UUID{}, false)
	require.Len(t, repos, 3, "the ancestor repo walk still runs for an empty item list")
	require.Empty(t, stocks, "an empty item list loads no stock (not every item)")
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
		[]uuid.UUID{leafID}, []uuid.UUID{itemID}, false)
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
		[]uuid.UUID{leafID}, []uuid.UUID{itemID}, true)
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

// TestLoadAncestorStocks_ProcMatchesGo is the Postgres proc path's primary
// guard. Every other test here builds &service{} (empty dialect), which routes
// to the Go fallback — so without this, inventory.load_ancestor_stocks (the code
// that actually runs in production) would be verified only by its migration
// applying, never by a query returning the right rows. It seeds a graph touching
// every dimension the proc projects — a virtual root, shared-ancestor dedup
// (two leaves under one mid), stale-vs-fresh version, item filter, an ancestor
// holding no stock — asserts the proc agrees with the Go path, then pins the
// values the proc must compute on its own.
func TestLoadAncestorStocks_ProcMatchesGo(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	// virtual root -> real mid -> {leafA, leafB}; the two leaves share mid+root
	// so the closure must dedup them.
	root := e.mkVirtualRepo("vroot", uuid.Nil)
	mid := e.mkRepo("mid", root)
	leafA := e.mkRepo("leafA", mid)
	leafB := e.mkRepo("leafB", mid)

	itemA := e.mkItem("procItemA")
	itemB := e.mkItem("procItemB")

	// Stale (5) then fresh (9) at leafA/itemA: DISTINCT ON ... version DESC must
	// pick 9. itemB at mid. root holds no stock (LEFT JOIN → repo, no stock row).
	e.mkStock(leafA, itemA, 5)
	e.mkStock(leafA, itemA, 9)
	e.mkStock(mid, itemB, 7)

	repos, stocks := e.assertLoaderParity(
		e.ctx, []uuid.UUID{leafA, leafB}, []uuid.UUID{itemA, itemB}, false)

	require.Len(t, repos, 4, "closure {root, mid, leafA, leafB} deduped")
	require.True(t, repos[root].VirtualRepo, "proc must project virtual_repo=true for the virtual root")
	require.False(t, repos[mid].VirtualRepo)
	require.Equal(t, uuid.Nil, repos[root].ParentID, "a tree root reports the zero uuid as parent")
	require.Equal(t, root, repos[mid].ParentID)

	require.Len(t, stocks, 2, "root holds no stock, so only leafA/itemA and mid/itemB")
	require.Equal(t, int64(9), stocks[stockKey{RepositoryID: leafA, ItemID: itemA}].Quantity,
		"proc must select the highest version, not the stale row")
	require.Equal(t, int64(7), stocks[stockKey{RepositoryID: mid, ItemID: itemB}].Quantity)
}

// TestLoadAncestorStocks_ProcMatchesGo_IncludeDeleted pins proc/Go parity for
// the includeDeleted toggle specifically: the proc expresses it as
// `p_include_deleted OR deleted_at IS NULL` at both the recursive walk and the
// stock join, structurally different from the Go path's SQL-branch approach, so
// the two must be checked to agree at a soft-deleted ancestor in both modes.
func TestLoadAncestorStocks_ProcMatchesGo_IncludeDeleted(t *testing.T) {
	t.Parallel()
	e := newAncestorTestEnv(t)

	root := e.mkRepo("root", uuid.Nil)
	middle := e.mkRepo("middle", root)
	leaf := e.mkRepo("leaf", middle)
	item := e.mkItem("procDelItem")
	e.mkStock(leaf, item, 3)

	deleteCtx := feature.Context(e.ctx, feature.FEATURE_SHOW_DELETED)
	_, err := e.client.Repository.UpdateOneID(middle).
		SetDeletedAt(time.Now().UTC()).
		Save(deleteCtx)
	require.NoError(t, err)

	// Default: the walk stops at soft-deleted middle, so only leaf is reachable.
	reposDefault, _ := e.assertLoaderParity(e.ctx, []uuid.UUID{leaf}, []uuid.UUID{item}, false)
	require.Contains(t, reposDefault, leaf)
	require.NotContains(t, reposDefault, middle)
	require.NotContains(t, reposDefault, root)

	// includeDeleted=true under a FEATURE_SHOW_DELETED ctx: the walk crosses
	// middle, so all three appear (see assertLoaderParity's ctx contract).
	reposAll, _ := e.assertLoaderParity(deleteCtx, []uuid.UUID{leaf}, []uuid.UUID{item}, true)
	require.Contains(t, reposAll, leaf)
	require.Contains(t, reposAll, middle)
	require.Contains(t, reposAll, root)
}
