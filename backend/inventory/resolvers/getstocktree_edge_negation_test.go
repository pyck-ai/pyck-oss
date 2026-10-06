package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetStockTreeNegatedEdgeFilters pins that getStockTree honours a negated
// edge filter. The seeded stock has a live item and repository, so hasItem:
// false and hasRepository: false must not match it (they would match a stock
// whose item or repository is soft-deleted) and the tree must come back
// empty, while the positive form still returns the seeded stock. The
// hand-written stock predicate builder mapped the false branch to a
// Has<Edge>With() with no predicates, which means "has any neighbour" and
// returned every stock instead of none.
func TestGetStockTreeNegatedEdgeFilters(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctx := te.ctx(userA)

	item := te.newItem(ctx, userA).Sku("edge-negation-item").Create()
	repo := te.newRepository(ctx, userA).Name("edge-negation-repo").Create()
	te.newStock(ctx, userA, item.ID, repo.ID).Quantity(3).Create()

	tree := func(t *testing.T, where string) []stockTreeEdge {
		t.Helper()
		data := execOK[queryStockTreeData](te, ctx, getStockTreeTemplate, map[string]any{"Where": where})
		return data.GetStockTree.Edges
	}
	scoped := func(extra string) string {
		return fmt.Sprintf(`{ repositoryID: %q, %s }`, repo.ID, extra)
	}

	t.Run("positive control: hasItem true returns the stock", func(t *testing.T) {
		t.Parallel()

		node := findStockTreeRepo(tree(t, scoped("hasItem: true")), repo.ID)
		require.NotNil(t, node)
		require.Len(t, node.Stocks, 1)
		assert.Equal(t, item.ID, node.Stocks[0].ItemID)
	})

	t.Run("hasItem false matches nothing", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, tree(t, scoped("hasItem: false")), "every stock has an item, so no repository may be returned")
	})

	t.Run("hasRepository false matches nothing", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, tree(t, scoped("hasRepository: false")), "every stock has a repository, so no repository may be returned")
	})

	t.Run("nested negation inside and/or/not agrees", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, tree(t, scoped("and: [{hasItem: false}]")), "and")
		// Empty whether the elements are ORed or ANDed, so this pins the
		// negation inside an or element, not the or semantics themselves.
		assert.Empty(t, tree(t, scoped("or: [{hasItem: false}, {hasRepository: false}]")), "or")
		assert.Empty(t, tree(t, scoped("not: {hasItem: true}")), "not")
	})
}
