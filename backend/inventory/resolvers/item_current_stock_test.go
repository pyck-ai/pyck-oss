package resolvers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

var queryItemsByCurrentStock = resolver.ParseTemplate(`query {
	inventoryItems(first: 50, where: {{.Where}}) {
		edges { node { id } }
	}
}`)

// currentStockItemIDs runs inventoryItems with the given where input and
// returns the matched item ids.
func currentStockItemIDs(te *testEnv, ctx context.Context, where string) map[uuid.UUID]bool {
	te.t.Helper()

	data := execOK[struct {
		InventoryItems struct {
			Edges []struct{ Node struct{ ID uuid.UUID } }
		}
	}](te, ctx, queryItemsByCurrentStock, map[string]any{"Where": where})

	ids := make(map[uuid.UUID]bool, len(data.InventoryItems.Edges))
	for _, e := range data.InventoryItems.Edges {
		ids[e.Node.ID] = true
	}
	return ids
}

// TestInventoryItemsHasCurrentStock pins hasCurrentStock / hasCurrentStockWith
// (#1572): they follow the current rows (highest version per repository, a
// deleted head hides its pair, empty rows never match) while the deprecated
// hasItemStocksWith keeps matching any row ever written.
func TestInventoryItemsHasCurrentStock(t *testing.T) {
	t.Parallel()

	const (
		positiveCurrent = `{hasCurrentStockWith: [{quantityGT: 0}]}`
		anyCurrent      = `{hasCurrentStock: true}`
		noCurrent       = `{hasCurrentStock: false}`
		legacy          = `{hasItemStocksWith: [{quantityGT: 0}]}`
	)

	t.Run("emptied item is not in stock, but still matches the deprecated filter", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(10).Create() // v0 positive, superseded
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(0).Create()  // v1 empty, current

		assert.NotContains(t, currentStockItemIDs(te, ctx, positiveCurrent), item.ID)
		assert.NotContains(t, currentStockItemIDs(te, ctx, anyCurrent), item.ID)
		assert.Contains(t, currentStockItemIDs(te, ctx, noCurrent), item.ID)
		// Why hasItemStocksWith is deprecated: it matches the superseded row.
		assert.Contains(t, currentStockItemIDs(te, ctx, legacy), item.ID)
	})

	t.Run("positive current row matches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(0).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(7).Create()

		assert.Contains(t, currentStockItemIDs(te, ctx, positiveCurrent), item.ID)
		assert.Contains(t, currentStockItemIDs(te, ctx, anyCurrent), item.ID)
		assert.NotContains(t, currentStockItemIDs(te, ctx, noCurrent), item.ID)
		assert.Contains(t, currentStockItemIDs(te, ctx, `{hasCurrentStockWith: [{quantityGT: 6}]}`), item.ID)
		assert.NotContains(t, currentStockItemIDs(te, ctx, `{hasCurrentStockWith: [{quantityGT: 7}]}`), item.ID)
	})

	t.Run("item without any stock has no current stock", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()

		assert.NotContains(t, currentStockItemIDs(te, ctx, anyCurrent), item.ID)
		assert.Contains(t, currentStockItemIDs(te, ctx, noCurrent), item.ID)
	})

	t.Run("a deleted head hides its pair", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(100).Create()           // v0 live, positive
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(100).Deleted().Create() // v1 tombstone, current

		assert.NotContains(t, currentStockItemIDs(te, ctx, positiveCurrent), item.ID)
		assert.NotContains(t, currentStockItemIDs(te, ctx, anyCurrent), item.ID)
		assert.Contains(t, currentStockItemIDs(te, ctx, noCurrent), item.ID)
	})

	t.Run("one empty and one positive repository matches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		empty := te.newRepository(ctx, userA).Create()
		full := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, empty.ID).Quantity(5).Create()
		te.newStock(ctx, userA, item.ID, empty.ID).Quantity(0).Create()
		te.newStock(ctx, userA, item.ID, full.ID).Quantity(3).Create()

		assert.Contains(t, currentStockItemIDs(te, ctx, positiveCurrent), item.ID)
		assert.NotContains(t, currentStockItemIDs(te, ctx, noCurrent), item.ID)

		// Repository plus value filter hold on the same current row.
		inFull := fmt.Sprintf(`{hasCurrentStockWith: [{repositoryID: %q, quantityGT: 0}]}`, full.ID)
		inEmpty := fmt.Sprintf(`{hasCurrentStockWith: [{repositoryID: %q, quantityGT: 0}]}`, empty.ID)
		assert.Contains(t, currentStockItemIDs(te, ctx, inFull), item.ID)
		assert.NotContains(t, currentStockItemIDs(te, ctx, inEmpty), item.ID,
			"the superseded positive row of the emptied repository must not match")
	})

	t.Run("list entries are ANDed on one current row", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo := te.newRepository(ctx, userA).Create()
		te.newStock(ctx, userA, item.ID, repo.ID).Quantity(5).Create()

		assert.Contains(t, currentStockItemIDs(te, ctx, `{hasCurrentStockWith: [{quantityGT: 0}, {quantityLT: 10}]}`), item.ID)
		assert.NotContains(t, currentStockItemIDs(te, ctx, `{hasCurrentStockWith: [{quantityGT: 0}, {quantityGT: 10}]}`), item.ID)
	})

	t.Run("another tenant's stock never matches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		itemA := te.newItem(ctxA, userA).Create()
		itemB := te.newItem(ctxB, userB).Create()
		repoB := te.newRepository(ctxB, userB).Create()
		te.newStock(ctxB, userB, itemB.ID, repoB.ID).Quantity(9).Create()

		// Tenant A sees neither tenant B's item nor a match for its own.
		inA := currentStockItemIDs(te, ctxA, positiveCurrent)
		assert.NotContains(t, inA, itemA.ID)
		assert.NotContains(t, inA, itemB.ID)
		assert.Contains(t, currentStockItemIDs(te, ctxA, noCurrent), itemA.ID)

		// Tenant B matches its own.
		assert.Contains(t, currentStockItemIDs(te, ctxB, positiveCurrent), itemB.ID)
	})

	t.Run("a time cutoff selects the as-of row", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		repo := te.newRepository(ctx, userA).Create()
		v0 := te.newStock(ctx, userA, item.ID, repo.ID).Quantity(10).Create()
		v1 := te.newStock(ctx, userA, item.ID, repo.ID).Quantity(0).Create()
		require.True(t, v1.CreatedAt.After(v0.CreatedAt))
		cutoff := v0.CreatedAt.Add(v1.CreatedAt.Sub(v0.CreatedAt) / 2).Format(time.RFC3339Nano)

		// Today the item is empty; as of the cutoff v0 (10) was current.
		assert.NotContains(t, currentStockItemIDs(te, ctx, positiveCurrent), item.ID)
		asOf := fmt.Sprintf(`{hasCurrentStockWith: [{quantityGT: 0, time: %q}]}`, cutoff)
		assert.Contains(t, currentStockItemIDs(te, ctx, asOf), item.ID)
	})
}
