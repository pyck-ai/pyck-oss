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
	patchItemData = resolver.ParseTemplate(`mutation {
		patchInventoryItemData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			inventoryItem { id data }
		}
	}`)

	patchRepositoryData = resolver.ParseTemplate(`mutation {
		patchInventoryRepositoryData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			inventoryRepository { id data }
		}
	}`)

	patchItemSetData = resolver.ParseTemplate(`mutation {
		patchInventoryItemSetData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			inventoryItemSet { id data }
		}
	}`)
)

type patchItemResult struct {
	PatchInventoryItemData struct {
		InventoryItem struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"inventoryItem"`
	} `json:"patchInventoryItemData"`
}

type patchRepositoryResult struct {
	PatchInventoryRepositoryData struct {
		InventoryRepository struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"inventoryRepository"`
	} `json:"patchInventoryRepositoryData"`
}

type patchItemSetResult struct {
	PatchInventoryItemSetData struct {
		InventoryItemSet struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"inventoryItemSet"`
	} `json:"patchInventoryItemSetData"`
}

type patch struct {
	Op    string
	Path  string
	Value string
	From  string
}

func TestPatchInventoryItemData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemResult](te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Patched\"`},
			},
		})

		meta := data.PatchInventoryItemData.InventoryItem.Data["meta"].(map[string]any)
		assert.Equal(t, "Patched", meta["name"])
		assert.InDelta(t, float64(50), meta["weight"], 0)

		te.assertEvents(ctx, Update("item", item.ID))
	})

	t.Run("add a new field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemResult](te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "ADD", Path: "/meta/tags/-", Value: `\"new-tag\"`},
			},
		})

		meta := data.PatchInventoryItemData.InventoryItem.Data["meta"].(map[string]any)
		tags := meta["tags"].([]any)
		assert.Len(t, tags, 3)
		assert.Equal(t, "new-tag", tags[2])
	})

	t.Run("remove a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Data(map[string]any{
			"type": "custom", "sum": float64(15),
			"meta":  map[string]any{"name": "Test", "weight": float64(50), "tags": []any{"a"}},
			"extra": "to-be-removed",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemResult](te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "REMOVE", Path: "/extra"},
			},
		})

		_, hasExtra := data.PatchInventoryItemData.InventoryItem.Data["extra"]
		assert.False(t, hasExtra)
		assert.Equal(t, "custom", data.PatchInventoryItemData.InventoryItem.Data["type"])
	})

	t.Run("multiple operations in one request", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemResult](te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "99"},
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Multi\"`},
			},
		})

		got := data.PatchInventoryItemData.InventoryItem.Data
		assert.InDelta(t, float64(99), got["sum"], 0)
		meta := got["meta"].(map[string]any)
		assert.Equal(t, "Multi", meta["name"])
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemResult](te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"custom\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		})

		assert.InDelta(t, float64(42), data.PatchInventoryItemData.InventoryItem.Data["sum"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.Item.Get(ctx, item.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(15), stored.Data["sum"], 0)
		te.assertNoEvents(ctx)
	})

	t.Run("move operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Data(map[string]any{
			"type": "custom", "sum": float64(15), "old_note": "hello",
			"meta": map[string]any{"name": "Test", "weight": float64(50), "tags": []any{"a"}},
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemResult](te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "MOVE", Path: "/note", From: "/old_note"},
			},
		})

		got := data.PatchInventoryItemData.InventoryItem.Data
		assert.Equal(t, "hello", got["note"])
		_, hasOld := got["old_note"]
		assert.False(t, hasOld)
	})

	t.Run("copy operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemResult](te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
			"Patches": []patch{
				{Op: "COPY", Path: "/type_backup", From: "/type"},
			},
		})

		got := data.PatchInventoryItemData.InventoryItem.Data
		assert.Equal(t, "custom", got["type"])
		assert.Equal(t, "custom", got["type_backup"])
	})

	t.Run("rejects patch that violates schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchItemData, map[string]any{
			"ID": item.ID,
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

		execErr(te, ctx, patchItemData, map[string]any{
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
		item := te.newItem(ctxA, userA).Create()

		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchItemData, map[string]any{
			"ID": item.ID,
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

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		invalidOp := resolver.ParseTemplate(`mutation {
			patchInventoryItemData(id: "{{.ID}}", patches: [
				{ op: INVALID, path: "/sum", value: "1" }
			]) { inventoryItem { id } }
		}`)

		execErr(te, ctx, invalidOp, map[string]any{"ID": item.ID}, "INVALID")
		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty patches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		item := te.newItem(ctx, userA).Create()
		te.clearEvents(ctx)

		emptyPatches := resolver.ParseTemplate(`mutation {
			patchInventoryItemData(id: "{{.ID}}", patches: []) {
				inventoryItem { id }
			}
		}`)

		execErr(te, ctx, emptyPatches, map[string]any{"ID": item.ID}, "patches must not be empty")
		te.assertNoEvents(ctx)
	})
}

func TestPatchInventoryRepositoryData(t *testing.T) {
	t.Parallel()

	t.Run("replace a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchRepositoryResult](te, ctx, patchRepositoryData, map[string]any{
			"ID": repo.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"PatchedRepo\"`},
			},
		})

		meta := data.PatchInventoryRepositoryData.InventoryRepository.Data["meta"].(map[string]any)
		assert.Equal(t, "PatchedRepo", meta["name"])

		te.assertEvents(ctx, Update("repository", repo.ID))
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchRepositoryResult](te, ctx, patchRepositoryData, map[string]any{
			"ID": repo.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"custom\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		})

		assert.InDelta(t, float64(42), data.PatchInventoryRepositoryData.InventoryRepository.Data["sum"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		repo := te.newRepository(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchRepositoryData, map[string]any{
			"ID": repo.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.Repository.Get(ctx, repo.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(15), stored.Data["sum"], 0)
		te.assertNoEvents(ctx)
	})
}

func TestPatchInventoryItemSetData(t *testing.T) {
	t.Parallel()

	t.Run("replace a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		itemSet := te.newItemSet(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemSetResult](te, ctx, patchItemSetData, map[string]any{
			"ID": itemSet.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"PatchedSet\"`},
			},
		})

		meta := data.PatchInventoryItemSetData.InventoryItemSet.Data["meta"].(map[string]any)
		assert.Equal(t, "PatchedSet", meta["name"])

		te.assertEvents(ctx, Update("itemset", itemSet.ID))
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		itemSet := te.newItemSet(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchItemSetResult](te, ctx, patchItemSetData, map[string]any{
			"ID": itemSet.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"custom\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		})

		assert.InDelta(t, float64(42), data.PatchInventoryItemSetData.InventoryItemSet.Data["sum"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		itemSet := te.newItemSet(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchItemSetData, map[string]any{
			"ID": itemSet.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.ItemSet.Get(ctx, itemSet.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(15), stored.Data["sum"], 0)
		te.assertNoEvents(ctx)
	})
}
