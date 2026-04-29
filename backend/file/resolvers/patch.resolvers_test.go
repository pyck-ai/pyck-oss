package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

var patchFileData = resolver.ParseTemplate(`mutation {
	patchFileData(id: "{{.ID}}", patches: [
		{{range $i, $p := .Patches}}{{if $i}},{{end}}
		{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
		{{end}}
	]) {
		id data
	}
}`)

type patchFileResult struct {
	PatchFileData struct {
		ID   uuid.UUID      `json:"id"`
		Data map[string]any `json:"data"`
	} `json:"patchFileData"`
}

type patch struct {
	Op    string
	Path  string
	Value string
	From  string
}

func TestPatchFileData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		data := execOK[patchFileResult](te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Patched\"`},
			},
		})

		meta := data.PatchFileData.Data["meta"].(map[string]any)
		assert.Equal(t, "Patched", meta["name"])

		te.assertEvents(ctx, Update("file", f.ID))
	})

	t.Run("add a new field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		data := execOK[patchFileResult](te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "ADD", Path: "/meta/tags/-", Value: `\"new-tag\"`},
			},
		})

		meta := data.PatchFileData.Data["meta"].(map[string]any)
		tags := meta["tags"].([]any)
		assert.Len(t, tags, 3)
		assert.Equal(t, "new-tag", tags[2])
	})

	t.Run("multiple operations in one request", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		data := execOK[patchFileResult](te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/type", Value: `\"document\"`},
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Multi\"`},
			},
		})

		got := data.PatchFileData.Data
		assert.Equal(t, "document", got["type"])
		meta := got["meta"].(map[string]any)
		assert.Equal(t, "Multi", meta["name"])
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		data := execOK[patchFileResult](te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"supplier\"`},
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Conditional\"`},
			},
		})

		meta := data.PatchFileData.Data["meta"].(map[string]any)
		assert.Equal(t, "Conditional", meta["name"])
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/meta/name", Value: `\"ShouldNotApply\"`},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.File.Get(ctx, f.ID)
		require.NoError(t, err)
		storedMeta := stored.Data["meta"].(map[string]any)
		assert.Equal(t, "Testfile", storedMeta["name"])
		te.assertNoEvents(ctx)
	})

	t.Run("move operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		fileData := map[string]any{
			"type": "supplier", "old_note": "hello",
			"meta": map[string]any{"name": "Test", "tags": []any{"a"}},
		}
		f := te.newFile(ctx, userA).Data(fileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		data := execOK[patchFileResult](te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "MOVE", Path: "/note", From: "/old_note"},
			},
		})

		got := data.PatchFileData.Data
		assert.Equal(t, "hello", got["note"])
		_, hasOld := got["old_note"]
		assert.False(t, hasOld)
	})

	t.Run("copy operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		data := execOK[patchFileResult](te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "COPY", Path: "/type_backup", From: "/type"},
			},
		})

		got := data.PatchFileData.Data
		assert.Equal(t, "supplier", got["type"])
		assert.Equal(t, "supplier", got["type_backup"])
	})

	t.Run("rejects patch that violates schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "REMOVE", Path: "/type"},
			},
		}, "validate patched data")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects patch on nonexistent entity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, patchFileData, map[string]any{
			"ID": uuid.New(),
			"Patches": []patch{
				{Op: "REPLACE", Path: "/type", Value: `\"x\"`},
			},
		}, "not found")
	})

	t.Run("tenant isolation: cannot patch other tenant entity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		f := te.newFile(ctxA, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()

		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchFileData, map[string]any{
			"ID": f.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/type", Value: `\"x\"`},
			},
		}, "not found")
	})

	t.Run("rejects invalid operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		invalidOp := resolver.ParseTemplate(`mutation {
			patchFileData(id: "{{.ID}}", patches: [
				{ op: INVALID, path: "/type", value: "\"x\"" }
			]) { id }
		}`)

		execErr(te, ctx, invalidOp, map[string]any{"ID": f.ID}, "INVALID")
		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty patches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Data(validFileData).DataTypeID(fileDataTypeID).Create()
		te.clearEvents(ctx)

		emptyPatches := resolver.ParseTemplate(`mutation {
			patchFileData(id: "{{.ID}}", patches: []) { id }
		}`)

		execErr(te, ctx, emptyPatches, map[string]any{"ID": f.ID}, "patches must not be empty")
		te.assertNoEvents(ctx)
	})
}
