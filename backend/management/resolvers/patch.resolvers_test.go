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
	patchLocationData = resolver.ParseTemplate(`mutation {
		patchLocationData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			location { id data }
		}
	}`)

	patchDeviceData = resolver.ParseTemplate(`mutation {
		patchDeviceData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			device { id data }
		}
	}`)
)

type patchLocationResult struct {
	PatchLocationData struct {
		Location struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"location"`
	} `json:"patchLocationData"`
}

type patchDeviceResult struct {
	PatchDeviceData struct {
		Device struct {
			ID   uuid.UUID      `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"device"`
	} `json:"patchDeviceData"`
}

type patch struct {
	Op    string
	Path  string
	Value string
	From  string
}

func TestPatchLocationData(t *testing.T) {
	t.Parallel()

	t.Run("replace a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"floor": float64(3), "zone": "A", "capacity": float64(100),
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchLocationResult](te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/zone", Value: `\"B\"`},
			},
		})

		got := data.PatchLocationData.Location.Data
		assert.Equal(t, "B", got["zone"])
		assert.InDelta(t, float64(3), got["floor"], 0)

		te.assertEvents(ctx, Update("location", location.ID))
	})

	t.Run("add a new field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"floor": float64(1),
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchLocationResult](te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "ADD", Path: "/aisle", Value: `\"12\"`},
			},
		})

		got := data.PatchLocationData.Location.Data
		assert.Equal(t, "12", got["aisle"])
		assert.InDelta(t, float64(1), got["floor"], 0)
	})

	t.Run("remove a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"floor": float64(1), "temp": "remove-me",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchLocationResult](te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "REMOVE", Path: "/temp"},
			},
		})

		got := data.PatchLocationData.Location.Data
		_, hasTemp := got["temp"]
		assert.False(t, hasTemp)
		assert.InDelta(t, float64(1), got["floor"], 0)
	})

	t.Run("multiple operations in one request", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"floor": float64(1), "zone": "A",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchLocationResult](te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/floor", Value: "5"},
				{Op: "REPLACE", Path: "/zone", Value: `\"C\"`},
				{Op: "ADD", Path: "/aisle", Value: `\"7\"`},
			},
		})

		got := data.PatchLocationData.Location.Data
		assert.InDelta(t, float64(5), got["floor"], 0)
		assert.Equal(t, "C", got["zone"])
		assert.Equal(t, "7", got["aisle"])
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"floor": float64(3), "zone": "A",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchLocationResult](te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/zone", Value: `\"A\"`},
				{Op: "REPLACE", Path: "/floor", Value: "10"},
			},
		})

		assert.InDelta(t, float64(10), data.PatchLocationData.Location.Data["floor"], 0)
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"floor": float64(3), "zone": "A",
		}).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/zone", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/floor", Value: "10"},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.Location.Get(ctx, location.ID)
		require.NoError(t, err)
		assert.InDelta(t, float64(3), stored.Data["floor"], 0)
		te.assertNoEvents(ctx)
	})

	t.Run("move operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"old_zone": "A",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchLocationResult](te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "MOVE", Path: "/zone", From: "/old_zone"},
			},
		})

		got := data.PatchLocationData.Location.Data
		assert.Equal(t, "A", got["zone"])
		_, hasOld := got["old_zone"]
		assert.False(t, hasOld)
	})

	t.Run("copy operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{
			"zone": "A",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchLocationResult](te, ctx, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "COPY", Path: "/zone_backup", From: "/zone"},
			},
		})

		got := data.PatchLocationData.Location.Data
		assert.Equal(t, "A", got["zone"])
		assert.Equal(t, "A", got["zone_backup"])
	})

	t.Run("rejects patch on nonexistent entity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, patchLocationData, map[string]any{
			"ID": uuid.New(),
			"Patches": []patch{
				{Op: "REPLACE", Path: "/zone", Value: `\"X\"`},
			},
		}, "not found")
	})

	t.Run("tenant isolation: cannot patch other tenant entity", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		location := te.newLocation(ctxA, userA).Data(map[string]any{"zone": "A"}).Create()

		ctxB := te.ctx(userB)
		execErr(te, ctxB, patchLocationData, map[string]any{
			"ID": location.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/zone", Value: `\"X\"`},
			},
		}, "not found")
	})

	t.Run("rejects invalid operation", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{"zone": "A"}).Create()
		te.clearEvents(ctx)

		invalidOp := resolver.ParseTemplate(`mutation {
			patchLocationData(id: "{{.ID}}", patches: [
				{ op: INVALID, path: "/zone", value: "\"X\"" }
			]) { location { id } }
		}`)

		execErr(te, ctx, invalidOp, map[string]any{"ID": location.ID}, "INVALID")
		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty patches", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		location := te.newLocation(ctx, userA).Data(map[string]any{"zone": "A"}).Create()
		te.clearEvents(ctx)

		emptyPatches := resolver.ParseTemplate(`mutation {
			patchLocationData(id: "{{.ID}}", patches: []) { location { id } }
		}`)

		execErr(te, ctx, emptyPatches, map[string]any{"ID": location.ID}, "patches must not be empty")
		te.assertNoEvents(ctx)
	})
}

func TestPatchDeviceData(t *testing.T) {
	t.Parallel()

	t.Run("replace a field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		device := te.newDevice(ctx, userA).Data(map[string]any{
			"model": "Scanner-X", "firmware": "1.0",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchDeviceResult](te, ctx, patchDeviceData, map[string]any{
			"ID": device.ID,
			"Patches": []patch{
				{Op: "REPLACE", Path: "/firmware", Value: `\"2.0\"`},
			},
		})

		got := data.PatchDeviceData.Device.Data
		assert.Equal(t, "2.0", got["firmware"])
		assert.Equal(t, "Scanner-X", got["model"])

		te.assertEvents(ctx, Update("device", device.ID))
	})

	t.Run("test operation succeeds then applies", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		device := te.newDevice(ctx, userA).Data(map[string]any{
			"model": "Scanner-X", "firmware": "1.0",
		}).Create()
		te.clearEvents(ctx)

		data := execOK[patchDeviceResult](te, ctx, patchDeviceData, map[string]any{
			"ID": device.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/model", Value: `\"Scanner-X\"`},
				{Op: "REPLACE", Path: "/firmware", Value: `\"3.0\"`},
			},
		})

		assert.Equal(t, "3.0", data.PatchDeviceData.Device.Data["firmware"])
	})

	t.Run("test operation fails rejects entire patch", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		device := te.newDevice(ctx, userA).Data(map[string]any{
			"model": "Scanner-X", "firmware": "1.0",
		}).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, patchDeviceData, map[string]any{
			"ID": device.ID,
			"Patches": []patch{
				{Op: "TEST", Path: "/model", Value: `\"wrong\"`},
				{Op: "REPLACE", Path: "/firmware", Value: `\"3.0\"`},
			},
		}, "failed to apply patch")

		stored, err := te.Ent.Device.Get(ctx, device.ID)
		require.NoError(t, err)
		assert.Equal(t, "1.0", stored.Data["firmware"])
		te.assertNoEvents(ctx)
	})
}
