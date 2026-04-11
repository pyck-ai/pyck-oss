package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createCustomer = resolver.ParseTemplate(`mutation {
		createCustomer(input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestCustomer"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			id
			tenantID
			dataTypeID
			data
		}
	}`)

	updateCustomer = resolver.ParseTemplate(`mutation {
		updateCustomer(id: "{{.ID}}", input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "TestCustomer"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			id
			tenantID
			dataTypeID
			data
		}
	}`)

	deleteCustomer = resolver.ParseTemplate(`mutation {
		deleteCustomer(id: "{{.ID}}") { deletedID }
	}`)

	queryCustomers = resolver.ParseTemplate(`query {
		customers(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID dataTypeID data } }
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)

	queryCustomersJSONOrder = resolver.ParseTemplate(`query {
		customers(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .JSONType}}, jsonType: {{.JSONType}}{{end}}
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

type customerNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	DataTypeID uuid.UUID
	Data       map[string]any
}

type createCustomerData struct {
	CreateCustomer customerNode
}

type updateCustomerData struct {
	UpdateCustomer customerNode
}

type deleteCustomerData struct {
	DeleteCustomer struct{ DeletedID uuid.UUID }
}

type queryCustomersData struct {
	Customers struct {
		TotalCount int
		Edges      []struct{ Node customerNode }
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

func TestCustomer_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates customer with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createCustomerData](te, ctx, createCustomer, map[string]any{
			"DataTypeID": dataTypeIDTenantA,
		})

		created := data.CreateCustomer
		assert.Equal(t, dataTypeIDTenantA, created.DataTypeID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.Customer.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, dataTypeIDTenantA, stored.DataTypeID)

		// Verify event
		te.assertEvents(ctx, Create("Customer", created.ID))
	})

	t.Run("creates customer with custom data values", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createCustomerData](te, ctx, createCustomer, map[string]any{
			"DataTypeID": dataTypeIDTenantA,
			"Sum":        100,
			"Weight":     75,
			"Name":       "CustomCustomer",
		})

		created := data.CreateCustomer
		assert.InDelta(t, float64(100), created.Data["sum"], 0.001)
		meta := created.Data["meta"].(map[string]any)
		assert.InDelta(t, float64(75), meta["weight"], 0.001)
		assert.Equal(t, "CustomCustomer", meta["name"])

		te.assertEvents(ctx, Create("Customer", created.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createCustomer, nil, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative sum", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createCustomer, map[string]any{
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

		execErr(te, ctx, createCustomer, map[string]any{
			"DataTypeID": dataTypeIDTenantA,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestCustomer_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateCustomerData](te, ctx, updateCustomer, map[string]any{
			"ID":         customer.ID,
			"DataTypeID": dataTypeIDTenantA,
			"Sum":        999,
		})

		assert.InDelta(t, float64(999), data.UpdateCustomer.Data["sum"], 0.001)
		te.assertEvents(ctx, Update("Customer", customer.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fakeID := newID()
		execErr(te, ctx, updateCustomer, map[string]any{
			"ID":         fakeID,
			"DataTypeID": dataTypeIDTenantA,
		}, "customer not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateCustomer, map[string]any{
			"ID": customer.ID,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's customer", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		customer := te.newCustomer(ctxB, userB).DataTypeID(dataTypeIDTenantB).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateCustomer, map[string]any{
			"ID":         customer.ID,
			"DataTypeID": dataTypeIDTenantA,
		}, "customer not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateCustomer, map[string]any{
			"ID":         customer.ID,
			"DataTypeID": dataTypeIDTenantA,
			"Sum":        -100,
		}, "'/sum' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestCustomer_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes customer", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteCustomerData](te, ctx, deleteCustomer, map[string]any{
			"ID": customer.ID,
		})

		assert.Equal(t, customer.ID, data.DeleteCustomer.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.Customer.Get(te.ctxWithDeleted(userA), customer.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)

		te.assertEvents(ctx, Delete("Customer", customer.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteCustomer, map[string]any{
			"ID": newID(),
		}, "customer not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's customer", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		customer := te.newCustomer(ctxB, userB).DataTypeID(dataTypeIDTenantB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteCustomer, map[string]any{
			"ID": customer.ID,
		}, "customer not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted customer", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteCustomer, map[string]any{
			"ID": customer.ID,
		}, "customer not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestCustomer_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryCustomersData](te, ctx, queryCustomers, nil)

		assert.Equal(t, 0, data.Customers.TotalCount)
		assert.Empty(t, data.Customers.Edges)
	})

	t.Run("returns only own tenant's customers", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		customerA := te.newCustomer(ctxA, userA).Create()
		te.newCustomer(ctxB, userB).DataTypeID(dataTypeIDTenantB).Create()

		data := execOK[queryCustomersData](te, ctxA, queryCustomers, nil)

		require.Equal(t, 1, data.Customers.TotalCount)
		assert.Equal(t, customerA.ID, data.Customers.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		active := te.newCustomer(ctx, userA).Create()
		te.newCustomer(ctx, userA).Deleted().Create()

		data := execOK[queryCustomersData](te, ctx, queryCustomers, nil)

		require.Equal(t, 1, data.Customers.TotalCount)
		assert.Equal(t, active.ID, data.Customers.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newCustomer(ctx, userA).Create()
		te.newCustomer(ctx, userA).Deleted().Create()

		data := execOK[queryCustomersData](te, te.ctxWithDeleted(userA), queryCustomers, nil)

		assert.Equal(t, 2, data.Customers.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		for range 5 {
			te.newCustomer(ctx, userA).Create()
		}

		data := execOK[queryCustomersData](te, ctx, queryCustomers, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.Customers.TotalCount)
		assert.Len(t, data.Customers.Edges, 2)
		assert.True(t, data.Customers.PageInfo.HasNextPage)
		assert.NotNil(t, data.Customers.PageInfo.EndCursor)
	})
}

// =============================================================================
// QUERY WITH FILTERS TESTS
// =============================================================================

func TestCustomer_QueryWithFilters(t *testing.T) {
	t.Parallel()

	t.Run("filters by data field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctx := te.ctx(userA)
		ctxB := te.ctx(userB)

		target := te.newCustomerNoDataType(ctx, userA).Create()
		te.newCustomerNoDataType(ctx, userA).NoData().Create()
		te.newCustomerNoDataType(ctxB, userB).Create() // other tenant

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
				data := execOK[queryCustomersData](te, ctx, queryCustomers, map[string]any{
					"Where": tc.filter,
				})

				assert.Equal(t, tc.count, data.Customers.TotalCount)
				require.Len(t, data.Customers.Edges, tc.count)

				if tc.count == 1 {
					assert.Equal(t, target.ID, data.Customers.Edges[0].Node.ID)
				}
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestCustomer_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		c1 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		c2 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		c3 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryCustomersData](te, ctx, queryCustomersJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.Customers.TotalCount)
		assert.Equal(t, c2.ID, data.Customers.Edges[0].Node.ID)
		assert.Equal(t, c3.ID, data.Customers.Edges[1].Node.ID)
		assert.Equal(t, c1.ID, data.Customers.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		c1 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		c2 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()
		c3 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(20)},
		}).Create()

		data := execOK[queryCustomersData](te, ctx, queryCustomersJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 3, data.Customers.TotalCount)
		assert.Equal(t, c2.ID, data.Customers.Edges[0].Node.ID)
		assert.Equal(t, c3.ID, data.Customers.Edges[1].Node.ID)
		assert.Equal(t, c1.ID, data.Customers.Edges[2].Node.ID)
	})

	t.Run("orders by JSON data with pagination", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newCustomerNoDataType(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		c2 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		c3 := te.newCustomerNoDataType(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryCustomersData](te, ctx, queryCustomersJSONOrder, map[string]any{
			"JSONPath": "sum",
			"First":    2,
		})

		require.Len(t, data.Customers.Edges, 2)
		assert.True(t, data.Customers.PageInfo.HasNextPage)
		assert.Equal(t, c2.ID, data.Customers.Edges[0].Node.ID)
		assert.Equal(t, c3.ID, data.Customers.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		c1 := te.newCustomerNoDataType(ctx, userA).Create()
		c2 := te.newCustomerNoDataType(ctx, userA).Create()

		data := execOK[queryCustomersData](te, ctx, queryCustomersJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Customers.TotalCount)
		assert.Equal(t, c2.ID, data.Customers.Edges[0].Node.ID)
		assert.Equal(t, c1.ID, data.Customers.Edges[1].Node.ID)
	})
}
