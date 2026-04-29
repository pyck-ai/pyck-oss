package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createOrder = resolver.ParseTemplate(`mutation {
		createPickingOrder(input: {
			{{if .CustomerID}}customerID: "{{.CustomerID}}",{{end}}
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}{{if .IncludeItems}},
			orderItems: [{
				sku: "MK-ENT-X2",
				quantity: 20,
				dataTypeID: "{{.DataTypeID}}",
				data: {
					type: "custom",
					sum: 15,
					meta: { name: "TestItem", weight: 50, tags: ["a", "b"] }
				}
			}]{{end}}
		}) {
			pickingOrder { id tenantID customerID dataTypeID data }
		}
	}`)

	updateOrder = resolver.ParseTemplate(`mutation {
		updatePickingOrder(id: "{{.ID}}", input: {
			{{if .CustomerID}}customerID: "{{.CustomerID}}",{{end}}
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}{{end}}
		}) {
			pickingOrder { id tenantID customerID dataTypeID data }
		}
	}`)

	deleteOrder = resolver.ParseTemplate(`mutation {
		deletePickingOrder(id: "{{.ID}}") { deletedID }
	}`)

	queryOrders = resolver.ParseTemplate(`query {
		pickingOrders(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID customerID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)

	queryOrdersJSONOrder = resolver.ParseTemplate(`query {
		pickingOrders(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID customerID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type orderNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	CustomerID uuid.UUID
	DataTypeID uuid.UUID
	Data       map[string]any
}

type createOrderData struct {
	CreatePickingOrder struct{ PickingOrder orderNode }
}

type updateOrderData struct {
	UpdatePickingOrder struct{ PickingOrder orderNode }
}

type deleteOrderData struct {
	DeletePickingOrder struct{ DeletedID uuid.UUID }
}

type queryOrdersData struct {
	PickingOrders struct {
		TotalCount int
		Edges      []struct{ Node orderNode }
		PageInfo   struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestOrder_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates order with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customerID := uuidgql.GenerateV7UUID()
		data := execOK[createOrderData](te, ctx, createOrder, map[string]any{
			"CustomerID": customerID,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreatePickingOrder.PickingOrder
		assert.Equal(t, customerID, created.CustomerID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.Order.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, customerID, stored.CustomerID)

		// Verify event
		te.assertEvents(ctx, Create("order", created.ID))
	})

	t.Run("creates order with items", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customerID := uuidgql.GenerateV7UUID()
		data := execOK[createOrderData](te, ctx, createOrder, map[string]any{
			"CustomerID":   customerID,
			"DataTypeID":   itemDataTypeID,
			"IncludeItems": true,
		})

		created := data.CreatePickingOrder.PickingOrder
		assert.Equal(t, customerID, created.CustomerID)

		// Verify order items
		items, err := te.Ent.OrderItems.Query().All(ctx)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "MK-ENT-X2", items[0].Sku)

		// Verify events (order + orderitems)
		te.assertEvents(ctx, Create("order", created.ID), Create("orderitems", items[0].ID))
	})

	t.Run("creates order with custom data values", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createOrderData](te, ctx, createOrder, map[string]any{
			"CustomerID": uuidgql.GenerateV7UUID(),
			"DataTypeID": itemDataTypeID,
			"Sum":        100,
			"Weight":     75,
			"Name":       "CustomItem",
		})

		created := data.CreatePickingOrder.PickingOrder
		assert.InDelta(t, float64(100), created.Data["sum"], 0.001)
		meta := created.Data["meta"].(map[string]any)
		assert.InDelta(t, float64(75), meta["weight"], 0.001)
		assert.Equal(t, "CustomItem", meta["name"])

		te.assertEvents(ctx, Create("order", created.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createOrder, map[string]any{
			"CustomerID": uuidgql.GenerateV7UUID(),
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative weight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createOrder, map[string]any{
			"CustomerID": uuidgql.GenerateV7UUID(),
			"DataTypeID": itemDataTypeID,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestOrder_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates customerID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		newCustomerID := uuidgql.GenerateV7UUID()
		data := execOK[updateOrderData](te, ctx, updateOrder, map[string]any{
			"ID":         order.ID,
			"CustomerID": newCustomerID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		})

		assert.Equal(t, newCustomerID, data.UpdatePickingOrder.PickingOrder.CustomerID)
		te.assertEvents(ctx, Update("order", order.ID))
	})

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateOrderData](te, ctx, updateOrder, map[string]any{
			"ID":         order.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Sum":        999,
		})

		assert.InDelta(t, float64(999), data.UpdatePickingOrder.PickingOrder.Data["sum"], 0.001)
		te.assertEvents(ctx, Update("order", order.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fakeID := uuid.New()
		execErr(te, ctx, updateOrder, map[string]any{
			"ID":         fakeID,
			"CustomerID": uuidgql.GenerateV7UUID(),
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "order not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateOrder, map[string]any{
			"ID":   order.ID,
			"Data": true, // Data included but no DataTypeID
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		order := te.newOrder(ctxB, userB).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateOrder, map[string]any{
			"ID":         order.ID,
			"CustomerID": uuidgql.GenerateV7UUID(),
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "order not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateOrder, map[string]any{
			"ID":         order.ID,
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

func TestOrder_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteOrderData](te, ctx, deleteOrder, map[string]any{
			"ID": order.ID,
		})

		assert.Equal(t, order.ID, data.DeletePickingOrder.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.Order.Get(te.ctxWithDeleted(userA), order.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("order", order.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteOrder, map[string]any{
			"ID": uuid.New(),
		}, "order not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		order := te.newOrder(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteOrder, map[string]any{
			"ID": order.ID,
		}, "order not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteOrder, map[string]any{
			"ID": order.ID,
		}, "order not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestOrder_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryOrdersData](te, ctx, queryOrders, nil)

		assert.Equal(t, 0, data.PickingOrders.TotalCount)
		assert.Empty(t, data.PickingOrders.Edges)
	})

	t.Run("returns only own tenant's orders", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		orderA := te.newOrder(ctxA, userA).Create()
		te.newOrder(ctxB, userB).Create()

		data := execOK[queryOrdersData](te, ctxA, queryOrders, nil)

		require.Equal(t, 1, data.PickingOrders.TotalCount)
		assert.Equal(t, orderA.ID, data.PickingOrders.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		active := te.newOrder(ctx, userA).Create()
		te.newOrder(ctx, userA).Deleted().Create()

		data := execOK[queryOrdersData](te, ctx, queryOrders, nil)

		require.Equal(t, 1, data.PickingOrders.TotalCount)
		assert.Equal(t, active.ID, data.PickingOrders.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newOrder(ctx, userA).Create()
		te.newOrder(ctx, userA).Deleted().Create()

		data := execOK[queryOrdersData](te, te.ctxWithDeleted(userA), queryOrders, nil)

		assert.Equal(t, 2, data.PickingOrders.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		for range 5 {
			te.newOrder(ctx, userA).Create()
		}

		data := execOK[queryOrdersData](te, ctx, queryOrders, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.PickingOrders.TotalCount)
		assert.Len(t, data.PickingOrders.Edges, 2)
		assert.True(t, data.PickingOrders.PageInfo.HasNextPage)
		assert.NotNil(t, data.PickingOrders.PageInfo.EndCursor)
	})

	t.Run("filters by customerID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		targetCustomerID := uuidgql.GenerateV7UUID()
		target := te.newOrder(ctx, userA).CustomerID(targetCustomerID).Create()
		te.newOrder(ctx, userA).Create()

		data := execOK[queryOrdersData](te, ctx, queryOrders, map[string]any{
			"Where": `{ customerIDIn: ["` + targetCustomerID.String() + `"] }`,
		})

		require.Equal(t, 1, data.PickingOrders.TotalCount)
		assert.Equal(t, target.ID, data.PickingOrders.Edges[0].Node.ID)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestOrder_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		o1 := te.newOrder(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		o2 := te.newOrder(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		o3 := te.newOrder(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryOrdersData](te, ctx, queryOrdersJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.PickingOrders.TotalCount)
		assert.Equal(t, o2.ID, data.PickingOrders.Edges[0].Node.ID)
		assert.Equal(t, o3.ID, data.PickingOrders.Edges[1].Node.ID)
		assert.Equal(t, o1.ID, data.PickingOrders.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		o1 := te.newOrder(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		o2 := te.newOrder(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryOrdersData](te, ctx, queryOrdersJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.PickingOrders.TotalCount)
		assert.Equal(t, o2.ID, data.PickingOrders.Edges[0].Node.ID)
		assert.Equal(t, o1.ID, data.PickingOrders.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		o1 := te.newOrder(ctx, userA).Create()
		o2 := te.newOrder(ctx, userA).Create()

		data := execOK[queryOrdersData](te, ctx, queryOrdersJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.PickingOrders.TotalCount)
		assert.Equal(t, o2.ID, data.PickingOrders.Edges[0].Node.ID)
		assert.Equal(t, o1.ID, data.PickingOrders.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestOrder_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newOrder(ctx, userA).Data(map[string]any{"type": "express"}).Create()
		o2 := te.newOrder(ctx, userA).Data(map[string]any{"type": "standard"}).Create()

		data := execOK[queryOrdersData](te, ctx, queryOrdersJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "standard"] }`,
		})

		require.Equal(t, 1, data.PickingOrders.TotalCount)
		assert.Equal(t, o2.ID, data.PickingOrders.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newOrder(ctx, userA).Data(map[string]any{"type": "express"}).Create()
		o2 := te.newOrder(ctx, userA).Data(map[string]any{"type": "standard", "priority": float64(1)}).Create()

		data := execOK[queryOrdersData](te, ctx, queryOrdersJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.PickingOrders.TotalCount)
		assert.Equal(t, o2.ID, data.PickingOrders.Edges[0].Node.ID)
	})
}
