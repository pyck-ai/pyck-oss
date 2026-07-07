package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createRepository = testresolver.ParseTemplate(`mutation {
		createInventoryRepository(input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			name: "{{.Name}}",
			type: {{.Type}},
			{{if .VirtualRepo}}virtualRepo: {{.VirtualRepo}},{{end}}
			{{if .ParentID}}parentID: "{{.ParentID}}",{{end}}
			{{if .Data}}data: {{.Data}}{{end}}
		}) {
			inventoryRepository { id tenantID dataTypeID name type parentID data }
		}
	}`)

	updateRepository = testresolver.ParseTemplate(`mutation {
		updateInventoryRepository(id: "{{.ID}}", input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Name}}name: "{{.Name}}",{{end}}
			{{if .Type}}type: {{.Type}},{{end}}
			{{if .Data}}data: {{.Data}}{{end}}
		}) {
			inventoryRepository {
				id tenantID dataTypeID name type parentID data
				createdAt createdBy updatedAt updatedBy
			}
		}
	}`)

	deleteRepository = testresolver.ParseTemplate(`mutation {
		deleteInventoryRepository(id: "{{.ID}}") { deletedID }
	}`)

	queryRepositories = testresolver.ParseTemplate(`query {
		repositories(
			{{if .First}}first: {{.First}},{{end}}
			{{if .After}}after: {{.After}},{{end}}
			{{if .OrderBy}}orderBy: {{.OrderBy}},{{end}}
			{{if .Where}}where: {{.Where}}{{else}}where: null{{end}}
		) {
			totalCount
			edges {
				node { id tenantID dataTypeID name type parentID data {{if .IncludeCreatedAt}}createdAt{{end}} }
				cursor
			}
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)

	queryRepositoriesJSONOrder = testresolver.ParseTemplate(`query {
		repositories(
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
			edges { node { id tenantID dataTypeID name type parentID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type repositoryNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	DataTypeID uuid.UUID
	Name       string
	Type       string
	ParentID   uuid.UUID
	Data       map[string]any
}

type createRepositoryData struct {
	CreateInventoryRepository struct{ InventoryRepository repositoryNode }
}

type updateRepositoryData struct {
	UpdateInventoryRepository struct{ InventoryRepository repositoryNode }
}

type deleteRepositoryData struct {
	DeleteInventoryRepository struct{ DeletedID uuid.UUID }
}

type queryRepositoriesData struct {
	Repositories struct {
		TotalCount int
		Edges      []struct {
			Node   repositoryNode
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
// TEST DATA
// =============================================================================

var (
	testRepositoryDataGQL = `{
		type: "custom",
		sum: 15,
		meta: { name: "Testrepository", weight: 50, tags: ["test", "foobar"] },
		available: true
	}`

	testRepositoryDataGQLInvalid = `{ type2: "custom" }`

	testRepositoryDataGQLBadWeight = `{
		type: "custom",
		sum: 15,
		meta: { name: "Testrepository2", weight: -50, tags: ["test", "foobar"] }
	}`

	testRepositoryDataGQLBadSum = `{
		type: "custom",
		sum: -15,
		meta: { name: "Testrepository2", weight: 50, tags: ["test", "foobar"] }
	}`

	testRepositoryDataGQLName2 = `{
		type: "custom",
		sum: 15,
		meta: { name: "Testrepository2", weight: 50, tags: ["test", "foobar"] }
	}`
)

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestRepository_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates repository with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createRepositoryData](te, ctx, createRepository, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Name":       "test-repository-1",
			"Type":       "static",
			"Data":       testRepositoryDataGQL,
		})

		created := data.CreateInventoryRepository.InventoryRepository
		assert.NotEqual(t, uuid.Nil, created.ID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)
		assert.Contains(t, created.Name, "test-repository-")
		assert.Equal(t, "static", created.Type)
		assert.NotNil(t, created.Data)

		te.assertEvents(ctx, Create("repository", created.ID))
	})

	t.Run("rejects invalid data schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userB)

		execErr(te, ctx, createRepository, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Name":       "test-repository-3",
			"Type":       "static",
			"Data":       testRepositoryDataGQLInvalid,
		}, "jsonschema")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects data that fails validation - negative weight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createRepository, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Name":       "test-repository-4",
			"Type":       "static",
			"Data":       testRepositoryDataGQLBadWeight,
		}, "/meta/weight")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects virtual child of physical parent", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create physical parent repository
		parentRepo := te.newRepository(ctx, userA).
			Name("parent-repo-1").
			TypeStatic().
			Virtual(false).
			Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createRepository, map[string]any{
			"DataTypeID":  itemDataTypeID,
			"Name":        "test-repository-5",
			"Type":        "static",
			"VirtualRepo": true,
			"ParentID":    parentRepo.ID,
			"Data":        testRepositoryDataGQL,
		}, "the repo child must have the same virtualRepo flag as the parent")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects duplicate unique data field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create first repository with unique name
		te.newRepository(ctx, userA).
			Name("unique-name-1").
			TypeDynamic().
			DataTypeID(itemDataTypeIDUniqueName).
			Create()
		te.clearEvents(ctx)

		// Try to create another with same unique field
		execErr(te, ctx, createRepository, map[string]any{
			"DataTypeID": itemDataTypeIDUniqueName,
			"Name":       "test-repository-6",
			"Type":       "static",
			"Data":       testRepositoryDataGQL,
		}, "field value unique constraint violated")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestRepository_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates repository name", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).
			Name("test-repo-update-1").
			TypeDynamic().
			Create()
		te.clearEvents(ctx)

		newName := "updated-name"
		data := execOK[updateRepositoryData](te, ctx, updateRepository, map[string]any{
			"ID":         repo.ID,
			"DataTypeID": repo.DataTypeID,
			"Name":       newName,
			"Data":       testRepositoryDataGQLName2,
		})

		updated := data.UpdateInventoryRepository.InventoryRepository
		assert.Equal(t, repo.ID, updated.ID)
		assert.Equal(t, newName, updated.Name)
		assert.Equal(t, tenantA, updated.TenantID)

		te.assertEvents(ctx, Update("repository", repo.ID))
	})

	t.Run("rejects update of other tenant's repository", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create repository with tenant B
		ctxB := te.ctx(userB)
		repo := te.newRepository(ctxB, userB).
			Name("test-repo-update-3").
			TypeDynamic().
			Create()
		te.clearEvents(ctxB)

		// Try to update with tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateRepository, map[string]any{
			"ID":         repo.ID,
			"DataTypeID": repo.DataTypeID,
			"Name":       "new-name",
		}, "repository not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects update with invalid data schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).
			Name("test-repo-update-4").
			TypeDynamic().
			Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateRepository, map[string]any{
			"ID":         repo.ID,
			"DataTypeID": repo.DataTypeID,
			"Name":       repo.Name,
			"Data":       testRepositoryDataGQLInvalid,
		}, "missing properties")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with data that fails validation - negative sum", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).
			Name("test-repo-update-5").
			TypeDynamic().
			Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateRepository, map[string]any{
			"ID":         repo.ID,
			"DataTypeID": repo.DataTypeID,
			"Name":       repo.Name,
			"Type":       "static",
			"Data":       testRepositoryDataGQLBadSum,
		}, "/sum")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestRepository_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes repository", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).
			Name("test-repo-delete-1").
			TypeDynamic().
			Create()
		te.clearEvents(ctx)

		data := execOK[deleteRepositoryData](te, ctx, deleteRepository, map[string]any{
			"ID": repo.ID,
		})

		assert.Equal(t, repo.ID, data.DeleteInventoryRepository.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.Repository.Get(te.ctxWithDeleted(userA), repo.ID)
		require.NoError(t, err)
		assert.False(t, deleted.DeletedAt.IsZero())
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("repository", repo.ID))
	})

	t.Run("rejects delete of other tenant's repository", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create repository with tenant B
		ctxB := te.ctx(userB)
		repo := te.newRepository(ctxB, userB).
			Name("test-repo-delete-tenant-2").
			TypeDynamic().
			Create()
		te.clearEvents(ctxB)

		// Try to delete with tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteRepository, map[string]any{
			"ID": repo.ID,
		}, "repository not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete when repository has stock", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).
			Name("test-repo-delete-3").
			TypeDynamic().
			Create()

		item := te.newItem(ctx, userA).
			Sku("test-item").
			Create()

		te.newStock(ctx, userA, item.ID, repo.ID).
			Quantity(1).
			Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteRepository, map[string]any{
			"ID": repo.ID,
		}, "unable to remove repository as long as there are items in stock")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete when repository has children", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create parent repository
		parentRepo := te.newRepository(ctx, userA).
			Name("test-repo-delete-parent").
			TypeDynamic().
			Create()

		// Create child repository
		te.newRepository(ctx, userA).
			Name("child-repo").
			TypeDynamic().
			Parent(parentRepo.ID).
			Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteRepository, map[string]any{
			"ID": parentRepo.ID,
		}, "unable to remove repository as long as it has children")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestRepository_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryRepositoriesData](te, ctx, queryRepositories, nil)

		assert.Equal(t, 0, data.Repositories.TotalCount)
		assert.Empty(t, data.Repositories.Edges)
		assert.False(t, data.Repositories.PageInfo.HasNextPage)
		assert.False(t, data.Repositories.PageInfo.HasPreviousPage)
		assert.Nil(t, data.Repositories.PageInfo.StartCursor)
		assert.Nil(t, data.Repositories.PageInfo.EndCursor)
	})

	t.Run("returns single repository", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).
			Name("test-query-repo-1").
			TypeDynamic().
			Create()

		data := execOK[queryRepositoriesData](te, ctx, queryRepositories, nil)

		assert.Equal(t, 1, data.Repositories.TotalCount)
		require.Len(t, data.Repositories.Edges, 1)
		assert.False(t, data.Repositories.PageInfo.HasNextPage)
		assert.False(t, data.Repositories.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.Repositories.PageInfo.StartCursor)
		assert.NotNil(t, data.Repositories.PageInfo.EndCursor)
		assert.Equal(t, repo.ID, data.Repositories.Edges[0].Node.ID)
		assert.Equal(t, repo.TenantID, data.Repositories.Edges[0].Node.TenantID)
		assert.Equal(t, repo.Name, data.Repositories.Edges[0].Node.Name)
		assert.NotNil(t, data.Repositories.Edges[0].Node.Data)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestRepository_QueryWithFilters(t *testing.T) {
	t.Parallel()

	te := setup(t)
	ctx := te.ctx(userA)

	// Create test repositories
	repoWithData := te.newRepository(ctx, userA).
		Name("test-repo-with-data").
		TypeDynamic().
		Create()

	repoNoData := te.newRepository(ctx, userA).
		Name("test-repo-no-data").
		TypeDynamic().
		NoData().
		Create()

	deletedRepo := te.newRepository(ctx, userA).
		Name("test-repo-deleted").
		TypeDynamic().
		NoData().
		Deleted().
		Create()

	// Create repository for other tenant (should not appear in results)
	ctxB := te.ctx(userB)
	te.newRepository(ctxB, userB).
		Name("test-repo-other-tenant").
		TypeDynamic().
		Create()

	t.Run("returns only non-deleted repositories", func(t *testing.T) {
		t.Parallel()

		data := execOK[queryRepositoriesData](te, ctx, queryRepositories, map[string]any{
			"First":   20,
			"OrderBy": "{ direction: ASC, field: CREATED_AT }",
		})

		assert.Equal(t, 2, data.Repositories.TotalCount)
		require.Len(t, data.Repositories.Edges, 2)
		assert.Equal(t, repoWithData.ID, data.Repositories.Edges[0].Node.ID)
		assert.Equal(t, repoNoData.ID, data.Repositories.Edges[1].Node.ID)
	})

	t.Run("includes deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()

		ctxDeleted := te.ctxWithDeleted(userA)

		data := execOK[queryRepositoriesData](te, ctxDeleted, queryRepositories, map[string]any{
			"First":   20,
			"OrderBy": "{ direction: ASC, field: CREATED_AT }",
		})

		assert.Equal(t, 3, data.Repositories.TotalCount)
		require.Len(t, data.Repositories.Edges, 3)
		assert.Equal(t, repoWithData.ID, data.Repositories.Edges[0].Node.ID)
		assert.Equal(t, repoNoData.ID, data.Repositories.Edges[1].Node.ID)
		assert.Equal(t, deletedRepo.ID, data.Repositories.Edges[2].Node.ID)
	})

	t.Run("filters by data fields", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			desc   string
			filter string
			count  int
			nodeID uuid.UUID
		}{
			{
				desc:   "Data key-value",
				filter: `{ Data: ["type", "custom"] }`,
				count:  1,
				nodeID: repoWithData.ID,
			},
			{
				desc:   "DataHasKey",
				filter: `{ DataHasKey: "meta.name" }`,
				count:  1,
				nodeID: repoWithData.ID,
			},
			{
				desc:   "DataIn",
				filter: `{ DataIn: ["meta.name", "TestItem", "foo"] }`,
				count:  1,
				nodeID: repoWithData.ID,
			},
			{
				desc:   "DataContains",
				filter: `{ DataContains: ["meta.tags", "bar"] }`,
				count:  1,
				nodeID: repoWithData.ID,
			},
			{
				desc:   "Data null returns all",
				filter: `{ Data: null }`,
				count:  2,
				nodeID: repoWithData.ID,
			},
			{
				desc:   "DataHasKey null returns all",
				filter: `{ DataHasKey: null }`,
				count:  2,
				nodeID: repoWithData.ID,
			},
			{
				desc:   "DataIn null returns all",
				filter: `{ DataIn: null }`,
				count:  2,
				nodeID: repoWithData.ID,
			},
			{
				desc:   "DataContains null returns all",
				filter: `{ DataContains: null }`,
				count:  2,
				nodeID: repoWithData.ID,
			},
		}

		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) {
				t.Parallel()

				data := execOK[queryRepositoriesData](te, ctx, queryRepositories, map[string]any{
					"First":   20,
					"OrderBy": "{ direction: ASC, field: CREATED_AT }",
					"Where":   tc.filter,
				})

				assert.Equal(t, tc.count, data.Repositories.TotalCount, "Test case: %s", tc.desc)
				require.Len(t, data.Repositories.Edges, tc.count)
				if tc.count > 0 {
					assert.Equal(t, tc.nodeID, data.Repositories.Edges[0].Node.ID)
					assert.Equal(t, tenantA, data.Repositories.Edges[0].Node.TenantID)
				}
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestRepository_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		r1 := te.newRepository(ctx, userA).Data(map[string]any{"priority": float64(30)}).Create()
		r2 := te.newRepository(ctx, userA).Data(map[string]any{"priority": float64(10)}).Create()
		r3 := te.newRepository(ctx, userA).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryRepositoriesData](te, ctx, queryRepositoriesJSONOrder, map[string]any{"JSONPath": "priority"})
		require.Equal(t, 3, data.Repositories.TotalCount)
		assert.Equal(t, r2.ID, data.Repositories.Edges[0].Node.ID)
		assert.Equal(t, r3.ID, data.Repositories.Edges[1].Node.ID)
		assert.Equal(t, r1.ID, data.Repositories.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		r1 := te.newRepository(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(10)}}).Create()
		r2 := te.newRepository(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(30)}}).Create()

		data := execOK[queryRepositoriesData](te, ctx, queryRepositoriesJSONOrder, map[string]any{"JSONPath": "meta.weight", "Direction": "DESC"})
		require.Equal(t, 2, data.Repositories.TotalCount)
		assert.Equal(t, r2.ID, data.Repositories.Edges[0].Node.ID)
		assert.Equal(t, r1.ID, data.Repositories.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		r1 := te.newRepository(ctx, userA).Create()
		r2 := te.newRepository(ctx, userA).Create()

		data := execOK[queryRepositoriesData](te, ctx, queryRepositoriesJSONOrder, map[string]any{"Field": "CREATED_AT", "Direction": "DESC"})
		require.Equal(t, 2, data.Repositories.TotalCount)
		assert.Equal(t, r2.ID, data.Repositories.Edges[0].Node.ID)
		assert.Equal(t, r1.ID, data.Repositories.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestRepository_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newRepository(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		r2 := te.newRepository(ctx, userA).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryRepositoriesData](te, ctx, queryRepositoriesJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})
		require.Equal(t, 1, data.Repositories.TotalCount)
		assert.Equal(t, r2.ID, data.Repositories.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newRepository(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		r2 := te.newRepository(ctx, userA).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryRepositoriesData](te, ctx, queryRepositoriesJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})
		require.Equal(t, 1, data.Repositories.TotalCount)
		assert.Equal(t, r2.ID, data.Repositories.Edges[0].Node.ID)
	})
}
