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
		createPickingOutboundShipmentNotification(input: {
			orderID: "{{.OrderID}}",
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}
		}) {
			pickingOutboundShipmentNotification { id tenantID orderID dataTypeID data }
		}
	}`)

	updateNotification = resolver.ParseTemplate(`mutation {
		updatePickingOutboundShipmentNotification(id: "{{.ID}}", input: {
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			{{if .Data}}data: {
				type: "{{or .Type "custom"}}",
				sum: {{or .Sum 15}},
				meta: { name: "{{or .Name "Test"}}", weight: {{or .Weight 50}}, tags: ["a", "b"] }
			}{{end}}
		}) {
			pickingOutboundShipmentNotification { id tenantID orderID dataTypeID data }
		}
	}`)

	deleteNotification = resolver.ParseTemplate(`mutation {
		deletePickingOutboundShipmentNotification(id: "{{.ID}}") { deletedID }
	}`)

	queryNotifications = resolver.ParseTemplate(`query {
		pickingOutboundShipmentNotifications(
			first: {{or .First 100}},
			orderBy: { direction: ASC, field: CREATED_AT }
			{{if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID orderID dataTypeID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)

	queryNotificationsJSONOrder = resolver.ParseTemplate(`query {
		pickingOutboundShipmentNotifications(
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

type notificationNode struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	OrderID    uuid.UUID
	DataTypeID uuid.UUID
	Data       map[string]any
}

type createNotificationData struct {
	CreatePickingOutboundShipmentNotification struct {
		PickingOutboundShipmentNotification notificationNode
	}
}

type updateNotificationData struct {
	UpdatePickingOutboundShipmentNotification struct {
		PickingOutboundShipmentNotification notificationNode
	}
}

type deleteNotificationData struct {
	DeletePickingOutboundShipmentNotification struct{ DeletedID uuid.UUID }
}

type queryNotificationsData struct {
	PickingOutboundShipmentNotifications struct {
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

func TestOutboundShipmentNotification_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates notification with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"OrderID":    order.ID,
			"DataTypeID": itemDataTypeID,
		})

		created := data.CreatePickingOutboundShipmentNotification.PickingOutboundShipmentNotification
		assert.Equal(t, order.ID, created.OrderID)
		assert.Equal(t, itemDataTypeID, created.DataTypeID)
		assert.Equal(t, tenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.OutboundShipmentNotification.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, order.ID, stored.OrderID)

		// Verify event
		te.assertEvents(ctx, Create("outboundshipmentnotification", created.ID))
	})

	t.Run("creates notification with custom data values", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[createNotificationData](te, ctx, createNotification, map[string]any{
			"OrderID":    order.ID,
			"DataTypeID": itemDataTypeID,
			"Sum":        100,
			"Weight":     75,
			"Name":       "CustomNotification",
		})

		created := data.CreatePickingOutboundShipmentNotification.PickingOutboundShipmentNotification
		assert.InDelta(t, float64(100), created.Data["sum"], 0.001)
		meta := created.Data["meta"].(map[string]any)
		assert.InDelta(t, float64(75), meta["weight"], 0.001)
		assert.Equal(t, "CustomNotification", meta["name"])

		te.assertEvents(ctx, Create("outboundshipmentnotification", created.ID))
	})

	t.Run("rejects missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createNotification, map[string]any{
			"OrderID": order.ID,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid schema - negative sum", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createNotification, map[string]any{
			"OrderID":    order.ID,
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

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createNotification, map[string]any{
			"OrderID":    order.ID,
			"DataTypeID": itemDataTypeID,
			"Weight":     -50,
		}, "'/meta/weight' does not validate")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestOutboundShipmentNotification_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates data fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		data := execOK[updateNotificationData](te, ctx, updateNotification, map[string]any{
			"ID":         notification.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
			"Sum":        999,
		})

		assert.InDelta(t, float64(999),
			data.UpdatePickingOutboundShipmentNotification.PickingOutboundShipmentNotification.Data["sum"], 0.001)
		te.assertEvents(ctx, Update("outboundshipmentnotification", notification.ID))
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
		}, "outbound_shipment_notification not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update with missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Create()
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
		orderB := te.newOrder(ctxB, userB).Create()
		notificationB := te.newNotification(ctxB, userB, orderB.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateNotification, map[string]any{
			"ID":         notificationB.ID,
			"DataTypeID": itemDataTypeID,
			"Data":       true,
		}, "outbound_shipment_notification not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects invalid schema on update", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Create()
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

func TestOutboundShipmentNotification_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		data := execOK[deleteNotificationData](te, ctx, deleteNotification, map[string]any{
			"ID": notification.ID,
		})

		assert.Equal(t, notification.ID, data.DeletePickingOutboundShipmentNotification.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.OutboundShipmentNotification.Get(te.ctxWithDeleted(userA), notification.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("outboundshipmentnotification", notification.ID))
	})

	t.Run("rejects delete of non-existent ID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteNotification, map[string]any{
			"ID": uuid.New(),
		}, "outbound_shipment_notification not found")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects delete of other tenant's notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		orderB := te.newOrder(ctxB, userB).Create()
		notificationB := te.newNotification(ctxB, userB, orderB.ID).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteNotification, map[string]any{
			"ID": notificationB.ID,
		}, "outbound_shipment_notification not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of already deleted notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, deleteNotification, map[string]any{
			"ID": notification.ID,
		}, "outbound_shipment_notification not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestOutboundShipmentNotification_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, nil)

		assert.Equal(t, 0, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Empty(t, data.PickingOutboundShipmentNotifications.Edges)
	})

	t.Run("returns only own tenant's notifications", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		orderA := te.newOrder(ctxA, userA).Create()
		notifA := te.newNotification(ctxA, userA, orderA.ID).Create()

		orderB := te.newOrder(ctxB, userB).Create()
		te.newNotification(ctxB, userB, orderB.ID).Create()

		data := execOK[queryNotificationsData](te, ctxA, queryNotifications, nil)

		require.Equal(t, 1, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, notifA.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
	})

	t.Run("excludes soft-deleted by default", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		active := te.newNotification(ctx, userA, order.ID).Create()
		te.newNotification(ctx, userA, order.ID).Deleted().Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, nil)

		require.Equal(t, 1, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, active.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
	})

	t.Run("includes soft-deleted with showDeleted feature", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.newNotification(ctx, userA, order.ID).Create()
		te.newNotification(ctx, userA, order.ID).Deleted().Create()

		data := execOK[queryNotificationsData](te, te.ctxWithDeleted(userA), queryNotifications, nil)

		assert.Equal(t, 2, data.PickingOutboundShipmentNotifications.TotalCount)
	})

	t.Run("paginates results", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		for range 5 {
			te.newNotification(ctx, userA, order.ID).Create()
		}

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, map[string]any{
			"First": 2,
		})

		assert.Equal(t, 5, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Len(t, data.PickingOutboundShipmentNotifications.Edges, 2)
		assert.True(t, data.PickingOutboundShipmentNotifications.PageInfo.HasNextPage)
		assert.NotNil(t, data.PickingOutboundShipmentNotifications.PageInfo.EndCursor)
	})

	t.Run("filters by orderID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order1 := te.newOrder(ctx, userA).Create()
		order2 := te.newOrder(ctx, userA).Create()

		notif1 := te.newNotification(ctx, userA, order1.ID).Create()
		te.newNotification(ctx, userA, order2.ID).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotifications, map[string]any{
			"Where": `{ orderID: "` + order1.ID.String() + `" }`,
		})

		require.Equal(t, 1, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, notif1.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestOutboundShipmentNotification_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		n1 := te.newNotification(ctx, userA, order.ID).Data(map[string]any{"sum": float64(30)}).Create()
		n2 := te.newNotification(ctx, userA, order.ID).Data(map[string]any{"sum": float64(10)}).Create()
		n3 := te.newNotification(ctx, userA, order.ID).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
		assert.Equal(t, n3.ID, data.PickingOutboundShipmentNotifications.Edges[1].Node.ID)
		assert.Equal(t, n1.ID, data.PickingOutboundShipmentNotifications.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		n1 := te.newNotification(ctx, userA, order.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		n2 := te.newNotification(ctx, userA, order.ID).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
		assert.Equal(t, n1.ID, data.PickingOutboundShipmentNotifications.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		n1 := te.newNotification(ctx, userA, order.ID).Create()
		n2 := te.newNotification(ctx, userA, order.ID).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
		assert.Equal(t, n1.ID, data.PickingOutboundShipmentNotifications.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestOutboundShipmentNotification_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.newNotification(ctx, userA, order.ID).Data(map[string]any{"type": "express"}).Create()
		n2 := te.newNotification(ctx, userA, order.ID).Data(map[string]any{"type": "standard"}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "standard"] }`,
		})

		require.Equal(t, 1, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.newNotification(ctx, userA, order.ID).Data(map[string]any{"type": "express"}).Create()
		n2 := te.newNotification(ctx, userA, order.ID).Data(map[string]any{"type": "standard", "priority": float64(1)}).Create()

		data := execOK[queryNotificationsData](te, ctx, queryNotificationsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.PickingOutboundShipmentNotifications.TotalCount)
		assert.Equal(t, n2.ID, data.PickingOutboundShipmentNotifications.Edges[0].Node.ID)
	})
}
