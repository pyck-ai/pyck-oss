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
	createSupplier = resolver.ParseTemplate(`mutation {
		createSupplier(input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestSupplier"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			id
			tenantID
			dataTypeID
			data
		}
	}`)

	updateSupplier = resolver.ParseTemplate(`mutation {
		updateSupplier(id: "{{.ID}}", input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestSupplier"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			id
			tenantID
			dataTypeID
			data
		}
	}`)

	deleteSupplier = resolver.ParseTemplate(`mutation {
		deleteSupplier(id: "{{.ID}}") { deletedID }
	}`)

	querySuppliers = resolver.ParseTemplate(`query {
		suppliers(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID dataTypeID data } }
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)

	querySuppliersJSONOrder = resolver.ParseTemplate(`query {
		suppliers(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
		) {
			totalCount
			edges { node { id tenantID dataTypeID data } }
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type supplierNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	DataTypeID uuid.UUID
	Data       map[string]any
}

type createSupplierData struct {
	CreateSupplier supplierNode
}

type updateSupplierData struct {
	UpdateSupplier supplierNode
}

type deleteSupplierData struct {
	DeleteSupplier struct{ DeletedID uuid.UUID }
}

type querySuppliersData struct {
	Suppliers struct {
		TotalCount int
		Edges      []struct{ Node supplierNode }
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

func TestSupplier_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates supplier with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createSupplierData](te, ctx, createSupplier, map[string]any{
			"DataTypeID": dataTypeIDTenantA,
		})

		created := data.CreateSupplier
		assert.Equal(t, dataTypeIDTenantA, created.DataTypeID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.Supplier.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, dataTypeIDTenantA, stored.DataTypeID)

		// Verify event
		te.assertEvents(ctx, Create("Supplier", created.ID))
	})

	t.Run("creates supplier with custom data values", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createSupplierData](te, ctx, createSupplier, map[string]any{
			"DataTypeID": dataTypeIDTenantA,
			"Sum":        100,
			"Weight":     75,
			"Name":       "CustomSupplier",
		})

		created := data.CreateSupplier
		assert.InDelta(t, float64(100), created.Data["sum"], 0.001)
		meta := created.Data["meta"].(map[string]any)
		assert.InDelta(t, float64(75), meta["weight"], 0.001)
		assert.Equal(t, "CustomSupplier", meta["name"])

		te.assertEvents(ctx, Create("Supplier", created.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createSupplier, nil, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative sum", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createSupplier, map[string]any{
			"DataTypeID": dataTypeIDTenantA,
			"Sum":        -10,
		}, "'/sum' does not validate")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative weight", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createSupplier, map[string]any{
			"DataTypeID": dataTypeIDTenantA,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestSupplier_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateSupplierData](te, ctx, updateSupplier, map[string]any{
			"ID":         supplier.ID,
			"DataTypeID": dataTypeIDTenantA,
			"Sum":        999,
		})

		assert.InDelta(t, float64(999), data.UpdateSupplier.Data["sum"], 0.001)
		te.assertEvents(ctx, Update("Supplier", supplier.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fakeID := newID()
		execErr(te, ctx, updateSupplier, map[string]any{
			"ID":         fakeID,
			"DataTypeID": dataTypeIDTenantA,
		}, "supplier not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateSupplier, map[string]any{
			"ID": supplier.ID,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's supplier", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		supplier := te.newSupplier(ctxB, userB).DataTypeID(dataTypeIDTenantB).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateSupplier, map[string]any{
			"ID":         supplier.ID,
			"DataTypeID": dataTypeIDTenantA,
		}, "supplier not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateSupplier, map[string]any{
			"ID":         supplier.ID,
			"DataTypeID": dataTypeIDTenantA,
			"Sum":        -100,
		}, "'/sum' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestSupplier_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes supplier", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteSupplierData](te, ctx, deleteSupplier, map[string]any{
			"ID": supplier.ID,
		})

		assert.Equal(t, supplier.ID, data.DeleteSupplier.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.Supplier.Get(te.ctxWithDeleted(userA), supplier.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("Supplier", supplier.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteSupplier, map[string]any{
			"ID": newID(),
		}, "supplier not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's supplier", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		supplier := te.newSupplier(ctxB, userB).DataTypeID(dataTypeIDTenantB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteSupplier, map[string]any{
			"ID": supplier.ID,
		}, "supplier not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted supplier", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteSupplier, map[string]any{
			"ID": supplier.ID,
		}, "supplier not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestSupplier_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[querySuppliersData](te, ctx, querySuppliers, nil)

		assert.Equal(t, 0, data.Suppliers.TotalCount)
		assert.Empty(t, data.Suppliers.Edges)
	})

	t.Run("returns only own tenant's suppliers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		supplierA := te.newSupplier(ctxA, userA).Create()
		te.newSupplier(ctxB, userB).DataTypeID(dataTypeIDTenantB).Create()

		data := execOK[querySuppliersData](te, ctxA, querySuppliers, nil)

		require.Equal(t, 1, data.Suppliers.TotalCount)
		assert.Equal(t, supplierA.ID, data.Suppliers.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		active := te.newSupplier(ctx, userA).Create()
		te.newSupplier(ctx, userA).Deleted().Create()

		data := execOK[querySuppliersData](te, ctx, querySuppliers, nil)

		require.Equal(t, 1, data.Suppliers.TotalCount)
		assert.Equal(t, active.ID, data.Suppliers.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newSupplier(ctx, userA).Create()
		te.newSupplier(ctx, userA).Deleted().Create()

		data := execOK[querySuppliersData](te, te.ctxWithDeleted(userA), querySuppliers, nil)

		assert.Equal(t, 2, data.Suppliers.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		for range 5 {
			te.newSupplier(ctx, userA).Create()
		}

		data := execOK[querySuppliersData](te, ctx, querySuppliers, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.Suppliers.TotalCount)
		assert.Len(t, data.Suppliers.Edges, 2)
		assert.True(t, data.Suppliers.PageInfo.HasNextPage)
		assert.NotNil(t, data.Suppliers.PageInfo.EndCursor)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestSupplier_QueryWithFilters(t *testing.T) {
	t.Parallel()

	t.Run("filters by data field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)
		ctxB := te.ctx(userB)

		target := te.newSupplierNoDataType(ctx, userA).Create()
		te.newSupplierNoDataType(ctx, userA).NoData().Create()
		te.newSupplierNoDataType(ctxB, userB).Create() // other tenant

		cases := []struct {
			desc   string
			filter string
			count  int
		}{
			{
				desc:   "Data filter",
				filter: `{ Data: ["type", "custom"] }`,
				count:  1,
			},
			{
				desc:   "DataHasKey filter",
				filter: `{ DataHasKey: "meta.name" }`,
				count:  1,
			},
			{
				desc:   "DataIn filter",
				filter: `{ DataIn: ["meta.name", "TestItem", "foo"] }`,
				count:  1,
			},
			{
				desc:   "DataContains filter",
				filter: `{ DataContains: ["meta.tags", "foo"] }`,
				count:  1,
			},
			{
				desc:   "Data null filter",
				filter: `{ Data: null }`,
				count:  2,
			},
			{
				desc:   "DataHasKey null filter",
				filter: `{ DataHasKey: null }`,
				count:  2,
			},
			{
				desc:   "DataIn null filter",
				filter: `{ DataIn: null }`,
				count:  2,
			},
			{
				desc:   "DataContains null filter",
				filter: `{ DataContains: null }`,
				count:  2,
			},
		}

		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) { //nolint:paralleltest // Subtests share test environment
				data := execOK[querySuppliersData](te, ctx, querySuppliers, map[string]any{
					"Where": tc.filter,
				})

				assert.Equal(t, tc.count, data.Suppliers.TotalCount)
				require.Len(t, data.Suppliers.Edges, tc.count)

				if tc.count == 1 {
					assert.Equal(t, target.ID, data.Suppliers.Edges[0].Node.ID)
				}
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestSupplier_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		s1 := te.newSupplierNoDataType(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		s2 := te.newSupplierNoDataType(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		s3 := te.newSupplierNoDataType(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[querySuppliersData](te, ctx, querySuppliersJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.Suppliers.TotalCount)
		assert.Equal(t, s2.ID, data.Suppliers.Edges[0].Node.ID)
		assert.Equal(t, s3.ID, data.Suppliers.Edges[1].Node.ID)
		assert.Equal(t, s1.ID, data.Suppliers.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		s1 := te.newSupplierNoDataType(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		s2 := te.newSupplierNoDataType(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[querySuppliersData](te, ctx, querySuppliersJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Suppliers.TotalCount)
		assert.Equal(t, s2.ID, data.Suppliers.Edges[0].Node.ID)
		assert.Equal(t, s1.ID, data.Suppliers.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		s1 := te.newSupplierNoDataType(ctx, userA).Create()
		s2 := te.newSupplierNoDataType(ctx, userA).Create()

		data := execOK[querySuppliersData](te, ctx, querySuppliersJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Suppliers.TotalCount)
		assert.Equal(t, s2.ID, data.Suppliers.Edges[0].Node.ID)
		assert.Equal(t, s1.ID, data.Suppliers.Edges[1].Node.ID)
	})
}
