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
	createDevice = resolver.ParseTemplate(`mutation {
		createDevice(input: {
			name: "{{.Name}}"
		}) {
			device { id tenantID name }
		}
	}`)

	createDeviceWithData = resolver.ParseTemplate(`mutation {
		createDevice(input: {
			name: "{{.Name}}",
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: { type:"custom", meta: { brand:"Acme", weight: 2 } }
		}) {
			device { id tenantID name }
		}
	}`)

	updateDevice = resolver.ParseTemplate(`mutation {
		updateDevice(
			id: "{{.ID}}",
			input: { name: "{{.Name}}" }
		) {
			device { id tenantID name }
		}
	}`)

	updateDeviceWithData = resolver.ParseTemplate(`mutation {
		updateDevice(
			id: "{{.ID}}",
			input: {
				name: "{{.Name}}",
				{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
				data: { type:"custom", meta: { brand:"Acme", weight: 3 } }
			}
		) {
			device { id tenantID name }
		}
	}`)

	deleteDevice = resolver.ParseTemplate(`mutation {
		deleteDevice(id: "{{.ID}}") {
			deletedID
		}
	}`)

	queryDevicesJSONOrder = resolver.ParseTemplate(`query {
		devices(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id tenantID name data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type deviceNode struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
	Data     map[string]any
}

type createDeviceData struct {
	CreateDevice struct{ Device deviceNode }
}

type updateDeviceData struct {
	UpdateDevice struct{ Device deviceNode }
}

type deleteDeviceData struct {
	DeleteDevice struct{ DeletedID uuid.UUID }
}

type queryDevicesData struct {
	Devices struct {
		TotalCount int
		Edges      []struct{ Node deviceNode }
		PageInfo   struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestDevice_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates device successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createDeviceData](te, ctx, createDevice, map[string]any{
			"Name": "Laptop-1",
		})

		created := data.CreateDevice.Device
		assert.Equal(t, "Laptop-1", created.Name)
		assert.Equal(t, resolver.TenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.Device.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "Laptop-1", stored.Name)

		// Verify event
		te.assertEvents(ctx, Create("device", created.ID))
	})

	t.Run("rejects create with data but missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDeviceWithData, map[string]any{
			"Name": "Laptop-X",
		}, "data type not set")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestDevice_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates device name", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Name("dev").Create()
		te.clearEvents(ctx)

		data := execOK[updateDeviceData](te, ctx, updateDevice, map[string]any{
			"ID":   dev.ID,
			"Name": "Laptop-2",
		})

		assert.Equal(t, "Laptop-2", data.UpdateDevice.Device.Name)
		te.assertEvents(ctx, Update("device", dev.ID))
	})

	t.Run("rejects update of other tenant's device", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dev := te.newDevice(ctxB, userB).Name("dev").Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateDevice, map[string]any{
			"ID":   dev.ID,
			"Name": "X",
		}, "device not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects update with data but missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Name("dev").Create()
		te.clearEvents(ctx)

		execErr(te, ctx, updateDeviceWithData, map[string]any{
			"ID":   dev.ID,
			"Name": "dev-2",
		}, "data type not set")

		// Verify not updated
		stored, err := te.Ent.Device.Get(ctx, dev.ID)
		require.NoError(t, err)
		assert.Equal(t, "dev", stored.Name)

		te.assertNoEvents(ctx)
	})

	t.Run("rejects update of non-existent device", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, updateDevice, map[string]any{
			"ID":   uuid.New(),
			"Name": "X",
		}, "device not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestDevice_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes device", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Name("dev").Create()
		te.clearEvents(ctx)

		data := execOK[deleteDeviceData](te, ctx, deleteDevice, map[string]any{
			"ID": dev.ID,
		})

		assert.Equal(t, dev.ID, data.DeleteDevice.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.Device.Get(te.ctxWithDeleted(userA), dev.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)

		te.assertEvents(ctx, Delete("device", dev.ID))
	})

	t.Run("rejects delete of other tenant's device", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dev := te.newDevice(ctxB, userB).Name("foreign").Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteDevice, map[string]any{
			"ID": dev.ID,
		}, "device not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects delete of non-existent device", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, deleteDevice, map[string]any{
			"ID": uuid.New(),
		}, "device not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestDevice_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		d1 := te.newDevice(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		d2 := te.newDevice(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		d3 := te.newDevice(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryDevicesData](te, ctx, queryDevicesJSONOrder, map[string]any{
			"JSONPath": "sum",
		})

		require.Equal(t, 3, data.Devices.TotalCount)
		assert.Equal(t, d2.ID, data.Devices.Edges[0].Node.ID)
		assert.Equal(t, d3.ID, data.Devices.Edges[1].Node.ID)
		assert.Equal(t, d1.ID, data.Devices.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		d1 := te.newDevice(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		d2 := te.newDevice(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryDevicesData](te, ctx, queryDevicesJSONOrder, map[string]any{
			"JSONPath":  "meta.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Devices.TotalCount)
		assert.Equal(t, d2.ID, data.Devices.Edges[0].Node.ID)
		assert.Equal(t, d1.ID, data.Devices.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		d1 := te.newDevice(ctx, userA).Create()
		d2 := te.newDevice(ctx, userA).Create()

		data := execOK[queryDevicesData](te, ctx, queryDevicesJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.Devices.TotalCount)
		assert.Equal(t, d2.ID, data.Devices.Edges[0].Node.ID)
		assert.Equal(t, d1.ID, data.Devices.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestDevice_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newDevice(ctx, userA).Data(map[string]any{"type": "scanner"}).Create()
		d2 := te.newDevice(ctx, userA).Data(map[string]any{"type": "printer"}).Create()

		data := execOK[queryDevicesData](te, ctx, queryDevicesJSONOrder, map[string]any{
			"Where": `{ Data: ["type", "printer"] }`,
		})

		require.Equal(t, 1, data.Devices.TotalCount)
		assert.Equal(t, d2.ID, data.Devices.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newDevice(ctx, userA).Data(map[string]any{"type": "scanner"}).Create()
		d2 := te.newDevice(ctx, userA).Data(map[string]any{"type": "printer", "firmware": "v2.1"}).Create()

		data := execOK[queryDevicesData](te, ctx, queryDevicesJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "firmware" }`,
		})

		require.Equal(t, 1, data.Devices.TotalCount)
		assert.Equal(t, d2.ID, data.Devices.Edges[0].Node.ID)
	})
}
