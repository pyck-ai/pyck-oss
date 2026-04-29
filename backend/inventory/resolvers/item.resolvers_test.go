package resolvers_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createItem = resolver.ParseTemplate(`mutation {
		createInventoryItem(input: {
			sku: "{{.Sku}}",
			dataTypeID: "{{.DataTypeID}}",
			data: {
				type: "custom",
				sum: 15,
				meta: { name: "Testitem", weight: 50, tags: ["test", "foobar"] }
			}
		}) {
			inventoryItem { id tenantID dataTypeID sku }
		}
	}`)

	updateItem = resolver.ParseTemplate(`mutation {
		updateInventoryItem(id: "{{.ID}}", input: {
			dataTypeID: "{{.DataTypeID}}",
			data: {
				type: "custom",
				sum: 15,
				meta: { name: "Testitem2", weight: 50, tags: ["test", "foobar"] }
			}
		}) {
			inventoryItem { id sku tenantID dataTypeID data createdAt createdBy updatedAt updatedBy }
		}
	}`)

	deleteItem = resolver.ParseTemplate(`mutation {
		deleteInventoryItem(id: "{{.ID}}") { deletedID }
	}`)

	queryItems = resolver.ParseTemplate(`query {
		inventoryItems(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
		) {
			totalCount
			edges { node { id tenantID sku dataTypeID data } cursor }
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)

	queryItemsWithFilter = resolver.ParseTemplate(`query {
		inventoryItems(
			first: 20,
			after: null,
			orderBy: { direction: ASC, field: CREATED_AT },
			where: {{or .Where "null"}}
		) {
			totalCount
			edges { node { id tenantID sku dataTypeID data createdAt } }
			pageInfo { hasPreviousPage startCursor endCursor }
		}
	}`)

	queryItemsJSONOrder = resolver.ParseTemplate(`query {
		inventoryItems(
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
			edges { node { id tenantID sku dataTypeID data } cursor }
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type itemNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	DataTypeID uuid.UUID
	Sku        string
	Data       map[string]any
}

type createItemData struct {
	CreateInventoryItem struct{ InventoryItem itemNode }
}

type updateItemData struct {
	UpdateInventoryItem struct{ InventoryItem itemNode }
}

type deleteItemData struct {
	DeleteInventoryItem struct{ DeletedID uuid.UUID }
}

type queryItemsData struct {
	InventoryItems struct {
		TotalCount int
		Edges      []struct {
			Node   itemNode
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

func TestItem_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates item with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createItemData](te, ctx, createItem, map[string]any{
			"Sku":        testItem1.Sku,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreateInventoryItem.InventoryItem
		assert.Equal(t, testItem1.Sku, created.Sku)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		items, err := te.Ent.Item.Query().All(ctx)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, created.ID, items[0].ID)
		assert.Equal(t, created.TenantID, items[0].TenantID)
		assert.Equal(t, created.Sku, items[0].Sku)

		// Verify event
		te.assertEvents(ctx, Create("item", created.ID))
	})

	t.Run("rejects duplicate sku constraint", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createItem, map[string]any{
			"Sku":        item.Sku,
			"DataTypeID": item.DataTypeID,
		}, "UNIQUE constraint failed:")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestItem_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates item dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()
		te.clearEvents(ctx)

		data := execOK[updateItemData](te, ctx, updateItem, map[string]any{
			"ID":         item.ID,
			"DataTypeID": itemDataTypeIDTenantB,
		})

		assert.Equal(t, tenantA, data.UpdateInventoryItem.InventoryItem.TenantID)
		assert.Equal(t, itemDataTypeIDTenantB, data.UpdateInventoryItem.InventoryItem.DataTypeID)

		te.assertEvents(ctx, Update("item", item.ID))
	})

	t.Run("rejects update of other tenant's item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		item := te.newItem(ctxB, userB).Sku(testItem1.Sku).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateItem, map[string]any{
			"ID":         item.ID,
			"DataTypeID": item.DataTypeID,
		}, "gen: item not found")

		te.assertNoEvents(ctxA)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestItem_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Sku(testItem1.Sku).Create()
		te.clearEvents(ctx)

		data := execOK[deleteItemData](te, ctx, deleteItem, map[string]any{
			"ID": item.ID,
		})

		assert.Equal(t, item.ID, data.DeleteInventoryItem.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.Item.Get(te.ctxWithDeleted(userA), item.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("item", item.ID))
	})

	t.Run("rejects delete of other tenant's item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		item := te.newItem(ctxB, userB).Sku(testItem1.Sku).Create()
		te.clearEvents(ctxB)

		// Try to delete as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteItem, map[string]any{
			"ID": item.ID,
		}, "gen: item not found")

		te.assertNoEvents(ctxA)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestItem_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryItemsData](te, ctx, queryItems, nil)

		assert.Equal(t, 0, data.InventoryItems.TotalCount)
		assert.Empty(t, data.InventoryItems.Edges)
		assert.False(t, data.InventoryItems.PageInfo.HasNextPage)
		assert.False(t, data.InventoryItems.PageInfo.HasPreviousPage)
		assert.Nil(t, data.InventoryItems.PageInfo.StartCursor)
		assert.Nil(t, data.InventoryItems.PageInfo.EndCursor)
	})

	t.Run("returns only own tenant's items", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		itemA := te.newItem(ctxA, userA).Sku("TENANT-A-SKU").Create()
		te.newItem(ctxB, userB).Sku("TENANT-B-SKU").Create()

		data := execOK[queryItemsData](te, ctxA, queryItems, nil)

		require.Equal(t, 1, data.InventoryItems.TotalCount)
		assert.Equal(t, itemA.ID, data.InventoryItems.Edges[0].Node.ID)
		assert.Equal(t, tenantA, data.InventoryItems.Edges[0].Node.TenantID)
		assert.Equal(t, itemA.Sku, data.InventoryItems.Edges[0].Node.Sku)
		assert.Equal(t, itemA.Data, data.InventoryItems.Edges[0].Node.Data)
		assert.False(t, data.InventoryItems.PageInfo.HasNextPage)
		assert.False(t, data.InventoryItems.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.InventoryItems.PageInfo.StartCursor)
		assert.NotNil(t, data.InventoryItems.PageInfo.EndCursor)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestItem_QueryWithFilters(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	// Create test items
	ctx := te.ctx(userA)
	itemTenant1 := te.newItem(ctx, userA).Sku(testItem1.Sku).Data(testItem1.Data).Create()
	noDataItemTenant1 := te.newItem(ctx, userA).Sku(testItem2.Sku).Data(nil).Create()

	t.Run("returns all without filters", func(t *testing.T) {
		t.Parallel()

		data := execOK[queryItemsData](te, ctx, queryItemsWithFilter, nil)

		assert.Equal(t, 2, data.InventoryItems.TotalCount)
		require.Len(t, data.InventoryItems.Edges, 2)
		assert.False(t, data.InventoryItems.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.InventoryItems.PageInfo.StartCursor)
		assert.NotNil(t, data.InventoryItems.PageInfo.EndCursor)

		// Check for the right tenant1 items
		resItem1 := data.InventoryItems.Edges[0].Node
		assert.Equal(t, itemTenant1.ID, resItem1.ID)
		assert.Equal(t, itemTenant1.TenantID, resItem1.TenantID)
		resItem2 := data.InventoryItems.Edges[1].Node
		assert.Equal(t, noDataItemTenant1.ID, resItem2.ID)
		assert.Equal(t, noDataItemTenant1.TenantID, resItem2.TenantID)
	})

	t.Run("filters table tests", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			desc   string
			filter string
			count  int
		}{
			{
				desc:   "TestSkuIn",
				filter: fmt.Sprintf(`{ skuIn: [%q, %q]	}`, testItem1.Sku, testItem1.Sku),
				count:  1,
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
				desc:   "TestDataIn",
				filter: `{ DataIn: ["meta.name", "TestItem", "foo"] }`,
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
				desc:   "TestDataContainsNull",
				filter: `{ DataContains: null }`,
				count:  2,
			},
		}
		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) {
				t.Parallel()

				data := execOK[queryItemsData](te, ctx, queryItemsWithFilter, map[string]any{
					"Where": tc.filter,
				})

				assert.Equal(t, tc.count, data.InventoryItems.TotalCount)
				require.Len(t, data.InventoryItems.Edges, tc.count)
				assert.False(t, data.InventoryItems.PageInfo.HasPreviousPage)
				assert.NotNil(t, data.InventoryItems.PageInfo.StartCursor)
				assert.NotNil(t, data.InventoryItems.PageInfo.EndCursor)

				// Check for the right tenant1 item
				if tc.count > 0 {
					resItem := data.InventoryItems.Edges[0].Node
					assert.Equal(t, itemTenant1.ID, resItem.ID)
					assert.Equal(t, itemTenant1.TenantID, resItem.TenantID)
				}
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestItem_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		i1 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		i2 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		i3 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryItemsData](te, ctx, queryItemsJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.InventoryItems.TotalCount)
		assert.Equal(t, i2.ID, data.InventoryItems.Edges[0].Node.ID)
		assert.Equal(t, i3.ID, data.InventoryItems.Edges[1].Node.ID)
		assert.Equal(t, i1.ID, data.InventoryItems.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		i1 := te.newItem(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		i2 := te.newItem(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryItemsData](te, ctx, queryItemsJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.InventoryItems.TotalCount)
		assert.Equal(t, i2.ID, data.InventoryItems.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.InventoryItems.Edges[1].Node.ID)
	})

	t.Run("orders by JSON data with pagination", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newItem(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		i2 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		i3 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryItemsData](te, ctx, queryItemsJSONOrder, map[string]any{
			"JSONPath": "sum",
			"First":    2,
		})

		require.Len(t, data.InventoryItems.Edges, 2)
		assert.True(t, data.InventoryItems.PageInfo.HasNextPage)
		assert.Equal(t, i2.ID, data.InventoryItems.Edges[0].Node.ID)
		assert.Equal(t, i3.ID, data.InventoryItems.Edges[1].Node.ID)
	})

	t.Run("orders by JSON data with type cast", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		i1 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		i2 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		i3 := te.newItem(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryItemsData](te, ctx, queryItemsJSONOrder, map[string]any{
			"JSONPath": "sum",
			"JSONType": "NUMBER",
		})

		require.Equal(t, 3, data.InventoryItems.TotalCount)
		assert.Equal(t, i2.ID, data.InventoryItems.Edges[0].Node.ID)
		assert.Equal(t, i3.ID, data.InventoryItems.Edges[1].Node.ID)
		assert.Equal(t, i1.ID, data.InventoryItems.Edges[2].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		i1 := te.newItem(ctx, userA).Create()
		i2 := te.newItem(ctx, userA).Create()

		data := execOK[queryItemsData](te, ctx, queryItemsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.InventoryItems.TotalCount)
		assert.Equal(t, i2.ID, data.InventoryItems.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.InventoryItems.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestItem_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newItem(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		i2 := te.newItem(ctx, userA).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryItemsData](te, ctx, queryItemsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})
		require.Equal(t, 1, data.InventoryItems.TotalCount)
		assert.Equal(t, i2.ID, data.InventoryItems.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newItem(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		i2 := te.newItem(ctx, userA).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryItemsData](te, ctx, queryItemsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})
		require.Equal(t, 1, data.InventoryItems.TotalCount)
		assert.Equal(t, i2.ID, data.InventoryItems.Edges[0].Node.ID)
	})
}
