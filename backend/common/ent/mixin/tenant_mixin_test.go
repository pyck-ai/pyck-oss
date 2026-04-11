package mixin_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen/entitywithtenantmixin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create an entity and verify its tenant ID
func createEntityWithTenant(t *testing.T, client *gen.Client, tenantID uuid.UUID, name string) *gen.EntityWithTenantMixin {
	t.Helper()

	ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID)
	entity, err := client.EntityWithTenantMixin.Create().
		SetStringField(name).
		Save(ctx)
	require.NoError(t, err)
	assert.Equal(t, tenantID, entity.TenantID, "Entity should have correct tenant ID")
	return entity
}

func TestTenantIDQueryFilter(t *testing.T) {
	t.Parallel()

	t.Run("regular user sees only their tenant's entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different tenants
		createEntityWithTenant(t, client, tenantID1, "entity1")
		createEntityWithTenant(t, client, tenantID2, "entity2")

		// Query as tenant1 user - should only see tenant1 entities
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		entities, err := client.EntityWithTenantMixin.Query().All(tenant1Ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Tenant1 user should see only 1 entity")

		for _, entity := range entities {
			assert.Equal(t, tenantID1, entity.TenantID)
		}

		// Query as tenant2 user - should only see tenant2 entities
		tenant2Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID2)
		entities2, err := client.EntityWithTenantMixin.Query().All(tenant2Ctx)
		require.NoError(t, err)
		assert.Len(t, entities2, 1, "Tenant2 user should see only 1 entity")

		for _, entity := range entities2 {
			assert.Equal(t, tenantID2, entity.TenantID)
		}
	})

	t.Run("system user bypasses tenant filter", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different tenants
		createEntityWithTenant(t, client, tenantID1, "entity1")
		createEntityWithTenant(t, client, tenantID2, "entity2")

		// System user should see all entities regardless of tenant
		entities, err := client.EntityWithTenantMixin.Query().All(systemContext(t))
		require.NoError(t, err)
		require.Len(t, entities, 2, "System user should see all entities")

		// Verify both entities are returned
		tenantIDs := make([]uuid.UUID, len(entities))
		for i, entity := range entities {
			tenantIDs[i] = entity.TenantID
		}
		assert.Contains(t, tenantIDs, tenantID1)
		assert.Contains(t, tenantIDs, tenantID2)
	})

	t.Run("no user context denies access", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create an entity using regular user
		createEntityWithTenant(t, client, tenantID1, "entity1")

		// Query without user context should fail
		_, err := client.EntityWithTenantMixin.Query().All(context.Background())
		require.Error(t, err, "Query without user should fail")
		assert.Contains(t, err.Error(), "no user", "Error should mention no user")
	})

	t.Run("tenant query filter override allows access to multiple tenants", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different tenants
		createEntityWithTenant(t, client, tenantID1, "entity1")
		createEntityWithTenant(t, client, tenantID2, "entity2")
		createEntityWithTenant(t, client, tenantID3, "entity3")

		// Admin user with tenant filter override
		adminCtx := requestContext(t, authn.ROLE_ADMIN, userID1, tenantID1, tenantID2)

		entities, err := client.EntityWithTenantMixin.Query().All(adminCtx)
		require.NoError(t, err)
		assert.Len(t, entities, 2, "Should see entities from tenant1 and tenant2 only")

		// Verify only entities from tenant1 and tenant2 are returned
		tenantIDs := make([]uuid.UUID, len(entities))
		for i, entity := range entities {
			tenantIDs[i] = entity.TenantID
		}
		assert.ElementsMatch(t, []uuid.UUID{tenantID1, tenantID2}, tenantIDs)
	})

	t.Run("user with nil tenant ID is denied access", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create an entity using regular user
		createEntityWithTenant(t, client, tenantID1, "entity1")

		// User with nil tenant ID should be denied access
		nilTenantCtx := requestContext(t, authn.ROLE_READER, userID1, uuid.Nil)

		_, err := client.EntityWithTenantMixin.Query().All(nilTenantCtx)
		require.Error(t, err, "User with nil tenant ID should be denied access")
		assert.Contains(t, err.Error(), authn.ErrUnauthorized.Error(), "Error should mention ErrUnauthorized")
	})

	t.Run("query filter works with specific entity lookups", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different tenants
		entity1 := createEntityWithTenant(t, client, tenantID1, "entity1")
		entity2 := createEntityWithTenant(t, client, tenantID2, "entity2")

		// Tenant1 user trying to get entity1 (same tenant) - should succeed
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		foundEntity, err := client.EntityWithTenantMixin.Get(tenant1Ctx, entity1.ID)
		require.NoError(t, err)
		assert.Equal(t, entity1.ID, foundEntity.ID)
		assert.Equal(t, tenantID1, foundEntity.TenantID)

		// Tenant1 user trying to get entity2 (different tenant) - should fail
		_, err = client.EntityWithTenantMixin.Get(tenant1Ctx, entity2.ID)
		require.Error(t, err, "Should not be able to get entity from different tenant")
	})

	t.Run("query filter works with WHERE conditions", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create multiple entities for tenant1
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		entity1 := createEntityWithTenant(t, client, tenantID1, "entity1")
		createEntityWithTenant(t, client, tenantID1, "entity2") // entity2 - we don't need to store it

		// Create entity for tenant2
		entity3 := createEntityWithTenant(t, client, tenantID2, "entity3")

		// Query with specific ID condition - should only return entity1 if it belongs to tenant1
		entities, err := client.EntityWithTenantMixin.Query().Where(
			entitywithtenantmixin.IDEQ(entity1.ID),
		).All(tenant1Ctx)
		require.NoError(t, err)
		assert.Len(t, entities, 1, "Should find exactly one entity")
		assert.Equal(t, entity1.ID, entities[0].ID)
		assert.Equal(t, tenantID1, entities[0].TenantID)

		// Count should also respect tenant filter
		count, err := client.EntityWithTenantMixin.Query().Count(tenant1Ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should count only entities from tenant1")

		// Query with ID from different tenant should return empty
		entities, err = client.EntityWithTenantMixin.Query().Where(
			entitywithtenantmixin.IDEQ(entity3.ID),
		).All(tenant1Ctx)
		require.NoError(t, err)
		assert.Empty(t, entities, "Should not find entity from different tenant")
	})
}

func TestTenantIDMutationFilter(t *testing.T) {
	t.Parallel()
	tenantID1 := uuid.New()
	tenantID2 := uuid.New()

	t.Run("regular user can only update their tenant's entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different tenants
		entity1 := createEntityWithTenant(t, client, tenantID1, "entity1")
		entity2 := createEntityWithTenant(t, client, tenantID2, "entity2")

		// Tenant1 user trying to update entity1 (same tenant) - should succeed
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		updatedEntity, err := client.EntityWithTenantMixin.UpdateOneID(entity1.ID).
			SetStringField("updated_entity1").
			Save(tenant1Ctx)
		require.NoError(t, err)
		assert.Equal(t, entity1.ID, updatedEntity.ID)
		assert.Equal(t, tenantID1, updatedEntity.TenantID)
		assert.Equal(t, "updated_entity1", updatedEntity.StringField)

		// Tenant1 user trying to update entity2 (different tenant) - should fail
		// The mutation filter should prevent this by applying a WHERE clause with tenant_id
		_, err = client.EntityWithTenantMixin.UpdateOneID(entity2.ID).
			SetStringField("should_not_update").
			Save(tenant1Ctx)
		require.Error(t, err, "Should not be able to update entity from different tenant")
		assert.Contains(t, err.Error(), "not found", "Error should indicate entity not found due to mutation filter")
	})

	t.Run("system user bypasses tenant filter for updates", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities for different tenants
		entity1 := createEntityWithTenant(t, client, tenantID1, "entity1")
		entity2 := createEntityWithTenant(t, client, tenantID2, "entity2")

		// System user should be able to update entities from any tenant
		updatedEntity1, err := client.EntityWithTenantMixin.UpdateOneID(entity1.ID).
			SetStringField("updated_by_system").
			Save(systemContext(t, tenantID1))
		require.NoError(t, err)
		assert.Equal(t, entity1.ID, updatedEntity1.ID)
		assert.Equal(t, tenantID1, updatedEntity1.TenantID)
		assert.Equal(t, "updated_by_system", updatedEntity1.StringField)

		updatedEntity2, err := client.EntityWithTenantMixin.UpdateOneID(entity2.ID).
			SetStringField("updated_by_system").
			Save(systemContext(t, tenantID1))
		require.NoError(t, err)
		assert.Equal(t, entity2.ID, updatedEntity2.ID)
		assert.Equal(t, tenantID2, updatedEntity2.TenantID)
		assert.Equal(t, "updated_by_system", updatedEntity2.StringField)
	})

	t.Run("no user context denies update access", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create an entity using regular user
		entity := createEntityWithTenant(t, client, tenantID1, "entity1")

		// Update without user context should fail
		_, err := client.EntityWithTenantMixin.UpdateOneID(entity.ID).
			SetStringField("should_not_update").
			Save(context.Background())
		require.Error(t, err, "Update without user should fail")
		assert.Contains(t, err.Error(), "no user", "Error should mention no user")
	})

	t.Run("user with nil tenant ID cannot create or update entities", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// User with nil tenant ID should not be able to create entities
		nilTenantCtx := requestContext(t, authn.ROLE_WRITER, userID1, uuid.Nil)
		_, err := client.EntityWithTenantMixin.Create().Save(nilTenantCtx)
		require.Error(t, err, "User with nil tenant ID should not be able to create entities")
		assert.Contains(t, err.Error(), authn.ErrUnauthorized.Error(), "Error should mention ErrUnauthorized")
	})

	t.Run("mutation filter works with bulk updates", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create multiple entities for tenant1
		createEntityWithTenant(t, client, tenantID1, "entity1")
		createEntityWithTenant(t, client, tenantID1, "entity2")

		// Create entity for tenant2
		createEntityWithTenant(t, client, tenantID2, "entity3")

		// Tenant1 user bulk update should only affect tenant1 entities
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		count, err := client.EntityWithTenantMixin.Update().
			SetStringField("updated_by_tenant1").
			Save(tenant1Ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should update only entities from tenant1")

		// Check tenant1 entities were updated
		tenant1Entities, err := client.EntityWithTenantMixin.Query().
			Where(entitywithtenantmixin.TenantIDEQ(tenantID1)).
			All(systemContext(t))
		require.NoError(t, err)
		assert.Len(t, tenant1Entities, 2, "Should have 2 tenant1 entities")

		for _, entity := range tenant1Entities {
			assert.Equal(t, "updated_by_tenant1", entity.StringField, "Tenant1 entity should be updated")
		}

		// Check tenant2 entity was NOT updated (should have default values)
		tenant2Entities, err := client.EntityWithTenantMixin.Query().
			Where(entitywithtenantmixin.TenantIDEQ(tenantID2)).
			All(systemContext(t))
		require.NoError(t, err)
		assert.Len(t, tenant2Entities, 1, "Should have 1 tenant2 entity")

		tenant2Entity := tenant2Entities[0]
		assert.Equal(t, "entity3", tenant2Entity.StringField, "Tenant2 entity should not be updated")
	})

	t.Run("mutation filter works with bulk delete operations", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create multiple entities for tenant1
		createEntityWithTenant(t, client, tenantID1, "entity1")
		createEntityWithTenant(t, client, tenantID1, "entity2")

		// Create entity for tenant2
		createEntityWithTenant(t, client, tenantID2, "entity3")

		// Tenant1 user bulk delete should only affect tenant1 entities
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		count, err := client.EntityWithTenantMixin.Delete().Exec(tenant1Ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should delete only entities from tenant1")

		// Verify tenant2 entity still exists
		totalCount, err := client.EntityWithTenantMixin.Query().Count(systemContext(t))
		require.NoError(t, err)
		assert.Equal(t, 1, totalCount, "Only tenant2 entity should remain")
	})
}

func TestTenantIDMutationHook(t *testing.T) {
	t.Parallel()
	tenantID1 := uuid.New()
	tenantID2 := uuid.New()

	t.Run("hook automatically sets tenant ID on create", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with tenant1 user
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		entity, err := client.EntityWithTenantMixin.Create().Save(tenant1Ctx)
		require.NoError(t, err)
		assert.Equal(t, tenantID1, entity.TenantID, "Hook should set tenant ID from user context")

		// Create entity with tenant2 user
		tenant2Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID2)
		entity2, err := client.EntityWithTenantMixin.Create().Save(tenant2Ctx)
		require.NoError(t, err)
		assert.Equal(t, tenantID2, entity2.TenantID, "Hook should set tenant ID from user context")
	})

	t.Run("hook prevents creation without user context", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Try to create entity without user context
		_, err := client.EntityWithTenantMixin.Create().Save(context.Background())
		require.Error(t, err, "Hook should prevent creation without user context")
		assert.Contains(t, err.Error(), "unauthorized", "Error should mention unauthorized")
	})

	t.Run("hook works with system user", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// System user should be able to create entities
		entity, err := client.EntityWithTenantMixin.Create().SetTenantID(tenantID1).Save(systemContext(t, tenantID1))
		require.NoError(t, err)
		assert.Equal(t, tenantID1, entity.TenantID, "Hook should set tenant ID even for system user")
	})

	t.Run("hook works with different user roles", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Test with different roles
		roles := []authn.Role{authn.ROLE_READER, authn.ROLE_WRITER, authn.ROLE_ADMIN}

		for _, role := range roles {
			userCtx := requestContext(t, role, userID1, tenantID1)
			entity, err := client.EntityWithTenantMixin.Create().SetTenantID(tenantID1).Save(userCtx)

			if role == authn.ROLE_READER {
				require.Error(t, err, "Role %d should not be able to create entities", role)
				assert.Contains(t, err.Error(), authn.ErrUnauthorized.Error(), "Error should mention ErrUnauthorized")
			} else {
				require.NoError(t, err, "Role %d should be able to create entities", role)
				assert.Equal(t, tenantID1, entity.TenantID, "Hook should set correct tenant ID for role %d", role)
			}
		}
	})

	t.Run("hook does not modify tenant ID on update operations", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entity with tenant1 user
		entity := createEntityWithTenant(t, client, tenantID1, "entity1")
		originalTenantID := entity.TenantID

		// Update entity - tenant ID should remain unchanged
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		updatedEntity, err := client.EntityWithTenantMixin.UpdateOneID(entity.ID).Save(tenant1Ctx)
		require.NoError(t, err)
		assert.Equal(t, originalTenantID, updatedEntity.TenantID, "Hook should not modify tenant ID on update")
	})

	t.Run("hook rejects creation with nil tenant ID in user context", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// User with nil tenant ID should be rejected
		nilTenantCtx := requestContext(t, authn.ROLE_WRITER, userID1, uuid.Nil)
		_, err := client.EntityWithTenantMixin.Create().Save(nilTenantCtx)
		require.Error(t, err, "Hook should reject creation with nil tenant ID")
		assert.Contains(t, err.Error(), authn.ErrUnauthorized.Error(), "Error should mention ErrUnauthorized")
	})

	t.Run("hook works with sequential creates from different tenants", func(t *testing.T) {
		t.Parallel()

		client := newDBClient(t)
		defer client.Close()

		// Create entities sequentially with different tenants to verify hook works correctly
		tenant1Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID1)
		entity1, err := client.EntityWithTenantMixin.Create().
			SetStringField("").
			Save(tenant1Ctx)
		require.NoError(t, err, "First entity creation should succeed")
		assert.Equal(t, tenantID1, entity1.TenantID, "First entity should have tenant1 ID")

		tenant2Ctx := requestContext(t, authn.ROLE_WRITER, userID1, tenantID2)
		entity2, err := client.EntityWithTenantMixin.Create().
			SetStringField("").
			Save(tenant2Ctx)
		require.NoError(t, err, "Second entity creation should succeed")
		assert.Equal(t, tenantID2, entity2.TenantID, "Second entity should have tenant2 ID")

		// Verify both entities were created with correct tenant IDs
		entities, err := client.EntityWithTenantMixin.Query().All(systemContext(t, tenantID1))
		require.NoError(t, err)
		assert.Len(t, entities, 2, "Should have created 2 entities")

		tenantIDs := make([]uuid.UUID, len(entities))
		for i, entity := range entities {
			tenantIDs[i] = entity.TenantID
		}

		assert.ElementsMatch(t, []uuid.UUID{tenantID1, tenantID2}, tenantIDs, "Both tenant IDs should be present in created entities")
	})
}
