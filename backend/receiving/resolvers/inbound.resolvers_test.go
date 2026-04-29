package resolvers_test

import (
	"testing"
	"time"

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
	createInbound = resolver.ParseTemplate(`mutation {
		createReceivingInbound(input: {
			{{if .OrderID}}orderID: "{{.OrderID}}",{{end}}
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			receivingInbound { id tenantID orderID dataTypeID data }
		}
	}`)

	updateInbound = resolver.ParseTemplate(`mutation {
		updateReceivingInbound(id: "{{.ID}}", input: {
			{{if .OrderID}}orderID: "{{.OrderID}}",{{end}}
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}{{end}}
		}) {
			receivingInbound { id tenantID orderID dataTypeID data }
		}
	}`)

	deleteInbound = resolver.ParseTemplate(`mutation {
		deleteReceivingInbound(id: "{{.ID}}") { deletedID }
	}`)

	queryInbounds = resolver.ParseTemplate(`query {
		receivingInbounds(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID orderID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)

	queryInboundsJSONOrder = resolver.ParseTemplate(`query {
		receivingInbounds(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID orderID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type inboundNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	OrderID    string
	DataTypeID uuid.UUID
	Data       map[string]any
}

type createInboundData struct {
	CreateReceivingInbound struct{ ReceivingInbound inboundNode }
}

type updateInboundData struct {
	UpdateReceivingInbound struct{ ReceivingInbound inboundNode }
}

type deleteInboundData struct {
	DeleteReceivingInbound struct{ DeletedID uuid.UUID }
}

type queryInboundsData struct {
	ReceivingInbounds struct {
		TotalCount int
		Edges      []struct{ Node inboundNode }
		PageInfo   struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestInbound_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates inbound with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createInboundData](te, ctx, createInbound, map[string]any{
			"OrderID":    "ORD-001",
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreateReceivingInbound.ReceivingInbound
		assert.Equal(t, "ORD-001", created.OrderID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.Inbound.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "ORD-001", stored.OrderID)

		// Verify event
		te.assertEvents(ctx, Create("inbound", created.ID))
	})

	t.Run("creates inbound with custom data values", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createInboundData](te, ctx, createInbound, map[string]any{
			"OrderID":    "ORD-002",
			"DataTypeID": itemDataTypeID,
			"Sum":        100,
			"Weight":     75,
			"Name":       "CustomItem",
		})

		created := data.CreateReceivingInbound.ReceivingInbound
		assert.InDelta(t, float64(100), created.Data["sum"], 0.001)
		meta := created.Data["meta"].(map[string]any)
		assert.InDelta(t, float64(75), meta["weight"], 0.001)
		assert.Equal(t, "CustomItem", meta["name"])

		te.assertEvents(ctx, Create("inbound", created.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createInbound, map[string]any{
			"OrderID": "ORD-003",
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative sum", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createInbound, map[string]any{
			"OrderID":    "ORD-004",
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

		execErr(te, ctx, createInbound, map[string]any{
			"OrderID":    "ORD-005",
			"DataTypeID": itemDataTypeID,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestInbound_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates orderID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).OrderID("OLD-ORDER").Create()
		te.clearEvents(ctx)

		data := execOK[updateInboundData](te, ctx, updateInbound, map[string]any{
			"ID":         inbound.ID,
			"OrderID":    "NEW-ORDER",
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		})

		assert.Equal(t, "NEW-ORDER", data.UpdateReceivingInbound.ReceivingInbound.OrderID)
		te.assertEvents(ctx, Update("inbound", inbound.ID))
	})

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateInboundData](te, ctx, updateInbound, map[string]any{
			"ID":         inbound.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Sum":        999,
		})

		assert.InDelta(t, float64(999), data.UpdateReceivingInbound.ReceivingInbound.Data["sum"], 0.001)
		te.assertEvents(ctx, Update("inbound", inbound.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fakeID := uuid.New()
		execErr(te, ctx, updateInbound, map[string]any{
			"ID":         fakeID,
			"OrderID":    "X",
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "inbound not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateInbound, map[string]any{
			"ID":   inbound.ID,
			"Data": true, // Data included but no DataTypeID
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's inbound", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create as tenant B
		ctxB := te.ctx(userB)
		inbound := te.newInbound(ctxB, userB).Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateInbound, map[string]any{
			"ID":         inbound.ID,
			"OrderID":    "HACKED",
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "inbound not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateInbound, map[string]any{
			"ID":         inbound.ID,
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

func TestInbound_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes inbound", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteInboundData](te, ctx, deleteInbound, map[string]any{
			"ID": inbound.ID,
		})

		assert.Equal(t, inbound.ID, data.DeleteReceivingInbound.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.Inbound.Get(te.ctxWithDeleted(userA), inbound.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("inbound", inbound.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteInbound, map[string]any{
			"ID": uuid.New(),
		}, "inbound not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's inbound", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		inbound := te.newInbound(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteInbound, map[string]any{
			"ID": inbound.ID,
		}, "inbound not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted inbound", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteInbound, map[string]any{
			"ID": inbound.ID,
		}, "inbound not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestInbound_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryInboundsData](te, ctx, queryInbounds, nil)

		assert.Equal(t, 0, data.ReceivingInbounds.TotalCount)
		assert.Empty(t, data.ReceivingInbounds.Edges)
	})

	t.Run("returns only own tenant's inbounds", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		inboundA := te.newInbound(ctxA, userA).OrderID("TENANT-A").Create()
		te.newInbound(ctxB, userB).OrderID("TENANT-B").Create()

		data := execOK[queryInboundsData](te, ctxA, queryInbounds, nil)

		require.Equal(t, 1, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, inboundA.ID, data.ReceivingInbounds.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		active := te.newInbound(ctx, userA).OrderID("ACTIVE").Create()
		te.newInbound(ctx, userA).OrderID("DELETED").Deleted().Create()

		data := execOK[queryInboundsData](te, ctx, queryInbounds, nil)

		require.Equal(t, 1, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, active.ID, data.ReceivingInbounds.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newInbound(ctx, userA).Create()
		te.newInbound(ctx, userA).Deleted().Create()

		data := execOK[queryInboundsData](te, te.ctxWithDeleted(userA), queryInbounds, nil)

		assert.Equal(t, 2, data.ReceivingInbounds.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		for range 5 {
			te.newInbound(ctx, userA).Create()
		}

		data := execOK[queryInboundsData](te, ctx, queryInbounds, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.ReceivingInbounds.TotalCount)
		assert.Len(t, data.ReceivingInbounds.Edges, 2)
		assert.True(t, data.ReceivingInbounds.PageInfo.HasNextPage)
		assert.NotNil(t, data.ReceivingInbounds.PageInfo.EndCursor)
	})

	t.Run("filters by orderID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		target := te.newInbound(ctx, userA).OrderID("FIND-ME").Create()
		te.newInbound(ctx, userA).OrderID("OTHER").Create()

		data := execOK[queryInboundsData](te, ctx, queryInbounds, map[string]any{
			"Where": `{ orderIDIn: ["FIND-ME"] }`,
		})

		require.Equal(t, 1, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, target.ID, data.ReceivingInbounds.Edges[0].Node.ID)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestInbound_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		i1 := te.newInbound(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		i2 := te.newInbound(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		i3 := te.newInbound(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryInboundsData](te, ctx, queryInboundsJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInbounds.Edges[0].Node.ID)
		assert.Equal(t, i3.ID, data.ReceivingInbounds.Edges[1].Node.ID)
		assert.Equal(t, i1.ID, data.ReceivingInbounds.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		i1 := te.newInbound(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		i2 := te.newInbound(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryInboundsData](te, ctx, queryInboundsJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInbounds.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.ReceivingInbounds.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		i1 := te.newInbound(ctx, userA).Create()
		i2 := te.newInbound(ctx, userA).Create()

		data := execOK[queryInboundsData](te, ctx, queryInboundsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInbounds.Edges[0].Node.ID)
		assert.Equal(t, i1.ID, data.ReceivingInbounds.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestInbound_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newInbound(ctx, userA).Data(map[string]any{"type": "express"}).Create()
		i2 := te.newInbound(ctx, userA).Data(map[string]any{"type": "standard"}).Create()

		data := execOK[queryInboundsData](te, ctx, queryInboundsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "standard"] }`,
		})

		require.Equal(t, 1, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInbounds.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newInbound(ctx, userA).Data(map[string]any{"type": "express"}).Create()
		i2 := te.newInbound(ctx, userA).Data(map[string]any{"type": "standard", "priority": float64(1)}).Create()

		data := execOK[queryInboundsData](te, ctx, queryInboundsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.ReceivingInbounds.TotalCount)
		assert.Equal(t, i2.ID, data.ReceivingInbounds.Edges[0].Node.ID)
	})
}
