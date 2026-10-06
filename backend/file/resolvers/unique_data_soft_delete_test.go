package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

// uniqueNameDataTypeID declares meta.name unique (item_unique_name schema).
var uniqueNameDataTypeID = uuid.MustParse("0192f5c4-7a1e-7c3b-9d2e-5b6a7c8d9e11")

// addUniqueNameDataType registers the item_unique_name data type for tenant A.
func (te *testEnv) addUniqueNameDataType() {
	te.t.Helper()
	schema, err := test.LoadSchemaByName("item_unique_name")
	require.NoError(te.t, err)
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         uniqueNameDataTypeID,
		Slug:       "item_unique_name",
		TenantID:   tenantA,
		JsonSchema: string(schema),
	})
}

// uniqueNameData renders a GraphQL data literal of the item_unique_name data
// type whose unique meta.name is name.
func uniqueNameData(name string) string {
	return fmt.Sprintf(`{ type: "custom", sum: 15, meta: { name: %q, weight: 50, tags: ["a"] } }`, name)
}

// createUniqueNameFile creates a file of the unique-name data type through
// the createFile mutation and returns its id.
func (te *testEnv) createUniqueNameFile(fileName, uniqueName string) uuid.UUID {
	te.t.Helper()
	return execOK[createFileData](te, te.ctx(userA), createFile, te.uniqueNameFileArgs(fileName, uniqueName)).CreateFile.ID
}

func (te *testEnv) uniqueNameFileArgs(fileName, uniqueName string) map[string]any {
	return map[string]any{
		"RefID":       testRefID.String(),
		"RefType":     "supplier",
		"Name":        fileName,
		"Size":        100,
		"ContentType": "text/plain",
		"Description": "unique data file",
		"DataTypeID":  uniqueNameDataTypeID.String(),
		"Data":        uniqueNameData(uniqueName),
	}
}

// metaName returns data.meta.name, failing the test when the shape differs.
func metaName(t *testing.T, data map[string]any) any {
	t.Helper()
	meta, ok := data["meta"].(map[string]any)
	require.True(t, ok, "data.meta is not an object: %v", data)
	return meta["name"]
}

// deleteFileSoftly deletes a file through the deleteFile mutation and checks
// the row is kept with deleted_at set, so the test really exercises a
// soft-deleted row rather than an absent one.
func (te *testEnv) deleteFileSoftly(id uuid.UUID) {
	te.t.Helper()
	deleted := execOK[deleteFileData](te, te.ctx(userA), deleteFile, map[string]any{"ID": id.String()})
	require.Equal(te.t, id, deleted.DeleteFile.DeletedID)

	row, err := te.Ent.File.Get(te.ctxWithDeleted(userA), id)
	require.NoError(te.t, err)
	require.False(te.t, row.DeletedAt.IsZero(), "deleteFile must soft-delete the row")
}

// TestFileUniqueData_SoftDeletedRowReleasesValue pins that a soft-deleted file
// no longer holds the value of a data field declared "unique": while the file
// is live a second file with the same value is refused, but once the file is
// deleted through deleteFile, creating a file with that value — or updating
// another live file to it — is accepted. A deleted row counting as a
// duplicate would lock the value forever, unlike the platform's partial
// unique indexes, which only cover rows with deleted_at IS NULL.
func TestFileUniqueData_SoftDeletedRowReleasesValue(t *testing.T) {
	t.Parallel()

	const taken = "unique-file-name"

	t.Run("create after delete accepts the deleted file's value", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		te.addUniqueNameDataType()
		ctx := te.ctx(userA)

		first := te.createUniqueNameFile("first.txt", taken)

		// Control: the value is held while the first file is live.
		execErr(te, ctx, createFile, te.uniqueNameFileArgs("second.txt", taken), validator.ErrFieldNotUnique.Error())

		te.deleteFileSoftly(first)

		replacement := execOK[createFileData](te, ctx, createFile, te.uniqueNameFileArgs("replacement.txt", taken)).CreateFile
		require.NotEqual(t, first, replacement.ID)
		require.Equal(t, taken, metaName(t, replacement.File.Data))
	})

	t.Run("update after delete accepts the deleted file's value", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		te.addUniqueNameDataType()
		ctx := te.ctx(userA)

		first := te.createUniqueNameFile("first.txt", taken)
		other := te.createUniqueNameFile("other.txt", "other-name")

		updateArgs := map[string]any{
			"ID":         other.String(),
			"DataTypeID": uniqueNameDataTypeID.String(),
			"Data":       uniqueNameData(taken),
		}

		// Control: the value is held while the first file is live.
		execErr(te, ctx, updateFile, updateArgs, validator.ErrFieldNotUnique.Error())

		te.deleteFileSoftly(first)

		updated := execOK[updateFileData](te, ctx, updateFile, updateArgs).UpdateFile
		require.Equal(t, other, updated.ID)

		stored, err := te.Ent.File.Get(ctx, other)
		require.NoError(t, err)
		require.Equal(t, taken, metaName(t, stored.Data))
	})
}
