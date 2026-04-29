package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

var (
	patchOrderData = resolver.ParseTemplate(`mutation {
		patchPickingOrderData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			pickingOrder { id data }
		}
	}`)

	patchOrderItemData = resolver.ParseTemplate(`mutation {
		patchPickingOrderItemData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			pickingOrderItem { id data }
		}
	}`)

	patchNotificationData = resolver.ParseTemplate(`mutation {
		patchPickingOutboundShipmentNotificationData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			pickingOutboundShipmentNotification { id data }
		}
	}`)
)

type patchOrderResult struct {
	PatchPickingOrderData struct {
		PickingOrder struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"pickingOrder"`
	} `json:"patchPickingOrderData"`
}

type patchOrderItemResult struct {
	PatchPickingOrderItemData struct {
		PickingOrderItem struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"pickingOrderItem"`
	} `json:"patchPickingOrderItemData"`
}

type patchNotificationResult struct {
	PatchPickingOutboundShipmentNotificationData struct {
		PickingOutboundShipmentNotification struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"pickingOutboundShipmentNotification"`
	} `json:"patchPickingOutboundShipmentNotificationData"`
}

type patch struct {
	Op    string
	Path  string
	Value string
	From  string
}

func TestPatchPickingOrderData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderResult](te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Patched\"`},
			},
		})

		meta := data.PatchPickingOrderData.PickingOrder.Data["meta"].(map[string]any)
		assert.Equal(t, "Patched", meta["name"])
		assert.InDelta(t, float64(50), meta["weight"], 0)

		te.assertEvents(ctx, Update("order", order.ID))
	})

	t.Run("add a new field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderResult](te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "ADD", Path: "/meta/tags/-", Value: `\"new-tag\"`},
			},
		})

		meta := data.PatchPickingOrderData.PickingOrder.Data["meta"].(map[string]any)
		tags := meta["tags"].([]any)
		assert.Len(t, tags, 3)
		assert.Equal(t, "new-tag", tags[2])
	})

	t.Run("remove a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Data(map[string]any{
			"type": "custom", "sum": float64(15),
			"meta":  map[string]any{"name": "Test", "weight": float64(50), "tags": []any{"a"}},
			"extra": "to-be-removed",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderResult](te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "REMOVE", Path: "/extra"},
			},
		})

		got := data.PatchPickingOrderData.PickingOrder.Data
		_, hasExtra := got["extra"]
		assert.False(t, hasExtra)
		assert.Equal(t, "custom", got["type"])
	})

	t.Run("multiple operations in one request", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderResult](te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "99"},
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Multi\"`},
				{Op: "REPLACE", Path: "/meta/weight", Value: "75"},
			},
		})

		got := data.PatchPickingOrderData.PickingOrder.Data
		assert.InDelta(t, float64(99), got["sum"], 0)
		meta := got["meta"].(map[string]any)
		assert.Equal(t, "Multi", meta["name"])
		assert.InDelta(t, float64(75), meta["weight"], 0)
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderResult](te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"custom\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		})

		assert.InDelta(t, float64(42), data.PatchPickingOrderData.PickingOrder.Data["sum"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.Order.Get(ctx, order.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(15), stored.Data["sum"], 0)
		te.assertNoEvents(ctx)
	})

	t.Run("move operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Data(map[string]any{
			"type": "custom", "sum": float64(15), "old_note": "hello",
			"meta": map[string]any{"name": "Test", "weight": float64(50), "tags": []any{"a"}},
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderResult](te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "MOVE", Path: "/note", From: "/old_note"},
			},
		})

		got := data.PatchPickingOrderData.PickingOrder.Data
		assert.Equal(t, "hello", got["note"])
		_, hasOld := got["old_note"]
		assert.False(t, hasOld)
	})

	t.Run("copy operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderResult](te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "COPY", Path: "/type_backup", From: "/type"},
			},
		})

		got := data.PatchPickingOrderData.PickingOrder.Data
		assert.Equal(t, "custom", got["type"])
		assert.Equal(t, "custom", got["type_backup"])
	})

	t.Run("rejects patch that violates schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "-1"},
			},
		}, "validate patched data")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects patch on nonexistent entity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, patchOrderData, map[string]any{
			"ID": uuid.New(),
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "1"},
			},
		}, "not found")
	})

	t.Run("tenant isolation: cannot patch other tenant entity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		order := te.newOrder(ctxA, userA).Create()

		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchOrderData, map[string]any{
			"ID": order.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "99"},
			},
		}, "not found")
	})

	t.Run("rejects invalid operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		invalidOp := resolver.ParseTemplate(`mutation {
			patchPickingOrderData(id: "{{.ID}}", patches: [
				{ op: INVALID, path: "/sum", value: "1" }
			]) { pickingOrder { id } }
		}`)

		execErr(te, ctx, invalidOp, map[string]any{"ID": order.ID}, "INVALID")
		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty patches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		te.clearEvents(ctx)

		emptyPatches := resolver.ParseTemplate(`mutation {
			patchPickingOrderData(id: "{{.ID}}", patches: []) {
				pickingOrder { id }
			}
		}`)

		execErr(te, ctx, emptyPatches, map[string]any{"ID": order.ID}, "patches must not be empty")
		te.assertNoEvents(ctx)
	})
}

func TestPatchPickingOrderItemData(t *testing.T) {
	t.Parallel()

	t.Run("replace a field on order item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		item := te.newOrderItem(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		data := execOK[patchOrderItemResult](te, ctx, patchOrderItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"PatchedItem\"`},
			},
		})

		meta := data.PatchPickingOrderItemData.PickingOrderItem.Data["meta"].(map[string]any)
		assert.Equal(t, "PatchedItem", meta["name"])

		te.assertEvents(ctx, Update("orderitems", item.ID))
	})
}

func TestPatchPickingOutboundShipmentNotificationData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		data := execOK[patchNotificationResult](te, ctx, patchNotificationData, map[string]any{
			"ID": notification.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"PatchedNotif\"`},
			},
		})

		got := data.PatchPickingOutboundShipmentNotificationData.PickingOutboundShipmentNotification
		assert.Equal(t, notification.ID, got.ID)
		meta := got.Data["meta"].(map[string]any)
		assert.Equal(t, "PatchedNotif", meta["name"])
		assert.InDelta(t, float64(50), meta["weight"], 0)

		te.assertEvents(ctx, Update("outboundshipmentnotification", notification.ID))
	})

	t.Run("rejects patch that violates schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchNotificationData, map[string]any{
			"ID": notification.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "-1"},
			},
		}, "validate patched data")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects patch on nonexistent entity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, patchNotificationData, map[string]any{
			"ID": uuid.New(),
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "1"},
			},
		}, "not found")
	})

	t.Run("rejects patch on soft-deleted notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		order := te.newOrder(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, order.ID).Deleted().Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchNotificationData, map[string]any{
			"ID": notification.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "not found")

		te.assertNoEvents(ctx)
	})

	t.Run("tenant isolation: cannot patch other tenant notification", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		order := te.newOrder(ctxA, userA).Create()
		notification := te.newNotification(ctxA, userA, order.ID).Create()

		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchNotificationData, map[string]any{
			"ID": notification.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "99"},
			},
		}, "not found")
	})
}
