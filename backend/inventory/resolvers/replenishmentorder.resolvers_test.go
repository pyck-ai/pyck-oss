package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createReplenishmentOrderTpl = resolver.ParseTemplate(`mutation {
		createReplenishmentOrder(input: {
			{{- if .DataTypeID }}
			dataTypeID: "{{ .DataTypeID }}",
			{{- end }}
			{{- if .Data }}
			data: {{ .Data }},
			{{- end }}
			{{- if .SupplierID }}
			supplierID: "{{ .SupplierID }}"
			{{- end }}
		}) {
			replenishmentOrder {
				id tenantID supplierID dataTypeID dataTypeSlug data
				createdAt createdBy updatedAt updatedBy deletedAt deletedBy
			}
			workflows { type id runID }
		}
	}`)

	updateReplenishmentOrderTpl = resolver.ParseTemplate(`mutation {
		updateReplenishmentOrder(
			id: "{{ .ID }}",
			input: {
				{{- if .DataTypeID }}
				dataTypeID: "{{ .DataTypeID }}",
				{{- end }}
				{{- if .Data }}
				data: {{ .Data }},
				{{- end }}
				{{- if .ClearSupplierID }}
				clearSupplierID: true
				{{- else if .SupplierID }}
				supplierID: "{{ .SupplierID }}"
				{{- end }}
			}) {
			replenishmentOrder {
				id tenantID supplierID dataTypeID dataTypeSlug data
				createdAt createdBy updatedAt updatedBy deletedAt deletedBy
			}
			workflows { type id runID }
		}
	}`)

	deleteReplenishmentOrderTpl = resolver.ParseTemplate(`mutation {
		deleteReplenishmentOrder(id: "{{ .ID }}") {
			deletedID
			workflows { type id runID }
		}
	}`)

	queryReplenishmentOrdersTpl = resolver.ParseTemplate(`query {
		replenishmentOrders(
			first: {{or .First 100}},
			{{- if .After }}
			after: "{{ .After }}",
			{{- end }}
			orderBy: {{or .OrderBy "{ direction: ASC, field: CREATED_AT }"}},
			where: {{or .Where "null"}}
		) {
			totalCount
			edges {
				node {
					id tenantID supplierID dataTypeID dataTypeSlug data
					createdAt createdBy updatedAt updatedBy deletedAt deletedBy
				}
				cursor
			}
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)

	queryReplenishmentOrdersJSONOrder = resolver.ParseTemplate(`query {
		replenishmentOrders(
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
			edges { node { id tenantID supplierID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)

	queryReplenishmentOrderItemsTpl = resolver.ParseTemplate(`query {
		replenishmentOrderItems(
			first: {{or .First 100}},
			{{- if .After }}
			after: "{{ .After }}",
			{{- end }}
			{{- if .OrderBy }}
			orderBy: {{ .OrderBy }},
			{{- end }}
			where: {{or .Where "null"}}
		) {
			totalCount
			edges {
				node {
					id tenantID replenishmentOrderID sku quantity data dataTypeID dataTypeSlug
					createdAt createdBy updatedAt updatedBy deletedAt deletedBy
				}
				cursor
			}
		}
	}`)

	createReplenishmentOrderWithItemsTpl = resolver.ParseTemplate(`mutation {
		createReplenishmentOrder(input: {
			{{- if .DataTypeID }}
			dataTypeID: "{{ .DataTypeID }}",
			{{- end }}
			{{- if .Data }}
			data: {{ .Data }},
			{{- end }}
			supplierID: "{{ .SupplierID }}"
			{{- if .Items }}
			items: [
				{{- range .Items }}
				{
					{{- if .DataTypeID }}
					dataTypeID: "{{ .DataTypeID }}",
					{{- end }}
					{{- if .Data }}
					data: {{ .Data }},
					{{- end }}
					sku: "{{ .Sku }}",
					quantity: {{ .Quantity }}
				},
				{{- end }}
			]
			{{- end }}
		}) {
			replenishmentOrder {
				id tenantID supplierID dataTypeID dataTypeSlug data
				createdAt createdBy updatedAt updatedBy deletedAt deletedBy
			}
			workflows { type id runID }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES (data-only, no Errors field)
// =============================================================================

type replenishmentOrderNode struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	SupplierID   uuid.UUID
	DataTypeID   uuid.UUID
	DataTypeSlug *string
	Data         map[string]any
	CreatedAt    time.Time
	CreatedBy    uuid.UUID
	UpdatedAt    *time.Time
	UpdatedBy    *uuid.UUID
	DeletedAt    *time.Time
	DeletedBy    *uuid.UUID
}

type createReplenishmentOrderData struct {
	CreateReplenishmentOrder struct {
		ReplenishmentOrder replenishmentOrderNode
		Workflows          []*struct{ Name string }
	}
}

type updateReplenishmentOrderData struct {
	UpdateReplenishmentOrder struct {
		ReplenishmentOrder replenishmentOrderNode
		Workflows          []*struct{ Name string }
	}
}

type deleteReplenishmentOrderData struct {
	DeleteReplenishmentOrder struct {
		DeletedID uuid.UUID
		Workflows []*struct{ Name string }
	}
}

type queryReplenishmentOrdersData struct {
	ReplenishmentOrders struct {
		TotalCount int
		Edges      []struct {
			Node   replenishmentOrderNode
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

type replenishmentOrderItemNode struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	ReplenishmentOrderID uuid.UUID
	Sku                  string
	Quantity             int
	Data                 map[string]any
	DataTypeID           uuid.UUID
	DataTypeSlug         *string
	CreatedAt            time.Time
	CreatedBy            uuid.UUID
	UpdatedAt            *time.Time
	UpdatedBy            *uuid.UUID
	DeletedAt            *time.Time
	DeletedBy            *uuid.UUID
}

type queryReplenishmentOrderItemsData struct {
	ReplenishmentOrderItems struct {
		TotalCount int
		Edges      []struct {
			Node   replenishmentOrderItemNode
			Cursor string
		}
	}
}

// =============================================================================
// TEST DATA
// =============================================================================

var (
	testReplenishmentOrderData = `{
		type: "custom",
		sum: 15,
		meta: {
			name: "TestOrder",
			weight: 50,
			tags: ["urgent", "supplier"]
		}
	}`

	testReplenishmentOrderDataUpdated = `{
		type: "custom",
		sum: 25,
		meta: {
			name: "UpdatedOrder",
			weight: 75,
			tags: ["updated", "test"]
		}
	}`

	testReplenishmentOrderDataInvalid = `{
		wrongField: "invalid"
	}`

	testReplenishmentOrderDataBadWeight = `{
		type: "custom",
		sum: 15,
		meta: {
			name: "TestOrder",
			weight: -50,
			tags: ["urgent"]
		}
	}`

	testReplenishmentOrderDataBadSum = `{
		type: "custom",
		sum: -15,
		meta: {
			name: "UpdatedOrder",
			weight: 50,
			tags: ["test"]
		}
	}`

	testReplenishmentItemData = `{
		type: "custom",
		sum: 10,
		meta: {
			name: "TestItem",
			weight: 25,
			tags: ["item", "test"]
		}
	}`
)

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestReplenishmentOrder_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates order with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
		})

		created := data.CreateReplenishmentOrder.ReplenishmentOrder
		assert.NotEqual(t, uuid.Nil, created.ID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.Equal(t, supplierID, created.SupplierID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)
		assert.Equal(t, "custom", created.Data["type"])
		assert.InDelta(t, float64(15), created.Data["sum"], 0.001)

		// Verify persisted
		orders, err := te.Ent.ReplenishmentOrder.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		require.Len(t, orders, 1)
		assert.Equal(t, created.ID, orders[0].ID)

		te.assertEvents(ctx, Create("replenishmentorder", created.ID))
	})

	t.Run("creates order without supplierID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			// SupplierID omitted
		})

		created := data.CreateReplenishmentOrder.ReplenishmentOrder
		assert.NotEqual(t, uuid.Nil, created.ID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.Equal(t, uuid.Nil, created.SupplierID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)

		// Verify persisted
		orders, err := te.Ent.ReplenishmentOrder.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		require.Len(t, orders, 1)
		assert.Equal(t, uuid.Nil, orders[0].SupplierID)

		te.assertEvents(ctx, Create("replenishmentorder", created.ID))
	})

	t.Run("rejects invalid data schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderDataInvalid,
			"SupplierID": supplierID,
		}, "missing properties")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects validation error (bad weight)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderDataBadWeight,
			"SupplierID": supplierID,
		}, "/meta/weight")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid UUID format", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": "invalid-uuid-format",
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
		}, "invalid UUID")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects database failure", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Close DB to simulate failure
		te.Ent.Close()

		ctx := te.ctx(userA)
		execErr(te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
		}, "database")
	})

	t.Run("rejects empty dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": "",
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
		}, "")

		te.assertNoEvents(ctx)
	})

	t.Run("allows concurrent creates", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// First create
		data1 := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
		})
		assert.NotEqual(t, uuid.Nil, data1.CreateReplenishmentOrder.ReplenishmentOrder.ID)

		// Second create with same data should succeed (no uniqueness constraint)
		data2 := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
		})
		assert.NotEqual(t, uuid.Nil, data2.CreateReplenishmentOrder.ReplenishmentOrder.ID)
		assert.NotEqual(t, data1.CreateReplenishmentOrder.ReplenishmentOrder.ID, data2.CreateReplenishmentOrder.ReplenishmentOrder.ID)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestReplenishmentOrder_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates order with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateReplenishmentOrderData](te, ctx, updateReplenishmentOrderTpl, map[string]any{
			"ID":         order.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderDataUpdated,
			"SupplierID": supplierID,
		})

		updated := data.UpdateReplenishmentOrder.ReplenishmentOrder
		assert.Equal(t, order.ID, updated.ID)
		assert.Equal(t, tenantA, updated.TenantID)
		assert.Equal(t, "custom", updated.Data["type"])
		assert.InDelta(t, float64(25), updated.Data["sum"], 0.001)

		te.assertEvents(ctx, Update("replenishmentorder", order.ID))
	})

	t.Run("sets supplierID on order without one", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).NoSupplier().Create()
		assert.Equal(t, uuid.Nil, order.SupplierID)
		te.clearEvents(ctx)

		data := execOK[updateReplenishmentOrderData](te, ctx, updateReplenishmentOrderTpl, map[string]any{
			"ID":         order.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderDataUpdated,
			"SupplierID": supplierID,
		})

		updated := data.UpdateReplenishmentOrder.ReplenishmentOrder
		assert.Equal(t, supplierID, updated.SupplierID)

		// Verify in database
		orderDB, err := te.Ent.ReplenishmentOrder.Get(ctx, order.ID)
		require.NoError(t, err)
		assert.Equal(t, supplierID, orderDB.SupplierID)

		te.assertEvents(ctx, Update("replenishmentorder", order.ID))
	})

	t.Run("clears supplierID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).SupplierID(supplierID).Create()
		assert.Equal(t, supplierID, order.SupplierID)
		te.clearEvents(ctx)

		data := execOK[updateReplenishmentOrderData](te, ctx, updateReplenishmentOrderTpl, map[string]any{
			"ID":              order.ID,
			"DataTypeID":      itemDataTypeID,
			"Data":            testReplenishmentOrderDataUpdated,
			"ClearSupplierID": true,
		})

		updated := data.UpdateReplenishmentOrder.ReplenishmentOrder
		assert.Equal(t, uuid.Nil, updated.SupplierID)

		// Verify in database
		orderDB, err := te.Ent.ReplenishmentOrder.Get(ctx, order.ID)
		require.NoError(t, err)
		assert.Equal(t, uuid.Nil, orderDB.SupplierID)

		te.assertEvents(ctx, Update("replenishmentorder", order.ID))
	})

	t.Run("rejects update of other tenant's order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		order := te.newReplenishmentOrder(ctxB, userB).DataTypeID(itemDataTypeIDTenantB).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateReplenishmentOrderTpl, map[string]any{
			"ID":         order.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderDataUpdated,
			"SupplierID": supplierID,
		}, "not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid data schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateReplenishmentOrderTpl, map[string]any{
			"ID":         order.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderDataInvalid,
			"SupplierID": supplierID,
		}, "missing properties")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects validation error (bad sum)", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateReplenishmentOrderTpl, map[string]any{
			"ID":         order.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderDataBadSum,
			"SupplierID": supplierID,
		}, "/sum")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestReplenishmentOrder_Delete(t *testing.T) {
	t.Parallel()

	t.Run("deletes order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteReplenishmentOrderData](te, ctx, deleteReplenishmentOrderTpl, map[string]any{
			"ID": order.ID,
		})

		assert.Equal(t, order.ID, data.DeleteReplenishmentOrder.DeletedID)

		// Verify deleted
		_, err := te.Ent.ReplenishmentOrder.Get(ctx, order.ID)
		require.Error(t, err)

		te.assertEvents(ctx, Delete("replenishmentorder", order.ID))
	})

	t.Run("rejects delete of other tenant's order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		order := te.newReplenishmentOrder(ctxB, userB).DataTypeID(itemDataTypeIDTenantB).Create()
		te.clearEvents(ctxB)

		// Try to delete as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteReplenishmentOrderTpl, map[string]any{
			"ID": order.ID,
		}, "not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("deletes order with existing items", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()

		// Create order item
		itemData := map[string]any{
			"sku":   "TEST-SKU-001",
			"size":  "M",
			"color": "blue",
		}
		var orderItem *ent.ReplenishmentOrderItem
		err := te.withTx(ctx, func(tx *ent.Tx) error {
			var err error
			orderItem, err = tx.ReplenishmentOrderItem.Create().
				SetTenantID(tenantA).
				SetReplenishmentOrderID(order.ID).
				SetSku("TEST-SKU-001").
				SetQuantity(50).
				SetData(itemData).
				SetDataTypeID(itemDataTypeID).
				SetDataTypeSlug("item").
				Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
			return err
		})
		require.NoError(t, err)
		te.clearEvents(ctx)

		data := execOK[deleteReplenishmentOrderData](te, ctx, deleteReplenishmentOrderTpl, map[string]any{
			"ID": order.ID,
		})

		assert.Equal(t, order.ID, data.DeleteReplenishmentOrder.DeletedID)

		// Verify order was deleted
		_, err = te.Ent.ReplenishmentOrder.Get(ctx, order.ID)
		require.Error(t, err)

		te.assertEvents(ctx,
			Delete("replenishmentorder", order.ID),
			Delete("replenishmentorderitem", orderItem.ID),
		)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestReplenishmentOrder_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersTpl, nil)

		assert.Equal(t, 0, data.ReplenishmentOrders.TotalCount)
		assert.Empty(t, data.ReplenishmentOrders.Edges)
		assert.False(t, data.ReplenishmentOrders.PageInfo.HasNextPage)
		assert.False(t, data.ReplenishmentOrders.PageInfo.HasPreviousPage)
		assert.Nil(t, data.ReplenishmentOrders.PageInfo.StartCursor)
		assert.Nil(t, data.ReplenishmentOrders.PageInfo.EndCursor)
	})

	t.Run("returns single order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersTpl, nil)

		assert.Equal(t, 1, data.ReplenishmentOrders.TotalCount)
		require.Len(t, data.ReplenishmentOrders.Edges, 1)
		assert.Equal(t, order.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
		assert.Equal(t, order.TenantID, data.ReplenishmentOrders.Edges[0].Node.TenantID)
		assert.NotNil(t, data.ReplenishmentOrders.Edges[0].Node.Data)
		assert.False(t, data.ReplenishmentOrders.PageInfo.HasNextPage)
		assert.False(t, data.ReplenishmentOrders.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.ReplenishmentOrders.PageInfo.StartCursor)
		assert.NotNil(t, data.ReplenishmentOrders.PageInfo.EndCursor)
	})

	t.Run("returns only own tenant's orders", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		orderA := te.newReplenishmentOrder(ctxA, userA).Create()
		te.newReplenishmentOrder(ctxB, userB).DataTypeID(itemDataTypeIDTenantB).Create()

		data := execOK[queryReplenishmentOrdersData](te, ctxA, queryReplenishmentOrdersTpl, nil)

		require.Equal(t, 1, data.ReplenishmentOrders.TotalCount)
		assert.Equal(t, orderA.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
		assert.Equal(t, tenantA, data.ReplenishmentOrders.Edges[0].Node.TenantID)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestReplenishmentOrder_QueryWithFilters(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	ctx := te.ctx(userA)

	// Create test orders
	orderWithData := te.newReplenishmentOrder(ctx, userA).Data(validData).Create()
	orderNoData := te.newReplenishmentOrder(ctx, userA).Data(nil).DataTypeID(uuid.Nil).Create()
	deletedOrder := te.newReplenishmentOrder(ctx, userA).Deleted().Create()

	// Create order for other tenant (should not appear in results)
	ctxB := te.ctx(userB)
	te.newReplenishmentOrder(ctxB, userB).DataTypeID(itemDataTypeIDTenantB).Create()

	t.Run("returns all non-deleted without filters", func(t *testing.T) {
		t.Parallel()

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersTpl, map[string]any{
			"First": 20,
		})

		assert.Equal(t, 2, data.ReplenishmentOrders.TotalCount)
		require.Len(t, data.ReplenishmentOrders.Edges, 2)
		assert.Equal(t, orderWithData.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
		assert.Equal(t, orderNoData.ID, data.ReplenishmentOrders.Edges[1].Node.ID)
	})

	t.Run("returns deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()

		ctxWithDeleted := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)

		data := execOK[queryReplenishmentOrdersData](te, ctxWithDeleted, queryReplenishmentOrdersTpl, map[string]any{
			"First": 20,
		})

		assert.Equal(t, 3, data.ReplenishmentOrders.TotalCount)
		require.Len(t, data.ReplenishmentOrders.Edges, 3)
		assert.Equal(t, orderWithData.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
		assert.Equal(t, orderNoData.ID, data.ReplenishmentOrders.Edges[1].Node.ID)
		assert.Equal(t, deletedOrder.ID, data.ReplenishmentOrders.Edges[2].Node.ID)
	})

	t.Run("filters table tests", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			desc   string
			filter string
			count  int
			nodeID uuid.UUID
		}{
			{
				desc:   "TestData",
				filter: `{ Data: ["type", "custom"] }`,
				count:  1,
				nodeID: orderWithData.ID,
			},
			{
				desc:   "TestDataHasKey",
				filter: `{ DataHasKey: "meta.name" }`,
				count:  1,
				nodeID: orderWithData.ID,
			},
			{
				desc:   "TestDataIn",
				filter: `{ DataIn: ["meta.name", "TestItem", "foo"] }`,
				count:  1,
				nodeID: orderWithData.ID,
			},
			{
				desc:   "TestDataContains",
				filter: `{ DataContains: ["meta.tags", "foo"] }`,
				count:  1,
				nodeID: orderWithData.ID,
			},
			{
				desc:   "TestDataNull",
				filter: `{ Data: null }`,
				count:  2,
				nodeID: orderWithData.ID,
			},
			{
				desc:   "TestDataHasKeyNull",
				filter: `{ DataHasKey: null }`,
				count:  2,
				nodeID: orderWithData.ID,
			},
			{
				desc:   "TestDataInNull",
				filter: `{ DataIn: null }`,
				count:  2,
				nodeID: orderWithData.ID,
			},
			{
				desc:   "TestDataContainsNull",
				filter: `{ DataContains: null }`,
				count:  2,
				nodeID: orderWithData.ID,
			},
		}

		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) {
				t.Parallel()

				data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersTpl, map[string]any{
					"First": 20,
					"Where": tc.filter,
				})

				assert.Equal(t, tc.count, data.ReplenishmentOrders.TotalCount, "Test case: %s", tc.desc)
				require.Len(t, data.ReplenishmentOrders.Edges, tc.count)
				if tc.count > 0 {
					assert.Equal(t, tc.nodeID, data.ReplenishmentOrders.Edges[0].Node.ID)
					assert.Equal(t, tenantA, data.ReplenishmentOrders.Edges[0].Node.TenantID)
				}
			})
		}
	})
}

// =============================================================================
// REPLENISHMENT ORDER ITEMS QUERY TESTS
// =============================================================================

func TestReplenishmentOrderItems_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns items for order", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()

		// Create order item
		testItemData := map[string]any{
			"sku":   "TEST-SKU-001",
			"size":  "M",
			"color": "blue",
			"warehouse": map[string]any{
				"location": "A1-B2",
				"zone":     "primary",
			},
		}

		var orderItem *ent.ReplenishmentOrderItem
		err := te.withTx(ctx, func(tx *ent.Tx) error {
			var err error
			orderItem, err = tx.ReplenishmentOrderItem.Create().
				SetTenantID(tenantA).
				SetReplenishmentOrderID(order.ID).
				SetSku("TEST-SKU-001").
				SetQuantity(50).
				SetData(testItemData).
				SetDataTypeID(itemDataTypeID).
				SetDataTypeSlug("item").
				Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
			return err
		})
		require.NoError(t, err)

		data := execOK[queryReplenishmentOrderItemsData](te, ctx, queryReplenishmentOrderItemsTpl, nil)

		assert.Equal(t, 1, data.ReplenishmentOrderItems.TotalCount)
		require.Len(t, data.ReplenishmentOrderItems.Edges, 1)

		returnedItem := data.ReplenishmentOrderItems.Edges[0].Node
		assert.Equal(t, orderItem.ID, returnedItem.ID)
		assert.Equal(t, orderItem.TenantID, returnedItem.TenantID)
		assert.Equal(t, orderItem.ReplenishmentOrderID, returnedItem.ReplenishmentOrderID)
		assert.Equal(t, orderItem.Sku, returnedItem.Sku)
		assert.Equal(t, int(orderItem.Quantity), returnedItem.Quantity)
		assert.Equal(t, "TEST-SKU-001", returnedItem.Data["sku"].(string))
	})

	t.Run("filters by data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newReplenishmentOrder(ctx, userA).Create()

		testItemData := map[string]any{
			"sku": "TEST-SKU-001",
			"warehouse": map[string]any{
				"location": "A1-B2",
			},
		}

		err := te.withTx(ctx, func(tx *ent.Tx) error {
			_, err := tx.ReplenishmentOrderItem.Create().
				SetTenantID(tenantA).
				SetReplenishmentOrderID(order.ID).
				SetSku("TEST-SKU-001").
				SetQuantity(50).
				SetData(testItemData).
				SetDataTypeID(itemDataTypeID).
				SetDataTypeSlug("item").
				Save(ent.NewTxContext(txid.With(ctx, txid.New()), tx))
			return err
		})
		require.NoError(t, err)

		// Test with SKU filter
		data := execOK[queryReplenishmentOrderItemsData](te, ctx, queryReplenishmentOrderItemsTpl, map[string]any{
			"Where": `{ Data: ["sku", "TEST-SKU-001"] }`,
		})
		assert.Equal(t, 1, data.ReplenishmentOrderItems.TotalCount)

		// Test with DataHasKey filter
		data2 := execOK[queryReplenishmentOrderItemsData](te, ctx, queryReplenishmentOrderItemsTpl, map[string]any{
			"Where": `{ DataHasKey: "warehouse.location" }`,
		})
		assert.Equal(t, 1, data2.ReplenishmentOrderItems.TotalCount)
	})
}

// =============================================================================
// CREATE WITH ITEMS TESTS
// =============================================================================

func TestReplenishmentOrder_CreateWithItems(t *testing.T) {
	t.Parallel()

	t.Run("creates order with multiple items", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		items := []map[string]any{
			{
				"DataTypeID": itemDataTypeID,
				"Data":       testReplenishmentItemData,
				"Sku":        "TEST-SKU-001",
				"Quantity":   50,
			},
			{
				"DataTypeID": itemDataTypeID,
				"Data":       testReplenishmentItemData,
				"Sku":        "TEST-SKU-002",
				"Quantity":   100,
			},
		}

		data := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderWithItemsTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
			"Items":      items,
		})

		created := data.CreateReplenishmentOrder.ReplenishmentOrder
		assert.NotEqual(t, uuid.Nil, created.ID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.Equal(t, supplierID, created.SupplierID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)

		// Verify order in database
		orders, err := te.Ent.ReplenishmentOrder.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		require.Len(t, orders, 1)
		assert.Equal(t, created.ID, orders[0].ID)

		// Verify items were created with correct foreign key
		orderItems, err := te.Ent.ReplenishmentOrderItem.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		require.Len(t, orderItems, 2)

		for _, item := range orderItems {
			assert.Equal(t, orders[0].ID, item.ReplenishmentOrderID)
			assert.Equal(t, tenantA, item.TenantID)
			assert.Equal(t, itemDataTypeID, item.DataTypeID)
		}

		// Verify SKUs and quantities
		skus := make(map[string]int64)
		for _, item := range orderItems {
			skus[item.Sku] = item.Quantity
		}
		assert.Equal(t, int64(50), skus["TEST-SKU-001"])
		assert.Equal(t, int64(100), skus["TEST-SKU-002"])
	})

	t.Run("creates order with empty items array", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderWithItemsTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
			"Items":      []map[string]any{},
		})

		created := data.CreateReplenishmentOrder.ReplenishmentOrder
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify no items were created
		orderItems, err := te.Ent.ReplenishmentOrderItem.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		assert.Empty(t, orderItems)
	})

	t.Run("rejects invalid supplier ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createReplenishmentOrderWithItemsTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": "invalid-uuid",
			"Items":      []map[string]any{},
		}, "invalid UUID")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid order dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		nonExistentDataTypeID := uuid.MustParse("00000000-0000-0000-0000-000000000000")

		execErr(te, ctx, createReplenishmentOrderWithItemsTpl, map[string]any{
			"DataTypeID": nonExistentDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
			"Items":      []map[string]any{},
		}, "data type")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects item with invalid data schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		invalidItemData := `{ wrongField: "invalid" }`

		items := []map[string]any{
			{
				"DataTypeID": itemDataTypeID,
				"Data":       invalidItemData,
				"Sku":        "TEST-SKU-001",
				"Quantity":   50,
			},
		}

		execErr(te, ctx, createReplenishmentOrderWithItemsTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
			"Items":      items,
		}, "missing properties")

		te.assertNoEvents(ctx)
	})

	t.Run("maintains tenant isolation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		items := []map[string]any{
			{
				"DataTypeID": itemDataTypeID,
				"Data":       testReplenishmentItemData,
				"Sku":        "TEST-SKU-001",
				"Quantity":   50,
			},
		}

		data := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderWithItemsTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
			"Items":      items,
		})

		assert.NotEqual(t, uuid.Nil, data.CreateReplenishmentOrder.ReplenishmentOrder.ID)

		// Query with tenant2 context - should not see tenant1's order
		ctxB := te.ctx(userB)
		orders, err := te.Ent.ReplenishmentOrder.Query().AllPages(ctxB, mixin.Limit)
		require.NoError(t, err)
		assert.Empty(t, orders)

		orderItems, err := te.Ent.ReplenishmentOrderItem.Query().AllPages(ctxB, mixin.Limit)
		require.NoError(t, err)
		assert.Empty(t, orderItems)
	})

	t.Run("all items have correct orderID foreign key", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		items := []map[string]any{
			{
				"DataTypeID": itemDataTypeID,
				"Data":       testReplenishmentItemData,
				"Sku":        "TEST-SKU-001",
				"Quantity":   50,
			},
			{
				"DataTypeID": itemDataTypeID,
				"Data":       testReplenishmentItemData,
				"Sku":        "TEST-SKU-002",
				"Quantity":   75,
			},
			{
				"DataTypeID": itemDataTypeID,
				"Data":       testReplenishmentItemData,
				"Sku":        "TEST-SKU-003",
				"Quantity":   100,
			},
		}

		data := execOK[createReplenishmentOrderData](te, ctx, createReplenishmentOrderWithItemsTpl, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Data":       testReplenishmentOrderData,
			"SupplierID": supplierID,
			"Items":      items,
		})

		orderID := data.CreateReplenishmentOrder.ReplenishmentOrder.ID

		// Verify all items have correct foreign key
		orderItems, err := te.Ent.ReplenishmentOrderItem.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		require.Len(t, orderItems, 3)

		for _, item := range orderItems {
			assert.Equal(t, orderID, item.ReplenishmentOrderID, "Item %s should have orderID %s", item.Sku, orderID)
		}
	})
}

// =============================================================================
// TENANT ISOLATION TESTS
// =============================================================================

func TestReplenishmentOrder_TenantIsolation(t *testing.T) {
	t.Parallel()

	t.Run("returns only own tenant orders", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ourOrder := te.newReplenishmentOrder(ctxA, userA).Create()

		ctxB := te.ctx(userB)
		te.newReplenishmentOrder(ctxB, userB).DataTypeID(itemDataTypeIDTenantB).Create()

		// Query with tenant1 - should only see own order
		data := execOK[queryReplenishmentOrdersData](te, ctxA, queryReplenishmentOrdersTpl, nil)

		require.Equal(t, 1, data.ReplenishmentOrders.TotalCount)
		assert.Equal(t, ourOrder.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestReplenishmentOrder_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		o1 := te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"priority": float64(30)}).Create()
		o2 := te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"priority": float64(10)}).Create()
		o3 := te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersJSONOrder, map[string]any{"JSONPath": "priority"})
		require.Equal(t, 3, data.ReplenishmentOrders.TotalCount)
		assert.Equal(t, o2.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
		assert.Equal(t, o3.ID, data.ReplenishmentOrders.Edges[1].Node.ID)
		assert.Equal(t, o1.ID, data.ReplenishmentOrders.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		o1 := te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(10)}}).Create()
		o2 := te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(30)}}).Create()

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersJSONOrder, map[string]any{"JSONPath": "meta.weight", "Direction": "DESC"})
		require.Equal(t, 2, data.ReplenishmentOrders.TotalCount)
		assert.Equal(t, o2.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
		assert.Equal(t, o1.ID, data.ReplenishmentOrders.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		o1 := te.newReplenishmentOrder(ctx, userA).Create()
		o2 := te.newReplenishmentOrder(ctx, userA).Create()

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersJSONOrder, map[string]any{"Field": "CREATED_AT", "Direction": "DESC"})
		require.Equal(t, 2, data.ReplenishmentOrders.TotalCount)
		assert.Equal(t, o2.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
		assert.Equal(t, o1.ID, data.ReplenishmentOrders.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestReplenishmentOrder_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		o2 := te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})
		require.Equal(t, 1, data.ReplenishmentOrders.TotalCount)
		assert.Equal(t, o2.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		o2 := te.newReplenishmentOrder(ctx, userA).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryReplenishmentOrdersData](te, ctx, queryReplenishmentOrdersJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})
		require.Equal(t, 1, data.ReplenishmentOrders.TotalCount)
		assert.Equal(t, o2.ID, data.ReplenishmentOrders.Edges[0].Node.ID)
	})
}
