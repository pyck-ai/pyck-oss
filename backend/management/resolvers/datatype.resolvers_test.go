package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	queryDataTypes = resolver.ParseTemplate(`query {
		dataTypes {
			totalCount
			edges {
				node { id tenantID name description jsonSchema }
				cursor
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)

	createDataType = resolver.ParseTemplate(`mutation {
		createDataType(input: {
			name: "{{.Name}}",
			slug: "{{.Slug}}",
			description: "{{.Description}}",
			entity: "item",
			jsonSchema: "{{.JsonSchema}}"
		}) {
			id name description tenantID jsonSchema
		}
	}`)

	updateDataType = resolver.ParseTemplate(`mutation {
		updateDataType(
			id: "{{.ID}}",
			input: {
				name: "{{.Name}}",
				description: "{{.Description}}",
				jsonSchema: "{{.JsonSchema}}"
			}
		) {
			id name description tenantID jsonSchema
		}
	}`)

	deleteDataType = resolver.ParseTemplate(`mutation {
		deleteDataType(id: "{{.ID}}") {
			deletedID
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type dataTypeNode struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description string
	JsonSchema  string
}

type queryDataTypesData struct {
	DataTypes struct {
		TotalCount int
		Edges      []struct {
			Node   dataTypeNode
			Cursor string
		}
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
	}
}

type createDataTypeData struct {
	CreateDataType dataTypeNode
}

type updateDataTypeData struct {
	UpdateDataType dataTypeNode
}

type deleteDataTypeData struct {
	DeleteDataType struct{ DeletedID uuid.UUID }
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestDataType_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userAWriter)

		data := execOK[queryDataTypesData](te, ctx, queryDataTypes, nil)

		assert.Equal(t, 0, data.DataTypes.TotalCount)
		assert.Empty(t, data.DataTypes.Edges)
	})

	t.Run("returns only own tenant's data types", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userAWriter)
		ctxB := te.ctx(userB)

		dtA := te.newDataType(ctxA, userAWriter).Create()
		te.newDataType(ctxB, userB).Create()

		data := execOK[queryDataTypesData](te, ctxA, queryDataTypes, nil)

		require.Equal(t, 1, data.DataTypes.TotalCount)
		assert.Equal(t, dtA.ID, data.DataTypes.Edges[0].Node.ID)
	})

	t.Run("reader role can query data types", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		ctxBReader := te.ctx(userBReader)

		te.newDataType(ctxB, userB).Create()

		data := execOK[queryDataTypesData](te, ctxBReader, queryDataTypes, nil)

		assert.Equal(t, 1, data.DataTypes.TotalCount)
	})

	t.Run("fails for unauthenticated user", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		unauthUser := &authn.User{}
		ctx := te.ctx(unauthUser)

		execErr(te, ctx, queryDataTypes, nil, "unauthorized")
	})
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestDataType_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates data type successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createDataTypeData](te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test description",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		})

		created := data.CreateDataType
		assert.Equal(t, "Test DataType", created.Name)
		assert.Equal(t, "Test description", created.Description)
		assert.Equal(t, resolver.TenantA, created.TenantID)
		assert.NotEqual(t, uuid.Nil, created.ID)

		// Verify persisted
		stored, err := te.Ent.DataType.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "Test DataType", stored.Name)

		// Verify event
		te.assertEvents(ctx, Create("datatype", created.ID))
	})

	t.Run("rejects duplicate slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newDataType(ctx, userA).Slug("duplicate-slug").Create()
		te.clearEvents(ctx)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Another DataType",
			"Slug":        "duplicate-slug",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		}, "UNIQUE constraint failed")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects reader role", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userBReader)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		}, "deny rule")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid slug", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "INVALID SLUG",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(testDataTypeSchema),
		}, "validator failed")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects malformed JSON schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(`{"type": "object", "properties": {`),
		}, "unexpected EOF")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid JSON schema type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  resolver.EscapeJSON(`{"type": "invalid_type"}`),
		}, "value must be one of")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects empty JSON schema", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createDataType, map[string]any{
			"Name":        "Test DataType",
			"Slug":        "test-datatype",
			"Description": "Test",
			"JsonSchema":  "",
		}, "EOF")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestDataType_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates data type successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateDataTypeData](te, ctx, updateDataType, map[string]any{
			"ID":          dt.ID,
			"Name":        dt.Name,
			"Description": "Updated description",
			"JsonSchema":  resolver.EscapeJSON(dt.JSONSchema),
		})

		assert.Equal(t, "Updated description", data.UpdateDataType.Description)
		te.assertEvents(ctx, Update("datatype", dt.ID))
	})

	t.Run("updates without changes succeeds", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateDataTypeData](te, ctx, updateDataType, map[string]any{
			"ID":          dt.ID,
			"Name":        dt.Name,
			"Description": dt.Description,
			"JsonSchema":  resolver.EscapeJSON(dt.JSONSchema),
		})

		assert.Equal(t, dt.Name, data.UpdateDataType.Name)
		te.assertEvents(ctx, Update("datatype", dt.ID))
	})

	t.Run("rejects update of other tenant's data type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateDataType, map[string]any{
			"ID":          dt.ID,
			"Name":        dt.Name,
			"Description": "Hacked",
			"JsonSchema":  resolver.EscapeJSON(dt.JSONSchema),
		}, "data_type not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects reader role", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxBReader := te.ctx(userBReader)
		execErr(te, ctxBReader, updateDataType, map[string]any{
			"ID":          dt.ID,
			"Name":        dt.Name,
			"Description": "Hacked",
			"JsonSchema":  resolver.EscapeJSON(dt.JSONSchema),
		}, "deny rule")

		te.assertNoEvents(ctxBReader)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestDataType_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes data type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		dt := te.newDataType(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteDataTypeData](te, ctx, deleteDataType, map[string]any{
			"ID": dt.ID,
		})

		assert.Equal(t, dt.ID, data.DeleteDataType.DeletedID)

		// Verify soft-deleted
		deleted, err := te.Ent.DataType.Get(te.ctxWithDeleted(userA), dt.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("datatype", dt.ID))
	})

	t.Run("rejects delete of other tenant's data type", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteDataType, map[string]any{
			"ID": dt.ID,
		}, "data_type not found")

		te.assertNoEvents(ctxA)
	})

	t.Run("rejects reader role", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxB := te.ctx(userB)
		dt := te.newDataType(ctxB, userB).Create()
		te.clearEvents(ctxB)

		ctxBReader := te.ctx(userBReader)
		execErr(te, ctxBReader, deleteDataType, map[string]any{
			"ID": dt.ID,
		}, "deny rule")

		te.assertNoEvents(ctxBReader)
	})
}
