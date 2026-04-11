package mixin_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen/entitywithlimitmixin"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen/enttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create a test client with unique database name
func createDBClientForEntityWithLimit(t *testing.T) *gen.Client {
	t.Helper()

	opts := []enttest.Option{
		enttest.WithOptions(gen.Log(t.Log), gen.Debug()),
	}
	return enttest.Open(t, dialect.SQLite, fmt.Sprintf("file:ent-%s-%d?mode=memory&_fk=1", t.Name(), time.Now().UnixNano()), opts...)
}

// Helper function to create multiple entities for testing
func createMultipleEntitiesWithLimit(t *testing.T, client *gen.Client, count int) {
	t.Helper()

	ctx := context.Background()
	for i := 0; i < count; i++ {
		_, err := client.EntityWithLimitMixin.Create().
			SetStringField(fmt.Sprintf("entity_%d", i)).
			Save(ctx)
		require.NoError(t, err)
	}
}

func TestLimitMixinDefaultLimit(t *testing.T) {
	t.Parallel()

	t.Run("applies default limit when no limit is specified", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		// Query without specifying limit - should return only default limit
		ctx := context.Background()
		entities, err := client.EntityWithLimitMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, mixin.Limit, "Should return default limit number of entities")
	})

	t.Run("respects explicit limit when less than default", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than we'll query for
		totalEntities := 100
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		// Query with explicit limit less than default
		explicitLimit := 10
		ctx := context.Background()
		entities, err := client.EntityWithLimitMixin.Query().Limit(explicitLimit).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, explicitLimit, "Should return explicit limit number of entities")
	})

	t.Run("respects explicit limit when equal to default", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		// Query with explicit limit equal to default
		ctx := context.Background()
		entities, err := client.EntityWithLimitMixin.Query().Limit(mixin.Limit).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, mixin.Limit, "Should return default limit number of entities")
	})

	t.Run("allows limit up to maximum", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the maximum limit
		totalEntities := mixin.Limit + 10
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		// Query with limit equal to maximum allowed
		maxAllowedLimit := mixin.Limit
		ctx := context.Background()
		entities, err := client.EntityWithLimitMixin.Query().Limit(maxAllowedLimit).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, maxAllowedLimit, "Should return maximum allowed limit number of entities")
	})

	t.Run("rejects limit exceeding maximum", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create some entities
		createMultipleEntitiesWithLimit(t, client, 10)

		// Query with limit exceeding maximum
		exceedingLimit := mixin.Limit + 1
		ctx := context.Background()
		_, err := client.EntityWithLimitMixin.Query().Limit(exceedingLimit).All(ctx)
		require.Error(t, err, "Should reject limit exceeding maximum")
		assert.Contains(t, err.Error(), fmt.Sprintf("the maximum accepted limit is %d", mixin.Limit), "Error should mention maximum limit")
	})

	t.Run("works with different query methods", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		ctx := context.Background()

		// Test with All()
		entities, err := client.EntityWithLimitMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, mixin.Limit, "All() should respect default limit")

		// Test with IDs()
		ids, err := client.EntityWithLimitMixin.Query().IDs(ctx)
		require.NoError(t, err)
		assert.Len(t, ids, mixin.Limit, "IDs() should respect default limit")
	})

	t.Run("works with offset", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		ctx := context.Background()

		// Test with offset
		offset := 10
		entities, err := client.EntityWithLimitMixin.Query().Offset(offset).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, mixin.Limit, "Should still apply default limit with offset")

		// Test with both offset and explicit limit
		explicitLimit := 5
		entities2, err := client.EntityWithLimitMixin.Query().Offset(offset).Limit(explicitLimit).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities2, explicitLimit, "Should respect explicit limit with offset")
	})

	t.Run("works with ordering", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		ctx := context.Background()

		// Test with ordering
		entities, err := client.EntityWithLimitMixin.Query().Order(gen.Asc("id")).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, mixin.Limit, "Should apply default limit with ordering")

		// Verify ordering is preserved
		for i := 1; i < len(entities); i++ {
			assert.Greater(t, entities[i].ID, entities[i-1].ID, "Entities should be ordered by ID")
		}
	})

	t.Run("works with where conditions", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create entities with different string fields
		ctx := context.Background()
		for i := 0; i < mixin.Limit+10; i++ {
			stringField := "test"
			if i%2 == 0 {
				stringField = "other"
			}
			_, err := client.EntityWithLimitMixin.Create().
				SetStringField(stringField).
				Save(ctx)
			require.NoError(t, err)
		}

		// Query with where condition
		entities, err := client.EntityWithLimitMixin.Query().
			Where(entitywithlimitmixin.StringFieldEQ("test")).
			All(ctx)
		require.NoError(t, err)

		// Should still apply limit, but only to matching entities
		for _, entity := range entities {
			assert.Equal(t, "test", entity.StringField, "All returned entities should match filter")
		}
		// The actual count depends on how many "test" entities exist, but should not exceed limit
		assert.LessOrEqual(t, len(entities), mixin.Limit, "Should not exceed default limit")
	})

	t.Run("count is not affected by limit", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		ctx := context.Background()

		// Count should return total count, not limited count
		count, err := client.EntityWithLimitMixin.Query().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, totalEntities, count, "Count should return total number of entities, not limited")

		// Count with explicit limit should still return total count
		count2, err := client.EntityWithLimitMixin.Query().Limit(10).Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, totalEntities, count2, "Count with explicit limit should still return total count")
	})

	t.Run("exist is not affected by limit", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		ctx := context.Background()

		// Exist should work regardless of limit
		exists, err := client.EntityWithLimitMixin.Query().Exist(ctx)
		require.NoError(t, err)
		assert.True(t, exists, "Exist should return true when entities exist")

		// Exist with explicit limit should still work
		exists2, err := client.EntityWithLimitMixin.Query().Limit(10).Exist(ctx)
		require.NoError(t, err)
		assert.True(t, exists2, "Exist with explicit limit should still work")
	})

	t.Run("first and only methods work with limit", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		ctx := context.Background()

		// First should work
		entity, err := client.EntityWithLimitMixin.Query().First(ctx)
		require.NoError(t, err)
		assert.NotNil(t, entity, "First should return an entity")

		// FirstID should work
		id, err := client.EntityWithLimitMixin.Query().FirstID(ctx)
		require.NoError(t, err)
		assert.NotZero(t, id, "FirstID should return a valid ID")

		// Only should fail with multiple entities (even with limit)
		_, err = client.EntityWithLimitMixin.Query().Only(ctx)
		require.Error(t, err, "Only should fail when multiple entities exist")
	})

	t.Run("limit works with transactions", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		ctx := context.Background()

		// Start a transaction
		tx, err := client.Tx(ctx)
		require.NoError(t, err)

		// Create entities within transaction
		totalEntities := mixin.Limit + 10
		for i := 0; i < totalEntities; i++ {
			_, err := tx.EntityWithLimitMixin.Create().
				SetStringField(fmt.Sprintf("entity_%d", i)).
				Save(ctx)
			require.NoError(t, err)
		}

		// Query within transaction should respect limit
		entities, err := tx.EntityWithLimitMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, mixin.Limit, "Should apply default limit within transaction")

		// Commit transaction
		err = tx.Commit()
		require.NoError(t, err)

		// Query after commit should still respect limit
		entities2, err := client.EntityWithLimitMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities2, mixin.Limit, "Should apply default limit after transaction commit")
	})

	t.Run("limit boundary conditions", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create exactly the default limit number of entities
		createMultipleEntitiesWithLimit(t, client, mixin.Limit)

		ctx := context.Background()

		// Query should return all entities
		entities, err := client.EntityWithLimitMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities, mixin.Limit, "Should return all entities when count equals default limit")

		// Test with limit of 0 - this should still apply default limit since LimitMixin ensures minimum functionality
		entities2, err := client.EntityWithLimitMixin.Query().Limit(0).All(ctx)
		require.NoError(t, err)
		// The LimitMixin applies default limit when no limit is set, but 0 is a valid explicit limit
		// Based on the SQL output, it seems 0 limit still gets the default limit applied
		assert.Len(t, entities2, mixin.Limit, "Should apply default limit even with explicit 0 limit")

		// Test with limit of 1
		entities3, err := client.EntityWithLimitMixin.Query().Limit(1).All(ctx)
		require.NoError(t, err)
		assert.Len(t, entities3, 1, "Should return 1 entity with limit 1")
	})

	t.Run("limit works with empty result set", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		ctx := context.Background()

		// Query without any entities
		entities, err := client.EntityWithLimitMixin.Query().All(ctx)
		require.NoError(t, err)
		assert.Empty(t, entities, "Should return empty result set when no entities exist")

		// Query with explicit limit on empty result set
		entities2, err := client.EntityWithLimitMixin.Query().Limit(10).All(ctx)
		require.NoError(t, err)
		assert.Empty(t, entities2, "Should return empty result set with explicit limit when no entities exist")
	})

	t.Run("limit configuration is consistent", func(t *testing.T) {
		t.Parallel()

		// Verify the default limit constant is reasonable
		assert.Equal(t, 200, mixin.Limit, "Default limit should be 200")
		assert.Positive(t, mixin.Limit, "Default limit should be positive")
		assert.Less(t, mixin.Limit, 10000, "Default limit should be reasonable (less than 10000)")
	})

	t.Run("limit works with group by", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		ctx := context.Background()

		// Create entities with different string fields for grouping
		for i := 0; i < mixin.Limit+10; i++ {
			stringField := fmt.Sprintf("group_%d", i%5) // Create 5 groups
			_, err := client.EntityWithLimitMixin.Create().
				SetStringField(stringField).
				Save(ctx)
			require.NoError(t, err)
		}

		// Group by string field
		var results []struct {
			StringField string `json:"string_field"`
			Count       int    `json:"count"`
		}

		err := client.EntityWithLimitMixin.Query().
			GroupBy("string_field").
			Aggregate(gen.Count()).
			Scan(ctx, &results)
		require.NoError(t, err)

		// Should return grouped results
		assert.Len(t, results, 5, "Should return 5 groups")

		// Verify total count across groups
		totalCount := 0
		for _, result := range results {
			totalCount += result.Count
		}
		assert.Equal(t, mixin.Limit+10, totalCount, "Total count across groups should match created entities")
	})

	t.Run("limit does not affect mutations", func(t *testing.T) {
		t.Parallel()

		client := createDBClientForEntityWithLimit(t)
		defer client.Close()

		// Create more entities than the default limit
		totalEntities := mixin.Limit + 50
		createMultipleEntitiesWithLimit(t, client, totalEntities)

		ctx := context.Background()

		// Bulk update should affect all entities, not just limited ones
		affected, err := client.EntityWithLimitMixin.Update().
			SetStringField("updated").
			Save(ctx)
		require.NoError(t, err)
		assert.Equal(t, totalEntities, affected, "Bulk update should affect all entities")

		// Verify all entities were updated
		count, err := client.EntityWithLimitMixin.Query().
			Where(entitywithlimitmixin.StringFieldEQ("updated")).
			Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, totalEntities, count, "All entities should be updated")

		// Bulk delete should affect all entities, not just limited ones
		affected2, err := client.EntityWithLimitMixin.Delete().Exec(ctx)
		require.NoError(t, err)
		assert.Equal(t, totalEntities, affected2, "Bulk delete should affect all entities")

		// Verify all entities were deleted
		finalCount, err := client.EntityWithLimitMixin.Query().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, finalCount, "All entities should be deleted")
	})
}
