package resolvers_test

import (
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
	createItem = resolver.ParseTemplate(`mutation {
		createReceivingInboundItem(input: {
			inboundID: "{{.InboundID}}",
			sku: "{{.Sku}}",
			quantity: {{or .Quantity 10}},
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestItem"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			receivingInboundItem { id tenantID inboundID sku quantity dataTypeID data }
		}
	}`)

	updateItem = resolver.ParseTemplate(`mutation {
		updateReceivingInboundItem(id: "{{.ID}}", input: {
			{{if .Sku}}sku: "{{.Sku}}",{{end}}
			{{if .Quantity}}quantity: {{.Quantity}},{{end}}
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestItem"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}{{end}}
		}) {
			receivingInboundItem { id tenantID inboundID sku quantity dataTypeID data }
		}
	}`)

	deleteItem = resolver.ParseTemplate(`mutation {
		deleteReceivingInboundItem(id: "{{.ID}}") { deletedID }
	}`)

	queryItems = resolver.ParseTemplate(`query {
		receivingInboundItems(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID inboundID sku quantity dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)

	queryInboundItemsJSONOrder = resolver.ParseTemplate(`query {
		receivingInboundItems(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID inboundID sku quantity dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type itemNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	InboundID  uuid.UUID
	Sku        string
	Quantity   int64
	DataTypeID uuid.UUID
	Data       map[string]any
}

type createItemData struct {
	CreateReceivingInboundItem struct{ ReceivingInboundItem itemNode }
}

type updateItemData struct {
	UpdateReceivingInboundItem struct{ ReceivingInboundItem itemNode }
}

type deleteItemData struct {
	DeleteReceivingInboundItem struct{ DeletedID uuid.UUID }
}

type queryItemsData struct {
	ReceivingInboundItems struct {
		TotalCount int
		Edges      []struct{ Node itemNode }
		PageInfo   struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestInboundItem_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates item with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[createItemData](te, ctx, createItem, map[string]any{
			"InboundID":  inbound.ID,
			"Sku":        "SKU-001",
			"Quantity":   25,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreateReceivingInboundItem.ReceivingInboundItem
		assert.Equal(t, "SKU-001", created.Sku)
		assert.Equal(t, int64(25), created.Quantity)
		assert.Equal(t, inbound.ID, created.InboundID)
		assert.Equal(t, tenantA, created.TenantID)

		// Verify persisted
		stored, err := te.Ent.InboundItem.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "SKU-001", stored.Sku)

		te.assertEvents(ctx, Create("inbounditem", created.ID))
	})

	t.Run("creates multiple items for same inbound", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		// Create 3 items
		var createdIDs []uuid.UUID
		for i := 1; i <= 3; i++ {
			d := execOK[createItemData](te, ctx, createItem, map[string]any{
				"InboundID":  inbound.ID,
				"Sku":        "SKU-" + string(rune('A'+i-1)),
				"DataTypeID": itemDataTypeID,
			})
			createdIDs = append(createdIDs, d.CreateReceivingInboundItem.ReceivingInboundItem.ID)
		}

		// Query all items for this inbound
		data := execOK[queryItemsData](te, ctx, queryItems, map[string]any{
			"Where": `{ inboundID: "` + inbound.ID.String() + `" }`,
		})

		assert.Equal(t, 3, data.ReceivingInboundItems.TotalCount)

		te.assertEvents(ctx,
			Create("inbounditem", createdIDs[0]),
			Create("inbounditem", createdIDs[1]),
			Create("inbounditem", createdIDs[2]),
		)
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createItem, map[string]any{
			"InboundID": inbound.ID,
			"Sku":       "SKU-X",
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative sum", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createItem, map[string]any{
			"InboundID":  inbound.ID,
			"Sku":        "SKU-X",
			"DataTypeID": itemDataTypeID,
			"Sum":        -10,
		}, "'/sum' does not validate")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative weight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createItem, map[string]any{
			"InboundID":  inbound.ID,
			"Sku":        "SKU-X",
			"DataTypeID": itemDataTypeID,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestInboundItem_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates sku and quantity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Sku("OLD-SKU").Quantity(10).Create()
		te.clearEvents(ctx)

		data := execOK[updateItemData](te, ctx, updateItem, map[string]any{
			"ID":         item.ID,
			"Sku":        "NEW-SKU",
			"Quantity":   99,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		})

		updated := data.UpdateReceivingInboundItem.ReceivingInboundItem
		assert.Equal(t, "NEW-SKU", updated.Sku)
		assert.Equal(t, int64(99), updated.Quantity)

		te.assertEvents(ctx, Update("inbounditem", item.ID))
	})

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		data := execOK[updateItemData](te, ctx, updateItem, map[string]any{
			"ID":         item.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Sum":        888,
			"Name":       "UpdatedName",
		})

		assert.InDelta(t, float64(888), data.UpdateReceivingInboundItem.ReceivingInboundItem.Data["sum"], 0.001)

		te.assertEvents(ctx, Update("inbounditem", item.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, updateItem, map[string]any{
			"ID":         uuid.New(),
			"Sku":        "X",
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "inbound_item not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateItem, map[string]any{
			"ID":   item.ID,
			"Data": true,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		inboundB := te.newInbound(ctxB, userB).Create()
		itemB := te.newItem(ctxB, userB, inboundB.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateItem, map[string]any{
			"ID":         itemB.ID,
			"Sku":        "HACKED",
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "inbound_item not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateItem, map[string]any{
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

func TestInboundItem_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		data := execOK[deleteItemData](te, ctx, deleteItem, map[string]any{
			"ID": item.ID,
		})

		assert.Equal(t, item.ID, data.DeleteReceivingInboundItem.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.InboundItem.Get(te.ctxWithDeleted(userA), item.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)

		te.assertEvents(ctx, Delete("inbounditem", item.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteItem, map[string]any{
			"ID": uuid.New(),
		}, "inbound_item not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		inboundB := te.newInbound(ctxB, userB).Create()
		itemB := te.newItem(ctxB, userB, inboundB.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteItem, map[string]any{
			"ID": itemB.ID,
		}, "inbound_item not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteItem, map[string]any{
			"ID": item.ID,
		}, "inbound_item not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestInboundItem_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryItemsData](te, ctx, queryItems, nil)

		assert.Equal(t, 0, data.ReceivingInboundItems.TotalCount)
		assert.Empty(t, data.ReceivingInboundItems.Edges)
	})

	t.Run("returns only own tenant's items", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		inboundA := te.newInbound(ctxA, userA).Create()
		itemA := te.newItem(ctxA, userA, inboundA.ID).Sku("TENANT-A").Create()

		inboundB := te.newInbound(ctxB, userB).Create()
		te.newItem(ctxB, userB, inboundB.ID).Sku("TENANT-B").Create()

		data := execOK[queryItemsData](te, ctxA, queryItems, nil)

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, itemA.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		active := te.newItem(ctx, userA, inbound.ID).Sku("ACTIVE").Create()
		te.newItem(ctx, userA, inbound.ID).Sku("DELETED").Deleted().Create()

		data := execOK[queryItemsData](te, ctx, queryItems, nil)

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, active.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.newItem(ctx, userA, inbound.ID).Create()
		te.newItem(ctx, userA, inbound.ID).Deleted().Create()

		data := execOK[queryItemsData](te, te.ctxWithDeleted(userA), queryItems, nil)

		assert.Equal(t, 2, data.ReceivingInboundItems.TotalCount)
	})

	t.Run("filters by sku", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		target := te.newItem(ctx, userA, inbound.ID).Sku("FIND-ME").Create()
		te.newItem(ctx, userA, inbound.ID).Sku("OTHER").Create()

		data := execOK[queryItemsData](te, ctx, queryItems, map[string]any{
			"Where": `{ skuIn: ["FIND-ME"] }`,
		})

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, target.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})

	t.Run("filters by inboundID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound1 := te.newInbound(ctx, userA).Create()
		inbound2 := te.newInbound(ctx, userA).Create()

		item1 := te.newItem(ctx, userA, inbound1.ID).Create()
		te.newItem(ctx, userA, inbound2.ID).Create()

		data := execOK[queryItemsData](te, ctx, queryItems, map[string]any{
			"Where": `{ inboundID: "` + inbound1.ID.String() + `" }`,
		})

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, item1.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})

	t.Run("filters by data field value", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Create()

		data := execOK[queryItemsData](te, ctx, queryItems, map[string]any{
			"Where": `{ Data: ["type", "custom"] }`,
		})

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, item.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Create()

		data := execOK[queryItemsData](te, ctx, queryItems, map[string]any{
			"Where": `{ DataHasKey: "meta.name" }`,
		})

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, item.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		for range 5 {
			te.newItem(ctx, userA, inbound.ID).Create()
		}

		data := execOK[queryItemsData](te, ctx, queryItems, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.ReceivingInboundItems.TotalCount)
		assert.Len(t, data.ReceivingInboundItems.Edges, 2)
		assert.True(t, data.ReceivingInboundItems.PageInfo.HasNextPage)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestInboundItem_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		i1 := te.newItem(ctx, userA, inbound.ID).Data(map[string]any{"sum": float64(30)}).Create()
		i2 := te.newItem(ctx, userA, inbound.ID).Data(map[string]any{"sum": float64(10)}).Create()
		i3 := te.newItem(ctx, userA, inbound.ID).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryItemsData](te, ctx, queryInboundItemsJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
		assert.Equal(t, i3.ID, data.ReceivingInboundItems.Edges[1].Node.ID)
		assert.Equal(t, i1.ID, data.ReceivingInboundItems.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		i1 := te.newItem(ctx, userA, inbound.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		i2 := te.newItem(ctx, userA, inbound.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryItemsData](te, ctx, queryInboundItemsJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.ReceivingInboundItems.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		i1 := te.newItem(ctx, userA, inbound.ID).Create()
		i2 := te.newItem(ctx, userA, inbound.ID).Create()

		data := execOK[queryItemsData](te, ctx, queryInboundItemsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.ReceivingInboundItems.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestInboundItem_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.newItem(ctx, userA, inbound.ID).Data(map[string]any{"type": "fragile"}).Create()
		i2 := te.newItem(ctx, userA, inbound.ID).Data(map[string]any{"type": "standard"}).Create()

		data := execOK[queryItemsData](te, ctx, queryInboundItemsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "standard"] }`,
		})

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.newItem(ctx, userA, inbound.ID).Data(map[string]any{"type": "fragile"}).Create()
		i2 := te.newItem(ctx, userA, inbound.ID).Data(map[string]any{"type": "standard", "priority": float64(1)}).Create()

		data := execOK[queryItemsData](te, ctx, queryInboundItemsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.ReceivingInboundItems.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInboundItems.Edges[0].Node.ID)
	})
}
