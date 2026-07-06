package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// TestGetStockTreeReturnsRepoBeyondDefaultLimit is a regression test for
// getStockTree returning an arbitrary, mostly-wrong slice of the stock tree on
// tenants with more than the default query limit (mixin.Limit) of distinct
// (item, repository) pairs.
//
// Root cause: getStockTree pre-computed the "latest stock id per (item,
// repository)" via latestStockIDs — an unordered .IDs(ctx) query that the
// LimitMixin interceptor silently capped at mixin.Limit. With no item filter
// the query spans the whole tenant, so on tenants with more pairs than the cap
// the latest rows of late-created repositories fell outside the arbitrary
// capped set and those repositories were missing from the tree.
//
// The fix deduplicates inline via latestStockPredicate, evaluated under the
// query's own paginated retrieval (AllPages), so no rows are silently dropped.
func TestGetStockTreeReturnsRepoBeyondDefaultLimit(t *testing.T) {
	t.Parallel()

	te := setup(t)
	defer te.Close(t)

	ctx := te.ctx(userA)

	// Seed more than mixin.Limit distinct (item, repository) pairs so the
	// tenant-wide latest-id pre-computation hits the default cap.
	const fillerRepos, fillerItems = 21, 10
	require.Greater(t, fillerRepos*fillerItems, mixin.Limit,
		"must seed more filler (item, repository) pairs than the default query limit")

	fillerItemIDs := make([]uuid.UUID, 0, fillerItems)
	for i := range fillerItems {
		fillerItemIDs = append(fillerItemIDs,
			te.newItem(ctx, userA).Sku(fmt.Sprintf("tree-filler-item-%02d", i)).Create().ID)
	}
	for r := range fillerRepos {
		repoID := te.newRepository(ctx, userA).Name(fmt.Sprintf("tree-filler-repo-%02d", r)).Create().ID
		for _, itemID := range fillerItemIDs {
			te.newStock(ctx, userA, itemID, repoID).Quantity(1).Create()
		}
	}

	// The target repo + item are created AFTER every filler, so their
	// time-ordered v7 ids sort last and fall outside the arbitrary capped set
	// under the buggy code (which selects ids with no ORDER BY).
	targetItem := te.newItem(ctx, userA).Sku("tree-target-item").Create().ID
	targetRepo := te.newRepository(ctx, userA).Name("tree-target-repo").Create().ID
	const targetQty = 42
	te.newStock(ctx, userA, targetItem, targetRepo).Quantity(targetQty).Create()

	// No item filter: getStockTree must dedup across the whole tenant and still
	// surface the target repository's latest stock.
	data := execOK[queryStockTreeData](te, ctx, getStockTreeTemplate, map[string]any{})

	node := findStockTreeRepo(data.GetStockTree.Edges, targetRepo)
	require.NotNil(t, node,
		"target repository must appear in the stock tree even when the tenant has "+
			"more than mixin.Limit (item, repository) pairs")
	require.Len(t, node.Stocks, 1, "target repository must carry exactly its latest stock row")
	assert.Equal(t, targetItem, node.Stocks[0].ItemID)
	assert.Equal(t, int64(targetQty), node.Stocks[0].Quantity)
}
