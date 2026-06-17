package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createItemSet = resolver.ParseTemplate(`mutation {
		createInventoryItemSet(input: {
			sku: "{{.Sku}}",
			itemIDs: ["{{.ItemID}}"],
			dataTypeID: "{{.DataTypeID}}",
			data: {
				type: "item_set",
				sum: 1,
				meta: { name: "Test Item Set", weight: 1, tags: ["Apple"] }
			}
		}) {
			inventoryItemSet {
				id
				tenantID
				createdAt
				createdBy
				updatedAt
				updatedBy
				deletedAt
				deletedBy
				dataTypeID
				data
				sku
				items {
					edges { node { id } }
				}
			}
		}
	}`)

	updateItemSet = resolver.ParseTemplate(`mutation {
		updateInventoryItemSet(id: "{{.ID}}", input: { addItemIDs: ["{{.ItemID}}"] }) {
			inventoryItemSet {
				id
				tenantID
				createdAt
				createdBy
				updatedAt
				updatedBy
				deletedAt
				deletedBy
				dataTypeID
				data
				sku
				items {
					edges { node { id } }
				}
			}
		}
	}`)

	deleteItemSet = resolver.ParseTemplate(`mutation {
		deleteInventoryItemSet(id: "{{.ID}}") { deletedID }
	}`)

	queryItemSets = resolver.ParseTemplate(`query {
		inventoryItemSets {
			totalCount
			edges {
				cursor
				node {
					id
					tenantID
					createdAt
					createdBy
					updatedAt
					updatedBy
					deletedAt
					deletedBy
					dataTypeID
					data
					sku
					items {
						edges {
							node {
								id
								tenantID
								createdAt
								createdBy
								updatedAt
								updatedBy
								deletedAt
								deletedBy
								dataTypeID
								sku
								data
							}
						}
					}
				}
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

	queryItemSetsWithFilter = resolver.ParseTemplate(`query {
		inventoryItemSets(
			after: null,
			orderBy: { direction: ASC, field: CREATED_AT },
			where: {{or .Where "null"}}
		) {
			totalCount
			edges {
				node {
					id
					tenantID
					dataTypeID
					data
					sku
				}
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

	queryItemSetsJSONOrder = resolver.ParseTemplate(`query {
		inventoryItemSets(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .JSONType}}, jsonType: {{.JSONType}}{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID dataTypeID data sku } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type itemSetNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	DataTypeID uuid.UUID
	Sku        string
	Data       map[string]any
	Items      struct {
		Edges []struct {
			Node struct {
				ID uuid.UUID
			}
		}
	}
}

type createItemSetData struct {
	CreateInventoryItemSet struct{ InventoryItemSet itemSetNode }
}

type updateItemSetData struct {
	UpdateInventoryItemSet struct{ InventoryItemSet itemSetNode }
}

type deleteItemSetData struct {
	DeleteInventoryItemSet struct{ DeletedID uuid.UUID }
}

type queryItemSetsData struct {
	InventoryItemSets struct {
		TotalCount int
		Edges      []struct {
			Node   itemSetNode
			Cursor string
		}
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestItemSet_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates item set with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()
		te.clearEvents(ctx)

		data := execOK[createItemSetData](te, ctx, createItemSet, map[string]any{
			"Sku":        testItem1.Sku,
			"ItemID":     item.ID,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreateInventoryItemSet.InventoryItemSet
		assert.Equal(t, testItem1.Sku, created.Sku)
		require.Len(t, created.Items.Edges, 1)
		assert.Equal(t, item.ID, created.Items.Edges[0].Node.ID)

		// Verify persisted
		itemSets, err := te.Ent.ItemSet.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		require.Len(t, itemSets, 1)
		assert.Equal(t, created.ID, itemSets[0].ID)
		assert.Equal(t, created.TenantID, itemSets[0].TenantID)
		assert.Equal(t, created.Sku, itemSets[0].Sku)

		te.assertEvents(ctx, Create("itemset", created.ID))
	})

	t.Run("rejects invalid item id", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createItemSet, map[string]any{
			"Sku":        testItem1.Sku,
			"ItemID":     uuid.Max,
			"DataTypeID": itemDataTypeID,
		}, "invalid itemIDs")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestItemSet_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates item set by adding item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item1 := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()
		item2 := te.newItem(ctx, userA).Sku(testItem2.Sku).Create()

		var itemset *ent.ItemSet
		err := te.withTx(ctx, func(tx *ent.Tx) error {
			var err error
			itemset, err = tx.ItemSet.Create().
				SetTenantID(userA.TenantID).
				SetSku(item1.Sku).
				AddItems(item1).
				Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
			return err
		})
		require.NoError(t, err)
		te.clearEvents(ctx)

		data := execOK[updateItemSetData](te, ctx, updateItemSet, map[string]any{
			"ID":     itemset.ID,
			"ItemID": item2.ID,
		})

		updated := data.UpdateInventoryItemSet.InventoryItemSet
		assert.Equal(t, itemset.ID, updated.ID)
		assert.Equal(t, itemset.TenantID, updated.TenantID)
		assert.Equal(t, itemset.Sku, updated.Sku)
		require.Len(t, updated.Items.Edges, 2)
		assert.Equal(t, item1.ID, updated.Items.Edges[0].Node.ID)
		assert.Equal(t, item2.ID, updated.Items.Edges[1].Node.ID)

		te.assertEvents(ctx, Update("itemset", itemset.ID))
	})

	t.Run("rejects invalid item id", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		itemset := te.newItemSet(ctx, userA).Sku(testItem1.Sku).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateItemSet, map[string]any{
			"ID":     itemset.ID,
			"ItemID": testItem1.ID,
		}, "invalid addItemIDs")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestItemSet_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes item set", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		itemset := te.newItemSet(ctx, userA).Sku(testItem1.Sku).Create()
		te.clearEvents(ctx)

		data := execOK[deleteItemSetData](te, ctx, deleteItemSet, map[string]any{
			"ID": itemset.ID,
		})

		assert.Equal(t, itemset.ID, data.DeleteInventoryItemSet.DeletedID)

		te.assertEvents(ctx, Delete("itemset", itemset.ID))
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestItemSet_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryItemSetsData](te, ctx, queryItemSets, nil)

		assert.Equal(t, 0, data.InventoryItemSets.TotalCount)
		assert.Empty(t, data.InventoryItemSets.Edges)
		assert.False(t, data.InventoryItemSets.PageInfo.HasNextPage)
		assert.False(t, data.InventoryItemSets.PageInfo.HasPreviousPage)
		assert.Nil(t, data.InventoryItemSets.PageInfo.StartCursor)
		assert.Nil(t, data.InventoryItemSets.PageInfo.EndCursor)
	})

	t.Run("returns item set with data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		itemset := te.newItemSet(ctx, userA).Sku(testItem1.Sku).Create()

		data := execOK[queryItemSetsData](te, ctx, queryItemSets, nil)

		assert.Equal(t, 1, data.InventoryItemSets.TotalCount)
		assert.False(t, data.InventoryItemSets.PageInfo.HasNextPage)
		assert.False(t, data.InventoryItemSets.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.InventoryItemSets.PageInfo.StartCursor)
		assert.NotNil(t, data.InventoryItemSets.PageInfo.EndCursor)
		require.Len(t, data.InventoryItemSets.Edges, 1)
		assert.Equal(t, itemset.ID, data.InventoryItemSets.Edges[0].Node.ID)
		assert.Equal(t, itemset.TenantID, data.InventoryItemSets.Edges[0].Node.TenantID)
		assert.Equal(t, itemset.Sku, data.InventoryItemSets.Edges[0].Node.Sku)
		assert.Equal(t, itemset.DataTypeID, data.InventoryItemSets.Edges[0].Node.DataTypeID)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestItemSet_QueryWithFilters(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctx := te.ctx(userA)

	// Create test item
	item := te.newItem(ctx, userA).Sku("MK-ENT-X2").Create()

	// Create item set with data
	var itemset1 *ent.ItemSet
	err := te.withTx(ctx, func(tx *ent.Tx) error {
		var err error
		itemset1, err = tx.ItemSet.Create().
			SetTenantID(userA.TenantID).
			SetSku("MK-ENT-QF1").
			SetDataTypeID(itemDataTypeID).
			SetData(testItem1.Data).
			AddItems(item).
			Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
		return err
	})
	require.NoError(t, err)

	// Create item set without data
	var itemset2 *ent.ItemSet
	err = te.withTx(ctx, func(tx *ent.Tx) error {
		var err error
		itemset2, err = tx.ItemSet.Create().
			SetTenantID(userA.TenantID).
			SetSku("MK-ENT-QFND1").
			Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
		return err
	})
	require.NoError(t, err)

	t.Run("returns all without filters", func(t *testing.T) {
		t.Parallel()

		data := execOK[queryItemSetsData](te, ctx, queryItemSetsWithFilter, nil)

		assert.Equal(t, 2, data.InventoryItemSets.TotalCount)
		require.Len(t, data.InventoryItemSets.Edges, 2)
		assert.False(t, data.InventoryItemSets.PageInfo.HasNextPage)
		assert.False(t, data.InventoryItemSets.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.InventoryItemSets.PageInfo.StartCursor)
		assert.NotNil(t, data.InventoryItemSets.PageInfo.EndCursor)

		// Check for the right item sets
		assert.Equal(t, itemset1.ID, data.InventoryItemSets.Edges[0].Node.ID)
		assert.Equal(t, itemset1.TenantID, data.InventoryItemSets.Edges[0].Node.TenantID)
		assert.Equal(t, itemset2.ID, data.InventoryItemSets.Edges[1].Node.ID)
		assert.Equal(t, itemset2.TenantID, data.InventoryItemSets.Edges[1].Node.TenantID)
	})

	t.Run("filters table tests", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			desc   string
			filter string
			count  int
		}{
			{
				desc:   "TestSkuIn(1)",
				filter: fmt.Sprintf(`{ skuIn: [%q] }`, itemset1.Sku),
				count:  1,
			},
			{
				desc:   "TestSkuIn(2)",
				filter: fmt.Sprintf(`{ skuIn: [%q, %q] }`, itemset1.Sku, itemset2.Sku),
				count:  2,
			},
			{
				desc:   "TestData",
				filter: `{ Data: ["type", "custom"] }`,
				count:  1,
			},
			{
				desc:   "TestDataHasKey",
				filter: `{ DataHasKey: "meta.name" }`,
				count:  1,
			},
			{
				desc:   "TestDataContains",
				filter: `{ DataContains: ["meta.tags", "foo"] }`,
				count:  1,
			},
			{
				desc:   "TestDataNull",
				filter: `{ Data: null }`,
				count:  2,
			},
			{
				desc:   "TestDataHasKeyNull",
				filter: `{ DataHasKey: null }`,
				count:  2,
			},
			{
				desc:   "TestDataInNull",
				filter: `{ DataIn: null }`,
				count:  2,
			},
			{
				desc:   "TestItemWith",
				filter: fmt.Sprintf(`{ hasItemsWith: [{ sku: %q }] }`, item.Sku),
				count:  1,
			},
		}
		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) {
				t.Parallel()

				data := execOK[queryItemSetsData](te, ctx, queryItemSetsWithFilter, map[string]any{
					"Where": tc.filter,
				})

				assert.Equal(t, tc.count, data.InventoryItemSets.TotalCount)
				require.Len(t, data.InventoryItemSets.Edges, tc.count)
				assert.False(t, data.InventoryItemSets.PageInfo.HasNextPage)
				assert.False(t, data.InventoryItemSets.PageInfo.HasPreviousPage)
				assert.NotNil(t, data.InventoryItemSets.PageInfo.StartCursor)
				assert.NotNil(t, data.InventoryItemSets.PageInfo.EndCursor)
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestItemSet_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		s1 := te.newItemSet(ctx, userA).Data(map[string]any{"priority": float64(30)}).Create()
		s2 := te.newItemSet(ctx, userA).Data(map[string]any{"priority": float64(10)}).Create()
		s3 := te.newItemSet(ctx, userA).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryItemSetsData](te, ctx, queryItemSetsJSONOrder, map[string]any{"JSONPath": "priority"})
		require.Equal(t, 3, data.InventoryItemSets.TotalCount)
		assert.Equal(t, s2.ID, data.InventoryItemSets.Edges[0].Node.ID)
		assert.Equal(t, s3.ID, data.InventoryItemSets.Edges[1].Node.ID)
		assert.Equal(t, s1.ID, data.InventoryItemSets.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		s1 := te.newItemSet(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(10)}}).Create()
		s2 := te.newItemSet(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(30)}}).Create()

		data := execOK[queryItemSetsData](te, ctx, queryItemSetsJSONOrder, map[string]any{"JSONPath": "meta.weight", "Direction": "DESC"})
		require.Equal(t, 2, data.InventoryItemSets.TotalCount)
		assert.Equal(t, s2.ID, data.InventoryItemSets.Edges[0].Node.ID)
		assert.Equal(t, s1.ID, data.InventoryItemSets.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		s1 := te.newItemSet(ctx, userA).Create()
		s2 := te.newItemSet(ctx, userA).Create()

		data := execOK[queryItemSetsData](te, ctx, queryItemSetsJSONOrder, map[string]any{"Field": "CREATED_AT", "Direction": "DESC"})
		require.Equal(t, 2, data.InventoryItemSets.TotalCount)
		assert.Equal(t, s2.ID, data.InventoryItemSets.Edges[0].Node.ID)
		assert.Equal(t, s1.ID, data.InventoryItemSets.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestItemSet_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newItemSet(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		s2 := te.newItemSet(ctx, userA).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryItemSetsData](te, ctx, queryItemSetsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})
		require.Equal(t, 1, data.InventoryItemSets.TotalCount)
		assert.Equal(t, s2.ID, data.InventoryItemSets.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newItemSet(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		s2 := te.newItemSet(ctx, userA).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryItemSetsData](te, ctx, queryItemSetsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})
		require.Equal(t, 1, data.InventoryItemSets.TotalCount)
		assert.Equal(t, s2.ID, data.InventoryItemSets.Edges[0].Node.ID)
	})
}
