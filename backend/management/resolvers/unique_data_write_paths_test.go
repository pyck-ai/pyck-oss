package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

var (
	updateLocationUniqueData = resolver.ParseTemplate(`mutation {
		updateLocation(id: "{{.ID}}", input: {
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.Value}}", weight: 50, tags: ["a", "b"] } }
		}) {
			location { id }
		}
	}`)

	updateDeviceUniqueData = resolver.ParseTemplate(`mutation {
		updateDevice(id: "{{.ID}}", input: {
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.Value}}", weight: 50, tags: ["a", "b"] } }
		}) {
			device { id }
		}
	}`)
)

// uniqueDataWriteEntity describes one management entity whose create and
// update mutations write a data type's data and must therefore run the
// data-type uniqueness check.
type uniqueDataWriteEntity struct {
	name string
	// createArgs returns the variables for a create mutation holding value
	// in meta.name; it creates any referenced records first.
	createArgs  func(te *testEnv, value string) map[string]any
	create      resolver.Template
	createField string
	// updateField is empty when the entity has no update mutation.
	update      resolver.Template
	updateField string
	outputField string
}

var uniqueDataWriteEntities = []uniqueDataWriteEntity{
	{
		name: "location",
		createArgs: func(_ *testEnv, value string) map[string]any {
			return map[string]any{
				"Name":       "Location-" + uuidgql.GenerateV7UUID().String(),
				"DataTypeID": uniqueNameDataTypeID,
				"Value":      value,
			}
		},
		create:      createLocationUniqueData,
		createField: "createLocation",
		update:      updateLocationUniqueData,
		updateField: "updateLocation",
		outputField: "location",
	},
	{
		name: "device",
		createArgs: func(_ *testEnv, value string) map[string]any {
			return map[string]any{
				"Name":       "Device-" + uuidgql.GenerateV7UUID().String(),
				"DataTypeID": uniqueNameDataTypeID,
				"Value":      value,
			}
		},
		create:      createDeviceUniqueData,
		createField: "createDevice",
		update:      updateDeviceUniqueData,
		updateField: "updateDevice",
		outputField: "device",
	},
	{
		// setDeviceLocation always inserts a new row; there is no update
		// mutation that writes a device location's data outside of
		// patchDeviceLocationData.
		name: "device location",
		createArgs: func(te *testEnv, value string) map[string]any {
			te.t.Helper()
			ctx := te.ctx(userA)
			dev := te.newDevice(ctx, userA).Create()
			loc := te.newLocation(ctx, userA).Create()
			return map[string]any{
				"DeviceID":   dev.ID,
				"LocationID": loc.ID,
				"DataTypeID": uniqueNameDataTypeID,
				"Value":      value,
			}
		},
		create:      setDeviceLocationUniqueData,
		createField: "setDeviceLocation",
		outputField: "DeviceLocation",
	},
}

// createUnique creates a record of e holding value in meta.name and returns
// its id.
func (e uniqueDataWriteEntity) createUnique(te *testEnv, value string) uuid.UUID {
	te.t.Helper()
	got := execOK[map[string]map[string]idNode](te, te.ctx(userA), e.create, e.createArgs(te, value))
	id := got[e.createField][e.outputField].ID
	require.NotEqual(te.t, uuid.Nil, id)
	return id
}

// TestCreateData_LiveRecordReservesUniqueValue pins that createLocation,
// createDevice and setDeviceLocation run the data-type uniqueness check: a
// create whose data repeats a `"unique": true` value held by a live record of
// the same data type is refused with validator.ErrFieldNotUnique, while a
// create with a value no live record holds is accepted.
func TestCreateData_LiveRecordReservesUniqueValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// value returns the meta.name to create with, given the live holder's.
		value   func(held string) string
		wantErr error
	}{
		{
			name:    "duplicate of live value refused",
			value:   func(held string) string { return held },
			wantErr: validator.ErrFieldNotUnique,
		},
		{
			name:  "new value accepted",
			value: func(string) string { return "Other-" + uuidgql.GenerateV7UUID().String() },
		},
	}

	for _, e := range uniqueDataWriteEntities {
		for _, tc := range cases {
			t.Run(e.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)
				te.registerUniqueNameDataType()
				ctx := te.ctx(userA)

				held := "Unique-" + uuidgql.GenerateV7UUID().String()
				e.createUnique(te, held)

				args := e.createArgs(te, tc.value(held))
				if tc.wantErr != nil {
					execErr(te, ctx, e.create, args, tc.wantErr.Error())
					return
				}
				got := execOK[map[string]map[string]idNode](te, ctx, e.create, args)
				require.NotEqual(t, uuid.Nil, got[e.createField][e.outputField].ID)
			})
		}
	}
}

// TestUpdateData_LiveRecordReservesUniqueValue pins that updateLocation and
// updateDevice run the data-type uniqueness check against every live record
// except the one being updated: moving record B onto record A's live
// `"unique": true` value is refused with validator.ErrFieldNotUnique, while B
// re-sending its own value, or switching to a value no live record holds, is
// accepted.
func TestUpdateData_LiveRecordReservesUniqueValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// value returns B's new meta.name, given A's and B's current values.
		value   func(valueA, valueB string) string
		wantErr error
	}{
		{
			name:    "to another record's live value refused",
			value:   func(valueA, _ string) string { return valueA },
			wantErr: validator.ErrFieldNotUnique,
		},
		{
			name:  "keeping own value accepted",
			value: func(_, valueB string) string { return valueB },
		},
		{
			name:  "to a new value accepted",
			value: func(_, _ string) string { return "New-" + uuidgql.GenerateV7UUID().String() },
		},
	}

	for _, e := range uniqueDataWriteEntities {
		if e.updateField == "" {
			continue
		}
		for _, tc := range cases {
			t.Run(e.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				te := setup(t)
				defer te.Close(t)
				te.registerUniqueNameDataType()
				ctx := te.ctx(userA)

				valueA := "Unique-A-" + uuidgql.GenerateV7UUID().String()
				valueB := "Unique-B-" + uuidgql.GenerateV7UUID().String()
				e.createUnique(te, valueA)
				b := e.createUnique(te, valueB)

				args := map[string]any{
					"ID":         b,
					"DataTypeID": uniqueNameDataTypeID,
					"Value":      tc.value(valueA, valueB),
				}
				if tc.wantErr != nil {
					execErr(te, ctx, e.update, args, tc.wantErr.Error())
					return
				}
				got := execOK[map[string]map[string]idNode](te, ctx, e.update, args)
				require.Equal(t, b, got[e.updateField][e.outputField].ID)
			})
		}
	}
}
