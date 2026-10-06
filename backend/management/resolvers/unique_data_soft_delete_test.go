package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

// uniqueNameDataTypeID is tenant A's data type whose schema (item_unique_name)
// declares meta.name `"unique": true`.
var uniqueNameDataTypeID = uuid.MustParse("0199a0c1-5e2b-7a51-9d1e-3c7f2b8e4a10")

var (
	createLocationUniqueData = resolver.ParseTemplate(`mutation {
		createLocation(input: {
			name: "{{.Name}}",
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.Value}}", weight: 50, tags: ["a", "b"] } }
		}) {
			location { id }
		}
	}`)

	createDeviceUniqueData = resolver.ParseTemplate(`mutation {
		createDevice(input: {
			name: "{{.Name}}",
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.Value}}", weight: 50, tags: ["a", "b"] } }
		}) {
			device { id }
		}
	}`)

	setDeviceLocationUniqueData = resolver.ParseTemplate(`mutation {
		setDeviceLocation(input: {
			deviceID: "{{.DeviceID}}",
			locationID: "{{.LocationID}}",
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.Value}}", weight: 50, tags: ["a", "b"] } }
		}) {
			DeviceLocation { id }
		}
	}`)

	patchDeviceLocationData = resolver.ParseTemplate(`mutation {
		patchDeviceLocationData(id: "{{.ID}}", patches: [
			{{range $i, $p := .Patches}}{{if $i}},{{end}}
			{ op: {{$p.Op}}, path: "{{$p.Path}}"{{if $p.Value}}, value: "{{$p.Value}}"{{end}}{{if $p.From}}, from: "{{$p.From}}"{{end}} }
			{{end}}
		]) {
			DeviceLocation { id data }
		}
	}`)

	setKeyValueUniqueData = resolver.ParseTemplate(`mutation {
		setKeyValue(input: {
			name: "{{.Name}}",
			dataTypeID: "{{.DataTypeID}}",
			data: { type: "custom", sum: 15, meta: { name: "{{.Value}}", weight: 50, tags: ["a", "b"] } }
		}) {
			id name
		}
	}`)

	deleteKeyValue = resolver.ParseTemplate(`mutation {
		deleteKeyValue(id: "{{.ID}}") { deletedID }
	}`)
)

// idNode decodes the `id` of an entity in a mutation payload.
type idNode struct {
	ID uuid.UUID
}

// deletedNode decodes the `deletedID` of a delete payload.
type deletedNode struct {
	DeletedID uuid.UUID
}

type setKeyValueUniqueResult struct {
	SetKeyValue struct {
		ID   uuid.UUID
		Name string
	}
}

// registerUniqueNameDataType makes the unique-name data type resolvable by
// the validator for tenant A.
func (te *testEnv) registerUniqueNameDataType() {
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         uniqueNameDataTypeID,
		Slug:       "item_unique_name",
		TenantID:   resolver.TenantA,
		JsonSchema: string(test.MustLoadSchemaByName("item_unique_name")),
	})
}

// uniqueDataPatchEntity describes one management entity whose data patch
// mutation runs the data-type uniqueness check, together with the mutation
// that soft-deletes it.
type uniqueDataPatchEntity struct {
	name string
	// create creates a record through its create mutation holding value in
	// meta.name and returns its id.
	create      func(te *testEnv, value string) uuid.UUID
	patch       resolver.Template
	patchField  string
	outputField string
	delete      resolver.Template
	deleteField string
}

var uniqueDataPatchEntities = []uniqueDataPatchEntity{
	{
		name: "location",
		create: func(te *testEnv, value string) uuid.UUID {
			te.t.Helper()
			got := execOK[map[string]map[string]idNode](te, te.ctx(userA), createLocationUniqueData, map[string]any{
				"Name":       "Location-" + uuidgql.GenerateV7UUID().String(),
				"DataTypeID": uniqueNameDataTypeID,
				"Value":      value,
			})
			return got["createLocation"]["location"].ID
		},
		patch:       patchLocationData,
		patchField:  "patchLocationData",
		outputField: "location",
		delete:      deleteLocation,
		deleteField: "deleteLocation",
	},
	{
		name: "device",
		create: func(te *testEnv, value string) uuid.UUID {
			te.t.Helper()
			got := execOK[map[string]map[string]idNode](te, te.ctx(userA), createDeviceUniqueData, map[string]any{
				"Name":       "Device-" + uuidgql.GenerateV7UUID().String(),
				"DataTypeID": uniqueNameDataTypeID,
				"Value":      value,
			})
			return got["createDevice"]["device"].ID
		},
		patch:       patchDeviceData,
		patchField:  "patchDeviceData",
		outputField: "device",
		delete:      deleteDevice,
		deleteField: "deleteDevice",
	},
	{
		name: "device location",
		create: func(te *testEnv, value string) uuid.UUID {
			te.t.Helper()
			ctx := te.ctx(userA)
			dev := te.newDevice(ctx, userA).Create()
			loc := te.newLocation(ctx, userA).Create()
			got := execOK[map[string]map[string]idNode](te, ctx, setDeviceLocationUniqueData, map[string]any{
				"DeviceID":   dev.ID,
				"LocationID": loc.ID,
				"DataTypeID": uniqueNameDataTypeID,
				"Value":      value,
			})
			return got["setDeviceLocation"]["DeviceLocation"].ID
		},
		patch:       patchDeviceLocationData,
		patchField:  "patchDeviceLocationData",
		outputField: "DeviceLocation",
		delete:      unsetDeviceLocation,
		deleteField: "unsetDeviceLocation",
	},
}

// TestPatchData_SoftDeletedRecordReleasesUniqueValue pins that a value of a
// data-type field declared `"unique": true` is only reserved by LIVE
// locations, devices and device locations: while a record holding the value
// exists, patching another record to that value is refused with
// validator.ErrFieldNotUnique; once the holder is soft-deleted through its
// delete mutation, the same patch is accepted. A deleted row must not block
// its unique value, matching the partial `WHERE deleted_at IS NULL` unique
// indexes on the platform's business keys.
func TestPatchData_SoftDeletedRecordReleasesUniqueValue(t *testing.T) {
	t.Parallel()

	for _, e := range uniqueDataPatchEntities {
		t.Run(e.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			te.registerUniqueNameDataType()
			ctx := te.ctx(userA)
			value := "Unique-" + uuidgql.GenerateV7UUID().String()

			first := e.create(te, value)
			other := e.create(te, "Other-"+uuidgql.GenerateV7UUID().String())
			require.NotEqual(t, uuid.Nil, first)
			require.NotEqual(t, uuid.Nil, other)
			patches := []patch{{Op: "REPLACE", Path: "/meta/name", Value: `\"` + value + `\"`}}

			// Control: the live record reserves the value.
			execErr(te, ctx, e.patch, map[string]any{
				"ID":      other,
				"Patches": patches,
			}, validator.ErrFieldNotUnique.Error())

			deleted := execOK[map[string]deletedNode](te, ctx, e.delete, map[string]any{"ID": first})
			require.Equal(t, first, deleted[e.deleteField].DeletedID)

			got := execOK[map[string]map[string]idNode](te, ctx, e.patch, map[string]any{
				"ID":      other,
				"Patches": patches,
			})
			require.Equal(t, other, got[e.patchField][e.outputField].ID)
		})
	}
}

// TestSetKeyValue_SoftDeletedEntryReleasesUniqueValue pins that a key-value
// entry's unique data value is only reserved while the entry is live: a
// second entry with the same value is refused with validator.ErrFieldNotUnique,
// but after deleteKeyValue soft-deletes the holder the value is accepted
// again — for a new name, for re-setting the deleted name, and for rewriting
// another live entry to that value.
func TestSetKeyValue_SoftDeletedEntryReleasesUniqueValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// target returns the key-value name whose setKeyValue must be refused
		// before and accepted after the holder is deleted; it may create a
		// live entry to rewrite.
		target func(te *testEnv, holderName string) string
	}{
		{
			name: "new name after delete",
			target: func(_ *testEnv, _ string) string {
				return "kv-" + uuidgql.GenerateV7UUID().String()
			},
		},
		{
			name: "same name after delete",
			target: func(_ *testEnv, holderName string) string {
				return holderName
			},
		},
		{
			name: "rewrite live entry after delete",
			target: func(te *testEnv, _ string) string {
				te.t.Helper()
				name := "kv-" + uuidgql.GenerateV7UUID().String()
				setKeyValueUnique(te, name, "Other-"+uuidgql.GenerateV7UUID().String())
				return name
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			te.registerUniqueNameDataType()
			ctx := te.ctx(userA)
			value := "Unique-" + uuidgql.GenerateV7UUID().String()

			holderName := "kv-" + uuidgql.GenerateV7UUID().String()
			holder := setKeyValueUnique(te, holderName, value)
			target := tc.target(te, holderName)

			// Control: the live holder reserves the value. Re-setting the
			// holder's own name rewrites the holder itself, so it is excluded.
			if target != holderName {
				execErr(te, ctx, setKeyValueUniqueData, map[string]any{
					"Name":       target,
					"DataTypeID": uniqueNameDataTypeID,
					"Value":      value,
				}, validator.ErrFieldNotUnique.Error())
			}

			deleted := execOK[map[string]deletedNode](te, ctx, deleteKeyValue, map[string]any{"ID": holder})
			require.Equal(t, holder, deleted["deleteKeyValue"].DeletedID)

			got := execOK[setKeyValueUniqueResult](te, ctx, setKeyValueUniqueData, map[string]any{
				"Name":       target,
				"DataTypeID": uniqueNameDataTypeID,
				"Value":      value,
			})
			require.Equal(t, target, got.SetKeyValue.Name)
			require.NotEqual(t, holder, got.SetKeyValue.ID,
				"the deleted holder must not be revived by the upsert")
		})
	}
}

// setKeyValueUnique sets the key-value entry name to the unique-name data
// type holding value in meta.name and returns the entry's id.
func setKeyValueUnique(te *testEnv, name, value string) uuid.UUID {
	te.t.Helper()
	got := execOK[setKeyValueUniqueResult](te, te.ctx(userA), setKeyValueUniqueData, map[string]any{
		"Name":       name,
		"DataTypeID": uniqueNameDataTypeID,
		"Value":      value,
	})
	require.NotEqual(te.t, uuid.Nil, got.SetKeyValue.ID)
	return got.SetKeyValue.ID
}
