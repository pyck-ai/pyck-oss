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
	createCollectionMovement = resolver.ParseTemplate(`mutation {
		createInventoryCollectionMovement(input: {
			dataTypeID: "{{.DataTypeID}}",
			collection: [
				{{range $i, $item := .Collection}}
				{{if $i}},{{end}}
				{
					{{if .ItemID}}itemID:"{{.ItemID}}",{{end}}
					{{if .RepositoryID}}repositoryID:"{{.RepositoryID}}",{{end}}
					{{if .FromID}}fromID:"{{.FromID}}",{{end}}
					{{if .ToID}}toID:"{{.ToID}}",{{end}}
					handler:"{{.Handler}}",
					{{if .Quantity}}quantity: {{.Quantity}},{{end}}
					{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
					{{if .Data}}data: {{.Data}}{{end}}
				}
				{{end}}
			]}) {
		id
		movements{
			id
			movementType
		}
	}
}`)

	updateCollectionMovement = resolver.ParseTemplate(`mutation {
		updateInventoryCollectionMovement(
			id: "{{.ID}}",
			input: {
				{{if .Handler}}handler: "{{.Handler}}"{{end}}
			}) {
			inventoryCollection{
				id
				handler
				tenantID
				createdAt
				createdBy
				updatedAt
				updatedBy
			}
		}
	}`)

	queryCollections = resolver.ParseTemplate(`query {
		inventoryCollections(
			{{if .First}}first: {{.First}},{{end}}
			{{if .After}}after: "{{.After}}",{{end}}
			{{if .OrderBy}}orderBy: {{.OrderBy}},{{end}}
			{{if .Where}}where: {{.Where}}{{else}}where: null{{end}}
		) {
			totalCount
			edges {
				node {
					id
					tenantID
					handler
					{{if .IncludeData}}dataTypeID
					data{{end}}
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

	queryCollectionsJSONOrder = resolver.ParseTemplate(`query {
		inventoryCollections(
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
			edges { node { id tenantID handler data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type collectionMovementItem struct {
	ItemID       *uuid.UUID
	RepositoryID *uuid.UUID
	FromID       uuid.UUID
	ToID         uuid.UUID
	Handler      string
	Quantity     *int64
	DataTypeID   *uuid.UUID
	Data         *string
}

type createCollectionMovementData struct {
	CreateInventoryCollectionMovement struct {
		ID        uuid.UUID
		Movements []struct {
			ID           uuid.UUID
			MovementType string
		}
	}
}

type updateCollectionMovementData struct {
	UpdateInventoryCollectionMovement struct {
		InventoryCollection struct {
			ID        uuid.UUID
			TenantID  uuid.UUID
			Handler   string
			CreatedAt string
			CreatedBy uuid.UUID
			UpdatedAt string
			UpdatedBy uuid.UUID
		}
	}
}

type collectionNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	DataTypeID uuid.UUID
	Handler    string
	Data       map[string]any
}

type queryCollectionsData struct {
	InventoryCollections struct {
		TotalCount int
		Edges      []struct {
			Node   collectionNode
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
// HELPER FUNCTIONS
// =============================================================================

// ptrUUID returns a pointer to a UUID.
func ptrUUID(u uuid.UUID) *uuid.UUID {
	return &u
}

// ptrInt64 returns a pointer to an int64.
func ptrInt64(i int64) *int64 {
	return &i
}

// ptrString returns a pointer to a string.
func ptrString(s string) *string {
	return &s
}

// setupRepositoryHierarchy creates a hierarchy of repositories for testing.
func setupRepositoryHierarchy(t *testing.T, te *testEnv) (map[string]uuid.UUID, uuid.UUID) {
	t.Helper()

	ctx := te.ctx(userA)
	repos := make(map[string]uuid.UUID)

	// Create repository hierarchy
	repo1 := te.newRepository(ctx, userA).Name("repo1").Create()
	repos["repo1"] = repo1.ID

	repo2 := te.newRepository(ctx, userA).Name("repo2").Parent(repo1.ID).Create()
	repos["repo2"] = repo2.ID

	repo3 := te.newRepository(ctx, userA).Name("repo3").Parent(repo1.ID).Create()
	repos["repo3"] = repo3.ID

	repo4 := te.newRepository(ctx, userA).Name("repo4").Parent(repo2.ID).Create()
	repos["repo4"] = repo4.ID

	repo5 := te.newRepository(ctx, userA).Name("repo5").Parent(repo3.ID).Create()
	repos["repo5"] = repo5.ID

	repo6 := te.newRepository(ctx, userA).Name("repo6").Parent(repo4.ID).Create()
	repos["repo6"] = repo6.ID

	repo7 := te.newRepository(ctx, userA).Name("repo7").Parent(repo5.ID).Create()
	repos["repo7"] = repo7.ID

	repo8 := te.newRepository(ctx, userA).Name("repo8").Parent(repo4.ID).Create()
	repos["repo8"] = repo8.ID

	repo9 := te.newRepository(ctx, userA).Name("repo9").Parent(repo4.ID).Create()
	repos["repo9"] = repo9.ID

	repo10 := te.newRepository(ctx, userA).Name("repo10").Parent(repo5.ID).Create()
	repos["repo10"] = repo10.ID

	repo11 := te.newRepository(ctx, userA).Name("repo11").Parent(repo2.ID).Create()
	repos["repo11"] = repo11.ID

	// Create item
	item := te.newItem(ctx, userA).Sku("TEST-COLLECTION-ITEM").Create()

	return repos, item.ID
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestCollectionMovement_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates collection movement with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		repos, itemID := setupRepositoryHierarchy(t, te)
		ctx := te.ctx(userA)

		// Create stock in repo11 for the item
		te.newStock(ctx, userA, itemID, repos["repo11"]).Quantity(20).Create()

		// Create stock in repo8 for the second movement
		te.newStock(ctx, userA, itemID, repos["repo8"]).Quantity(10).Create()
		te.clearEvents(ctx)

		dataStr := `{
			type: "custom",
			sum: 15,
			meta: {
				name: "TestitemMovement2",
				weight: 50,
				tags: ["test", "foobar"]
			}
		}`

		data := execOK[createCollectionMovementData](te, ctx, createCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Collection": []collectionMovementItem{
				{
					ItemID:     &itemID,
					FromID:     repos["repo11"],
					ToID:       repos["repo8"],
					Handler:    testHandler,
					Quantity:   ptrInt64(10),
					DataTypeID: ptrUUID(itemDataTypeID),
					Data:       ptrString(dataStr),
				},
				{
					ItemID:   &itemID,
					FromID:   repos["repo8"],
					ToID:     repos["repo10"],
					Handler:  testHandler,
					Quantity: ptrInt64(5),
				},
				{
					RepositoryID: ptrUUID(repos["repo7"]),
					FromID:       repos["repo5"],
					ToID:         repos["repo4"],
					Handler:      testHandler,
				},
			},
		})

		assert.NotEqual(t, uuid.Nil, data.CreateInventoryCollectionMovement.ID)
		for _, v := range data.CreateInventoryCollectionMovement.Movements {
			assert.NotEqual(t, uuid.Nil, v.ID)
		}

		te.assertEventCounts(ctx, map[string]int{
			"collection-movement": 1,
			"itemmovement":        2,
			"repositorymovement":  1,
			"stock":               21,
		})
	})

	t.Run("rejects collection movement with invalid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		repos, itemID := setupRepositoryHierarchy(t, te)
		ctx := te.ctx(userA)
		te.clearEvents(ctx)

		invalidData := `{type2: "custom"}`

		execErr(te, ctx, createCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Collection": []collectionMovementItem{
				{
					ItemID:     &itemID,
					FromID:     repos["repo11"],
					ToID:       repos["repo8"],
					Handler:    testHandler,
					Quantity:   ptrInt64(10),
					DataTypeID: ptrUUID(itemDataTypeID),
					Data:       ptrString(invalidData),
				},
				{
					ItemID:   &itemID,
					FromID:   repos["repo8"],
					ToID:     repos["repo10"],
					Handler:  testHandler,
					Quantity: ptrInt64(10),
				},
				{
					RepositoryID: ptrUUID(repos["repo10"]),
					FromID:       repos["repo5"],
					ToID:         repos["repo4"],
					Handler:      testHandler,
				},
			},
		}, "jsonschema")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects collection movement without repositoryID or itemID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		repos, _ := setupRepositoryHierarchy(t, te)
		ctx := te.ctx(userA)
		te.clearEvents(ctx)

		execErr(te, ctx, createCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Collection": []collectionMovementItem{
				{
					FromID:   repos["repo11"],
					ToID:     repos["repo8"],
					Handler:  testHandler,
					Quantity: ptrInt64(10),
				},
			},
		}, "repositoryID or itemID should be sent")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects collection movement with invalid quantity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		repos, itemID := setupRepositoryHierarchy(t, te)
		ctx := te.ctx(userA)
		te.clearEvents(ctx)

		execErr(te, ctx, createCollectionMovement, map[string]any{
			"DataTypeID": itemDataTypeID,
			"Collection": []collectionMovementItem{
				{
					ItemID:  &itemID,
					FromID:  repos["repo11"],
					ToID:    repos["repo8"],
					Handler: testHandler,
					// No quantity specified
				},
			},
		}, "invalid quantity")

		te.assertNoEvents(ctx)
	})
}

// TestCollectionMovement_CreateFromVirtualRepo verifies that a collection
// movement from a virtual source repository succeeds without requiring stock,
// even when the virtual repo is created after enough other repos to push it
// beyond the 200-row default limit of GetRepositoriesDetails.
func TestCollectionMovement_CreateFromVirtualRepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		noiseCount int
	}{
		{"few repos (below limit)", 3},
		{"many repos (above limit)", 300},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			ctx := te.ctx(userA)

			// Create noise repos first so the virtual source and dest repos
			// are inserted after them. In the 300-repo case this pushes them
			// beyond the 200-row default limit of GetRepositoriesDetails,
			// reproducing the bug where VirtualRepo defaults to false.
			for range tc.noiseCount {
				te.newRepository(ctx, userA).Create()
			}

			src := te.newRepository(ctx, userA).Virtual(true).Create()
			dst := te.newRepository(ctx, userA).Create()
			item := te.newItem(ctx, userA).Create()
			te.clearEvents(ctx)

			data := execOK[createCollectionMovementData](te, ctx, createCollectionMovement, map[string]any{
				"DataTypeID": itemDataTypeID,
				"Collection": []collectionMovementItem{
					{
						ItemID:   &item.ID,
						FromID:   src.ID,
						ToID:     dst.ID,
						Handler:  testHandler,
						Quantity: ptrInt64(1),
					},
				},
			})

			assert.NotEqual(t, uuid.Nil, data.CreateInventoryCollectionMovement.ID)
			assert.Len(t, data.CreateInventoryCollectionMovement.Movements, 1)
		})
	}
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestCollectionMovement_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates collection movement handler", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		cm := te.newCollectionMovement(ctx, userA).Handler("Handler123").Create()
		te.clearEvents(ctx)

		data := execOK[updateCollectionMovementData](te, ctx, updateCollectionMovement, map[string]any{
			"ID":      cm.ID,
			"Handler": "Handler321",
		})

		assert.Equal(t, tenantA, data.UpdateInventoryCollectionMovement.InventoryCollection.TenantID)
		assert.Equal(t, "Handler321", data.UpdateInventoryCollectionMovement.InventoryCollection.Handler)

		te.assertEvents(ctx, Update("collection-movement", cm.ID))
	})

	t.Run("rejects update of other tenant's collection movement", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		cm := te.newCollectionMovement(ctxB, userB).Handler("Handler123").Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateCollectionMovement, map[string]any{
			"ID":      cm.ID,
			"Handler": "Handler321",
		}, "collection_movement not found")

		te.assertNoEvents(ctxA)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestCollectionMovement_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryCollectionsData](te, ctx, queryCollections, nil)

		assert.Zero(t, data.InventoryCollections.TotalCount)
		assert.Empty(t, data.InventoryCollections.Edges)
		assert.False(t, data.InventoryCollections.PageInfo.HasNextPage)
		assert.False(t, data.InventoryCollections.PageInfo.HasPreviousPage)
		assert.Nil(t, data.InventoryCollections.PageInfo.StartCursor)
		assert.Nil(t, data.InventoryCollections.PageInfo.EndCursor)
	})

	t.Run("returns only own tenant's collections", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		collection := te.newCollectionMovement(ctxA, userA).Handler("Handler").Create()

		data := execOK[queryCollectionsData](te, ctxA, queryCollections, nil)

		assert.Equal(t, 1, data.InventoryCollections.TotalCount)
		assert.False(t, data.InventoryCollections.PageInfo.HasNextPage)
		assert.False(t, data.InventoryCollections.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.InventoryCollections.PageInfo.StartCursor)
		assert.NotNil(t, data.InventoryCollections.PageInfo.EndCursor)

		require.Len(t, data.InventoryCollections.Edges, 1)
		assert.Equal(t, collection.ID, data.InventoryCollections.Edges[0].Node.ID)
		assert.Equal(t, tenantA, data.InventoryCollections.Edges[0].Node.TenantID)
		assert.Equal(t, collection.Handler, data.InventoryCollections.Edges[0].Node.Handler)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestCollectionMovement_QueryWithFilters(t *testing.T) {
	t.Parallel()

	te := setup(t)
	t.Cleanup(func() { te.Close(t) })

	// Create test collections
	ctxA := te.ctx(userA)
	collectionTenant1 := te.newCollectionMovement(ctxA, userA).Handler("Handler1").Create()

	// Create deleted collection (won't show without showDeleted)
	te.newCollectionMovement(ctxA, userA).Handler("Handler2").Deleted().Create()

	// Create collection from other tenant (shouldn't appear in results)
	ctxB := te.ctx(userB)
	te.newCollectionMovement(ctxB, userB).Handler("Handler").Create()

	t.Run("returns all without filters", func(t *testing.T) {
		t.Parallel()

		data := execOK[queryCollectionsData](te, ctxA, queryCollections, map[string]any{
			"First":   20,
			"OrderBy": "{ direction: ASC, field: CREATED_AT }",
			"Where":   "null",
		})

		assert.Equal(t, 1, data.InventoryCollections.TotalCount)
		assert.Len(t, data.InventoryCollections.Edges, 1)
		assert.False(t, data.InventoryCollections.PageInfo.HasNextPage)
		assert.False(t, data.InventoryCollections.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.InventoryCollections.PageInfo.StartCursor)
		assert.NotNil(t, data.InventoryCollections.PageInfo.EndCursor)

		// Check for the right tenant1 collection
		resItem1 := data.InventoryCollections.Edges[0].Node
		assert.Equal(t, collectionTenant1.ID, resItem1.ID)
		assert.Equal(t, tenantA, resItem1.TenantID)
	})

	t.Run("filters with showDeleted parameter", func(t *testing.T) {
		t.Parallel()

		// Note: Without the showDeleted feature flag, only non-deleted collections are returned
		data := execOK[queryCollectionsData](te, ctxA, queryCollections, map[string]any{
			"First":   20,
			"OrderBy": "{ direction: ASC, field: CREATED_AT }",
			"Where":   "null",
		})

		assert.Equal(t, 1, data.InventoryCollections.TotalCount)
		assert.Len(t, data.InventoryCollections.Edges, 1)
		assert.False(t, data.InventoryCollections.PageInfo.HasNextPage)
		assert.False(t, data.InventoryCollections.PageInfo.HasPreviousPage)
		assert.NotNil(t, data.InventoryCollections.PageInfo.StartCursor)
		assert.NotNil(t, data.InventoryCollections.PageInfo.EndCursor)

		// Check for the right tenant1 collection (non-deleted)
		resCollection1 := data.InventoryCollections.Edges[0].Node
		assert.Equal(t, collectionTenant1.ID, resCollection1.ID)
		assert.Equal(t, tenantA, resCollection1.TenantID)
	})

	t.Run("filters table tests", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			desc   string
			filter string
			count  int
		}{
			{
				desc: "handlerIn filter",
				filter: `{
					handlerIn: ["Handler1", "Handler2"],
				}`,
				count: 1,
			},
		}

		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) {
				t.Parallel()

				data := execOK[queryCollectionsData](te, ctxA, queryCollections, map[string]any{
					"First":   20,
					"OrderBy": "{ direction: ASC, field: CREATED_AT }",
					"Where":   tc.filter,
				})

				assert.Equal(t, tc.count, data.InventoryCollections.TotalCount, "Case: %s", tc.desc)
				assert.Len(t, data.InventoryCollections.Edges, tc.count, "Case: %s", tc.desc)
				assert.False(t, data.InventoryCollections.PageInfo.HasNextPage)
				assert.False(t, data.InventoryCollections.PageInfo.HasPreviousPage)
				assert.NotNil(t, data.InventoryCollections.PageInfo.StartCursor)
				assert.NotNil(t, data.InventoryCollections.PageInfo.EndCursor)

				// Check for the right tenant1 collection
				resItem := data.InventoryCollections.Edges[0].Node
				assert.Equal(t, collectionTenant1.ID, resItem.ID, "Case: %s", tc.desc)
				assert.Equal(t, tenantA, resItem.TenantID, "Case: %s", tc.desc)
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestCollectionMovement_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		c1 := te.newCollectionMovement(ctx, userA).Data(map[string]any{"priority": float64(30)}).Create()
		c2 := te.newCollectionMovement(ctx, userA).Data(map[string]any{"priority": float64(10)}).Create()
		c3 := te.newCollectionMovement(ctx, userA).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryCollectionsData](te, ctx, queryCollectionsJSONOrder, map[string]any{"JSONPath": "priority"})
		require.Equal(t, 3, data.InventoryCollections.TotalCount)
		assert.Equal(t, c2.ID, data.InventoryCollections.Edges[0].Node.ID)
		assert.Equal(t, c3.ID, data.InventoryCollections.Edges[1].Node.ID)
		assert.Equal(t, c1.ID, data.InventoryCollections.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		c1 := te.newCollectionMovement(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(10)}}).Create()
		c2 := te.newCollectionMovement(ctx, userA).Data(map[string]any{"meta": map[string]any{"weight": float64(30)}}).Create()

		data := execOK[queryCollectionsData](te, ctx, queryCollectionsJSONOrder, map[string]any{"JSONPath": "meta.weight", "Direction": "DESC"})
		require.Equal(t, 2, data.InventoryCollections.TotalCount)
		assert.Equal(t, c2.ID, data.InventoryCollections.Edges[0].Node.ID)
		assert.Equal(t, c1.ID, data.InventoryCollections.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		c1 := te.newCollectionMovement(ctx, userA).Create()
		c2 := te.newCollectionMovement(ctx, userA).Create()

		data := execOK[queryCollectionsData](te, ctx, queryCollectionsJSONOrder, map[string]any{"Field": "CREATED_AT", "Direction": "DESC"})
		require.Equal(t, 2, data.InventoryCollections.TotalCount)
		assert.Equal(t, c2.ID, data.InventoryCollections.Edges[0].Node.ID)
		assert.Equal(t, c1.ID, data.InventoryCollections.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestCollectionMovement_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newCollectionMovement(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		c2 := te.newCollectionMovement(ctx, userA).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryCollectionsData](te, ctx, queryCollectionsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})
		require.Equal(t, 1, data.InventoryCollections.TotalCount)
		assert.Equal(t, c2.ID, data.InventoryCollections.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newCollectionMovement(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		c2 := te.newCollectionMovement(ctx, userA).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryCollectionsData](te, ctx, queryCollectionsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})
		require.Equal(t, 1, data.InventoryCollections.TotalCount)
		assert.Equal(t, c2.ID, data.InventoryCollections.Edges[0].Node.ID)
	})
}
