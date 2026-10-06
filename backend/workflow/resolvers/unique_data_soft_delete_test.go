package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/feature"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

// uniqueNameDataTypeID declares meta.name unique (item_unique_name schema).
var uniqueNameDataTypeID = uuid.MustParse("0192f5c4-7a1e-7c3b-9d2e-5b6a7c8d9e12")

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

// uniqueNameWorkflowArgs are registerWorkflow arguments for workflow name on
// the shared test queue, carrying uniqueName in the unique meta.name field.
func uniqueNameWorkflowArgs(name, uniqueName string) map[string]any {
	return map[string]any{
		"Name":       name,
		"TaskQueue":  "unique-queue",
		"DataTypeID": uniqueNameDataTypeID,
		"DataName":   uniqueName,
		"DataWeight": 50,
	}
}

// deleteWorkflowSoftly deletes a workflow through the deleteWorkflow mutation
// and checks the row is kept with deleted_at set, so the test really
// exercises a soft-deleted row rather than an absent one.
func (te *testEnv) deleteWorkflowSoftly(id uuid.UUID) {
	te.t.Helper()
	ctx := te.ctx(userA)
	deleted := execOK[deleteWorkflowData](te, ctx, deleteWorkflow, map[string]any{"ID": id})
	require.Equal(te.t, id, deleted.DeleteWorkflow.DeletedID)

	row, err := te.Ent.Workflow.Get(feature.Context(ctx, feature.FEATURE_SHOW_DELETED), id)
	require.NoError(te.t, err)
	require.False(te.t, row.DeletedAt.IsZero(), "deleteWorkflow must soft-delete the row")
}

// metaName returns data.meta.name, failing the test when the shape differs.
func metaName(t *testing.T, data map[string]any) any {
	t.Helper()
	meta, ok := data["meta"].(map[string]any)
	require.True(t, ok, "data.meta is not an object: %v", data)
	return meta["name"]
}

// TestWorkflowUniqueData_SoftDeletedRowReleasesValue pins that a soft-deleted
// workflow no longer holds the value of a data field declared "unique": while
// the workflow is live another registration with the same value is refused,
// but once it is deleted through deleteWorkflow, registering a workflow with
// that value — a new one, the deleted one's name again, or an existing live
// workflow re-registered with changed data — is accepted. A deleted row
// counting as a duplicate would lock the value forever, unlike the
// platform's partial unique indexes, which only cover rows with
// deleted_at IS NULL.
func TestWorkflowUniqueData_SoftDeletedRowReleasesValue(t *testing.T) {
	t.Parallel()

	const taken = "unique-workflow-name"

	t.Run("register new workflow after delete accepts the deleted workflow's value", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		te.addUniqueNameDataType()
		ctx := te.ctx(userA)

		first := execOK[registerWorkflowData](te, ctx, registerWorkflow, uniqueNameWorkflowArgs("first", taken)).RegisterWorkflow.ID

		// Control: the value is held while the first workflow is live.
		execErr(te, ctx, registerWorkflow, uniqueNameWorkflowArgs("second", taken), validator.ErrFieldNotUnique.Error())

		te.deleteWorkflowSoftly(first)

		replacement := execOK[registerWorkflowData](te, ctx, registerWorkflow, uniqueNameWorkflowArgs("replacement", taken)).RegisterWorkflow
		require.NotEqual(t, first, replacement.ID)
		require.Equal(t, "replacement", replacement.Name)
		require.Equal(t, taken, metaName(t, replacement.Data))
	})

	t.Run("re-register deleted workflow name accepts its own former value", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		te.addUniqueNameDataType()
		ctx := te.ctx(userA)

		first := execOK[registerWorkflowData](te, ctx, registerWorkflow, uniqueNameWorkflowArgs("first", taken)).RegisterWorkflow.ID

		te.deleteWorkflowSoftly(first)

		// A worker registering the deleted workflow again creates a fresh row
		// (the lookup skips deleted rows) with the same data.
		again := execOK[registerWorkflowData](te, ctx, registerWorkflow, uniqueNameWorkflowArgs("first", taken)).RegisterWorkflow
		require.NotEqual(t, first, again.ID)
		require.Equal(t, taken, metaName(t, again.Data))
	})

	t.Run("re-register live workflow with changed data after delete accepts the deleted workflow's value", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		te.addUniqueNameDataType()
		ctx := te.ctx(userA)

		first := execOK[registerWorkflowData](te, ctx, registerWorkflow, uniqueNameWorkflowArgs("first", taken)).RegisterWorkflow.ID
		other := execOK[registerWorkflowData](te, ctx, registerWorkflow, uniqueNameWorkflowArgs("other", "other-name")).RegisterWorkflow.ID

		// Control: the value is held while the first workflow is live.
		execErr(te, ctx, registerWorkflow, uniqueNameWorkflowArgs("other", taken), validator.ErrFieldNotUnique.Error())

		te.deleteWorkflowSoftly(first)

		updated := execOK[registerWorkflowData](te, ctx, registerWorkflow, uniqueNameWorkflowArgs("other", taken)).RegisterWorkflow
		require.Equal(t, other, updated.ID, "re-registration must update the live row, not create one")

		stored, err := te.Ent.Workflow.Get(ctx, other)
		require.NoError(t, err)
		require.Equal(t, taken, metaName(t, stored.Data))
	})
}
