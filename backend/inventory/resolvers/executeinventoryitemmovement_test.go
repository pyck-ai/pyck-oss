package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	_ "github.com/mattn/go-sqlite3"

	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var executeItemMovement = testresolver.ParseTemplate(`
	mutation {
		executeInventoryItemMovement(id: "{{.ID}}") {
			inventoryItemMovement {
				id
				tenantID
				toID
				fromID
				handler
				quantity
				executed
			}
		}
	}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type executeItemMovementData struct {
	ExecuteInventoryItemMovement struct {
		InventoryItemMovement struct {
			ID       uuid.UUID
			TenantID uuid.UUID
			ToID     uuid.UUID
			FromID   uuid.UUID
			Handler  string
			Quantity int64
			Executed bool
		}
	}
}

type transactionsData struct {
	Transactions struct {
		TotalCount int
		Edges      []struct {
			Node struct {
				ID           uuid.UUID
				ItemID       uuid.UUID
				RepositoryID uuid.UUID
				Quantity     int64
				Type         string
			}
		}
	}
}

type stocksData struct {
	Stocks struct {
		TotalCount int
		Edges      []struct {
			Node struct {
				ID           uuid.UUID
				RepositoryID uuid.UUID
				ItemID       uuid.UUID
				Quantity     int64
			}
		}
	}
}

// =============================================================================
// EXECUTE ITEM MOVEMENT TESTS
// =============================================================================

func TestExecuteItemMovement(t *testing.T) {
	t.Parallel()

	t.Run("executes item movement successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create repositories
		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()

		// Create item
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()

		// Add initial stock to fromRepo
		te.newStock(ctx, userA, item.ID, fromRepo.ID).Quantity(9223372036854775807).Create()

		// Create item movement
		movement := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(9223372036854775807).
			Create()
		te.clearEvents(ctx)

		// Execute item movement
		data := execOK[executeItemMovementData](te, ctx, executeItemMovement, map[string]any{
			"ID": movement.ID,
		})

		assert.True(t, data.ExecuteInventoryItemMovement.InventoryItemMovement.Executed)

		// Verify transactions were created
		txData := execOK[transactionsData](te, ctx, queryTransactions, nil)
		assert.Equal(t, 2, txData.Transactions.TotalCount)

		for _, edge := range txData.Transactions.Edges {
			assert.Equal(t, item.ID, edge.Node.ItemID)
			assert.Equal(t, int64(9223372036854775807), edge.Node.Quantity)

			if edge.Node.RepositoryID == fromRepo.ID {
				assert.Equal(t, "out", edge.Node.Type)
			}
			if edge.Node.RepositoryID == toRepo.ID {
				assert.Equal(t, "into", edge.Node.Type)
			}
		}

		// Verify stock levels were updated correctly
		stockData := execOK[stocksData](te, ctx, stocksQueryTemplate, nil)

		// Build stock map to verify final quantities
		var fromRepoFinalStock, toRepoFinalStock int64 = -1, -1
		for _, edge := range stockData.Stocks.Edges {
			if edge.Node.ItemID == item.ID {
				if edge.Node.RepositoryID == fromRepo.ID && edge.Node.Quantity == 0 {
					fromRepoFinalStock = edge.Node.Quantity
				}
				if edge.Node.RepositoryID == toRepo.ID && edge.Node.Quantity == 9223372036854775807 {
					toRepoFinalStock = edge.Node.Quantity
				}
			}
		}

		assert.Equal(t, int64(0), fromRepoFinalStock, "Source repository should have 0 stock after movement")
		assert.Equal(t, int64(9223372036854775807), toRepoFinalStock, "Destination repository should have the moved stock")

		// Verify events: 1 itemmovement update + 2 transaction creates + 2 stock creates
		te.assertEventCounts(ctx, map[string]int{
			"itemmovement": 1,
			"transaction":  2,
			"stock":        2,
		})
	})
	t.Run("rejects executing same movement twice", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("From Repository").Create()
		toRepo := te.newRepository(ctx, userA).Name("To Repository").Create()
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()

		// Seed stock into the source repo so execution can succeed
		te.newStock(ctx, userA, item.ID, fromRepo.ID).Quantity(25).Create()

		movement := te.newItemMovement(ctx, userA, item.ID, fromRepo.ID, toRepo.ID).
			Quantity(25).
			Create()

		// First execution should succeed
		execOK[executeItemMovementData](te, ctx, executeItemMovement, map[string]any{
			"ID": movement.ID,
		})
		te.clearEvents(ctx)

		// Second execution should fail
		execErr(te, ctx, executeItemMovement, map[string]any{
			"ID": movement.ID,
		}, "already executed")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects executing movement from different tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create movement with tenant B
		ctxB := te.ctx(userB)
		fromRepo := te.newRepository(ctxB, userB).DataTypeID(itemDataTypeIDTenantB).Create()
		toRepo := te.newRepository(ctxB, userB).DataTypeID(itemDataTypeIDTenantB).Create()
		item := te.newItem(ctxB, userB).DataType(itemDataTypeIDTenantB, itemDataTypeSlug).Create()

		movement := te.newItemMovement(ctxB, userB, item.ID, fromRepo.ID, toRepo.ID).
			DataTypeID(itemDataTypeIDTenantB).
			Quantity(100).
			Create()

		// Try to execute with tenant A's context
		ctxA := te.ctx(userA)
		te.clearEvents(ctxA)
		execErr(te, ctxA, executeItemMovement, map[string]any{
			"ID": movement.ID,
		}, "not found")

		te.assertNoEvents(ctxA)
	})
}
