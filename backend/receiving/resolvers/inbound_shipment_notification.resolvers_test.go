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
	createNotification = resolver.ParseTemplate(`mutation {
		createReceivingInboundShipmentNotification(input: {
			inboundID: "{{.InboundID}}",
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			receivingInboundShipmentNotification { id tenantID inboundID dataTypeID data }
		}
	}`)

	updateNotification = resolver.ParseTemplate(`mutation {
		updateReceivingInboundShipmentNotification(id: "{{.ID}}", input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}{{end}}
		}) {
			receivingInboundShipmentNotification { id tenantID inboundID dataTypeID data }
		}
	}`)

	deleteNotification = resolver.ParseTemplate(`mutation {
		deleteReceivingInboundShipmentNotification(id: "{{.ID}}") { deletedID }
	}`)

	queryNotifications = resolver.ParseTemplate(`query {
		receivingInboundShipmentNotifications(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID inboundID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)

	queryNotificationsJSONOrder = resolver.ParseTemplate(`query {
		receivingInboundShipmentNotifications(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID inboundID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type notificationNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	InboundID  uuid.UUID
	DataTypeID uuid.UUID
	Data       map[string]any
}

type createNotificationData struct {
	CreateReceivingInboundShipmentNotification struct {
		ReceivingInboundShipmentNotification notificationNode
	}
}

type updateNotificationData struct {
	UpdateReceivingInboundShipmentNotification struct {
		ReceivingInboundShipmentNotification notificationNode
	}
}

type deleteNotificationData struct {
	DeleteReceivingInboundShipmentNotification struct{ DeletedID uuid.UUID }
}

type queryNotificationsData struct {
	ReceivingInboundShipmentNotifications struct {
		TotalCount int
		Edges      []struct{ Node notificationNode }
		PageInfo   struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestInboundShipmentNotification_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates notification with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"InboundID":  inbound.ID,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreateReceivingInboundShipmentNotification.ReceivingInboundShipmentNotification
		assert.Equal(t, inbound.ID, created.InboundID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.InboundShipmentNotification.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, inbound.ID, stored.InboundID)

		// Verify event
		te.assertEvents(ctx, Create("inboundshipmentnotification", created.ID))
	})

	t.Run("creates notification with custom data values", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"InboundID":  inbound.ID,
			"DataTypeID": itemDataTypeID,
			"Sum":        100,
			"Weight":     75,
			"Name":       "CustomNotification",
		})

		created := data.CreateReceivingInboundShipmentNotification.ReceivingInboundShipmentNotification
		assert.InDelta(t, float64(100), created.Data["sum"], 0.001)
		meta := created.Data["meta"].(map[string]any)
		assert.InDelta(t, float64(75), meta["weight"], 0.001)
		assert.Equal(t, "CustomNotification", meta["name"])

		te.assertEvents(ctx, Create("inboundshipmentnotification", created.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createNotification, map[string]any{
			"InboundID": inbound.ID,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative sum", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createNotification, map[string]any{
			"InboundID":  inbound.ID,
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

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createNotification, map[string]any{
			"InboundID":  inbound.ID,
			"DataTypeID": itemDataTypeID,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestInboundShipmentNotification_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		data := execOK[updateNotificationData](te, ctx, updateNotification, map[string]any{
			"ID":         notification.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Sum":        999,
		})

		assert.InDelta(t, float64(999),
			data.UpdateReceivingInboundShipmentNotification.ReceivingInboundShipmentNotification.Data["sum"], 0.001)
		te.assertEvents(ctx, Update("inboundshipmentnotification", notification.ID))
	})

	t.Run("rejects update of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fakeID := uuid.New()
		execErr(te, ctx, updateNotification, map[string]any{
			"ID":         fakeID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "inbound_shipment_notification not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateNotification, map[string]any{
			"ID":   notification.ID,
			"Data": true,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of other tenant's notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		inboundB := te.newInbound(ctxB, userB).Create()
		notificationB := te.newNotification(ctxB, userB, inboundB.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateNotification, map[string]any{
			"ID":         notificationB.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "inbound_shipment_notification not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateNotification, map[string]any{
			"ID":         notification.ID,
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

func TestInboundShipmentNotification_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		data := execOK[deleteNotificationData](te, ctx, deleteNotification, map[string]any{
			"ID": notification.ID,
		})

		assert.Equal(t, notification.ID, data.DeleteReceivingInboundShipmentNotification.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.InboundShipmentNotification.Get(te.ctxWithDeleted(userA), notification.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("inboundshipmentnotification", notification.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteNotification, map[string]any{
			"ID": uuid.New(),
		}, "inbound_shipment_notification not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		inboundB := te.newInbound(ctxB, userB).Create()
		notificationB := te.newNotification(ctxB, userB, inboundB.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteNotification, map[string]any{
			"ID": notificationB.ID,
		}, "inbound_shipment_notification not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteNotification, map[string]any{
			"ID": notification.ID,
		}, "inbound_shipment_notification not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestInboundShipmentNotification_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, nil)

		assert.Equal(t, 0, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Empty(t, data.ReceivingInboundShipmentNotifications.Edges)
	})

	t.Run("returns only own tenant's notifications", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		inboundA := te.newInbound(ctxA, userA).Create()
		notifA := te.newNotification(ctxA, userA, inboundA.ID).Create()

		inboundB := te.newInbound(ctxB, userB).Create()
		te.newNotification(ctxB, userB, inboundB.ID).Create()

		data := execOK[queryNotificationsData](te, ctxA, queryNotifications, nil)

		require.Equal(t, 1, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, notifA.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		active := te.newNotification(ctx, userA, inbound.ID).Create()
		te.newNotification(ctx, userA, inbound.ID).Deleted().Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, nil)

		require.Equal(t, 1, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, active.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.newNotification(ctx, userA, inbound.ID).Create()
		te.newNotification(ctx, userA, inbound.ID).Deleted().Create()

		data := execOK[queryNotificationsData](te, te.ctxWithDeleted(userA), queryNotifications, nil)

		assert.Equal(t, 2, data.ReceivingInboundShipmentNotifications.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		for range 5 {
			te.newNotification(ctx, userA, inbound.ID).Create()
		}

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Len(t, data.ReceivingInboundShipmentNotifications.Edges, 2)
		assert.True(t, data.ReceivingInboundShipmentNotifications.PageInfo.HasNextPage)
		assert.NotNil(t, data.ReceivingInboundShipmentNotifications.PageInfo.EndCursor)
	})

	t.Run("filters by inboundID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound1 := te.newInbound(ctx, userA).Create()
		inbound2 := te.newInbound(ctx, userA).Create()

		notif1 := te.newNotification(ctx, userA, inbound1.ID).Create()
		te.newNotification(ctx, userA, inbound2.ID).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, map[string]any{
			"Where": `{ inboundID: "` + inbound1.ID.String() + `" }`,
		})

		require.Equal(t, 1, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, notif1.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestInboundShipmentNotification_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		n1 := te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{"sum": float64(30)}).Create()
		n2 := te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{"sum": float64(10)}).Create()
		n3 := te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
		assert.Equal(t, n3.ID, data.ReceivingInboundShipmentNotifications.Edges[1].Node.ID)
		assert.Equal(t, n1.ID, data.ReceivingInboundShipmentNotifications.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		n1 := te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		n2 := te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
		assert.Equal(t, n1.ID, data.ReceivingInboundShipmentNotifications.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		n1 := te.newNotification(ctx, userA, inbound.ID).Create()
		n2 := te.newNotification(ctx, userA, inbound.ID).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
		assert.Equal(t, n1.ID, data.ReceivingInboundShipmentNotifications.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestInboundShipmentNotification_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{"type": "express"}).Create()
		n2 := te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{"type": "standard"}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "standard"] }`,
		})

		require.Equal(t, 1, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{"type": "express"}).Create()
		n2 := te.newNotification(ctx, userA, inbound.ID).Data(map[string]any{"type": "standard", "priority": float64(1)}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.ReceivingInboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.ReceivingInboundShipmentNotifications.Edges[0].Node.ID)
	})
}
