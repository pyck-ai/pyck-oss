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
	patchCustomerData = resolver.ParseTemplate(`mutation {
		patchCustomerData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			id data
		}
	}`)

	patchSupplierData = resolver.ParseTemplate(`mutation {
		patchSupplierData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			id data
		}
	}`)
)

type patchCustomerResult struct {
	PatchCustomerData struct {
		ID   uuid.UUID      `json:"id"`
		Data map[string]any `json:"data"`
	} `json:"patchCustomerData"`
}

type patchSupplierResult struct {
	PatchSupplierData struct {
		ID   uuid.UUID      `json:"id"`
		Data map[string]any `json:"data"`
	} `json:"patchSupplierData"`
}

type patch struct {
	Op    string
	Path  string
	Value string
	From  string
}

func TestPatchCustomerData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchCustomerResult](te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Patched\"`},
			},
		})

		meta := data.PatchCustomerData.Data["meta"].(map[string]any)
		assert.Equal(t, "Patched", meta["name"])
		assert.InDelta(t, float64(50), meta["weight"], 0)

		te.assertEvents(ctx, Update("Customer", customer.ID))
	})

	t.Run("add a new field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchCustomerResult](te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "ADD", Path: "/meta/tags/-", Value: `\"new-tag\"`},
			},
		})

		meta := data.PatchCustomerData.Data["meta"].(map[string]any)
		tags := meta["tags"].([]any)
		assert.Len(t, tags, 3)
		assert.Equal(t, "new-tag", tags[2])
	})

	t.Run("remove a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Data(map[string]any{
			"type": "custom", "sum": float64(15),
			"meta":  map[string]any{"name": "Test", "weight": float64(50), "tags": []any{"a"}},
			"extra": "to-be-removed",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchCustomerResult](te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "REMOVE", Path: "/extra"},
			},
		})

		_, hasExtra := data.PatchCustomerData.Data["extra"]
		assert.False(t, hasExtra)
		assert.Equal(t, "custom", data.PatchCustomerData.Data["type"])
	})

	t.Run("multiple operations in one request", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchCustomerResult](te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/sum", Value: "99"},
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Multi\"`},
			},
		})

		got := data.PatchCustomerData.Data
		assert.InDelta(t, float64(99), got["sum"], 0)
		meta := got["meta"].(map[string]any)
		assert.Equal(t, "Multi", meta["name"])
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchCustomerResult](te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"custom\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		})

		assert.InDelta(t, float64(42), data.PatchCustomerData.Data["sum"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.Customer.Get(ctx, customer.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(15), stored.Data["sum"], 0)
		te.assertNoEvents(ctx)
	})

	t.Run("move operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Data(map[string]any{
			"type": "custom", "sum": float64(15), "old_note": "hello",
			"meta": map[string]any{"name": "Test", "weight": float64(50), "tags": []any{"a"}},
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchCustomerResult](te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "MOVE", Path: "/note", From: "/old_note"},
			},
		})

		got := data.PatchCustomerData.Data
		assert.Equal(t, "hello", got["note"])
		_, hasOld := got["old_note"]
		assert.False(t, hasOld)
	})

	t.Run("copy operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchCustomerResult](te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
			"Patches": []patch{
				{Op: "COPY", Path: "/type_backup", From: "/type"},
			},
		})

		got := data.PatchCustomerData.Data
		assert.Equal(t, "custom", got["type"])
		assert.Equal(t, "custom", got["type_backup"])
	})

	t.Run("rejects patch that violates schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchCustomerData, map[string]any{
			"ID": customer.ID,
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

		execErr(te, ctx, patchCustomerData, map[string]any{
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
		customer := te.newCustomer(ctxA, userA).Create()

		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchCustomerData, map[string]any{
			"ID": customer.ID,
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

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		invalidOp := resolver.ParseTemplate(`mutation {
			patchCustomerData(id: "{{.ID}}", patches: [
				{ op: INVALID, path: "/sum", value: "1" }
			]) { id }
		}`)

		execErr(te, ctx, invalidOp, map[string]any{"ID": customer.ID}, "INVALID")
		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty patches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		customer := te.newCustomer(ctx, userA).Create()
		te.clearEvents(ctx)

		emptyPatches := resolver.ParseTemplate(`mutation {
			patchCustomerData(id: "{{.ID}}", patches: []) { id }
		}`)

		execErr(te, ctx, emptyPatches, map[string]any{"ID": customer.ID}, "patches must not be empty")
		te.assertNoEvents(ctx)
	})
}

func TestPatchSupplierData(t *testing.T) {
	t.Parallel()

	t.Run("replace a nested field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchSupplierResult](te, ctx, patchSupplierData, map[string]any{
			"ID": supplier.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/meta/name", Value: `\"Patched\"`},
			},
		})

		meta := data.PatchSupplierData.Data["meta"].(map[string]any)
		assert.Equal(t, "Patched", meta["name"])

		te.assertEvents(ctx, Update("Supplier", supplier.ID))
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[patchSupplierResult](te, ctx, patchSupplierData, map[string]any{
			"ID": supplier.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"custom\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		})

		assert.InDelta(t, float64(42), data.PatchSupplierData.Data["sum"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		supplier := te.newSupplier(ctx, userA).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchSupplierData, map[string]any{
			"ID": supplier.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/type", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/sum", Value: "42"},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.Supplier.Get(ctx, supplier.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(15), stored.Data["sum"], 0)
		te.assertNoEvents(ctx)
	})
}
