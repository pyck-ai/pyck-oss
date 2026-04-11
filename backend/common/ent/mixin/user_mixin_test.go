package mixin_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen/entitywithusermixin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create an entity and verify its user ID
func createEntityWithUser(t *testing.T, client *gen.Client, userID uuid.UUID, tenantID uuid.UUID, stringField string) *gen.EntityWithUserMixin {
	t.Helper()

	ctx := requestContext(t, authn.ROLE_WRITER, userID, tenantID)
	entity, err := client.EntityWithUserMixin.Create().
		SetStringField(stringField).
		Save(ctx)
	require.NoError(t, err)
	assert.Equal(t, userID, entity.UserID, "Entity should have correct user ID")
	return entity
}

func TestUserIDQueryFilter(t *testing.T) {
	t.Parallel()
	userID1 := uuid.New()
	userID2 := uuid.New()
	tenantID := uuid.New()

	t.Run("regular user sees only their own entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different users
		createEntityWithUser(t, client, userID1, tenantID, "entity1")
		createEntityWithUser(t, client, userID2, tenantID, "entity2")

		// Query as user1 - should only see user1 entities
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		entities, err := client.EntityWithUserMixin.Query().All(user1Ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "User1 should see only 1 entity")

		for _, entity := range entities {
			assert.Equal(t, userID1, entity.UserID)
		}

		// Query as user2 - should only see user2 entities
		user2Ctx := requestContext(t, authn.ROLE_WRITER, userID2, tenantID)
		entities2, err := client.EntityWithUserMixin.Query().All(user2Ctx)
		require.NoError(t, err)
		assert.Len(t, entities2, 1, "User2 should see only 1 entity")

		for _, entity := range entities2 {
			assert.Equal(t, userID2, entity.UserID)
		}
	})

	t.Run("system user bypasses user filter", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different users
		createEntityWithUser(t, client, userID1, tenantID, "entity1")
		createEntityWithUser(t, client, userID2, tenantID, "entity2")

		// System user should see all entities regardless of user
		entities, err := client.EntityWithUserMixin.Query().All(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Len(t, entities, 2, "System user should see all entities")

		// Verify both entities are returned
		userIDs := make([]uuid.UUID, len(entities))
		for i, entity := range entities {
			userIDs[i] = entity.UserID
		}

		assert.ElementsMatch(t, []uuid.UUID{userID1, userID2}, userIDs, "Both user IDs should be present in created entities")
	})

	t.Run("no user context denies access", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create an entity using regular user
		createEntityWithUser(t, client, userID1, tenantID, "entity1")

		// Query without user context should fail
		_, err := client.EntityWithUserMixin.Query().All(context.Background())
		require.Error(t, err, "Query without user should fail")
		assert.Contains(t, err.Error(), "no user", "Error should mention no user")
	})

	t.Run("user with nil user ID is denied access", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create an entity using regular user
		createEntityWithUser(t, client, userID1, tenantID, "entity1")

		// User with nil user ID should be denied access
		nilUserCtx := requestContext(t, authn.ROLE_READER, uuid.Nil, tenantID)

		_, err := client.EntityWithUserMixin.Query().All(nilUserCtx)
		require.Error(t, err, "User with nil user ID should be denied access")
		assert.Contains(t, err.Error(), authn.ErrUnauthorized.Error(), "Error should mention ErrUnauthorized")
	})

	t.Run("query filter works with specific entity lookups", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different users
		entity1 := createEntityWithUser(t, client, userID1, tenantID, "entity1")
		entity2 := createEntityWithUser(t, client, userID2, tenantID, "entity2")

		// User1 trying to get entity1 (same user) - should succeed
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		foundEntity, err := client.EntityWithUserMixin.Get(user1Ctx, entity1.ID)
		require.NoError(t, err)
		assert.Equal(t, entity1.ID, foundEntity.ID)
		assert.Equal(t, userID1, foundEntity.UserID)

		// User1 trying to get entity2 (different user) - should fail
		_, err = client.EntityWithUserMixin.Get(user1Ctx, entity2.ID)
		require.Error(t, err, "Should not be able to get entity from different user")
	})

	t.Run("query filter works with WHERE conditions", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create multiple entities for user1
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		entity1 := createEntityWithUser(t, client, userID1, tenantID, "entity1")
		createEntityWithUser(t, client, userID1, tenantID, "entity2") // entity2 - we don't need to store it

		// Create entity for user2
		entity3 := createEntityWithUser(t, client, userID2, tenantID, "entity3")

		// Query with specific ID condition - should only return entity1 if it belongs to user1
		entities, err := client.EntityWithUserMixin.Query().Where(
			entitywithusermixin.IDEQ(entity1.ID),
		).All(user1Ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find exactly one entity")
		assert.Equal(t, entity1.ID, entities[0].ID)
		assert.Equal(t, userID1, entities[0].UserID)

		// Count should also respect user filter
		count, err := client.EntityWithUserMixin.Query().Count(user1Ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should count only entities from user1")

		// Query with ID from different user should return empty
		entities, err = client.EntityWithUserMixin.Query().Where(
			entitywithusermixin.IDEQ(entity3.ID),
		).All(user1Ctx)
		require.NoError(t, err)
		assert.Empty(t, entities, "Should not find entity from different user")
	})

	t.Run("query filter works with null user_id entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity for user1
		createEntityWithUser(t, client, userID1, tenantID, "entity1")

		// Create entity with null user_id using system user
		nullUserEntity, err := client.EntityWithUserMixin.Create().
			SetStringField("null_user_entity").
			Save(systemContext(t, uuid.Nil))
		require.NoError(t, err)

		// Manually set user_id to null (simulate entities without user ownership)
		// This would typically be done through raw SQL or migration
		// For this test, we'll verify the filter logic handles null values

		// User1 should see their own entity plus entities with null user_id
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		entities, err := client.EntityWithUserMixin.Query().All(user1Ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 2, "User1 should see their entity plus null user_id entity")

		// Verify the entities returned
		userIDs := make([]uuid.UUID, len(entities))
		for i, entity := range entities {
			userIDs[i] = entity.UserID
		}
		assert.ElementsMatch(t, userIDs, []uuid.UUID{userID1, nullUserEntity.UserID}, "User1 should see their entity plus null user_id entity")
	})
}

func TestUserIDMutationFilter(t *testing.T) {
	t.Parallel()
	userID1 := uuid.New()
	userID2 := uuid.New()
	tenantID := uuid.New()

	t.Run("regular user can only update their own entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different users
		entity1 := createEntityWithUser(t, client, userID1, tenantID, "entity1")
		entity2 := createEntityWithUser(t, client, userID2, tenantID, "entity2")

		// User1 trying to update entity1 (same user) - should succeed
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		updatedEntity, err := client.EntityWithUserMixin.UpdateOneID(entity1.ID).
			SetStringField("updated_entity1").
			Save(user1Ctx)
		require.NoError(t, err)
		assert.Equal(t, entity1.ID, updatedEntity.ID)
		assert.Equal(t, userID1, updatedEntity.UserID)
		assert.Equal(t, "updated_entity1", updatedEntity.StringField)

		// User1 trying to update entity2 (different user) - should fail
		// The mutation filter should prevent this by applying a WHERE clause with user_id
		_, err = client.EntityWithUserMixin.UpdateOneID(entity2.ID).
			SetStringField("should_not_update").
			Save(user1Ctx)
		require.Error(t, err, "Should not be able to update entity from different user")
		assert.Contains(t, err.Error(), "not found", "Error should indicate entity not found due to mutation filter")
	})

	t.Run("system user bypasses user filter for updates", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different users
		entity1 := createEntityWithUser(t, client, userID1, tenantID, "entity1")
		entity2 := createEntityWithUser(t, client, userID2, tenantID, "entity2")

		// System user should be able to update entities from any user
		updatedEntity1, err := client.EntityWithUserMixin.UpdateOneID(entity1.ID).
			SetStringField("updated_by_system").
			Save(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Equal(t, entity1.ID, updatedEntity1.ID)
		assert.Equal(t, userID1, updatedEntity1.UserID)
		assert.Equal(t, "updated_by_system", updatedEntity1.StringField)

		updatedEntity2, err := client.EntityWithUserMixin.UpdateOneID(entity2.ID).
			SetStringField("updated_by_system").
			Save(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Equal(t, entity2.ID, updatedEntity2.ID)
		assert.Equal(t, userID2, updatedEntity2.UserID)
		assert.Equal(t, "updated_by_system", updatedEntity2.StringField)
	})

	t.Run("no user context denies update access", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create an entity using regular user
		entity := createEntityWithUser(t, client, userID1, tenantID, "entity1")

		// Update without user context should fail
		_, err := client.EntityWithUserMixin.UpdateOneID(entity.ID).
			SetStringField("should_not_update").
			Save(context.Background())
		require.Error(t, err, "Update without user should fail")
		assert.Contains(t, err.Error(), "no user", "Error should mention no user")
	})

	t.Run("user with nil user ID cannot update entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create an entity using regular user
		entity := createEntityWithUser(t, client, userID1, tenantID, "entity1")

		// User with nil user ID should not be able to update entities
		nilUserCtx := requestContext(t, authn.ROLE_WRITER, uuid.Nil, tenantID)
		_, err := client.EntityWithUserMixin.UpdateOneID(entity.ID).
			SetStringField("should_not_update").
			Save(nilUserCtx)
		require.Error(t, err, "User with nil user ID should not be able to update entities")
		assert.Contains(t, err.Error(), authn.ErrUnauthorized.Error(), "Error should mention ErrUnauthorized")
	})

	t.Run("mutation filter works with bulk updates", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create multiple entities for user1
		createEntityWithUser(t, client, userID1, tenantID, "entity1")
		createEntityWithUser(t, client, userID1, tenantID, "entity2")

		// Create entity for user2
		createEntityWithUser(t, client, userID2, tenantID, "entity3")

		// User1 bulk update should only affect user1 entities
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		count, err := client.EntityWithUserMixin.Update().
			SetStringField("updated_by_user1").
			Save(user1Ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should update only entities from user1")

		// Check user1 entities were updated
		user1Entities, err := client.EntityWithUserMixin.Query().
			Where(entitywithusermixin.UserIDEQ(userID1)).
			All(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Len(t, user1Entities, 2, "Should have 2 user1 entities")

		for _, entity := range user1Entities {
			assert.Equal(t, "updated_by_user1", entity.StringField, "User1 entity should be updated")
		}

		// Check user2 entity was NOT updated (should have original value)
		user2Entities, err := client.EntityWithUserMixin.Query().
			Where(entitywithusermixin.UserIDEQ(userID2)).
			All(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Len(t, user2Entities, 1, "Should have 1 user2 entity")

		user2Entity := user2Entities[0]
		assert.Equal(t, "entity3", user2Entity.StringField, "User2 entity should not be updated")
	})

	t.Run("mutation filter works with bulk delete operations", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create multiple entities for user1
		createEntityWithUser(t, client, userID1, tenantID, "entity1")
		createEntityWithUser(t, client, userID1, tenantID, "entity2")

		// Create entity for user2
		createEntityWithUser(t, client, userID2, tenantID, "entity3")

		// User1 bulk delete should only affect user1 entities
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		count, err := client.EntityWithUserMixin.Delete().Exec(user1Ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should delete only entities from user1")

		// Verify user2 entity still exists
		totalCount, err := client.EntityWithUserMixin.Query().Count(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Equal(t, 1, totalCount, "Only user2 entity should remain")
	})

	t.Run("mutation filter validates user ID in mutation", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity for user1
		entity := createEntityWithUser(t, client, userID1, tenantID, "entity1")

		// Try to update with different user context but entity belongs to user1
		user2Ctx := requestContext(t, authn.ROLE_WRITER, userID2, tenantID)
		_, err := client.EntityWithUserMixin.UpdateOneID(entity.ID).
			SetStringField("should_not_update").
			Save(user2Ctx)
		require.Error(t, err, "Should not be able to update entity from different user")
		assert.Contains(t, err.Error(), "not found", "Error should indicate entity not found due to mutation filter")
	})
}

func TestUserIDMutationHook(t *testing.T) {
	t.Parallel()
	userID1 := uuid.New()
	userID2 := uuid.New()
	tenantID := uuid.New()

	t.Run("hook automatically sets user ID on create", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with user1
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		entity, err := client.EntityWithUserMixin.Create().
			SetStringField("entity1").
			Save(user1Ctx)
		require.NoError(t, err)
		assert.Equal(t, userID1, entity.UserID, "Hook should set user ID from user context")

		// Create entity with user2
		user2Ctx := requestContext(t, authn.ROLE_WRITER, userID2, tenantID)
		entity2, err := client.EntityWithUserMixin.Create().
			SetStringField("entity2").
			Save(user2Ctx)
		require.NoError(t, err)
		assert.Equal(t, userID2, entity2.UserID, "Hook should set user ID from user context")
	})

	t.Run("hook prevents creation without user context", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Try to create entity without user context
		_, err := client.EntityWithUserMixin.Create().
			SetStringField("should_fail").
			Save(context.Background())
		require.Error(t, err, "Hook should prevent creation without user context")
		assert.Contains(t, err.Error(), "unauthorized", "Error should mention unauthorized")
	})

	t.Run("hook works with system user", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// System user should be able to create entities
		entity, err := client.EntityWithUserMixin.Create().
			SetStringField("system_entity").
			SetNillableUserID(&userID1).
			Save(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Equal(t, userID1, entity.UserID, "Hook should not override user ID for system user")
	})

	t.Run("hook works with different user roles", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Test with different roles
		roles := []authn.Role{authn.ROLE_READER, authn.ROLE_WRITER, authn.ROLE_ADMIN}

		for _, role := range roles {
			userCtx := requestContext(t, role, userID1, tenantID)
			entity, err := client.EntityWithUserMixin.Create().
				SetStringField(fmt.Sprintf("entity_role_%d", role)).
				Save(userCtx)
			require.NoError(t, err, "Role %d should be able to create entities", role)
			assert.Equal(t, userID1, entity.UserID, "Hook should set correct user ID for role %d", role)
		}
	})

	t.Run("hook does not modify user ID on update operations", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with user1
		entity := createEntityWithUser(t, client, userID1, tenantID, "entity1")
		originalUserID := entity.UserID

		// Update entity - user ID should remain unchanged
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		updatedEntity, err := client.EntityWithUserMixin.UpdateOneID(entity.ID).
			SetStringField("updated_entity").
			Save(user1Ctx)
		require.NoError(t, err)
		assert.Equal(t, originalUserID, updatedEntity.UserID, "Hook should not modify user ID on update")
	})

	t.Run("hook rejects creation with nil user ID in user context", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// User with nil user ID should be rejected
		nilUserCtx := requestContext(t, authn.ROLE_WRITER, uuid.Nil, tenantID)
		_, err := client.EntityWithUserMixin.Create().
			SetStringField("should_fail").
			Save(nilUserCtx)
		require.Error(t, err, "Hook should reject creation with nil user ID")
		assert.Contains(t, err.Error(), "unauthorized", "Error should mention unauthorized")
	})

	t.Run("hook works with sequential creates from different users", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities sequentially with different users to verify hook works correctly
		user1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
		entity1, err := client.EntityWithUserMixin.Create().
			SetStringField("entity1").
			Save(user1Ctx)
		require.NoError(t, err, "First entity creation should succeed")
		assert.Equal(t, userID1, entity1.UserID, "First entity should have user1 ID")

		user2Ctx := requestContext(t, authn.ROLE_WRITER, userID2, tenantID)
		entity2, err := client.EntityWithUserMixin.Create().
			SetStringField("entity2").
			Save(user2Ctx)
		require.NoError(t, err, "Second entity creation should succeed")
		assert.Equal(t, userID2, entity2.UserID, "Second entity should have user2 ID")

		// Verify both entities were created with correct user IDs
		entities, err := client.EntityWithUserMixin.Query().All(systemContext(t, tenantID))
		require.NoError(t, err)
		assert.Len(t, entities, 2, "Should have created 2 entities")

		userIDs := make([]uuid.UUID, len(entities))
		for i, entity := range entities {
			userIDs[i] = entity.UserID
		}
		assert.Contains(t, userIDs, userID1)
		assert.Contains(t, userIDs, userID2)
	})
}
