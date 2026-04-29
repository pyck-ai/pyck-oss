package mixin_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen/entitywithhistorymixin"
)

// Helper function to create an entity and verify its history fields
func createEntityWithHistory(t *testing.T, client *gen.Client, userID uuid.UUID, name string) *gen.EntityWithHistoryMixin {
	t.Helper()

	ctx := requestContext(t, authn.ROLE_WRITER, userID, uuid.New())
	entity, err := client.EntityWithHistoryMixin.Create().
		SetName(name).
		SetStringField("test_field").
		Save(ctx)
	require.NoError(t, err)
	assert.Equal(t, userID, entity.CreatedBy, "Entity should have correct created_by")
	assert.False(t, entity.CreatedAt.IsZero(), "Entity should have created_at set")
	return entity
}

func TestHistoryMixinHook(t *testing.T) {
	t.Parallel()

	t.Run("hook automatically sets created_at and created_by on create", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with user1
		ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		beforeCreate := time.Now()
		entity, err := client.EntityWithHistoryMixin.Create().
			SetName("test_entity").
			SetStringField("test_field").
			Save(ctx)
		afterCreate := time.Now()

		require.NoError(t, err)

		// Created fields should be set and within the time range of the create operation
		assert.False(t, entity.CreatedAt.IsZero(), "created_at should be set on create")
		assert.True(t, entity.CreatedAt.After(beforeCreate) || entity.CreatedAt.Equal(beforeCreate), "created_at should be set to current time")
		assert.True(t, entity.CreatedAt.Before(afterCreate) || entity.CreatedAt.Equal(afterCreate), "created_at should be set to current time")
		assert.Equal(t, userID1, entity.CreatedBy, "created_by should be set to user ID")

		// Updated fields should be zero
		assert.True(t, entity.UpdatedAt.IsZero(), "updated_at should be zero on create")
		assert.Equal(t, uuid.Nil, entity.UpdatedBy, "updated_by should be nil on create")

		// Deleted fields should be zero
		assert.True(t, entity.DeletedAt.IsZero(), "deleted_at should be zero on create")
		assert.Equal(t, uuid.Nil, entity.DeletedBy, "deleted_by should be nil on create")
	})

	t.Run("hook automatically sets updated_at and updated_by on update", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with user1
		entity := createEntityWithHistory(t, client, userID1, "test_entity")

		// Update entity with different user
		ctx := requestContext(t, authn.ROLE_WRITER, userID2, tenantID1)
		beforeUpdate := time.Now()
		updatedEntity, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetStringField("updated_field").
			Save(ctx)
		afterUpdate := time.Now()

		require.NoError(t, err)

		// Created fields should remain unchanged
		assert.Equal(t, entity.CreatedAt.UTC(), updatedEntity.CreatedAt.UTC(), "created_at should not change on update")
		assert.Equal(t, userID1, updatedEntity.CreatedBy, "created_by should not change on update")

		// Updated fields should be set and within the time range of the update operation
		assert.False(t, updatedEntity.UpdatedAt.IsZero(), "updated_at should be set on update")
		assert.True(t, updatedEntity.UpdatedAt.After(beforeUpdate) || updatedEntity.UpdatedAt.Equal(beforeUpdate), "updated_at should be set to current time")
		assert.True(t, updatedEntity.UpdatedAt.Before(afterUpdate) || updatedEntity.UpdatedAt.Equal(afterUpdate), "updated_at should be set to current time")
		assert.Equal(t, userID2, updatedEntity.UpdatedBy, "updated_by should be set to user2 ID")

		// Deleted fields should remain unchanged
		assert.True(t, updatedEntity.DeletedAt.IsZero(), "deleted_at should remain zero on update")
		assert.Equal(t, uuid.Nil, updatedEntity.DeletedBy, "deleted_by should remain nil on update")
	})

	t.Run("hook automatically sets deleted_at and deleted_by on soft delete", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with user1
		entity := createEntityWithHistory(t, client, userID1, "test_entity")

		assert.True(t, entity.UpdatedAt.IsZero(), "updated_at should be zero after creation")
		assert.Equal(t, uuid.Nil, entity.UpdatedBy, "updated_by should be nil after creation")

		assert.True(t, entity.DeletedAt.IsZero(), "deleted_at should be zero after creation")
		assert.Equal(t, uuid.Nil, entity.DeletedBy, "deleted_by should be nil after creation")

		// Soft delete entity with user2 (by setting deleted_at field)
		ctx := requestContext(t, authn.ROLE_WRITER, userID2, tenantID1)

		beforeDelete := time.Now()
		deletedEntity, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		afterDelete := time.Now()

		require.NoError(t, err)

		// Created fields should remain unchanged
		assert.Equal(t, entity.CreatedAt.UTC(), deletedEntity.CreatedAt.UTC(), "created_at should not change on soft delete")
		assert.Equal(t, entity.CreatedBy, deletedEntity.CreatedBy, "created_by should not change on soft delete")

		// Updated fields should remain unchanged (since we're doing a soft delete, not a regular update)
		assert.True(t, deletedEntity.UpdatedAt.IsZero(), "updated_at should remain zero on soft delete")
		assert.Equal(t, uuid.Nil, deletedEntity.UpdatedBy, "updated_by should remain nil on soft delete")

		// Deleted fields should be set and within the time range of the delete operation
		assert.False(t, deletedEntity.DeletedAt.IsZero(), "deleted_at should be set on soft delete")
		assert.True(t, deletedEntity.DeletedAt.After(beforeDelete) || deletedEntity.DeletedAt.Equal(beforeDelete), "deleted_at should be set to current time")
		assert.True(t, deletedEntity.DeletedAt.Before(afterDelete) || deletedEntity.DeletedAt.Equal(afterDelete), "deleted_at should be set to current time")
		assert.Equal(t, userID2, deletedEntity.DeletedBy, "deleted_by should be set to user2 ID")
	})

	t.Run("hook prevents creation without user context", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Try to create entity without user context
		_, err := client.EntityWithHistoryMixin.Create().
			SetName("test_entity").
			SetStringField("test_field").
			Save(context.Background())

		require.Error(t, err, "Hook should prevent creation without user context")
		assert.ErrorIs(t, err, mixin.ErrUnauthorized, "Error should be ErrUnauthorized")
	})

	t.Run("hook prevents update without user context", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with user context
		entity := createEntityWithHistory(t, client, userID1, "test_entity")

		// Try to update entity without user context
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetStringField("updated_field").
			Save(context.Background())

		require.Error(t, err, "Hook should prevent update without user context")
		assert.ErrorIs(t, err, mixin.ErrUnauthorized, "Error should be ErrUnauthorized")
	})

	t.Run("hook works with all user roles", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Test with different roles
		roles := []authn.Role{authn.ROLE_READER, authn.ROLE_WRITER, authn.ROLE_ADMIN}

		// Note: The roles are not enforced by the hook, but by the policies.
		// This means even the ROLE_READER should be able to create entities
		// here.

		for _, role := range roles {
			userCtx := requestContext(t, role, userID1, tenantID1)
			entity, err := client.EntityWithHistoryMixin.Create().
				SetName(fmt.Sprintf("entity_role_%d", role)).
				SetStringField("test_field").
				Save(userCtx)

			require.NoError(t, err, "Role %d should be able to create entities", role)
			assert.Equal(t, userID1, entity.CreatedBy, "Hook should set correct created_by for role %d", role)
			assert.False(t, entity.CreatedAt.IsZero(), "Hook should set created_at for role %d", role)
		}
	})

	t.Run("hook handles sequential creates and updates correctly", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with user1
		ctx1 := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		entity1, err := client.EntityWithHistoryMixin.Create().
			SetName("entity1").
			SetStringField("field1").
			Save(ctx1)
		require.NoError(t, err)

		// Create entity with user2
		ctx2 := requestContext(t, authn.ROLE_WRITER, userID2, tenantID1)
		entity2, err := client.EntityWithHistoryMixin.Create().
			SetName("entity2").
			SetStringField("field2").
			Save(ctx2)
		require.NoError(t, err)

		// Update entity1 with user2
		time.Sleep(1 * time.Millisecond) // Ensure different timestamps
		updatedEntity1, err := client.EntityWithHistoryMixin.UpdateOneID(entity1.ID).
			SetStringField("updated_field1").
			Save(ctx2)
		require.NoError(t, err)

		// Update entity2 with user1
		time.Sleep(1 * time.Millisecond) // Ensure different timestamps
		updatedEntity2, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetStringField("updated_field2").
			Save(ctx1)
		require.NoError(t, err)

		// Verify entity1 history
		assert.Equal(t, userID1, updatedEntity1.CreatedBy, "Entity1 should be created by user1")
		assert.Equal(t, userID2, updatedEntity1.UpdatedBy, "Entity1 should be updated by user2")
		assert.NotEqual(t, updatedEntity1.CreatedAt, updatedEntity1.UpdatedAt, "Created and updated times should be different")

		// Verify entity2 history
		assert.Equal(t, userID2, updatedEntity2.CreatedBy, "Entity2 should be created by user2")
		assert.Equal(t, userID1, updatedEntity2.UpdatedBy, "Entity2 should be updated by user1")
		assert.NotEqual(t, updatedEntity2.CreatedAt, updatedEntity2.UpdatedAt, "Created and updated times should be different")
	})
}

func TestHistoryMixinHook_TimestampsAreUTC(t *testing.T) {
	t.Parallel()

	client := newDBClient(t)
	defer client.Close()

	ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)

	// 1. Create — created_at must be UTC
	entity, err := client.EntityWithHistoryMixin.Create().
		SetName("utc_test").
		SetStringField("field").
		Save(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.UTC, entity.CreatedAt.Location(),
		"created_at should be in UTC")

	// 2. Update — updated_at must be UTC
	time.Sleep(1 * time.Millisecond)
	updated, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
		SetStringField("changed").
		Save(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.UTC, updated.UpdatedAt.Location(),
		"updated_at should be in UTC")
	assert.Equal(t, time.UTC, updated.CreatedAt.Location(),
		"created_at should remain in UTC after update")

	// 3. Soft-delete — deleted_at must be UTC (set via the hook's updated_at path)
	time.Sleep(1 * time.Millisecond)
	deleted, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
		SetDeletedAt(time.Now().UTC()).
		Save(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.UTC, deleted.DeletedAt.Location(),
		"deleted_at should be in UTC")
	assert.Equal(t, time.UTC, deleted.CreatedAt.Location(),
		"created_at should remain in UTC after soft-delete")
}

func TestHistoryMixinQueryFilter(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tenantID := uuid.New()

	t.Run("query filter hides soft-deleted entities by default", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create multiple entities
		entity1 := createEntityWithHistory(t, client, userID, "entity1")
		entity2 := createEntityWithHistory(t, client, userID, "entity2")
		entity3 := createEntityWithHistory(t, client, userID, "entity3")

		// Soft delete entity2
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Query should only return non-deleted entities
		entities, err := client.EntityWithHistoryMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 2, "Should only return non-deleted entities")

		// Verify returned entities are the correct ones
		entityIDs := make([]int, len(entities))
		for i, entity := range entities {
			entityIDs[i] = entity.ID
		}
		assert.Contains(t, entityIDs, entity1.ID, "Should contain entity1")
		assert.Contains(t, entityIDs, entity3.ID, "Should contain entity3")
		assert.NotContains(t, entityIDs, entity2.ID, "Should not contain soft-deleted entity2")
	})

	t.Run("query filter shows soft-deleted entities when showDeleted context is true", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create multiple entities
		entity1 := createEntityWithHistory(t, client, userID, "entity1")
		entity2 := createEntityWithHistory(t, client, userID, "entity2")

		// Soft delete entity2
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Query with showDeleted context should return all entities
		showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
		entities, err := client.EntityWithHistoryMixin.Query().All(showDeletedCtx)
		require.NoError(t, err)
		assert.Len(t, entities, 2, "Should return all entities including soft-deleted")

		// Verify both entities are returned
		entityIDs := make([]int, len(entities))
		for i, entity := range entities {
			entityIDs[i] = entity.ID
		}
		assert.Contains(t, entityIDs, entity1.ID, "Should contain entity1")
		assert.Contains(t, entityIDs, entity2.ID, "Should contain soft-deleted entity2")
	})

	t.Run("query filter works with specific entity lookups", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entities
		entity1 := createEntityWithHistory(t, client, userID, "entity1")
		entity2 := createEntityWithHistory(t, client, userID, "entity2")

		// Soft delete entity2
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Try to get non-deleted entity - should succeed
		foundEntity, err := client.EntityWithHistoryMixin.Get(ctx, entity1.ID)
		require.NoError(t, err)
		assert.Equal(t, entity1.ID, foundEntity.ID)

		// Try to get soft-deleted entity - should fail
		_, err = client.EntityWithHistoryMixin.Get(ctx, entity2.ID)
		require.Error(t, err, "Should not be able to get soft-deleted entity")

		// Try to get soft-deleted entity with showDeleted context - should succeed
		showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
		foundDeletedEntity, err := client.EntityWithHistoryMixin.Get(showDeletedCtx, entity2.ID)
		require.NoError(t, err)
		assert.Equal(t, entity2.ID, foundDeletedEntity.ID)
	})

	t.Run("query filter works with WHERE conditions", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entities
		createEntityWithHistory(t, client, userID, "entity1")
		entity2 := createEntityWithHistory(t, client, userID, "entity2")
		createEntityWithHistory(t, client, userID, "entity3")

		// Soft delete entity2
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Query with name condition - should only return non-deleted entities
		entities, err := client.EntityWithHistoryMixin.Query().Where(
			entitywithhistorymixin.NameIn("entity1", "entity2", "entity3"),
		).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 2, "Should return only non-deleted entities matching condition")

		// Count should also respect soft-delete filter
		count, err := client.EntityWithHistoryMixin.Query().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should count only non-deleted entities")

		// Count with showDeleted should include soft-deleted entities
		showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
		countWithDeleted, err := client.EntityWithHistoryMixin.Query().Count(showDeletedCtx)
		require.NoError(t, err)
		assert.Equal(t, 3, countWithDeleted, "Should count all entities including soft-deleted")
	})

	t.Run("query filter handles entities with zero-time deleted_at", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entity - it should have zero-time deleted_at by default
		entity := createEntityWithHistory(t, client, userID, "entity1")

		// Verify the entity has zero-time deleted_at (not nil, but zero time)
		assert.True(t, entity.DeletedAt.IsZero(), "Entity should have zero-time deleted_at")

		// Query should return the entity (zero time is treated as not deleted)
		entities, err := client.EntityWithHistoryMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should return entity with zero-time deleted_at")
		assert.Equal(t, entity.ID, entities[0].ID, "Should return the correct entity")
	})
}

func TestHistoryMixinMutationFilter(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tenantID := uuid.New()

	t.Run("mutation filter prevents updates to soft-deleted entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entities
		entity1 := createEntityWithHistory(t, client, userID, "entity1")
		entity2 := createEntityWithHistory(t, client, userID, "entity2")

		// Soft delete entity2
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Try to update non-deleted entity - should succeed
		updatedEntity1, err := client.EntityWithHistoryMixin.UpdateOneID(entity1.ID).
			SetStringField("updated_field").
			Save(ctx)
		require.NoError(t, err)
		assert.Equal(t, "updated_field", updatedEntity1.StringField)

		// Try to update soft-deleted entity - should fail
		_, err = client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetStringField("should_not_update").
			Save(ctx)
		require.Error(t, err, "Should not be able to update soft-deleted entity")
		assert.Contains(t, err.Error(), "not found", "Error should indicate entity not found due to mutation filter")
	})

	t.Run("mutation filter prevents updates to soft-deleted entities when showDeleted context is true", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entity
		entity := createEntityWithHistory(t, client, userID, "entity1")

		// Soft delete entity
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Try to update soft-deleted entity with showDeleted context - should succeed
		showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
		_, err = client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetStringField("updated_deleted_entity").
			Save(showDeletedCtx)
		require.Error(t, err, "Should not be able to update soft-deleted entity")
		assert.Contains(t, err.Error(), "not found", "Error should indicate entity not found due to mutation filter")
	})

	t.Run("mutation filter works with bulk updates", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entities
		entity1 := createEntityWithHistory(t, client, userID, "entity1")
		entity2 := createEntityWithHistory(t, client, userID, "entity2")
		entity3 := createEntityWithHistory(t, client, userID, "entity3")

		// Soft delete entity2
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Bulk update should only affect non-deleted entities
		count, err := client.EntityWithHistoryMixin.Update().
			SetStringField("bulk_updated").
			Save(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should update only non-deleted entities")

		// Verify that only non-deleted entities were updated
		showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
		entities, err := client.EntityWithHistoryMixin.Query().All(showDeletedCtx)
		require.NoError(t, err)

		for _, entity := range entities {
			switch entity.ID {
			case entity1.ID, entity3.ID:
				assert.Equal(t, "bulk_updated", entity.StringField, "Non-deleted entity should be updated")
			case entity2.ID:
				assert.NotEqual(t, "bulk_updated", entity.StringField, "Soft-deleted entity should not be updated")
			}
		}
	})

	t.Run("mutation filter works with bulk delete operations", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entities
		createEntityWithHistory(t, client, userID, "entity1")
		entity2 := createEntityWithHistory(t, client, userID, "entity2")
		createEntityWithHistory(t, client, userID, "entity3")

		// Soft delete entity2
		_, err := client.EntityWithHistoryMixin.UpdateOneID(entity2.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// Bulk delete should only affect non-deleted entities
		count, err := client.EntityWithHistoryMixin.Delete().Exec(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should delete only non-deleted entities")

		// Verify that soft-deleted entity still exists
		showDeletedCtx := feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
		entities, err := client.EntityWithHistoryMixin.Query().All(showDeletedCtx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Only soft-deleted entity should remain")
		assert.Equal(t, entity2.ID, entities[0].ID, "Remaining entity should be the previously soft-deleted one")
	})

	t.Run("mutation filter handles entities with zero-time deleted_at", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)

		// Create entity
		entity := createEntityWithHistory(t, client, userID, "entity1")

		// The entity should be updatable (zero time deleted_at is treated as not deleted)
		// Since the entity was just created, it should have zero time deleted_at
		updatedEntity, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetStringField("updated_field").
			Save(ctx)
		require.NoError(t, err)
		assert.Equal(t, "updated_field", updatedEntity.StringField)
	})
}

func TestHistoryMixinIntegration(t *testing.T) {
	t.Parallel()

	userID1 := uuid.New()
	userID2 := uuid.New()
	tenantID := uuid.New()

	t.Run("complete entity lifecycle with history tracking", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		writerCtx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		adminCtx := requestContext(t, authn.ROLE_ADMIN, userID2, tenantID)

		// 1. Create entity
		beforeCreate := time.Now()
		entity, err := client.EntityWithHistoryMixin.Create().
			SetName("lifecycle_entity").
			SetStringField("initial_value").
			Save(writerCtx)
		require.NoError(t, err)

		// Verify creation history
		assert.Equal(t, userID1, entity.CreatedBy)
		assert.True(t, entity.CreatedAt.After(beforeCreate))
		assert.True(t, entity.UpdatedAt.IsZero())
		assert.Equal(t, uuid.Nil, entity.UpdatedBy)
		assert.True(t, entity.DeletedAt.IsZero())
		assert.Equal(t, uuid.Nil, entity.DeletedBy)

		// 2. Update entity
		time.Sleep(1 * time.Millisecond)
		beforeUpdate := time.Now()
		updatedEntity, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetStringField("updated_value").
			Save(adminCtx)
		require.NoError(t, err)

		// Verify update history
		assert.Equal(t, userID1, updatedEntity.CreatedBy, "Created by should remain unchanged")
		assert.Equal(t, entity.CreatedAt.UTC(), updatedEntity.CreatedAt.UTC(), "Created at should remain unchanged")
		assert.False(t, updatedEntity.UpdatedAt.IsZero())
		assert.True(t, updatedEntity.UpdatedAt.After(beforeUpdate))
		assert.Equal(t, userID2, updatedEntity.UpdatedBy)
		assert.True(t, updatedEntity.DeletedAt.IsZero())
		assert.Equal(t, uuid.Nil, updatedEntity.DeletedBy)

		// 3. Soft delete entity
		time.Sleep(1 * time.Millisecond)
		beforeDelete := time.Now()
		deletedEntity, err := client.EntityWithHistoryMixin.UpdateOneID(entity.ID).
			SetDeletedAt(time.Now()).
			Save(writerCtx)
		require.NoError(t, err)

		// Verify deletion history
		assert.Equal(t, userID1, deletedEntity.CreatedBy, "Created by should remain unchanged")
		assert.Equal(t, entity.CreatedAt.UTC(), deletedEntity.CreatedAt.UTC(), "Created at should remain unchanged")
		assert.Equal(t, updatedEntity.UpdatedAt.UTC(), deletedEntity.UpdatedAt.UTC(), "Updated at should remain unchanged")
		assert.Equal(t, userID2, deletedEntity.UpdatedBy, "Updated by should remain unchanged")
		assert.False(t, deletedEntity.DeletedAt.IsZero())
		assert.True(t, deletedEntity.DeletedAt.After(beforeDelete))
		assert.Equal(t, userID1, deletedEntity.DeletedBy)

		// 4. Verify entity is hidden from normal queries
		entities, err := client.EntityWithHistoryMixin.Query().All(writerCtx)
		require.NoError(t, err)
		assert.Empty(t, entities, "Soft-deleted entity should be hidden from normal queries")

		// 5. Verify entity is visible with showDeleted context
		showDeletedCtx := feature.Context(writerCtx, feature.FEATURE_SHOW_DELETED)
		entitiesWithDeleted, err := client.EntityWithHistoryMixin.Query().All(showDeletedCtx)
		require.NoError(t, err)
		assert.Len(t, entitiesWithDeleted, 1, "Soft-deleted entity should be visible with showDeleted context")

		foundEntity := entitiesWithDeleted[0]
		assert.Equal(t, deletedEntity.ID, foundEntity.ID)
		assert.Equal(t, deletedEntity.CreatedBy, foundEntity.CreatedBy)
		assert.Equal(t, deletedEntity.UpdatedBy, foundEntity.UpdatedBy)
		assert.Equal(t, deletedEntity.DeletedBy, foundEntity.DeletedBy)
	})

	t.Run("partial index annotation works correctly", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)

		// Create entity with specific name
		entity1, err := client.EntityWithHistoryMixin.Create().
			SetName("unique_name").
			SetStringField("field1").
			Save(ctx)
		require.NoError(t, err)

		// Should be able to create another entity with same name if first is soft-deleted
		_, err = client.EntityWithHistoryMixin.UpdateOneID(entity1.ID).
			SetDeletedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		// This should succeed due to partial index (soft-deleted entities don't conflict)
		entity2, err := client.EntityWithHistoryMixin.Create().
			SetName("unique_name").
			SetStringField("field2").
			Save(ctx)
		require.NoError(t, err)
		assert.Equal(t, "unique_name", entity2.Name)
		assert.Equal(t, "field2", entity2.StringField)
	})
}
