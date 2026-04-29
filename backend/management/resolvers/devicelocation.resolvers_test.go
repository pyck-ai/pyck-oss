package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	setDeviceLocation = resolver.ParseTemplate(`mutation {
		setDeviceLocation(input: {
			deviceID: "{{.DeviceID}}",
			locationID: "{{.LocationID}}"
		}) {
			DeviceLocation { id deviceID locationID }
		}
	}`)

	setDeviceLocationWithData = resolver.ParseTemplate(`mutation {
		setDeviceLocation(input: {
			deviceID: "{{.DeviceID}}",
			locationID: "{{.LocationID}}",
			{{if .DataTypeID}}dataTypeID: "{{.DataTypeID}}",{{end}}
			data: { type:"custom", props: { assigned:true } }
		}) {
			DeviceLocation { id deviceID locationID }
		}
	}`)

	unsetDeviceLocation = resolver.ParseTemplate(`mutation {
		unsetDeviceLocation(id: "{{.ID}}") {
			deletedID
		}
	}`)

	checkInUserDevice = resolver.ParseTemplate(`mutation {
		checkInUserDevice(input: {
			deviceID: "{{.DeviceID}}"
		}) {
			deviceUser { id deviceID userID }
		}
	}`)

	checkOutUserDevice = resolver.ParseTemplate(`mutation {
		checkOutUserDevice(input: {
			deviceID: "{{.DeviceID}}"
		}) {
			deviceUser { id deviceID userID }
		}
	}`)

	queryDeviceLocationsJSONOrder = resolver.ParseTemplate(`query {
		deviceLocations(
			first: {{or .First 100}},
			orderBy: {
				direction: {{or .Direction "ASC"}}
				{{- if .JSONPath}}, jsonPath: "{{.JSONPath}}"{{end}}
				{{- if .Field}}, field: {{.Field}}{{end}}
			}
			{{- if .Where}}, where: {{.Where}}{{end}}
		) {
			totalCount
			edges { node { id deviceID locationID data } }
			pageInfo { hasNextPage endCursor }
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type deviceLocationNode struct {
	ID         uuid.UUID
	DeviceID   uuid.UUID
	LocationID uuid.UUID
}

type setDeviceLocationData struct {
	SetDeviceLocation struct{ DeviceLocation deviceLocationNode }
}

type unsetDeviceLocationData struct {
	UnsetDeviceLocation struct{ DeletedID uuid.UUID }
}

type deviceUserNode struct {
	ID       uuid.UUID
	DeviceID uuid.UUID
	UserID   uuid.UUID
}

type checkInUserDeviceData struct {
	CheckInUserDevice struct{ DeviceUser deviceUserNode }
}

type checkOutUserDeviceData struct {
	CheckOutUserDevice struct{ DeviceUser []deviceUserNode }
}

type queryDeviceLocationsData struct {
	DeviceLocations struct {
		TotalCount int
		Edges      []struct {
			Node struct {
				ID         uuid.UUID
				DeviceID   uuid.UUID
				LocationID uuid.UUID
				Data       map[string]any
			}
		}
		PageInfo struct {
			HasNextPage bool
			EndCursor   *string
		}
	}
}

// =============================================================================
// SET DEVICE LOCATION TESTS
// =============================================================================

func TestDeviceLocation_Set(t *testing.T) {
	t.Parallel()

	t.Run("sets device location successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		loc := te.newLocation(ctx, userA).Name("loc").Create()
		dev := te.newDevice(ctx, userA).Name("dev").Create()
		te.clearEvents(ctx)

		data := execOK[setDeviceLocationData](te, ctx, setDeviceLocation, map[string]any{
			"DeviceID":   dev.ID,
			"LocationID": loc.ID,
		})

		dl := data.SetDeviceLocation.DeviceLocation
		assert.Equal(t, dev.ID, dl.DeviceID)
		assert.Equal(t, loc.ID, dl.LocationID)
		assert.NotEqual(t, uuid.Nil, dl.ID)

		// Verify event
		te.assertEvents(ctx, Create("devicelocation", dl.ID))
	})

	t.Run("rejects set with data but missing dataTypeID", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		loc := te.newLocation(ctx, userA).Name("loc").Create()
		dev := te.newDevice(ctx, userA).Name("dev").Create()
		te.clearEvents(ctx)

		execErr(te, ctx, setDeviceLocationWithData, map[string]any{
			"DeviceID":   dev.ID,
			"LocationID": loc.ID,
		}, "data type not set")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UNSET DEVICE LOCATION TESTS
// =============================================================================

func TestDeviceLocation_Unset(t *testing.T) {
	t.Parallel()

	t.Run("unsets device location successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		loc := te.newLocation(ctx, userA).Name("loc").Create()
		dev := te.newDevice(ctx, userA).Name("dev").Create()
		dl := te.newDeviceLocation(ctx, userA, dev.ID, loc.ID).Create()
		te.clearEvents(ctx)

		data := execOK[unsetDeviceLocationData](te, ctx, unsetDeviceLocation, map[string]any{
			"ID": dl.ID,
		})

		assert.Equal(t, dl.ID, data.UnsetDeviceLocation.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.DeviceLocation.Get(te.ctxWithDeleted(userA), dl.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("devicelocation", dl.ID))
	})

	t.Run("rejects unset of non-existent device location", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, unsetDeviceLocation, map[string]any{
			"ID": uuid.New(),
		}, "device_location not found")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// CHECK IN USER DEVICE TESTS
// =============================================================================

func TestDeviceUser_CheckIn(t *testing.T) {
	t.Parallel()

	t.Run("checks in user to device successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Name("dev").Create()
		loc := te.newLocation(ctx, userA).Name("loc").Create()
		te.newDeviceLocation(ctx, userA, dev.ID, loc.ID).Create()
		te.newTenant(ctx, resolver.TenantA).Create()
		te.newUser(ctx, userA).ID(userA.ID).Create()
		te.clearEvents(ctx)

		data := execOK[checkInUserDeviceData](te, ctx, checkInUserDevice, map[string]any{
			"DeviceID": dev.ID,
		})

		du := data.CheckInUserDevice.DeviceUser
		assert.Equal(t, dev.ID, du.DeviceID)
		assert.Equal(t, userA.ID, du.UserID)

		// Verify event
		te.assertEvents(ctx, Create("deviceuser", du.ID))
	})

	t.Run("rejects check-in without device location", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Name("dev").Create()
		te.newTenant(ctx, resolver.TenantA).Create()
		te.newUser(ctx, userA).ID(userA.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, checkInUserDevice, map[string]any{
			"DeviceID": dev.ID,
		}, "device must be associated with a location before check-in")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects check-in when another user is checked in", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Name("dev").Create()
		loc := te.newLocation(ctx, userA).Name("loc").Create()
		te.newDeviceLocation(ctx, userA, dev.ID, loc.ID).Create()
		te.newTenant(ctx, resolver.TenantA).Create()
		te.newUser(ctx, userA).ID(userA.ID).Username("admin-user").Email("admin@test.com").Create()

		// Create another user and check them in
		otherUser := te.newUser(ctx, userA).Create()
		te.newDeviceUser(ctx, userA, dev.ID, otherUser.ID).Create()
		te.clearEvents(ctx)

		execErr(te, ctx, checkInUserDevice, map[string]any{
			"DeviceID": dev.ID,
		}, "another user is already checked in on this device")

		te.assertNoEvents(ctx)
	})

	t.Run("allows same user on multiple devices", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev1 := te.newDevice(ctx, userA).Name("dev1").Create()
		dev2 := te.newDevice(ctx, userA).Name("dev2").Create()
		loc1 := te.newLocation(ctx, userA).Name("loc1").Create()
		loc2 := te.newLocation(ctx, userA).Name("loc2").Create()
		te.newDeviceLocation(ctx, userA, dev1.ID, loc1.ID).Create()
		te.newDeviceLocation(ctx, userA, dev2.ID, loc2.ID).Create()
		te.newTenant(ctx, resolver.TenantA).Create()
		te.newUser(ctx, userA).ID(userA.ID).Create()
		te.clearEvents(ctx)

		// Check in to first device
		data1 := execOK[checkInUserDeviceData](te, ctx, checkInUserDevice, map[string]any{
			"DeviceID": dev1.ID,
		})
		assert.Equal(t, dev1.ID, data1.CheckInUserDevice.DeviceUser.DeviceID)

		// Check in to second device
		data2 := execOK[checkInUserDeviceData](te, ctx, checkInUserDevice, map[string]any{
			"DeviceID": dev2.ID,
		})
		assert.Equal(t, dev2.ID, data2.CheckInUserDevice.DeviceUser.DeviceID)
	})
}

// =============================================================================
// CHECK OUT USER DEVICE TESTS
// =============================================================================

func TestDeviceUser_CheckOut(t *testing.T) {
	t.Parallel()

	t.Run("checks out user from device successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newTenant(ctx, resolver.TenantA).Create()
		te.newUser(ctx, userA).ID(userA.ID).Create()
		dev := te.newDevice(ctx, userA).Name("dev").Create()
		du := te.newDeviceUser(ctx, userA, dev.ID, userA.ID).Create()
		te.clearEvents(ctx)

		data := execOK[checkOutUserDeviceData](te, ctx, checkOutUserDevice, map[string]any{
			"DeviceID": dev.ID,
		})

		require.Len(t, data.CheckOutUserDevice.DeviceUser, 1)
		assert.Equal(t, du.ID, data.CheckOutUserDevice.DeviceUser[0].ID)

		// Verify soft-deleted
		deleted, err := te.Ent.DeviceUser.Get(te.ctxWithDeleted(userA), du.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("deviceuser", du.ID))
	})

	t.Run("returns empty when no active assignments", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newTenant(ctx, resolver.TenantA).Create()
		te.newUser(ctx, userA).ID(userA.ID).Create()
		dev := te.newDevice(ctx, userA).Name("dev").Create()
		te.clearEvents(ctx)

		data := execOK[checkOutUserDeviceData](te, ctx, checkOutUserDevice, map[string]any{
			"DeviceID": dev.ID,
		})

		assert.Empty(t, data.CheckOutUserDevice.DeviceUser)
		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestDeviceLocation_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Create()
		loc1 := te.newLocation(ctx, userA).Create()
		loc2 := te.newLocation(ctx, userA).Create()
		loc3 := te.newLocation(ctx, userA).Create()

		dl1 := te.newDeviceLocation(ctx, userA, dev.ID, loc1.ID).Data(map[string]any{"priority": float64(30)}).Create()
		dl2 := te.newDeviceLocation(ctx, userA, dev.ID, loc2.ID).Data(map[string]any{"priority": float64(10)}).Create()
		dl3 := te.newDeviceLocation(ctx, userA, dev.ID, loc3.ID).Data(map[string]any{"priority": float64(20)}).Create()

		data := execOK[queryDeviceLocationsData](te, ctx, queryDeviceLocationsJSONOrder, map[string]any{
			"JSONPath": "priority",
		})

		require.Equal(t, 3, data.DeviceLocations.TotalCount)
		assert.Equal(t, dl2.ID, data.DeviceLocations.Edges[0].Node.ID)
		assert.Equal(t, dl3.ID, data.DeviceLocations.Edges[1].Node.ID)
		assert.Equal(t, dl1.ID, data.DeviceLocations.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Create()
		loc1 := te.newLocation(ctx, userA).Create()
		loc2 := te.newLocation(ctx, userA).Create()

		dl1 := te.newDeviceLocation(ctx, userA, dev.ID, loc1.ID).Data(map[string]any{
			"props": map[string]any{"weight": float64(10)},
		}).Create()
		dl2 := te.newDeviceLocation(ctx, userA, dev.ID, loc2.ID).Data(map[string]any{
			"props": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryDeviceLocationsData](te, ctx, queryDeviceLocationsJSONOrder, map[string]any{
			"JSONPath":  "props.weight",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.DeviceLocations.TotalCount)
		assert.Equal(t, dl2.ID, data.DeviceLocations.Edges[0].Node.ID)
		assert.Equal(t, dl1.ID, data.DeviceLocations.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Create()
		loc1 := te.newLocation(ctx, userA).Create()
		loc2 := te.newLocation(ctx, userA).Create()

		dl1 := te.newDeviceLocation(ctx, userA, dev.ID, loc1.ID).Create()
		dl2 := te.newDeviceLocation(ctx, userA, dev.ID, loc2.ID).Create()

		data := execOK[queryDeviceLocationsData](te, ctx, queryDeviceLocationsJSONOrder, map[string]any{
			"Field":     "CREATED_AT",
			"Direction": "DESC",
		})

		require.Equal(t, 2, data.DeviceLocations.TotalCount)
		assert.Equal(t, dl2.ID, data.DeviceLocations.Edges[0].Node.ID)
		assert.Equal(t, dl1.ID, data.DeviceLocations.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestDeviceLocation_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data equality", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Create()
		loc1 := te.newLocation(ctx, userA).Create()
		loc2 := te.newLocation(ctx, userA).Create()

		te.newDeviceLocation(ctx, userA, dev.ID, loc1.ID).Data(map[string]any{"zone": "north"}).Create()
		dl2 := te.newDeviceLocation(ctx, userA, dev.ID, loc2.ID).Data(map[string]any{"zone": "south"}).Create()

		data := execOK[queryDeviceLocationsData](te, ctx, queryDeviceLocationsJSONOrder, map[string]any{
			"Where": `{ Data: ["zone", "south"] }`,
		})

		require.Equal(t, 1, data.DeviceLocations.TotalCount)
		assert.Equal(t, dl2.ID, data.DeviceLocations.Edges[0].Node.ID)
	})

	t.Run("filters by dataHasKey", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dev := te.newDevice(ctx, userA).Create()
		loc1 := te.newLocation(ctx, userA).Create()
		loc2 := te.newLocation(ctx, userA).Create()

		te.newDeviceLocation(ctx, userA, dev.ID, loc1.ID).Data(map[string]any{"zone": "north"}).Create()
		dl2 := te.newDeviceLocation(ctx, userA, dev.ID, loc2.ID).Data(map[string]any{"zone": "south", "priority": float64(1)}).Create()

		data := execOK[queryDeviceLocationsData](te, ctx, queryDeviceLocationsJSONOrder, map[string]any{
			"Where": `{ DataHasKey: "priority" }`,
		})

		require.Equal(t, 1, data.DeviceLocations.TotalCount)
		assert.Equal(t, dl2.ID, data.DeviceLocations.Edges[0].Node.ID)
	})
}
