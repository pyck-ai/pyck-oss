package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createLocation = resolver.ParseTemplate(`mutation {
		createLocation(input: {
			name: "{{.Name}}"
		}) {
			location { id tenantID name }
		}
	}`)

	createLocationWithData = resolver.ParseTemplate(`mutation {
		createLocation(input: {
			name: "{{.Name}}",
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: { type: "custom", geo: { lat: 44, lng: 26 } }
		}) {
			location { id tenantID name }
		}
	}`)

	updateLocation = resolver.ParseTemplate(`mutation {
		updateLocation(
			id: "{{.ID}}",
			input: { name: "{{.Name}}" }
		) {
			location { id tenantID name }
		}
	}`)

	updateLocationWithData = resolver.ParseTemplate(`mutation {
		updateLocation(
			id: "{{.ID}}",
			input: {
				name: "{{.Name}}",
				{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
				data: { type: "custom", geo: { lat: 45, lng: 25 } }
			}
		) {
			location { id tenantID name }
		}
	}`)

	deleteLocation = resolver.ParseTemplate(`mutation {
		deleteLocation(id: "{{.ID}}") {
			deletedID
		}
	}`)

	queryLocationsJSONOrder = resolver.ParseTemplate(`query {
		locations(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id name data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type locationNode struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
}

type createLocationData struct {
	CreateLocation struct{ Location locationNode }
}

type updateLocationData struct {
	UpdateLocation struct{ Location locationNode }
}

type deleteLocationData struct {
	DeleteLocation struct{ DeletedID uuid.UUID }
}

type queryLocationsData struct {
	Locations struct {
		TotalCount int
		Edges      []struct {
			Node struct {
				ID   uuid.UUID
				Name string
				Data map[string]any
			}
		}
		PageInfo struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestLocation_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates location successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createLocationData](te, ctx, createLocation, map[string]any{
			"Name": "HQ",
		})

		created := data.CreateLocation.Location
		assert.Equal(t, "HQ", created.Name)
		assert.Equal(t, resolver.TenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.Location.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "HQ", stored.Name)

		// Verify event
		te.assertEvents(ctx, Create("location", created.ID))
	})

	t.Run("rejects create with data but missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createLocationWithData, map[string]any{
			"Name": "HQ",
		}, "data type not set")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestLocation_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates location name", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		loc := te.newLocation(ctx, userA).Name("HQ").Create()
		te.clearEvents(ctx)

		data := execOK[updateLocationData](te, ctx, updateLocation, map[string]any{
			"ID":   loc.ID,
			"Name": "HQ-Updated",
		})

		assert.Equal(t, "HQ-Updated", data.UpdateLocation.Location.Name)
		te.assertEvents(ctx, Update("location", loc.ID))
	})

	t.Run("rejects update of other tenant's location", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		loc := te.newLocation(ctxB, userB).Name("HQ").Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateLocation, map[string]any{
			"ID":   loc.ID,
			"Name": "HACKED",
		}, "location not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects update with data but missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		loc := te.newLocation(ctx, userA).Name("HQ").Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateLocationWithData, map[string]any{
			"ID":   loc.ID,
			"Name": "HQ-Updated",
		}, "data type not set")

		// Verify not updated
		stored, err := te.Ent.Location.Get(ctx, loc.ID)
		require.NoError(t, err)
		assert.Equal(t, "HQ", stored.Name)

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of non-existent location", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, updateLocation, map[string]any{
			"ID":   uuid.New(),
			"Name": "X",
		}, "location not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestLocation_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes location", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		loc := te.newLocation(ctx, userA).Name("HQ").Create()
		te.clearEvents(ctx)

		data := execOK[deleteLocationData](te, ctx, deleteLocation, map[string]any{
			"ID": loc.ID,
		})

		assert.Equal(t, loc.ID, data.DeleteLocation.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.Location.Get(te.ctxWithDeleted(userA), loc.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("location", loc.ID))
	})

	t.Run("rejects delete of other tenant's location", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		loc := te.newLocation(ctxB, userB).Name("HQ").Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteLocation, map[string]any{
			"ID": loc.ID,
		}, "location not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of non-existent location", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteLocation, map[string]any{
			"ID": uuid.New(),
		}, "location not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestLocation_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		l1 := te.newLocation(ctx, userA).Data(map[string]any{"priority": float64(30)}).Create()
		l2 := te.newLocation(ctx, userA).Data(map[string]any{"priority": float64(10)}).Create()
		l3 := te.newLocation(ctx, userA).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryLocationsData](te, ctx, queryLocationsJSONOrder, map[string]any{
			"JSONPath": "priority",
		})

		require.Equal(t, 3, data.Locations.TotalCount)
		assert.Equal(t, l2.ID, data.Locations.Edges[0].Node.ID)
		assert.Equal(t, l3.ID, data.Locations.Edges[1].Node.ID)
		assert.Equal(t, l1.ID, data.Locations.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		l1 := te.newLocation(ctx, userA).Data(map[string]any{
			"geo": map[string]any{"lat": float64(10)},
		}).Create()
		l2 := te.newLocation(ctx, userA).Data(map[string]any{
			"geo": map[string]any{"lat": float64(30)},
		}).Create()

		data := execOK[queryLocationsData](te, ctx, queryLocationsJSONOrder, map[string]any{
			"JSONPath":  "geo.lat",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Locations.TotalCount)
		assert.Equal(t, l2.ID, data.Locations.Edges[0].Node.ID)
		assert.Equal(t, l1.ID, data.Locations.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		l1 := te.newLocation(ctx, userA).Create()
		l2 := te.newLocation(ctx, userA).Create()

		data := execOK[queryLocationsData](te, ctx, queryLocationsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Locations.TotalCount)
		assert.Equal(t, l2.ID, data.Locations.Edges[0].Node.ID)
		assert.Equal(t, l1.ID, data.Locations.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestLocation_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newLocation(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		l2 := te.newLocation(ctx, userA).Data(map[string]any{"type": "beta"}).Create()

		data := execOK[queryLocationsData](te, ctx, queryLocationsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "beta"] }`,
		})

		require.Equal(t, 1, data.Locations.TotalCount)
		assert.Equal(t, l2.ID, data.Locations.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newLocation(ctx, userA).Data(map[string]any{"type": "alpha"}).Create()
		l2 := te.newLocation(ctx, userA).Data(map[string]any{"type": "beta", "priority": float64(1)}).Create()

		data := execOK[queryLocationsData](te, ctx, queryLocationsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.Locations.TotalCount)
		assert.Equal(t, l2.ID, data.Locations.Edges[0].Node.ID)
	})
}
