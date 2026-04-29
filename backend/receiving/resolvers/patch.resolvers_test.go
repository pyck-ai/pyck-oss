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
	patchInboundData = resolver.ParseTemplate(`mutation {
		patchReceivingInboundData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			receivingInbound { id data }
		}
	}`)

	patchInboundItemData = resolver.ParseTemplate(`mutation {
		patchReceivingInboundItemData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			receivingInboundItem { id data }
		}
	}`)

	patchNotificationData = resolver.ParseTemplate(`mutation {
		patchReceivingInboundShipmentNotificationData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			receivingInboundShipmentNotification { id data }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type patchInboundResult struct {
	PatchReceivingInboundData struct {
		ReceivingInbound struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"receivingInbound"`
	} `json:"patchReceivingInboundData"`
}

type patchInboundItemResult struct {
	PatchReceivingInboundItemData struct {
		ReceivingInboundItem struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"receivingInboundItem"`
	} `json:"patchReceivingInboundItemData"`
}

type patchNotificationResult struct {
	PatchReceivingInboundShipmentNotificationData struct {
		ReceivingInboundShipmentNotification struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"receivingInboundShipmentNotification"`
	} `json:"patchReceivingInboundShipmentNotificationData"`
}

// patch is a helper for building patch template args.
type patch struct {
	Op    string
	Path  string
	Value string
	From  string
}

// =============================================================================
// INBOUND PATCH TESTS
// =============================================================================

func TestPatchInboundData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundResult](te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Updated\"`},
			},
		})

		got := data.PatchReceivingInboundData.ReceivingInbound
		assert.Equal(t, inbound.ID, got.ID)

		meta, ok := got.Data["meta"].(map[string]any)
		require.True(t, ok, "meta should be a map")
		assert.Equal(t, "Updated", meta["name"])
		// Other fields should be preserved
		assert.InDelta(t, float64(50), meta["weight"], 0)

		te.assertEvents(ctx, Update("inbound", inbound.ID))
	})

	t.Run("add a new field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundResult](te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "ADD", Path: "/meta/tags/-", Value: `\"new-tag\"`},
			},
		})

		meta := data.PatchReceivingInboundData.ReceivingInbound.Data["meta"].(map[string]any)
		tags := meta["tags"].([]any)
		assert.Len(t, tags, 3)
		assert.Equal(t, "new-tag", tags[2])
	})

	t.Run("remove a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		// Create with extra optional field in meta (tags is required but we can
		// add an extra one and then remove it — actually tags is required,
		// let's add an extra field first via a combined patch)
		inbound := te.newInbound(ctx, userA).Data(map[string]any{
			"type": "custom",
			"sum":  float64(15),
			"meta": map[string]any{
				"name":   "TestItem",
				"weight": float64(50),
				"tags":   []any{"foo"},
			},
			"extra": "to-be-removed",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundResult](te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "REMOVE", Path: "/extra"},
			},
		})

		got := data.PatchReceivingInboundData.ReceivingInbound.Data
		_, hasExtra := got["extra"]
		assert.False(t, hasExtra, "extra field should be removed")
		assert.Equal(t, "custom", got["type"], "other fields preserved")
	})

	t.Run("multiple operations in one request", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundResult](te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "99"},
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Multi\"`},
				{Op: "REPLACE", Path: "/meta/weight", Value: "75"},
			},
		})

		got := data.PatchReceivingInboundData.ReceivingInbound.Data
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

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundResult](te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"custom\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		})

		got := data.PatchReceivingInboundData.ReceivingInbound.Data
		assert.InDelta(t, float64(42), got["sum"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong-value\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "failed to apply patch")

		// Verify nothing changed
		stored, err := te.Ent.Inbound.Get(ctx, inbound.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(15), stored.Data["sum"], 0)

		te.assertNoEvents(ctx)
	})

	t.Run("move operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Data(map[string]any{
			"type":     "custom",
			"sum":      float64(15),
			"old_note": "hello",
			"meta": map[string]any{
				"name":   "TestItem",
				"weight": float64(50),
				"tags":   []any{"foo"},
			},
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundResult](te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "MOVE", Path: "/note", From: "/old_note"},
			},
		})

		got := data.PatchReceivingInboundData.ReceivingInbound.Data
		assert.Equal(t, "hello", got["note"])
		_, hasOld := got["old_note"]
		assert.False(t, hasOld, "old_note should be moved away")
	})

	t.Run("copy operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundResult](te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
			"Patches": []patch{
				{Op: "COPY", Path: "/type_backup", From: "/type"},
			},
		})

		got := data.PatchReceivingInboundData.ReceivingInbound.Data
		assert.Equal(t, "custom", got["type"])
		assert.Equal(t, "custom", got["type_backup"])
	})

	t.Run("rejects patch that violates schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		// sum has minimum: 0, setting to -1 should fail schema validation
		execErr(te, ctx, patchInboundData, map[string]any{
			"ID": inbound.ID,
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

		execErr(te, ctx, patchInboundData, map[string]any{
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

		// Create inbound as userA (tenantA)
		ctxA := te.ctx(userA)
		inbound := te.newInbound(ctxA, userA).Create()

		// Try to patch as userB (tenantB)
		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchInboundData, map[string]any{
			"ID": inbound.ID,
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

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		// Use a raw template to bypass the typed enum and send an invalid op string.
		invalidOpMutation := resolver.ParseTemplate(`mutation {
			patchReceivingInboundData(id: "{{.ID}}", patches: [
				{ op: INVALID, path: "/sum", value: "1" }
			]) {
				receivingInbound { id data }
			}
		}`)

		execErr(te, ctx, invalidOpMutation, map[string]any{
			"ID": inbound.ID,
		}, "INVALID")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty patches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		te.clearEvents(ctx)

		emptyPatchMutation := resolver.ParseTemplate(`mutation {
			patchReceivingInboundData(id: "{{.ID}}", patches: []) {
				receivingInbound { id data }
			}
		}`)

		execErr(te, ctx, emptyPatchMutation, map[string]any{
			"ID": inbound.ID,
		}, "patches must not be empty")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// INBOUND ITEM PATCH TESTS
// =============================================================================

func TestPatchInboundItemData(t *testing.T) {
	t.Parallel()

	t.Run("replace a field on item", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		item := te.newItem(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		data := execOK[patchInboundItemResult](te, ctx, patchInboundItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"PatchedItem\"`},
			},
		})

		got := data.PatchReceivingInboundItemData.ReceivingInboundItem
		assert.Equal(t, item.ID, got.ID)
		meta := got.Data["meta"].(map[string]any)
		assert.Equal(t, "PatchedItem", meta["name"])

		te.assertEvents(ctx, Update("inbounditem", item.ID))
	})
}

// =============================================================================
// INBOUND SHIPMENT NOTIFICATION PATCH TESTS
// =============================================================================

func TestPatchReceivingInboundShipmentNotificationData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Create()
		te.clearEvents(ctx)

		data := execOK[patchNotificationResult](te, ctx, patchNotificationData, map[string]any{
			"ID": notification.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"PatchedNotif\"`},
			},
		})

		got := data.PatchReceivingInboundShipmentNotificationData.ReceivingInboundShipmentNotification
		assert.Equal(t, notification.ID, got.ID)
		meta := got.Data["meta"].(map[string]any)
		assert.Equal(t, "PatchedNotif", meta["name"])
		assert.InDelta(t, float64(50), meta["weight"], 0)

		te.assertEvents(ctx, Update("inboundshipmentnotification", notification.ID))
	})

	t.Run("rejects patch that violates schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Create()
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

		inbound := te.newInbound(ctx, userA).Create()
		notification := te.newNotification(ctx, userA, inbound.ID).Deleted().Create()
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
		inbound := te.newInbound(ctxA, userA).Create()
		notification := te.newNotification(ctxA, userA, inbound.ID).Create()

		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchNotificationData, map[string]any{
			"ID": notification.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "99"},
			},
		}, "not found")
	})
}

// =============================================================================
// COMBINED MUTATION TESTS
// =============================================================================

var combinedUpdateAndPatch = resolver.ParseTemplate(`mutation {
	updateReceivingInbound(id: "{{.ID}}", input: {
		orderID: "{{.OrderID}}"
		dataTypeID: "{{.DataTypeID}}"
		data: {
			type: "custom",
			sum: {{.Sum}},
			meta: { name: "{{.Name}}", weight: {{.Weight}}, tags: ["a", "b"] }
		}
	}) {
		receivingInbound { id orderID data }
	}
	patchReceivingInboundData(id: "{{.ID}}", patches: [
		{ op: REPLACE, path: "/meta/name", value: "\"{{.PatchName}}\"" }
	]) {
		receivingInbound { id data }
	}
}`)

type combinedResult struct {
	UpdateReceivingInbound struct {
		ReceivingInbound struct {
			ID      uuid.UUID      `json:"id"`
			OrderID string         `json:"orderID"`
			Data    map[string]any `json:"data"`
		} `json:"receivingInbound"`
	} `json:"updateReceivingInbound"`
	PatchReceivingInboundData struct {
		ReceivingInbound struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"receivingInbound"`
	} `json:"patchReceivingInboundData"`
}

func TestCombinedUpdateAndPatch(t *testing.T) {
	t.Parallel()

	t.Run("update native fields and patch data in same transaction", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		inbound := te.newInbound(ctx, userA).OrderID("OLD-ORDER").Create()
		te.clearEvents(ctx)

		data := execOK[combinedResult](te, ctx, combinedUpdateAndPatch, map[string]any{
			"ID":         inbound.ID,
			"OrderID":    "NEW-ORDER",
			"DataTypeID": itemDataTypeID,
			"Sum":        20,
			"Name":       "BeforePatch",
			"Weight":     60,
			"PatchName":  "AfterPatch",
		})

		// The update mutation runs first, then the patch runs on the updated data
		updateResult := data.UpdateReceivingInbound.ReceivingInbound
		assert.Equal(t, "NEW-ORDER", updateResult.OrderID)

		patchResult := data.PatchReceivingInboundData.ReceivingInbound
		meta := patchResult.Data["meta"].(map[string]any)
		// The patch should have replaced the name set by the update
		assert.Equal(t, "AfterPatch", meta["name"])
		// The update's other fields should still be there
		assert.InDelta(t, float64(60), meta["weight"], 0)

		// Verify persisted state
		stored, err := te.Ent.Inbound.Get(ctx, inbound.ID)
		require.NoError(t, err)
		assert.Equal(t, "NEW-ORDER", stored.OrderID)
		storedMeta := stored.Data["meta"].(map[string]any)
		assert.Equal(t, "AfterPatch", storedMeta["name"])

		// Both mutations should emit update events
		te.assertEvents(ctx,
			Update("inbound", inbound.ID),
			Update("inbound", inbound.ID),
		)
	})
}
