package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	entitemmovement "github.com/pyck-ai/pyck/backend/inventory/ent/gen/itemmovement"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createItemMovement = resolver.ParseTemplate(`mutation {
		createInventoryItemMovement(input: {
			itemID: "{{.ItemID}}",
			fromID: "{{.FromID}}",
			toID: "{{.ToID}}",
			handler: "{{.Handler}}",
			blockedBy: {{.BlockedBy}},
			quantity: {{.Quantity}},
			executed: false,
			dataTypeID: "{{.DataTypeID}}",
			data: {{or .Data "null" }}
		}) {
			inventoryItemMovement {
				id
				itemID
				toID
				fromID
				handler
				blockedBy
				quantity
				executed
				executedAt
				dataTypeID
				data
				tenantID
			}
		}
	}`)

	updateItemMovement = resolver.ParseTemplate(`mutation {
		updateInventoryItemMovement(
			id: "{{.ID}}",
			input: {
				{{if .Handler}}handler: "{{.Handler}}",{{end}}
				{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
				{{if .Data}}data: {{.Data}}{{end}}
			}
		) {
			inventoryItemMovement {
				id
				tenantID
				toID
				fromID
				handler
				quantity
				executed
				dataTypeID
				data
				createdAt
				createdBy
				updatedAt
				updatedBy
			}
		}
	}`)

	deleteItemMovement = resolver.ParseTemplate(`mutation {
		deleteInventoryItemMovement(id: "{{.ID}}") {
			deletedID
		}
	}`)

	queryItemMovements = resolver.ParseTemplate(`query {
		itemMovements(
			{{if .First}}first: {{.First}},{{end}}
			{{if .After}}after: "{{.After}}",{{end}}
			{{if .OrderBy}}orderBy: {{.OrderBy}},{{end}}
			{{if .Where}}where: {{.Where}}{{else}}where: null{{end}}
		) {
			totalCount
			edges {
				node {
					id
					toID
					fromID
					handler
					blockedBy
					quantity
					executed
					executedAt
					dataTypeID
					data
					tenantID
				}
				cursor
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

	queryItemMovementsJSONOrder = resolver.ParseTemplate(`query {
		itemMovements(
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
			edges { node { id toID fromID handler data tenantID } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type itemMovementNode struct {
	ID         uuid.UUID
	ItemID     uuid.UUID
	TenantID   uuid.UUID
	ToID       uuid.UUID
	FromID     uuid.UUID
	Handler    string
	BlockedBy  entitemmovement.BlockedBy
	Quantity   int64
	Executed   bool
	ExecutedAt *string
	DataTypeID uuid.UUID
	Data       map[string]any
	CreatedAt  string
	CreatedBy  uuid.UUID
	UpdatedAt  string
	UpdatedBy  uuid.UUID
}

type createItemMovementData struct {
	CreateInventoryItemMovement struct {
		InventoryItemMovement itemMovementNode
	}
}

type updateItemMovementData struct {
	UpdateInventoryItemMovement struct {
		InventoryItemMovement itemMovementNode
	}
}

type deleteItemMovementData struct {
	DeleteInventoryItemMovement struct {
		DeletedID uuid.UUID
	}
}

type queryItemMovementsData struct {
	ItemMovements struct {
		TotalCount int
		Edges      []struct {
			Node   itemMovementNode
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

func TestItemMovement_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates item movement with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()

		// Create item and stock
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()
		te.newStock(ctx, userA, item.ID, fromRepo.ID).Quantity(100).Create()
		te.clearEvents(ctx)

		data := execOK[createItemMovementData](te, ctx, createItemMovement, map[string]any{
			"ItemID":     item.ID,
			"FromID":     fromRepo.ID,
			"ToID":       toRepo.ID,
			"Handler":    testHandler,
			"BlockedBy":  testBlockedBy.String(),
			"Quantity":   5,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreateInventoryItemMovement.InventoryItemMovement
		assert.Equal(t, item.ID, created.ItemID)
		assert.Equal(t, fromRepo.ID, created.FromID)
		assert.Equal(t, toRepo.ID, created.ToID)
		assert.Equal(t, testHandler, created.Handler)
		assert.Equal(t, tenantA, created.TenantID)
		assert.Equal(t, int64(5), created.Quantity)

		// Verify persisted
		stored, err := te.Ent.ItemMovement.Query().AllPages(ctx, mixin.Limit)
		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, created.ID, stored[0].ID)
	})

	t.Run("rejects insufficient stock", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()

		// Create item with limited stock
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()
		te.newStock(ctx, userA, item.ID, fromRepo.ID).Quantity(100).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createItemMovement, map[string]any{
			"ItemID":     item.ID,
			"FromID":     fromRepo.ID,
			"ToID":       toRepo.ID,
			"Handler":    testHandler,
			"BlockedBy":  testBlockedBy.String(),
			"Quantity":   1000, // More than available stock
			"DataTypeID": itemDataTypeID,
		}, "insufficient stock")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid data - missing required fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories with data type
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").
			Data(map[string]any{"type": "physical"}).
			DataTypeID(itemDataTypeID).
			Create()

		// Create item with data type
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").
			Data(validData).
			DataType(itemDataTypeID, itemDataTypeSlug).
			Create()
		te.newStock(ctx, userA, item.ID, fromRepo.ID).Quantity(100).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createItemMovement, map[string]any{
			"ItemID":     item.ID,
			"FromID":     fromRepo.ID,
			"ToID":       toRepo.ID,
			"Handler":    testHandler,
			"BlockedBy":  testBlockedBy.String(),
			"Quantity":   25,
			"DataTypeID": itemDataTypeID,
			"Data":       `{type2: "value"}`, // Missing required "type" field
		}, "missing properties")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid data - negative weight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories with data type
		toRepo := te.newRepository(ctx, userA).Name("To Repository").
			Data(map[string]any{"type": "physical"}).
			DataTypeID(itemDataTypeID).
			Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").
			Data(map[string]any{"type": "physical"}).
			DataTypeID(itemDataTypeID).
			Create()

		// Create item with data type
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").
			Data(validData).
			DataType(itemDataTypeID, itemDataTypeSlug).
			Create()
		te.newStock(ctx, userA, item.ID, fromRepo.ID).Quantity(100).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createItemMovement, map[string]any{
			"ItemID":     item.ID,
			"FromID":     fromRepo.ID,
			"ToID":       toRepo.ID,
			"Handler":    testHandler,
			"BlockedBy":  testBlockedBy.String(),
			"Quantity":   25,
			"DataTypeID": itemDataTypeID,
			"Data": `{
				type: "custom",
				sum: 15,
				meta: {
					name: "TestItemMovement2",
					weight: -50,
					tags: ["test", "foobar"]
				}
			}`,
		}, "/meta/weight")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestItemMovement_Delete(t *testing.T) {
	t.Parallel()

	t.Run("deletes item movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()

		// Create item and movement
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()
		movement := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(100).
			Create()
		te.clearEvents(ctx)

		data := execOK[deleteItemMovementData](te, ctx, deleteItemMovement, map[string]any{
			"ID": movement.ID,
		})

		assert.Equal(t, movement.ID, data.DeleteInventoryItemMovement.DeletedID)
	})

	t.Run("rejects delete of other tenant's item movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		toRepo := te.newRepository(ctxB, userB).Name("To Repository").
			DataTypeID(itemDataTypeIDTenantB).
			Create()
		fromRepo := te.newRepository(ctxB, userB).Name("From Repository").
			DataTypeID(itemDataTypeIDTenantB).
			Create()
		item := te.newItem(ctxB, userB).Sku("TEST-ITEM").
			DataType(itemDataTypeIDTenantB, itemDataTypeSlug).
			Create()
		movement := te.newItemMovement(ctxB, userB, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(100).
			DataTypeID(itemDataTypeIDTenantB).
			Create()
		te.clearEvents(ctxB)

		// Try to delete as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteItemMovement, map[string]any{
			"ID": movement.ID,
		}, "not found")

		te.assertNoEvents(ctxA)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestItemMovement_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates item movement handler", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()

		// Create item and movement
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()
		movement := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(25).
			Create()
		te.clearEvents(ctx)

		data := execOK[updateItemMovementData](te, ctx, updateItemMovement, map[string]any{
			"ID":      movement.ID,
			"Handler": "updated-handler",
		})

		updated := data.UpdateInventoryItemMovement.InventoryItemMovement
		assert.Equal(t, toRepo.ID, updated.ToID)
		assert.Equal(t, "updated-handler", updated.Handler)
		assert.Equal(t, tenantA, updated.TenantID)
	})

	t.Run("rejects update of other tenant's item movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		toRepo := te.newRepository(ctxB, userB).Name("To Repository").
			DataTypeID(itemDataTypeIDTenantB).
			Create()
		fromRepo := te.newRepository(ctxB, userB).Name("From Repository").
			DataTypeID(itemDataTypeIDTenantB).
			Create()
		item := te.newItem(ctxB, userB).Sku("TEST-ITEM").
			DataType(itemDataTypeIDTenantB, itemDataTypeSlug).
			Create()
		movement := te.newItemMovement(ctxB, userB, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(100).
			DataTypeID(itemDataTypeIDTenantB).
			Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateItemMovement, map[string]any{
			"ID":      movement.ID,
			"Handler": "updated-handler",
		}, "not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects update with invalid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories with data type
		toRepo := te.newRepository(ctx, userA).Name("To Repository").
			Data(map[string]any{"type": "physical"}).
			DataTypeID(itemDataTypeID).
			Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").
			Data(map[string]any{"type": "physical"}).
			DataTypeID(itemDataTypeID).
			Create()

		// Create item with data type
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").
			Data(validData).
			DataType(itemDataTypeID, itemDataTypeSlug).
			Create()

		// Create movement with valid data
		movement := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(100).
			Data(validData).
			DataTypeID(itemDataTypeID).
			Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateItemMovement, map[string]any{
			"ID":         movement.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       `{type2: "custom"}`, // Missing required "type" field
		}, "missing properties")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestItemMovement_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryItemMovementsData](te, ctx, queryItemMovements, nil)

		assert.Equal(t, 0, data.ItemMovements.TotalCount)
		assert.Empty(t, data.ItemMovements.Edges)
		assert.False(t, data.ItemMovements.PageInfo.HasNextPage)
		assert.False(t, data.ItemMovements.PageInfo.HasPreviousPage)
		assert.Nil(t, data.ItemMovements.PageInfo.StartCursor)
		assert.Nil(t, data.ItemMovements.PageInfo.EndCursor)
	})

	t.Run("returns single item movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()

		// Create item and movement
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()
		movement := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(100).
			Create()

		data := execOK[queryItemMovementsData](te, ctx, queryItemMovements, nil)

		assert.Equal(t, 1, data.ItemMovements.TotalCount)
		assert.False(t, data.ItemMovements.PageInfo.HasNextPage)
		assert.False(t, data.ItemMovements.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.ItemMovements.PageInfo.StartCursor)
		assert.NotNil(t, data.ItemMovements.PageInfo.EndCursor)

		require.Len(t, data.ItemMovements.Edges, 1)
		node := data.ItemMovements.Edges[0].Node
		assert.Equal(t, movement.ID, node.ID)
		assert.Equal(t, movement.TenantID, node.TenantID)
		assert.Equal(t, movement.Quantity, node.Quantity)
		assert.Equal(t, movement.ToID, node.ToID)
		assert.Equal(t, movement.FromID, node.FromID)
		assert.Equal(t, movement.Handler, node.Handler)
	})

	// Filter tests
	filterCases := []struct {
		name            string
		where           string
		expectedResults int
	}{
		{
			name:            "no filter",
			where:           "null",
			expectedResults: 2,
		},
		{
			name: "filter by data value",
			where: `{
				Data: ["type", "custom"],
			}`,
			expectedResults: 1,
		},
		{
			name: "filter by data has key",
			where: `{
				DataHasKey: "meta.name",
			}`,
			expectedResults: 1,
		},
		{
			name: "filter by data contains",
			where: `{
				DataContains: ["meta.tags", "foo"],
			}`,
			expectedResults: 1,
		},
		{
			name: "null data filter returns all",
			where: `{
				Data: null,
			}`,
			expectedResults: 2,
		},
		{
			name: "null data has key filter returns all",
			where: `{
				DataHasKey: null,
			}`,
			expectedResults: 2,
		},
		{
			name: "null data in filter returns all",
			where: `{
				DataIn: null,
			}`,
			expectedResults: 2,
		},
		{
			name: "null data contains filter returns all",
			where: `{
				DataContains: null,
			}`,
			expectedResults: 2,
		},
	}

	for _, tc := range filterCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			ctx := te.ctx(userA)

			// Create repositories with data type
			toRepo := te.newRepository(ctx, userA).Name("To Repository").
				Data(map[string]any{"type": "physical"}).
				DataTypeID(itemDataTypeID).
				Create()
			fromRepo := te.newRepository(ctx, userA).Name("From Repository").
				Data(map[string]any{"type": "physical"}).
				DataTypeID(itemDataTypeID).
				Create()

			// Create item
			item := te.newItem(ctx, userA).Sku("TEST-ITEM").
				Data(validData).
				DataType(itemDataTypeID, itemDataTypeSlug).
				Create()

			// Create two movements - one with data, one without
			te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
				Quantity(100).
				Data(validData).
				Create()
			te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
				Quantity(100).
				Data(nil).
				Create()

			// Create movement from other tenant (shouldn't appear in results)
			ctxB := te.ctx(userB)
			toRepo2 := te.newRepository(ctxB, userB).Name("To Repository 2").
				Data(map[string]any{"type": "physical"}).
				DataTypeID(itemDataTypeIDTenantB).
				Create()
			fromRepo2 := te.newRepository(ctxB, userB).Name("From Repository 2").
				Data(map[string]any{"type": "physical"}).
				DataTypeID(itemDataTypeIDTenantB).
				Create()
			item2 := te.newItem(ctxB, userB).Sku("TEST-ITEM-2").
				Data(validData).
				DataType(itemDataTypeIDTenantB, itemDataTypeSlug).
				Create()
			te.newItemMovement(ctxB, userB, item2.ID, fromRepo2.ID, toRepo2.ID).
				Quantity(100).
				Data(validData).
				DataTypeID(itemDataTypeIDTenantB).
				Create()

			// Query as tenant A
			data := execOK[queryItemMovementsData](te, ctx, queryItemMovements, map[string]any{
				"OrderBy": "{ direction: ASC, field: CREATED_AT }",
				"Where":   tc.where,
			})

			assert.Equal(t, tc.expectedResults, data.ItemMovements.TotalCount)
			assert.Len(t, data.ItemMovements.Edges, tc.expectedResults)
			assert.False(t, data.ItemMovements.PageInfo.HasNextPage)
			assert.False(t, data.ItemMovements.PageInfo.HasPreviousPage)
			assert.NotNil(t, data.ItemMovements.PageInfo.StartCursor)
			assert.NotNil(t, data.ItemMovements.PageInfo.EndCursor)
		})
	}
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestItemMovement_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		fromRepo := te.newRepository(ctx, userA).Create()
		toRepo := te.newRepository(ctx, userA).Create()

		m1 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"priority": float64(30)}).Create()
		m2 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"priority": float64(10)}).Create()
		m3 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryItemMovementsData](te, ctx, queryItemMovementsJSONOrder, map[string]any{"JSONPath": "priority"})
		require.Equal(t, 3, data.ItemMovements.TotalCount)
		assert.Equal(t, m2.ID, data.ItemMovements.Edges[0].Node.ID)
		assert.Equal(t, m3.ID, data.ItemMovements.Edges[1].Node.ID)
		assert.Equal(t, m1.ID, data.ItemMovements.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		fromRepo := te.newRepository(ctx, userA).Create()
		toRepo := te.newRepository(ctx, userA).Create()

		m1 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"meta": map[string]any{"weight": float64(10)}}).Create()
		m2 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"meta": map[string]any{"weight": float64(30)}}).Create()

		data := execOK[queryItemMovementsData](te, ctx, queryItemMovementsJSONOrder, map[string]any{"JSONPath": "meta.weight", "Direction": "DESC"})
		require.Equal(t, 2, data.ItemMovements.TotalCount)
		assert.Equal(t, m2.ID, data.ItemMovements.Edges[0].Node.ID)
		assert.Equal(t, m1.ID, data.ItemMovements.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		fromRepo := te.newRepository(ctx, userA).Create()
		toRepo := te.newRepository(ctx, userA).Create()

		m1 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Create()
		m2 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Create()

		data := execOK[queryItemMovementsData](te, ctx, queryItemMovementsJSONOrder, map[string]any{"Field": "CREATED_AT", "Direction": "DESC"})
		require.Equal(t, 2, data.ItemMovements.TotalCount)
		assert.Equal(t, m2.ID, data.ItemMovements.Edges[0].Node.ID)
		assert.Equal(t, m1.ID, data.ItemMovements.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestItemMovement_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		fromRepo := te.newRepository(ctx, userA).Create()
		toRepo := te.newRepository(ctx, userA).Create()

		te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"type": "alpha"}).Create()
		m2 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryItemMovementsData](te, ctx, queryItemMovementsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})
		require.Equal(t, 1, data.ItemMovements.TotalCount)
		assert.Equal(t, m2.ID, data.ItemMovements.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		fromRepo := te.newRepository(ctx, userA).Create()
		toRepo := te.newRepository(ctx, userA).Create()

		te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"type": "alpha"}).Create()
		m2 := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryItemMovementsData](te, ctx, queryItemMovementsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})
		require.Equal(t, 1, data.ItemMovements.TotalCount)
		assert.Equal(t, m2.ID, data.ItemMovements.Edges[0].Node.ID)
	})
}
