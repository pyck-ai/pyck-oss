package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createRepositoryMovement = resolver.ParseTemplate(`mutation {
		createInventoryRepositoryMovement(input: {
			repositoryID: "{{.RepositoryID}}",
			toID: "{{.ToID}}",
			{{if .FromID}}fromID: "{{.FromID}}",{{end}}
			handler: "{{.Handler}}",
			executed: {{.Executed}},
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestMovement"}}", weight: {{or .Weight 50}}, tags: ["foo", "bar"] }
			}{{end}}
		}) {
			inventoryRepositoryMovement {
				id
				repositoryID
				toID
				fromID
				handler
				executed
				executedAt
				dataTypeID
				data
				tenantID
			}
		}
	}`)

	updateRepositoryMovement = resolver.ParseTemplate(`mutation {
		updateInventoryRepositoryMovement(id: "{{.ID}}", input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "UpdatedMovement"}}", weight: {{or .Weight 50}}, tags: ["test", "foobar"] }
			}{{end}}
		}) {
			inventoryRepositoryMovement {
				id
				tenantID
				toID
				fromID
				handler
				executed
				dataTypeID
				data
			}
		}
	}`)

	deleteRepositoryMovement = resolver.ParseTemplate(`mutation {
		deleteInventoryRepositoryMovement(id: "{{.ID}}") { deletedID }
	}`)

	queryRepositoryMovements = resolver.ParseTemplate(`query {
		repositoryMovements(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges {
				node {
					id
					toID
					fromID
					handler
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

	queryRepositoryMovementsJSONOrder = resolver.ParseTemplate(`query {
		repositoryMovements(
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

type repositoryMovementNode struct {
	ID           uuid.UUID
	RepositoryID uuid.UUID
	TenantID     uuid.UUID
	ToID         uuid.UUID
	FromID       uuid.UUID
	Handler      string
	Executed     bool
	DataTypeID   uuid.UUID
	Data         map[string]any
}

type createRepositoryMovementData struct {
	CreateInventoryRepositoryMovement struct{ InventoryRepositoryMovement repositoryMovementNode }
}

type updateRepositoryMovementData struct {
	UpdateInventoryRepositoryMovement struct{ InventoryRepositoryMovement repositoryMovementNode }
}

type deleteRepositoryMovementData struct {
	DeleteInventoryRepositoryMovement struct{ DeletedID uuid.UUID }
}

type queryRepositoryMovementsData struct {
	RepositoryMovements struct {
		TotalCount int
		Edges      []struct{ Node repositoryMovementNode }
		PageInfo   struct {
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

func TestRepositoryMovement_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates repository movement with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Setup repositories
		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		te.clearEvents(ctx)

		data := execOK[createRepositoryMovementData](te, ctx, createRepositoryMovement, map[string]any{
			"RepositoryID": repo.ID,
			"ToID":         toRepo.ID,
			"Handler":      testHandler,
			"Executed":     false,
			"DataTypeID":   itemDataTypeID,
			"Data":         true,
		})

		created := data.CreateInventoryRepositoryMovement.InventoryRepositoryMovement
		assert.Equal(t, repo.ID, created.RepositoryID)
		assert.Equal(t, toRepo.ID, created.ToID)
		assert.Equal(t, testHandler, created.Handler)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.RepositoryMovement.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, repo.ID, stored.RepositoryID)

		// Verify event
		te.assertEvents(ctx, Create("repositorymovement", created.ID))
	})

	t.Run("creates repository movement with fromID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		te.clearEvents(ctx)

		data := execOK[createRepositoryMovementData](te, ctx, createRepositoryMovement, map[string]any{
			"RepositoryID": repo.ID,
			"ToID":         toRepo.ID,
			"FromID":       fromRepo.ID,
			"Handler":      testHandler,
			"Executed":     false,
			"DataTypeID":   itemDataTypeID,
			"Data":         true,
		})

		created := data.CreateInventoryRepositoryMovement.InventoryRepositoryMovement
		assert.Equal(t, fromRepo.ID, created.FromID)
		te.assertEvents(ctx, Create("repositorymovement", created.ID))
	})

	t.Run("rejects invalid schema - missing properties", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		te.clearEvents(ctx)

		// Use custom template for invalid data (missing required fields)
		invalidTpl := resolver.ParseTemplate(`mutation {
			createInventoryRepositoryMovement(input: {
				repositoryID: "{{.RepositoryID}}",
				toID: "{{.ToID}}",
				handler: "{{.Handler}}",
				executed: false,
				dataTypeID: "{{.DataTypeID}}",
				data: { type2: "custom" }
			}) {
				inventoryRepositoryMovement { id }
			}
		}`)

		execErr(te, ctx, invalidTpl, map[string]any{
			"RepositoryID": repo.ID,
			"ToID":         toRepo.ID,
			"Handler":      testHandler,
			"DataTypeID":   itemDataTypeID,
		}, "missing properties")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative weight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createRepositoryMovement, map[string]any{
			"RepositoryID": repo.ID,
			"ToID":         toRepo.ID,
			"Handler":      testHandler,
			"Executed":     false,
			"DataTypeID":   itemDataTypeID,
			"Data":         true,
			"Weight":       -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestRepositoryMovement_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		movement := te.newRepositoryMovement(ctx, userA, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.clearEvents(ctx)

		data := execOK[updateRepositoryMovementData](te, ctx, updateRepositoryMovement, map[string]any{
			"ID":         movement.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Name":       "UpdatedItem",
		})

		updated := data.UpdateInventoryRepositoryMovement.InventoryRepositoryMovement
		assert.Equal(t, toRepo.ID, updated.ToID)
		assert.Equal(t, tenantA, updated.TenantID)
		te.assertEvents(ctx, Update("repositorymovement", movement.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fakeID := uuid.New()
		execErr(te, ctx, updateRepositoryMovement, map[string]any{
			"ID":         fakeID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "repository_movement not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		fromRepo := te.newRepository(ctxB, userB).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctxB, userB).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctxB, userB).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		movement := te.newRepositoryMovement(ctxB, userB, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateRepositoryMovement, map[string]any{
			"ID":   movement.ID,
			"Data": true,
		}, "")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		movement := te.newRepositoryMovement(ctx, userA, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.clearEvents(ctx)

		// Use custom template for invalid data
		invalidTpl := resolver.ParseTemplate(`mutation {
			updateInventoryRepositoryMovement(id: "{{.ID}}", input: {
				dataTypeID: "{{.DataTypeID}}",
				data: { type2: "custom" }
			}) {
				inventoryRepositoryMovement { id }
			}
		}`)

		execErr(te, ctx, invalidTpl, map[string]any{
			"ID":         movement.ID,
			"DataTypeID": itemDataTypeID,
		}, "missing properties")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestRepositoryMovement_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes repository movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		movement := te.newRepositoryMovement(ctx, userA, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.clearEvents(ctx)

		data := execOK[deleteRepositoryMovementData](te, ctx, deleteRepositoryMovement, map[string]any{
			"ID": movement.ID,
		})

		assert.Equal(t, movement.ID, data.DeleteInventoryRepositoryMovement.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.RepositoryMovement.Get(te.ctxWithDeleted(userA), movement.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("repositorymovement", movement.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteRepositoryMovement, map[string]any{
			"ID": uuid.New(),
		}, "repositoryMovement not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		fromRepo := te.newRepository(ctxB, userB).NoData().Create()
		toRepo := te.newRepository(ctxB, userB).NoData().Create()
		repo := te.newRepository(ctxB, userB).Parent(fromRepo.ID).NoData().Create()
		movement := te.newRepositoryMovement(ctxB, userB, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteRepositoryMovement, map[string]any{
			"ID": movement.ID,
		}, "repositoryMovement not found")

		te.assertNoEvents(ctxA)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestRepositoryMovement_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, nil)

		assert.Equal(t, 0, data.RepositoryMovements.TotalCount)
		assert.Empty(t, data.RepositoryMovements.Edges)
		assert.False(t, data.RepositoryMovements.PageInfo.HasNextPage)
		assert.False(t, data.RepositoryMovements.PageInfo.HasPreviousPage)
	})

	t.Run("returns movement with data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		movement := te.newRepositoryMovement(ctx, userA, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, nil)

		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, movement.ID, data.RepositoryMovements.Edges[0].Node.ID)
		assert.Equal(t, movement.TenantID, data.RepositoryMovements.Edges[0].Node.TenantID)
		assert.Equal(t, movement.ToID, data.RepositoryMovements.Edges[0].Node.ToID)
		assert.Equal(t, movement.FromID, data.RepositoryMovements.Edges[0].Node.FromID)
		assert.Equal(t, movement.Handler, data.RepositoryMovements.Edges[0].Node.Handler)
	})

	t.Run("returns only own tenant's movements", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		// Create movement for tenant A
		fromRepoA := te.newRepository(ctxA, userA).Name("from-repo-a").NoData().Create()
		toRepoA := te.newRepository(ctxA, userA).Name("to-repo-a").NoData().Create()
		repoA := te.newRepository(ctxA, userA).Name("test-repo-a").Parent(fromRepoA.ID).NoData().Create()
		movementA := te.newRepositoryMovement(ctxA, userA, repoA.ID, toRepoA.ID).FromID(fromRepoA.ID).Create()

		// Create movement for tenant B
		fromRepoB := te.newRepository(ctxB, userB).Name("from-repo-b").NoData().Create()
		toRepoB := te.newRepository(ctxB, userB).Name("to-repo-b").NoData().Create()
		repoB := te.newRepository(ctxB, userB).Name("test-repo-b").Parent(fromRepoB.ID).NoData().Create()
		te.newRepositoryMovement(ctxB, userB, repoB.ID, toRepoB.ID).FromID(fromRepoB.ID).Create()

		data := execOK[queryRepositoryMovementsData](te, ctxA, queryRepositoryMovements, nil)

		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, movementA.ID, data.RepositoryMovements.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()

		activeMovement := te.newRepositoryMovement(ctx, userA, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()

		// Create a different repo for the deleted movement to avoid conflict
		repo2 := te.newRepository(ctx, userA).Name("test-repo-2").Parent(fromRepo.ID).NoData().Create()
		te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).Deleted().Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, nil)

		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, activeMovement.ID, data.RepositoryMovements.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo := te.newRepository(ctx, userA).Name("test-repo").Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Name("test-repo-2").Parent(fromRepo.ID).NoData().Create()

		te.newRepositoryMovement(ctx, userA, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).Deleted().Create()

		data := execOK[queryRepositoryMovementsData](te, te.ctxWithDeleted(userA), queryRepositoryMovements, nil)

		assert.Equal(t, 2, data.RepositoryMovements.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).NoData().Create()
		toRepo := te.newRepository(ctx, userA).NoData().Create()

		for range 5 {
			repo := te.newRepository(ctx, userA).NoData().Create()
			te.newRepositoryMovement(ctx, userA, repo.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		}

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.RepositoryMovements.TotalCount)
		assert.Len(t, data.RepositoryMovements.Edges, 2)
		assert.True(t, data.RepositoryMovements.PageInfo.HasNextPage)
		assert.NotNil(t, data.RepositoryMovements.PageInfo.EndCursor)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestRepositoryMovement_QueryWithFilters(t *testing.T) {
	t.Parallel()

	t.Run("filters by data key-value", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo1 := te.newRepository(ctx, userA).Name("repo-1").Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Name("repo-2").Parent(fromRepo.ID).NoData().Create()

		movement := te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).NoData().Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, map[string]any{
			"Where": `{ Data: ["type", "custom"] }`,
		})

		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, movement.ID, data.RepositoryMovements.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo1 := te.newRepository(ctx, userA).Name("repo-1").Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Name("repo-2").Parent(fromRepo.ID).NoData().Create()

		movement := te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).NoData().Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, map[string]any{
			"Where": `{ DataHasKey: "meta.name" }`,
		})

		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, movement.ID, data.RepositoryMovements.Edges[0].Node.ID)
	})

	t.Run("filters by dataContains", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo1 := te.newRepository(ctx, userA).Name("repo-1").Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Name("repo-2").Parent(fromRepo.ID).NoData().Create()

		movement := te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).NoData().Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, map[string]any{
			"Where": `{ DataContains: ["meta.tags", "bar"] }`,
		})

		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, movement.ID, data.RepositoryMovements.Edges[0].Node.ID)
	})

	t.Run("null filters return all", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).Name("from-repo").NoData().Create()
		toRepo := te.newRepository(ctx, userA).Name("to-repo").NoData().Create()
		repo1 := te.newRepository(ctx, userA).Name("repo-1").Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Name("repo-2").Parent(fromRepo.ID).NoData().Create()

		te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).NoData().Create()

		cases := []struct {
			name  string
			where string
		}{
			{"Data null", `{ Data: null }`},
			{"DataHasKey null", `{ DataHasKey: null }`},
			{"DataIn null", `{ DataIn: null }`},
			{"DataContains null", `{ DataContains: null }`},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest // Subtests share test environment
				data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovements, map[string]any{
					"Where": tc.where,
				})
				assert.Equal(t, 2, data.RepositoryMovements.TotalCount, "Case: %s", tc.name)
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestRepositoryMovement_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).NoData().Create()
		toRepo := te.newRepository(ctx, userA).NoData().Create()
		repo1 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()
		repo3 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()

		m1 := te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"priority": float64(30)}).Create()
		m2 := te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"priority": float64(10)}).Create()
		m3 := te.newRepositoryMovement(ctx, userA, repo3.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovementsJSONOrder, map[string]any{"JSONPath": "priority"})
		require.Equal(t, 3, data.RepositoryMovements.TotalCount)
		assert.Equal(t, m2.ID, data.RepositoryMovements.Edges[0].Node.ID)
		assert.Equal(t, m3.ID, data.RepositoryMovements.Edges[1].Node.ID)
		assert.Equal(t, m1.ID, data.RepositoryMovements.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).NoData().Create()
		toRepo := te.newRepository(ctx, userA).NoData().Create()
		repo1 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()

		m1 := te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"meta": map[string]any{"weight": float64(10)}}).Create()
		m2 := te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"meta": map[string]any{"weight": float64(30)}}).Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovementsJSONOrder, map[string]any{"JSONPath": "meta.weight", "Direction": "DESC"})
		require.Equal(t, 2, data.RepositoryMovements.TotalCount)
		assert.Equal(t, m2.ID, data.RepositoryMovements.Edges[0].Node.ID)
		assert.Equal(t, m1.ID, data.RepositoryMovements.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).NoData().Create()
		toRepo := te.newRepository(ctx, userA).NoData().Create()
		repo1 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()

		m1 := te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Create()
		m2 := te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovementsJSONOrder, map[string]any{"Field": "CREATED_AT", "Direction": "DESC"})
		require.Equal(t, 2, data.RepositoryMovements.TotalCount)
		assert.Equal(t, m2.ID, data.RepositoryMovements.Edges[0].Node.ID)
		assert.Equal(t, m1.ID, data.RepositoryMovements.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestRepositoryMovement_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).NoData().Create()
		toRepo := te.newRepository(ctx, userA).NoData().Create()
		repo1 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()

		te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"type": "alpha"}).Create()
		m2 := te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovementsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})
		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, m2.ID, data.RepositoryMovements.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fromRepo := te.newRepository(ctx, userA).NoData().Create()
		toRepo := te.newRepository(ctx, userA).NoData().Create()
		repo1 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()
		repo2 := te.newRepository(ctx, userA).Parent(fromRepo.ID).NoData().Create()

		te.newRepositoryMovement(ctx, userA, repo1.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"type": "alpha"}).Create()
		m2 := te.newRepositoryMovement(ctx, userA, repo2.ID, toRepo.ID).FromID(fromRepo.ID).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryRepositoryMovementsData](te, ctx, queryRepositoryMovementsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})
		require.Equal(t, 1, data.RepositoryMovements.TotalCount)
		assert.Equal(t, m2.ID, data.RepositoryMovements.Edges[0].Node.ID)
	})
}
