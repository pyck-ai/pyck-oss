package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var executeRepositoryMovement = testresolver.ParseTemplate(`
	mutation {
		executeInventoryRepositoryMovement(id: "{{.ID}}") {
			inventoryRepositoryMovement {
				id
				tenantID
				toID
				fromID
				handler
				executed
			}
		}
	}`)

var queryRepositoriesForExecMovement = testresolver.ParseTemplate(`
	query {
		repositories {
			totalCount
			edges {
				node {
					id
					name
					type
					parentID
					data
				}
			}
		}
	}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type executeRepositoryMovementData struct {
	ExecuteInventoryRepositoryMovement struct {
		InventoryRepositoryMovement struct {
			ID       uuid.UUID
			TenantID uuid.UUID
			ToID     uuid.UUID
			FromID   *uuid.UUID
			Handler  string
			Executed bool
		}
	}
}

type queryRepositoriesForExecMovementData struct {
	Repositories struct {
		TotalCount int
		Edges      []struct {
			Node struct {
				ID       uuid.UUID
				Name     string
				Type     string
				ParentID *uuid.UUID
				Data     map[string]any
			}
		}
	}
}

// =============================================================================
// EXECUTE REPOSITORY MOVEMENT TESTS
// =============================================================================

func TestExecuteRepositoryMovement(t *testing.T) {
	t.Parallel()

	t.Run("executes repository movement successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create parent repositories
		repoParent1 := te.newRepository(ctx, userA).Name("Parent Repo 1").Create()
		repoParent2 := te.newRepository(ctx, userA).Name("Parent Repo 2").Create()

		// Create child repository under parent 1
		repoChild1 := te.newRepository(ctx, userA).Name("Child Repo 1").Parent(repoParent1.ID).Create()

		// Create item and stocks
		item := te.newItem(ctx, userA).Sku("TEST-ITEM").Create()
		te.newStock(ctx, userA, item.ID, repoParent1.ID).Quantity(10).Create()
		te.newStock(ctx, userA, item.ID, repoChild1.ID).Quantity(10).Create()

		// Create repository movement
		movement := te.newRepositoryMovement(ctx, userA, repoChild1.ID, repoParent2.ID).
			FromID(repoParent1.ID).
			Create()
		te.clearEvents(ctx)

		// Execute repository movement
		data := execOK[executeRepositoryMovementData](te, ctx, executeRepositoryMovement, map[string]any{
			"ID": movement.ID,
		})

		assert.True(t, data.ExecuteInventoryRepositoryMovement.InventoryRepositoryMovement.Executed)

		// Verify repository was moved correctly
		repoData := execOK[queryRepositoriesForExecMovementData](te, ctx, queryRepositoriesForExecMovement, nil)
		assert.Equal(t, 3, repoData.Repositories.TotalCount)

		// Find the moved repository and verify its parent changed
		for _, edge := range repoData.Repositories.Edges {
			if edge.Node.ID == repoChild1.ID {
				require.NotNil(t, edge.Node.ParentID)
				assert.Equal(t, repoParent2.ID, *edge.Node.ParentID)
			}
		}

		// Verify stock was updated
		stockData := execOK[stocksData](te, ctx, stocksQueryTemplate, nil)

		stockMap := make(map[uuid.UUID]int64)
		for _, edge := range stockData.Stocks.Edges {
			stockMap[edge.Node.RepositoryID] = edge.Node.Quantity
		}

		assert.Equal(t, int64(0), stockMap[repoParent1.ID])  // Source should be empty
		assert.Equal(t, int64(10), stockMap[repoParent2.ID]) // Destination should have stock
		assert.Equal(t, int64(10), stockMap[repoChild1.ID])  // Child should keep its stock

		// Verify events: 1 repositorymovement update + 2 stock creates + 1 repository update
		te.assertEventCounts(ctx, map[string]int{
			"repositorymovement": 1,
			"stock":              2,
			"repository":         1,
		})
	})

	t.Run("rejects executing same movement twice", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create minimal test setup
		repo1 := te.newRepository(ctx, userA).Name("Test Repo 1").Create()
		repo2 := te.newRepository(ctx, userA).Name("Test Repo 2").Create()

		// Create repository movement
		movement := te.newRepositoryMovement(ctx, userA, repo1.ID, repo2.ID).
			FromID(repo1.ID).
			Create()

		// Execute first time (should succeed)
		execOK[executeRepositoryMovementData](te, ctx, executeRepositoryMovement, map[string]any{
			"ID": movement.ID,
		})
		te.clearEvents(ctx)

		// Execute second time (should fail)
		execErr(te, ctx, executeRepositoryMovement, map[string]any{
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
		repo1 := te.newRepository(ctxB, userB).Name("Test Repo").DataTypeID(itemDataTypeIDTenantB).Create()

		movement := te.newRepositoryMovement(ctxB, userB, repo1.ID, repo1.ID).
			FromID(repo1.ID).
			DataTypeID(itemDataTypeIDTenantB).
			Create()

		// Try to execute with tenant A's context
		ctxA := te.ctx(userA)
		te.clearEvents(ctxA)
		execErr(te, ctxA, executeRepositoryMovement, map[string]any{
			"ID": movement.ID,
		}, "not found")

		te.assertNoEvents(ctxA)
	})
}
