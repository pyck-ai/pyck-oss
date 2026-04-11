package mixin_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	ent "github.com/pyck-ai/pyck/backend/common/test/ent/gen"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen/entitywithdatamixin"
)

// Helper function to create an entity with data fields
func createEntityWithData(t *testing.T, client *ent.Client, userID uuid.UUID, tenantID uuid.UUID, stringField string, dataTypeID *uuid.UUID, dataTypeSlug *string, data map[string]interface{}) *ent.EntityWithDataMixin {
	t.Helper()

	ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)
	create := client.EntityWithDataMixin.Create().
		SetStringField(stringField)

	if dataTypeID != nil {
		create = create.SetDataTypeID(*dataTypeID)
	}
	if dataTypeSlug != nil {
		create = create.SetDataTypeSlug(*dataTypeSlug)
	}
	if data != nil {
		create = create.SetData(data)
	}

	entity, err := create.Save(ctx)
	require.NoError(t, err)
	return entity
}

func TestDataMixinFields(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tenantID := uuid.New()
	dataTypeID := uuid.New()
	dataTypeSlug := "test-data-type"
	testData := map[string]interface{}{
		"field1": "value1",
		"field2": 42,
		"field3": true,
		"nested": map[string]interface{}{
			"subfield": "subvalue",
		},
	}

	t.Run("creates entity with all data fields", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		entity := createEntityWithData(t, client, userID, tenantID, "test_entity", &dataTypeID, &dataTypeSlug, testData)

		assert.Equal(t, dataTypeID, entity.DataTypeID, "Entity should have correct data_type_id")
		assert.Equal(t, dataTypeSlug, entity.DataTypeSlug, "Entity should have correct data_type_slug")
		assert.Equal(t, testData, entity.Data, "Entity should have correct data")
		assert.Equal(t, "test_entity", entity.StringField, "Entity should have correct string_field")
	})

	t.Run("creates entity with only data_type_id", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		entity := createEntityWithData(t, client, userID, tenantID, "test_entity", &dataTypeID, nil, nil)

		assert.Equal(t, dataTypeID, entity.DataTypeID, "Entity should have correct data_type_id")
		assert.Empty(t, entity.DataTypeSlug, "Entity should have empty data_type_slug")
		assert.Nil(t, entity.Data, "Entity should have nil data")
	})

	t.Run("creates entity with only data_type_slug", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		entity := createEntityWithData(t, client, userID, tenantID, "test_entity", nil, &dataTypeSlug, nil)

		assert.Equal(t, uuid.Nil, entity.DataTypeID, "Entity should have nil data_type_id")
		assert.Equal(t, dataTypeSlug, entity.DataTypeSlug, "Entity should have correct data_type_slug")
		assert.Nil(t, entity.Data, "Entity should have nil data")
	})

	t.Run("creates entity with only data", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		entity := createEntityWithData(t, client, userID, tenantID, "test_entity", nil, nil, testData)

		assert.Equal(t, uuid.Nil, entity.DataTypeID, "Entity should have nil data_type_id")
		assert.Empty(t, entity.DataTypeSlug, "Entity should have empty data_type_slug")
		assert.Equal(t, testData, entity.Data, "Entity should have correct data")
	})

	t.Run("creates entity with no data fields", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		entity := createEntityWithData(t, client, userID, tenantID, "test_entity", nil, nil, nil)

		assert.Equal(t, uuid.Nil, entity.DataTypeID, "Entity should have nil data_type_id")
		assert.Empty(t, entity.DataTypeSlug, "Entity should have empty data_type_slug")
		assert.Nil(t, entity.Data, "Entity should have nil data")
	})

	t.Run("updates entity data fields", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with initial data
		entity := createEntityWithData(t, client, userID, tenantID, "test_entity", &dataTypeID, &dataTypeSlug, testData)

		// Update with new data
		newDataTypeID := uuid.New()
		newDataTypeSlug := "updated-data-type"
		newData := map[string]interface{}{
			"updated_field": "updated_value",
		}

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)
		updatedEntity, err := client.EntityWithDataMixin.UpdateOneID(entity.ID).
			SetDataTypeID(newDataTypeID).
			SetDataTypeSlug(newDataTypeSlug).
			SetData(newData).
			Save(ctx)
		require.NoError(t, err)

		assert.Equal(t, newDataTypeID, updatedEntity.DataTypeID, "Entity should have updated data_type_id")
		assert.Equal(t, newDataTypeSlug, updatedEntity.DataTypeSlug, "Entity should have updated data_type_slug")
		assert.Equal(t, newData, updatedEntity.Data, "Entity should have updated data")
	})

	t.Run("clears data fields", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with initial data
		entity := createEntityWithData(t, client, userID, tenantID, "test_entity", &dataTypeID, &dataTypeSlug, testData)

		// Clear all data fields
		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)
		updatedEntity, err := client.EntityWithDataMixin.UpdateOneID(entity.ID).
			ClearDataTypeID().
			ClearDataTypeSlug().
			ClearData().
			Save(ctx)
		require.NoError(t, err)

		assert.Equal(t, uuid.Nil, updatedEntity.DataTypeID, "Entity should have cleared data_type_id")
		assert.Empty(t, updatedEntity.DataTypeSlug, "Entity should have cleared data_type_slug")
		assert.Nil(t, updatedEntity.Data, "Entity should have cleared data")
	})
}

func TestDataMixinQueries(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tenantID := uuid.New()
	dataTypeID1 := uuid.New()
	dataTypeID2 := uuid.New()
	dataTypeSlug1 := "data-type-1"
	dataTypeSlug2 := "data-type-2"
	testData1 := map[string]interface{}{"type": "type1", "value": 100}
	testData2 := map[string]interface{}{"type": "type2", "value": 200}

	t.Run("queries entities by data_type_id", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities with different data types
		entity1 := createEntityWithData(t, client, userID, tenantID, "entity1", &dataTypeID1, &dataTypeSlug1, testData1)
		entity2 := createEntityWithData(t, client, userID, tenantID, "entity2", &dataTypeID2, &dataTypeSlug2, testData2)
		createEntityWithData(t, client, userID, tenantID, "entity3", nil, nil, nil) // No data type

		ctx := requestContext(t, authn.ROLE_READER, userID, tenantID)

		// Query by data_type_id1
		entities, err := client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID1)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find exactly one entity with data_type_id1")
		assert.Equal(t, entity1.ID, entities[0].ID, "Should find the correct entity")

		// Query by data_type_id2
		entities, err = client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID2)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find exactly one entity with data_type_id2")
		assert.Equal(t, entity2.ID, entities[0].ID, "Should find the correct entity")

		// Query by non-existent data_type_id
		entities, err = client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(uuid.New())).
			All(ctx)
		require.NoError(t, err)
		assert.Empty(t, entities, "Should find no entities with non-existent data_type_id")
	})

	t.Run("queries entities by data_type_slug", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities with different data type slugs
		entity1 := createEntityWithData(t, client, userID, tenantID, "entity1", &dataTypeID1, &dataTypeSlug1, testData1)
		entity2 := createEntityWithData(t, client, userID, tenantID, "entity2", &dataTypeID2, &dataTypeSlug2, testData2)
		createEntityWithData(t, client, userID, tenantID, "entity3", nil, nil, nil) // No data type

		ctx := requestContext(t, authn.ROLE_READER, userID, tenantID)

		// Query by data_type_slug1
		entities, err := client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeSlugEQ(dataTypeSlug1)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find exactly one entity with data_type_slug1")
		assert.Equal(t, entity1.ID, entities[0].ID, "Should find the correct entity")

		// Query by data_type_slug2
		entities, err = client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeSlugEQ(dataTypeSlug2)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find exactly one entity with data_type_slug2")
		assert.Equal(t, entity2.ID, entities[0].ID, "Should find the correct entity")

		// Query by non-existent data_type_slug
		entities, err = client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeSlugEQ("non-existent-slug")).
			All(ctx)
		require.NoError(t, err)
		assert.Empty(t, entities, "Should find no entities with non-existent data_type_slug")
	})

	t.Run("queries entities with complex WHERE conditions", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities with different combinations
		entity1 := createEntityWithData(t, client, userID, tenantID, "entity1", &dataTypeID1, &dataTypeSlug1, testData1)
		entity2 := createEntityWithData(t, client, userID, tenantID, "entity2", &dataTypeID2, &dataTypeSlug2, testData2)
		entity3 := createEntityWithData(t, client, userID, tenantID, "entity3", &dataTypeID1, &dataTypeSlug1, testData2) // Same type, different data

		ctx := requestContext(t, authn.ROLE_READER, userID, tenantID)

		// Query by data_type_id1 and specific string field
		entities, err := client.EntityWithDataMixin.Query().
			Where(
				entitywithdatamixin.And(
					entitywithdatamixin.DataTypeIDEQ(dataTypeID1),
					entitywithdatamixin.StringFieldEQ("entity1"),
				),
			).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find exactly one entity matching both conditions")
		assert.Equal(t, entity1.ID, entities[0].ID, "Should find the correct entity")

		// Query by data_type_id1 OR data_type_id2
		entities, err = client.EntityWithDataMixin.Query().
			Where(
				entitywithdatamixin.Or(
					entitywithdatamixin.DataTypeIDEQ(dataTypeID1),
					entitywithdatamixin.DataTypeIDEQ(dataTypeID2),
				),
			).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 3, "Should find all entities with either data type")

		// Verify all entities are returned
		entityIDs := make([]int, len(entities))
		for i, entity := range entities {
			entityIDs[i] = entity.ID
		}
		assert.Contains(t, entityIDs, entity1.ID)
		assert.Contains(t, entityIDs, entity2.ID)
		assert.Contains(t, entityIDs, entity3.ID)
	})

	t.Run("counts entities by data type", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities with different data types
		createEntityWithData(t, client, userID, tenantID, "entity1", &dataTypeID1, &dataTypeSlug1, testData1)
		createEntityWithData(t, client, userID, tenantID, "entity2", &dataTypeID1, &dataTypeSlug1, testData2)
		createEntityWithData(t, client, userID, tenantID, "entity3", &dataTypeID2, &dataTypeSlug2, testData1)
		createEntityWithData(t, client, userID, tenantID, "entity4", nil, nil, nil) // No data type

		ctx := requestContext(t, authn.ROLE_READER, userID, tenantID)

		// Count entities with data_type_id1
		count, err := client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID1)).
			Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should count 2 entities with data_type_id1")

		// Count entities with data_type_id2
		count, err = client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID2)).
			Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "Should count 1 entity with data_type_id2")

		// Count all entities
		count, err = client.EntityWithDataMixin.Query().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 4, count, "Should count all 4 entities")
	})
}

func TestDataMixinBulkOperations(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tenantID := uuid.New()
	dataTypeID1 := uuid.New()
	dataTypeID2 := uuid.New()
	dataTypeSlug1 := "data-type-1"
	dataTypeSlug2 := "data-type-2"
	testData1 := map[string]interface{}{"type": "type1", "value": 100}
	testData2 := map[string]interface{}{"type": "type2", "value": 200}

	t.Run("bulk update entities by data_type_id", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities with different data types
		createEntityWithData(t, client, userID, tenantID, "entity1", &dataTypeID1, &dataTypeSlug1, testData1)
		createEntityWithData(t, client, userID, tenantID, "entity2", &dataTypeID1, &dataTypeSlug1, testData2)
		createEntityWithData(t, client, userID, tenantID, "entity3", &dataTypeID2, &dataTypeSlug2, testData1)

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Bulk update entities with data_type_id1
		newSlug := "updated-data-type-1"
		count, err := client.EntityWithDataMixin.Update().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID1)).
			SetDataTypeSlug(newSlug).
			Save(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should update 2 entities with data_type_id1")

		// Verify the updates
		entities, err := client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID1)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 2, "Should find 2 entities with data_type_id1")

		for _, entity := range entities {
			assert.Equal(t, newSlug, entity.DataTypeSlug, "Entity should have updated data_type_slug")
		}

		// Verify entity with data_type_id2 was not updated
		entities, err = client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID2)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find 1 entity with data_type_id2")
		assert.Equal(t, dataTypeSlug2, entities[0].DataTypeSlug, "Entity should have original data_type_slug")
	})

	t.Run("bulk delete entities by data_type_slug", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities with different data types
		createEntityWithData(t, client, userID, tenantID, "entity1", &dataTypeID1, &dataTypeSlug1, testData1)
		createEntityWithData(t, client, userID, tenantID, "entity2", &dataTypeID1, &dataTypeSlug1, testData2)
		createEntityWithData(t, client, userID, tenantID, "entity3", &dataTypeID2, &dataTypeSlug2, testData1)

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Bulk delete entities with data_type_slug1
		count, err := client.EntityWithDataMixin.Delete().
			Where(entitywithdatamixin.DataTypeSlugEQ(dataTypeSlug1)).
			Exec(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should delete 2 entities with data_type_slug1")

		// Verify the deletions
		entities, err := client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeSlugEQ(dataTypeSlug1)).
			All(ctx)
		require.NoError(t, err)
		assert.Empty(t, entities, "Should find no entities with data_type_slug1")

		// Verify entity with data_type_slug2 still exists
		entities, err = client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeSlugEQ(dataTypeSlug2)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find 1 entity with data_type_slug2")
	})
}

func TestPatchDataTypeIdSlugInput(t *testing.T) {
	t.Parallel()

	dataTypeID := uuid.New()
	dataTypeSlug := "test-data-type"
	dt := &json_schema.DataType{
		ID:   dataTypeID,
		Slug: dataTypeSlug,
	}

	t.Run("patches struct with both DataTypeID and DataTypeSlug fields", func(t *testing.T) {
		t.Parallel()

		// Create a struct with both fields
		type TestInput struct {
			DataTypeID   uuid.UUID
			DataTypeSlug string
			OtherField   string
		}

		input := &TestInput{
			DataTypeID:   uuid.New(), // Different ID to verify patching
			DataTypeSlug: "old-slug", // Different slug to verify patching
			OtherField:   "unchanged",
		}

		// Patch the input
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Verify the fields were patched
		assert.Equal(t, dataTypeID, input.DataTypeID, "DataTypeID should be patched")
		assert.Equal(t, dataTypeSlug, input.DataTypeSlug, "DataTypeSlug should be patched")
		assert.Equal(t, "unchanged", input.OtherField, "Other fields should remain unchanged")
	})

	t.Run("patches struct with only DataTypeID field", func(t *testing.T) {
		t.Parallel()

		// Create a struct with only DataTypeID field
		type TestInput struct {
			DataTypeID uuid.UUID
			OtherField string
		}

		input := &TestInput{
			DataTypeID: uuid.New(), // Different ID to verify patching
			OtherField: "unchanged",
		}

		// Patch the input
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Verify only DataTypeID was patched
		assert.Equal(t, dataTypeID, input.DataTypeID, "DataTypeID should be patched")
		assert.Equal(t, "unchanged", input.OtherField, "Other fields should remain unchanged")
	})

	t.Run("patches struct with only DataTypeSlug field", func(t *testing.T) {
		t.Parallel()

		// Create a struct with only DataTypeSlug field
		type TestInput struct {
			DataTypeSlug string
			OtherField   string
		}

		input := &TestInput{
			DataTypeSlug: "old-slug", // Different slug to verify patching
			OtherField:   "unchanged",
		}

		// Patch the input
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Verify only DataTypeSlug was patched
		assert.Equal(t, dataTypeSlug, input.DataTypeSlug, "DataTypeSlug should be patched")
		assert.Equal(t, "unchanged", input.OtherField, "Other fields should remain unchanged")
	})

	t.Run("handles struct with no matching fields", func(t *testing.T) {
		t.Parallel()

		// Create a struct with no matching fields
		type TestInput struct {
			SomeField  string
			OtherField int
		}

		input := &TestInput{
			SomeField:  "unchanged",
			OtherField: 42,
		}

		// Patch the input - should not panic or change anything
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Verify fields remain unchanged
		assert.Equal(t, "unchanged", input.SomeField, "SomeField should remain unchanged")
		assert.Equal(t, 42, input.OtherField, "OtherField should remain unchanged")
	})

	t.Run("handles struct with wrong field types", func(t *testing.T) {
		t.Parallel()

		// Create a struct with wrong field types
		type TestInput struct {
			DataTypeID   string // Wrong type - should be uuid.UUID
			DataTypeSlug int    // Wrong type - should be string
			OtherField   string
		}

		input := &TestInput{
			DataTypeID:   "old-id",
			DataTypeSlug: 123,
			OtherField:   "unchanged",
		}

		// Patch the input - should not panic or change anything
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Verify fields remain unchanged due to type mismatch
		assert.Equal(t, "old-id", input.DataTypeID, "DataTypeID should remain unchanged due to type mismatch")
		assert.Equal(t, 123, input.DataTypeSlug, "DataTypeSlug should remain unchanged due to type mismatch")
		assert.Equal(t, "unchanged", input.OtherField, "Other fields should remain unchanged")
	})

	t.Run("handles non-pointer struct", func(t *testing.T) {
		t.Parallel()

		// Create a struct (not a pointer)
		type TestInput struct {
			DataTypeID   uuid.UUID
			DataTypeSlug string
			OtherField   string
		}

		input := TestInput{
			DataTypeID:   uuid.New(),
			DataTypeSlug: "old-slug",
			OtherField:   "unchanged",
		}

		// Patch the input - should work with non-pointer struct
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Note: Since we're passing by value, the original struct won't be modified
		// This test verifies the function doesn't panic with non-pointer structs
		assert.Equal(t, "unchanged", input.OtherField, "Other fields should remain unchanged")
	})

	t.Run("handles nil input", func(t *testing.T) {
		t.Parallel()

		// Test with nil input - should not panic
		mixin.PatchDataTypeIdSlugInput(nil, dt)
		// If we reach here, the function handled nil input gracefully
	})

	t.Run("handles nil DataType", func(t *testing.T) {
		t.Parallel()

		type TestInput struct {
			DataTypeID   uuid.UUID
			DataTypeSlug string
		}

		input := &TestInput{
			DataTypeID:   uuid.New(),
			DataTypeSlug: "old-slug",
		}

		originalID := input.DataTypeID
		originalSlug := input.DataTypeSlug

		// Test with nil DataType - should not panic or change anything
		mixin.PatchDataTypeIdSlugInput(input, nil)

		// Verify fields remain unchanged
		assert.Equal(t, originalID, input.DataTypeID, "DataTypeID should remain unchanged")
		assert.Equal(t, originalSlug, input.DataTypeSlug, "DataTypeSlug should remain unchanged")
	})

	t.Run("handles non-struct input", func(t *testing.T) {
		t.Parallel()

		// Test with non-struct input - should not panic
		input := "not a struct"
		mixin.PatchDataTypeIdSlugInput(input, dt)
		// If we reach here, the function handled non-struct input gracefully
	})

	t.Run("handles unexported fields", func(t *testing.T) {
		t.Parallel()

		// Create a struct with unexported matching fields
		type TestInput struct {
			dataTypeID   uuid.UUID // unexported - should not be patched
			dataTypeSlug string    // unexported - should not be patched
			OtherField   string
		}

		input := &TestInput{
			dataTypeID:   uuid.New(),
			dataTypeSlug: "old-slug",
			OtherField:   "unchanged",
		}

		originalID := input.dataTypeID
		originalSlug := input.dataTypeSlug

		// Patch the input - unexported fields should not be modified
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Verify unexported fields remain unchanged
		assert.Equal(t, originalID, input.dataTypeID, "Unexported dataTypeID should remain unchanged")
		assert.Equal(t, originalSlug, input.dataTypeSlug, "Unexported dataTypeSlug should remain unchanged")
		assert.Equal(t, "unchanged", input.OtherField, "Other fields should remain unchanged")
	})

	t.Run("patches struct with pointer-type fields", func(t *testing.T) {
		t.Parallel()

		// This matches the actual generated model types (e.g. CreatePickingOrderWithItemsInput)
		type TestInput struct {
			DataTypeID   *uuid.UUID
			DataTypeSlug *string
			OtherField   string
		}

		oldID := uuid.New()
		oldSlug := "old-slug"
		input := &TestInput{
			DataTypeID:   &oldID,
			DataTypeSlug: &oldSlug,
			OtherField:   "unchanged",
		}

		mixin.PatchDataTypeIdSlugInput(input, dt)

		assert.NotNil(t, input.DataTypeID, "DataTypeID should not be nil")
		assert.Equal(t, dataTypeID, *input.DataTypeID, "DataTypeID should be patched")
		assert.NotNil(t, input.DataTypeSlug, "DataTypeSlug should not be nil")
		assert.Equal(t, dataTypeSlug, *input.DataTypeSlug, "DataTypeSlug should be patched")
		assert.Equal(t, "unchanged", input.OtherField, "Other fields should remain unchanged")
	})

	t.Run("patches struct with nil pointer-type fields", func(t *testing.T) {
		t.Parallel()

		type TestInput struct {
			DataTypeID   *uuid.UUID
			DataTypeSlug *string
		}

		input := &TestInput{} // both fields are nil

		mixin.PatchDataTypeIdSlugInput(input, dt)

		assert.NotNil(t, input.DataTypeID, "DataTypeID should be set")
		assert.Equal(t, dataTypeID, *input.DataTypeID, "DataTypeID should be patched")
		assert.NotNil(t, input.DataTypeSlug, "DataTypeSlug should be set")
		assert.Equal(t, dataTypeSlug, *input.DataTypeSlug, "DataTypeSlug should be patched")
	})

	t.Run("patches struct with map input does nothing", func(t *testing.T) {
		t.Parallel()

		// This is the original bug: passing map[string]any instead of a struct
		input := map[string]any{
			"DataTypeID":   uuid.New(),
			"DataTypeSlug": "old-slug",
		}

		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Map should be unchanged - function silently skips non-structs
		assert.NotEqual(t, dataTypeID, input["DataTypeID"], "Map should not be patched")
	})

	t.Run("verifies reflection behavior", func(t *testing.T) {
		t.Parallel()

		type TestInput struct {
			DataTypeID   uuid.UUID
			DataTypeSlug string
		}

		input := &TestInput{
			DataTypeID:   uuid.New(),
			DataTypeSlug: "old-slug",
		}

		// Verify reflection works as expected
		v := reflect.ValueOf(input)
		assert.Equal(t, reflect.Ptr, v.Kind(), "Input should be a pointer")

		v = v.Elem()
		assert.Equal(t, reflect.Struct, v.Kind(), "Input should point to a struct")

		// Test field access
		dataTypeIDField := v.FieldByName("DataTypeID")
		assert.True(t, dataTypeIDField.IsValid(), "DataTypeID field should be valid")
		assert.True(t, dataTypeIDField.CanSet(), "DataTypeID field should be settable")
		assert.Equal(t, reflect.TypeOf(uuid.UUID{}), dataTypeIDField.Type(), "DataTypeID field should have correct type")

		dataTypeSlugField := v.FieldByName("DataTypeSlug")
		assert.True(t, dataTypeSlugField.IsValid(), "DataTypeSlug field should be valid")
		assert.True(t, dataTypeSlugField.CanSet(), "DataTypeSlug field should be settable")
		assert.Equal(t, reflect.TypeOf(""), dataTypeSlugField.Type(), "DataTypeSlug field should have correct type")
	})
}

func TestDataMixinIntegration(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tenantID := uuid.New()
	dataTypeID := uuid.New()
	dataTypeSlug := "integration-test-type"

	t.Run("full lifecycle with data fields", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entity with data fields
		initialData := map[string]interface{}{
			"name":        "Test Entity",
			"description": "A test entity for integration testing",
			"metadata": map[string]interface{}{
				"version": "1.0",
				"author":  "test-user",
			},
		}

		entity, err := client.EntityWithDataMixin.Create().
			SetStringField("integration_test").
			SetDataTypeID(dataTypeID).
			SetDataTypeSlug(dataTypeSlug).
			SetData(initialData).
			Save(ctx)
		require.NoError(t, err)

		// Verify creation
		assert.Equal(t, dataTypeID, entity.DataTypeID)
		assert.Equal(t, dataTypeSlug, entity.DataTypeSlug)
		assert.Equal(t, initialData, entity.Data)

		// Update data
		updatedData := map[string]interface{}{
			"name":        "Updated Test Entity",
			"description": "An updated test entity",
			"metadata": map[string]interface{}{
				"version": "2.0",
				"author":  "updated-user",
			},
			"new_field": "new_value",
		}

		updatedEntity, err := client.EntityWithDataMixin.UpdateOneID(entity.ID).
			SetData(updatedData).
			Save(ctx)
		require.NoError(t, err)

		// Verify update
		assert.Equal(t, dataTypeID, updatedEntity.DataTypeID, "DataTypeID should remain unchanged")
		assert.Equal(t, dataTypeSlug, updatedEntity.DataTypeSlug, "DataTypeSlug should remain unchanged")
		assert.Equal(t, updatedData, updatedEntity.Data, "Data should be updated")

		// Query by data type
		entities, err := client.EntityWithDataMixin.Query().
			Where(entitywithdatamixin.DataTypeIDEQ(dataTypeID)).
			All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find the entity by data type ID")
		assert.Equal(t, entity.ID, entities[0].ID, "Should find the correct entity")

		// Delete entity
		err = client.EntityWithDataMixin.DeleteOneID(entity.ID).Exec(ctx)
		require.NoError(t, err)

		// Verify deletion
		_, err = client.EntityWithDataMixin.Get(ctx, entity.ID)
		require.Error(t, err, "Entity should be deleted")
	})

	t.Run("PatchDataTypeIdSlugInput integration", func(t *testing.T) {
		t.Parallel()

		// Create a mock input struct that might be used in GraphQL mutations
		type CreateEntityInput struct {
			StringField  string
			DataTypeID   uuid.UUID
			DataTypeSlug string
			Data         map[string]interface{}
		}

		input := &CreateEntityInput{
			StringField:  "test_entity",
			DataTypeID:   uuid.New(), // Will be overridden
			DataTypeSlug: "old-slug", // Will be overridden
			Data: map[string]interface{}{
				"field1": "value1",
			},
		}

		// Create DataType
		dt := &json_schema.DataType{
			ID:   dataTypeID,
			Slug: dataTypeSlug,
		}

		// Patch the input
		mixin.PatchDataTypeIdSlugInput(input, dt)

		// Verify patching worked
		assert.Equal(t, dataTypeID, input.DataTypeID, "DataTypeID should be patched")
		assert.Equal(t, dataTypeSlug, input.DataTypeSlug, "DataTypeSlug should be patched")
		assert.Equal(t, "test_entity", input.StringField, "Other fields should remain unchanged")
		assert.Equal(t, map[string]interface{}{"field1": "value1"}, input.Data, "Data should remain unchanged")

		// Use the patched input to create an entity
		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		entity, err := client.EntityWithDataMixin.Create().
			SetStringField(input.StringField).
			SetDataTypeID(input.DataTypeID).
			SetDataTypeSlug(input.DataTypeSlug).
			SetData(input.Data).
			Save(ctx)
		require.NoError(t, err)

		// Verify entity was created with patched values
		assert.Equal(t, input.DataTypeID, entity.DataTypeID)
		assert.Equal(t, input.DataTypeSlug, entity.DataTypeSlug)
		assert.Equal(t, input.StringField, entity.StringField)
		assert.Equal(t, input.Data, entity.Data)
	})
}
