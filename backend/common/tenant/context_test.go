package tenant_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContext(t *testing.T) {
	t.Run("adds single tenant ID to context", func(t *testing.T) {
		ctx := context.Background()
		tenantID := uuid.New()

		newCtx := tenant.Context(ctx, tenantID)

		assert.NotNil(t, newCtx)
		assert.NotEqual(t, ctx, newCtx)

		// Verify the tenant ID was added
		tenantIDs := tenant.ForContext(newCtx)
		require.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID, tenantIDs[0])
	})

	t.Run("adds multiple tenant IDs to context", func(t *testing.T) {
		ctx := context.Background()
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		tenantID3 := uuid.New()

		newCtx := tenant.Context(ctx, tenantID1, tenantID2, tenantID3)

		assert.NotNil(t, newCtx)

		// Verify all tenant IDs were added
		tenantIDs := tenant.ForContext(newCtx)
		require.Len(t, tenantIDs, 3)
		assert.Equal(t, tenantID1, tenantIDs[0])
		assert.Equal(t, tenantID2, tenantIDs[1])
		assert.Equal(t, tenantID3, tenantIDs[2])
	})

	t.Run("adds empty tenant IDs list to context", func(t *testing.T) {
		ctx := context.Background()

		newCtx := tenant.Context(ctx)

		assert.NotNil(t, newCtx)

		// Verify empty tenant IDs list
		tenantIDs := tenant.ForContext(newCtx)
		assert.Empty(t, tenantIDs)
	})

	t.Run("overwrites existing tenant IDs in context", func(t *testing.T) {
		ctx := context.Background()
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()

		// Add initial tenant IDs
		ctxWithTenants := tenant.Context(ctx, tenantID1)

		// Overwrite with new tenant IDs
		newCtx := tenant.Context(ctxWithTenants, tenantID2)

		// Should only have the new tenant ID
		tenantIDs := tenant.ForContext(newCtx)
		require.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID2, tenantIDs[0])
	})

	t.Run("preserves existing context values", func(t *testing.T) {
		type contextKey struct{}
		ctx := context.Background()
		ctx = context.WithValue(ctx, contextKey{}, "preserved")
		tenantID := uuid.New()

		newCtx := tenant.Context(ctx, tenantID)

		// Original context value should be preserved
		assert.Equal(t, "preserved", newCtx.Value(contextKey{}))

		// Tenant ID should also be available
		tenantIDs := tenant.ForContext(newCtx)
		require.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID, tenantIDs[0])
	})

	t.Run("handles nil UUIDs", func(t *testing.T) {
		ctx := context.Background()
		nilUUID := uuid.Nil

		newCtx := tenant.Context(ctx, nilUUID)

		tenantIDs := tenant.ForContext(newCtx)
		require.Len(t, tenantIDs, 1)
		assert.Equal(t, uuid.Nil, tenantIDs[0])
	})

	t.Run("handles duplicate UUIDs", func(t *testing.T) {
		ctx := context.Background()
		tenantID := uuid.New()

		// Add the same UUID multiple times
		newCtx := tenant.Context(ctx, tenantID, tenantID, tenantID)

		tenantIDs := tenant.ForContext(newCtx)
		require.Len(t, tenantIDs, 3)
		// All should be the same
		assert.Equal(t, tenantID, tenantIDs[0])
		assert.Equal(t, tenantID, tenantIDs[1])
		assert.Equal(t, tenantID, tenantIDs[2])
	})
}

func TestForContext(t *testing.T) {
	t.Run("retrieves tenant IDs from context", func(t *testing.T) {
		ctx := context.Background()
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()

		ctxWithTenants := tenant.Context(ctx, tenantID1, tenantID2)

		tenantIDs := tenant.ForContext(ctxWithTenants)

		require.Len(t, tenantIDs, 2)
		assert.Equal(t, tenantID1, tenantIDs[0])
		assert.Equal(t, tenantID2, tenantIDs[1])
	})

	t.Run("returns empty slice when no tenant IDs in context", func(t *testing.T) {
		ctx := context.Background()

		tenantIDs := tenant.ForContext(ctx)

		assert.NotNil(t, tenantIDs)
		assert.Empty(t, tenantIDs)
	})

	t.Run("preserves UUID order", func(t *testing.T) {
		ctx := context.Background()
		tenantIDs := []uuid.UUID{
			uuid.New(),
			uuid.New(),
			uuid.New(),
			uuid.New(),
		}

		ctxWithTenants := tenant.Context(ctx, tenantIDs...)

		retrieved := tenant.ForContext(ctxWithTenants)

		require.Len(t, retrieved, len(tenantIDs))
		for i, id := range tenantIDs {
			assert.Equal(t, id, retrieved[i])
		}
	})

	t.Run("handles empty context correctly", func(t *testing.T) {
		// Create context with empty tenant IDs
		ctx := tenant.Context(context.Background())

		tenantIDs := tenant.ForContext(ctx)

		assert.Empty(t, tenantIDs)
	})

	t.Run("returns same reference", func(t *testing.T) {
		ctx := context.Background()
		originalIDs := []uuid.UUID{uuid.New(), uuid.New()}

		ctxWithTenants := tenant.Context(ctx, originalIDs...)

		retrieved1 := tenant.ForContext(ctxWithTenants)
		retrieved2 := tenant.ForContext(ctxWithTenants)

		// Both retrievals should have the same values
		assert.Equal(t, retrieved1, retrieved2)

		// ForContext returns the same slice reference, so modifying one affects the other
		// This is the actual behavior of the implementation
		if len(retrieved1) > 0 {
			originalValue := retrieved1[0]
			newValue := uuid.New()
			retrieved1[0] = newValue
			assert.Equal(t, newValue, retrieved2[0])
			assert.NotEqual(t, originalValue, retrieved2[0])
		}
	})
}

func TestContextIntegration(t *testing.T) {
	t.Run("multiple context operations", func(t *testing.T) {
		// Start with empty context
		ctx := context.Background()

		// Add some tenant IDs
		tenantID1 := uuid.New()
		tenantID2 := uuid.New()
		ctx = tenant.Context(ctx, tenantID1, tenantID2)

		// Verify they're there
		tenantIDs := tenant.ForContext(ctx)
		assert.Len(t, tenantIDs, 2)

		// Replace with new tenant IDs
		tenantID3 := uuid.New()
		ctx = tenant.Context(ctx, tenantID3)

		// Verify replacement
		tenantIDs = tenant.ForContext(ctx)
		assert.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID3, tenantIDs[0])
	})

	t.Run("context with mixed values", func(t *testing.T) {
		type userKey struct{}
		type requestKey struct{}

		ctx := context.Background()
		ctx = context.WithValue(ctx, userKey{}, "user123")
		ctx = context.WithValue(ctx, requestKey{}, "req456")

		// Add tenant IDs
		tenantID := uuid.New()
		ctx = tenant.Context(ctx, tenantID)

		// All values should be preserved
		assert.Equal(t, "user123", ctx.Value(userKey{}))
		assert.Equal(t, "req456", ctx.Value(requestKey{}))

		tenantIDs := tenant.ForContext(ctx)
		assert.Len(t, tenantIDs, 1)
		assert.Equal(t, tenantID, tenantIDs[0])
	})
}

func BenchmarkContext(b *testing.B) {
	ctx := context.Background()
	tenantID := uuid.New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tenant.Context(ctx, tenantID)
	}
}

func BenchmarkContextMultipleTenants(b *testing.B) {
	ctx := context.Background()
	tenantIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tenant.Context(ctx, tenantIDs...)
	}
}

func BenchmarkForContext(b *testing.B) {
	ctx := tenant.Context(context.Background(), uuid.New(), uuid.New(), uuid.New())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tenant.ForContext(ctx)
	}
}

func BenchmarkForContextEmpty(b *testing.B) {
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tenant.ForContext(ctx)
	}
}

func BenchmarkForContextManyTenants(b *testing.B) {
	tenantIDs := make([]uuid.UUID, 100)
	for i := range tenantIDs {
		tenantIDs[i] = uuid.New()
	}
	ctx := tenant.Context(context.Background(), tenantIDs...)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tenant.ForContext(ctx)
	}
}
