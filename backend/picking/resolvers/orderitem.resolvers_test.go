package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createOrderItem = resolver.ParseTemplate(`mutation {
		createPickingOrderItem(input: {
			sku: "{{.Sku}}",
			quantity: {{.Quantity}},
			orderID: "{{.OrderID}}",
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestItem"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			pickingOrderItem { id tenantID orderID dataTypeID sku quantity data }
		}
	}`)

	updateOrderItem = resolver.ParseTemplate(`mutation {
		updatePickingOrderItem(id: "{{.ID}}", input: {
			{{if .Sku}}sku: "{{.Sku}}",{{end}}
			{{if .Quantity}}quantity: {{.Quantity}},{{end}}
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestItem"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}{{end}}
		}) {
			pickingOrderItem { id tenantID orderID dataTypeID sku quantity data }
		}
	}`)

	deleteOrderItem = resolver.ParseTemplate(`mutation {
		deletePickingOrderItem(id: "{{.ID}}") { deletedID }
	}`)

	queryOrderItems = resolver.ParseTemplate(`query {
		pickingOrderItems(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID orderID dataTypeID sku quantity data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)

	queryOrderItemsJSONOrder = resolver.ParseTemplate(`query {
		pickingOrderItems(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID orderID dataTypeID sku quantity data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type orderItemNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	OrderID    uuid.UUID
	DataTypeID uuid.UUID
	Sku        string
	Quantity   int64
	Data       map[string]any
}

type createOrderItemData struct {
	CreatePickingOrderItem struct{ PickingOrderItem orderItemNode }
}

type updateOrderItemData struct {
	UpdatePickingOrderItem struct{ PickingOrderItem orderItemNode }
}

type deleteOrderItemData struct {
	DeletePickingOrderItem struct{ DeletedID uuid.UUID }
}

type queryOrderItemsData struct {
	PickingOrderItems struct {
		TotalCount int
		Edges      []struct{ Node orderItemNode }
		PageInfo   struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestOrderItem_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates order item with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[createOrderItemData](te, ctx, createOrderItem, map[string]any{
			"Sku":        "MK-ENT-X2",
			"Quantity":   10,
			"OrderID":    order.ID,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreatePickingOrderItem.PickingOrderItem
		assert.Equal(t, "MK-ENT-X2", created.Sku)
		assert.Equal(t, int64(10), created.Quantity)
		assert.Equal(t, order.ID, created.OrderID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.OrderItems.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "MK-ENT-X2", stored.Sku)

		// Verify event
		te.assertEvents(ctx, Create("orderitems", created.ID))
	})

	t.Run("creates order item with custom data values", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[createOrderItemData](te, ctx, createOrderItem, map[string]any{
			"Sku":        "MK-ENT-X3",
			"Quantity":   20,
			"OrderID":    order.ID,
			"DataTypeID": itemDataTypeID,
			"Sum":        100,
			"Weight":     75,
			"Name":       "CustomItem",
		})

		created := data.CreatePickingOrderItem.PickingOrderItem
		assert.InDelta(t, float64(100), created.Data["sum"], 0.001)
		meta := created.Data["meta"].(map[string]any)
		assert.InDelta(t, float64(75), meta["weight"], 0.001)
		assert.Equal(t, "CustomItem", meta["name"])

		te.assertEvents(ctx, Create("orderitems", created.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createOrderItem, map[string]any{
			"Sku":      "AB-CD-12",
			"Quantity": 10,
			"OrderID":  order.ID,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative weight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createOrderItem, map[string]any{
			"Sku":        "MK-ENT-X4",
			"Quantity":   10,
			"OrderID":    order.ID,
			"DataTypeID": itemDataTypeID,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestOrderItem_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates sku and quantity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		item := te.newOrderItem(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		data := execOK[updateOrderItemData](te, ctx, updateOrderItem, map[string]any{
			"ID":         item.ID,
			"Sku":        "MX-2T-RX",
			"Quantity":   20,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		})

		updated := data.UpdatePickingOrderItem.PickingOrderItem
		assert.Equal(t, "MX-2T-RX", updated.Sku)
		assert.Equal(t, int64(20), updated.Quantity)
		te.assertEvents(ctx, Update("orderitems", item.ID))
	})

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		item := te.newOrderItem(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		data := execOK[updateOrderItemData](te, ctx, updateOrderItem, map[string]any{
			"ID":         item.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Sum":        999,
		})

		assert.InDelta(t, float64(999), data.UpdatePickingOrderItem.PickingOrderItem.Data["sum"], 0.001)
		te.assertEvents(ctx, Update("orderitems", item.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fakeID := uuid.New()
		execErr(te, ctx, updateOrderItem, map[string]any{
			"ID":         fakeID,
			"Sku":        "X",
			"Quantity":   1,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "order_items not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		item := te.newOrderItem(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateOrderItem, map[string]any{
			"ID":   item.ID,
			"Data": true, // Data included but no DataTypeID
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's order item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		order := te.newOrder(ctxB, userB).Create()
		item := te.newOrderItem(ctxB, userB, order.ID).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateOrderItem, map[string]any{
			"ID":         item.ID,
			"Sku":        "HACKED",
			"Quantity":   1,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "order_items not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		item := te.newOrderItem(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateOrderItem, map[string]any{
			"ID":         item.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Sum":        -100,
		}, "'/sum' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestOrderItem_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes order item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		item := te.newOrderItem(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		data := execOK[deleteOrderItemData](te, ctx, deleteOrderItem, map[string]any{
			"ID": item.ID,
		})

		assert.Equal(t, item.ID, data.DeletePickingOrderItem.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.OrderItems.Get(te.ctxWithDeleted(userA), item.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)

		te.assertEvents(ctx, Delete("orderitems", item.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteOrderItem, map[string]any{
			"ID": uuid.New(),
		}, "order_items not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's order item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		order := te.newOrder(ctxB, userB).Create()
		item := te.newOrderItem(ctxB, userB, order.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteOrderItem, map[string]any{
			"ID": item.ID,
		}, "order_items not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted order item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		item := te.newOrderItem(ctx, userA, order.ID).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteOrderItem, map[string]any{
			"ID": item.ID,
		}, "order_items not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestOrderItem_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItems, nil)

		assert.Equal(t, 0, data.PickingOrderItems.TotalCount)
		assert.Empty(t, data.PickingOrderItems.Edges)
	})

	t.Run("returns only own tenant's order items", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		orderA := te.newOrder(ctxA, userA).Create()
		itemA := te.newOrderItem(ctxA, userA, orderA.ID).Create()

		orderB := te.newOrder(ctxB, userB).Create()
		te.newOrderItem(ctxB, userB, orderB.ID).Create()

		data := execOK[queryOrderItemsData](te, ctxA, queryOrderItems, nil)

		require.Equal(t, 1, data.PickingOrderItems.TotalCount)
		assert.Equal(t, itemA.ID, data.PickingOrderItems.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		active := te.newOrderItem(ctx, userA, order.ID).Create()
		te.newOrderItem(ctx, userA, order.ID).Deleted().Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItems, nil)

		require.Equal(t, 1, data.PickingOrderItems.TotalCount)
		assert.Equal(t, active.ID, data.PickingOrderItems.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.newOrderItem(ctx, userA, order.ID).Create()
		te.newOrderItem(ctx, userA, order.ID).Deleted().Create()

		data := execOK[queryOrderItemsData](te, te.ctxWithDeleted(userA), queryOrderItems, nil)

		assert.Equal(t, 2, data.PickingOrderItems.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		for range 5 {
			te.newOrderItem(ctx, userA, order.ID).Create()
		}

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItems, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.PickingOrderItems.TotalCount)
		assert.Len(t, data.PickingOrderItems.Edges, 2)
		assert.True(t, data.PickingOrderItems.PageInfo.HasNextPage)
		assert.NotNil(t, data.PickingOrderItems.PageInfo.EndCursor)
	})

	t.Run("filters by sku", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		target := te.newOrderItem(ctx, userA, order.ID).Sku("FIND-ME").Create()
		te.newOrderItem(ctx, userA, order.ID).Sku("OTHER").Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItems, map[string]any{
			"Where": `{ skuIn: ["FIND-ME"] }`,
		})

		require.Equal(t, 1, data.PickingOrderItems.TotalCount)
		assert.Equal(t, target.ID, data.PickingOrderItems.Edges[0].Node.ID)
	})

	t.Run("filters by data field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		target := te.newOrderItem(ctx, userA, order.ID).Create()

		cases := []struct {
			desc   string
			filter string
			count  int
		}{
			{
				desc:   "Data filter",
				filter: `{ Data: ["type", "custom"] }`,
				count:  1,
			},
			{
				desc:   "DataHasKey filter",
				filter: `{ DataHasKey: "meta.name" }`,
				count:  1,
			},
		}

		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) { //nolint:paralleltest // Subtests share test environment
				data := execOK[queryOrderItemsData](te, ctx, queryOrderItems, map[string]any{
					"Where": tc.filter,
				})

				require.Equal(t, tc.count, data.PickingOrderItems.TotalCount)
				if tc.count > 0 {
					assert.Equal(t, target.ID, data.PickingOrderItems.Edges[0].Node.ID)
				}
			})
		}
	})

	t.Run("filters by multiple skus", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.newOrderItem(ctx, userA, order.ID).Sku("SKU-A").Create()
		te.newOrderItem(ctx, userA, order.ID).Sku("SKU-B").Create()
		te.newOrderItem(ctx, userA, order.ID).Sku("SKU-C").Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItems, map[string]any{
			"Where": fmt.Sprintf(`{ skuIn: [%q, %q] }`, "SKU-A", "SKU-B"),
		})

		assert.Equal(t, 2, data.PickingOrderItems.TotalCount)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestOrderItem_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		i1 := te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{"sum": float64(30)}).Create()
		i2 := te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{"sum": float64(10)}).Create()
		i3 := te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItemsJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.PickingOrderItems.TotalCount)
		assert.Equal(t, i2.ID, data.PickingOrderItems.Edges[0].Node.ID)
		assert.Equal(t, i3.ID, data.PickingOrderItems.Edges[1].Node.ID)
		assert.Equal(t, i1.ID, data.PickingOrderItems.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		i1 := te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		i2 := te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItemsJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.PickingOrderItems.TotalCount)
		assert.Equal(t, i2.ID, data.PickingOrderItems.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.PickingOrderItems.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		i1 := te.newOrderItem(ctx, userA, order.ID).Create()
		i2 := te.newOrderItem(ctx, userA, order.ID).Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItemsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.PickingOrderItems.TotalCount)
		assert.Equal(t, i2.ID, data.PickingOrderItems.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.PickingOrderItems.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestOrderItem_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{"type": "fragile"}).Create()
		i2 := te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{"type": "standard"}).Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItemsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "standard"] }`,
		})

		require.Equal(t, 1, data.PickingOrderItems.TotalCount)
		assert.Equal(t, i2.ID, data.PickingOrderItems.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{"type": "fragile"}).Create()
		i2 := te.newOrderItem(ctx, userA, order.ID).Data(map[string]any{"type": "standard", "priority": float64(1)}).Create()

		data := execOK[queryOrderItemsData](te, ctx, queryOrderItemsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.PickingOrderItems.TotalCount)
		assert.Equal(t, i2.ID, data.PickingOrderItems.Edges[0].Node.ID)
	})
}
