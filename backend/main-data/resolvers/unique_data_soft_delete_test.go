package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

// uniqueDataEntity describes one main-data entity whose create, update and
// patch mutations run the data-type uniqueness check, together with the
// mutation that soft-deletes it.
type uniqueDataEntity struct {
	name        string
	create      resolver.Template
	createField string
	update      resolver.Template
	updateField string
	patch       resolver.Template
	patchField  string
	delete      resolver.Template
	deleteField string
}

// idNode decodes the `id` of a mutation payload; the per-entity payloads are
// keyed by the mutation name, so a map of these covers every entity.
type idNode struct {
	ID uuid.UUID
}

// deletedNode decodes the `deletedID` of a delete payload.
type deletedNode struct {
	DeletedID uuid.UUID
}

var uniqueDataEntities = []uniqueDataEntity{
	{
		name:        "customer",
		create:      createCustomer,
		createField: "createCustomer",
		update:      updateCustomer,
		updateField: "updateCustomer",
		patch:       patchCustomerData,
		patchField:  "patchCustomerData",
		delete:      deleteCustomer,
		deleteField: "deleteCustomer",
	},
	{
		name:        "supplier",
		create:      createSupplier,
		createField: "createSupplier",
		update:      updateSupplier,
		updateField: "updateSupplier",
		patch:       patchSupplierData,
		patchField:  "patchSupplierData",
		delete:      deleteSupplier,
		deleteField: "deleteSupplier",
	},
}

// TestUniqueDataField_SoftDeletedRecordReleasesValue pins that a value of a
// data-type field declared `"unique": true` is only reserved by LIVE records:
// while a record holding the value exists, a second create/update/patch with
// the same value is refused with validator.ErrFieldNotUnique; once that record
// is soft-deleted through its delete mutation, the same value is accepted
// again on create, update and patch. This matches the partial
// `WHERE deleted_at IS NULL` unique indexes on the platform's business keys —
// a deleted row must not block its unique value forever.
func TestUniqueDataField_SoftDeletedRecordReleasesValue(t *testing.T) {
	t.Parallel()

	for _, e := range uniqueDataEntities {
		t.Run(e.name+"/create after delete", func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			ctx := te.ctx(userA)
			value := "Unique-" + uuidgql.GenerateV7UUID().String()

			first := createUniqueRecord(te, e, value)

			// Control: the live record reserves the value.
			execErr(te, ctx, e.create, map[string]any{
				"DataTypeID": dataTypeIDTenantA2,
				"Name":       value,
			}, validator.ErrFieldNotUnique.Error())

			deleteUniqueRecord(te, e, first)

			got := execOK[map[string]idNode](te, ctx, e.create, map[string]any{
				"DataTypeID": dataTypeIDTenantA2,
				"Name":       value,
			})
			require.NotEqual(t, first, got[e.createField].ID,
				"a new %s must be created, not the deleted one returned", e.name)
			require.NotEqual(t, uuid.Nil, got[e.createField].ID)
		})

		t.Run(e.name+"/update after delete", func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			ctx := te.ctx(userA)
			value := "Unique-" + uuidgql.GenerateV7UUID().String()

			first := createUniqueRecord(te, e, value)
			other := createUniqueRecord(te, e, "Other-"+uuidgql.GenerateV7UUID().String())

			// Control: the live record reserves the value.
			execErr(te, ctx, e.update, map[string]any{
				"ID":         other,
				"DataTypeID": dataTypeIDTenantA2,
				"Name":       value,
			}, validator.ErrFieldNotUnique.Error())

			deleteUniqueRecord(te, e, first)

			got := execOK[map[string]idNode](te, ctx, e.update, map[string]any{
				"ID":         other,
				"DataTypeID": dataTypeIDTenantA2,
				"Name":       value,
			})
			require.Equal(t, other, got[e.updateField].ID)
		})

		t.Run(e.name+"/patch after delete", func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			ctx := te.ctx(userA)
			value := "Unique-" + uuidgql.GenerateV7UUID().String()

			first := createUniqueRecord(te, e, value)
			other := createUniqueRecord(te, e, "Other-"+uuidgql.GenerateV7UUID().String())
			patches := []patch{{Op: "REPLACE", Path: "/meta/name", Value: `\"` + value + `\"`}}

			// Control: the live record reserves the value.
			execErr(te, ctx, e.patch, map[string]any{
				"ID":      other,
				"Patches": patches,
			}, validator.ErrFieldNotUnique.Error())

			deleteUniqueRecord(te, e, first)

			got := execOK[map[string]idNode](te, ctx, e.patch, map[string]any{
				"ID":      other,
				"Patches": patches,
			})
			require.Equal(t, other, got[e.patchField].ID)
		})
	}
}

// createUniqueRecord creates a record of the unique-name data type holding
// value in meta.name through the entity's create mutation.
func createUniqueRecord(te *testEnv, e uniqueDataEntity, value string) uuid.UUID {
	te.t.Helper()
	got := execOK[map[string]idNode](te, te.ctx(userA), e.create, map[string]any{
		"DataTypeID": dataTypeIDTenantA2,
		"Name":       value,
	})
	id := got[e.createField].ID
	require.NotEqual(te.t, uuid.Nil, id)
	return id
}

// deleteUniqueRecord soft-deletes id through the entity's delete mutation.
func deleteUniqueRecord(te *testEnv, e uniqueDataEntity, id uuid.UUID) {
	te.t.Helper()
	got := execOK[map[string]deletedNode](te, te.ctx(userA), e.delete, map[string]any{"ID": id})
	require.Equal(te.t, id, got[e.deleteField].DeletedID)
}
