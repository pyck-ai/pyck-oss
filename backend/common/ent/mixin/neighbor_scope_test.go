package mixin_test

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	entprivacy "entgo.io/ent/privacy"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
)

// neighbor builds the kind of sub-select a Has<Edge>With predicate produces
// for a tenant-scoped neighbour; like sqlgraph, it carries the query context.
func neighbor(ctx context.Context) *sql.Selector {
	s := sql.Dialect(dialect.Postgres).Select("id").From(sql.Table("users"))
	s.WithContext(ctx)
	return s
}

// neighborQuery applies the scope and returns the rendered SQL with its
// arguments.
func neighborQuery(s *sql.Selector) (string, []any) {
	mixin.ScopeNeighborToTenants(s)
	return s.Query()
}

func TestScopeNeighborToTenants(t *testing.T) {
	t.Parallel()

	userID := uuid.New()

	t.Run("tenant user is narrowed to the readable acting tenants", func(t *testing.T) {
		t.Parallel()

		query, args := neighborQuery(neighbor(requestContext(t, authn.ROLE_READER, userID, tenantID1, tenantID2)))

		assert.Contains(t, query, `"users"."tenant_id" IN ($1, $2)`)
		assert.Equal(t, []any{tenantID1, tenantID2}, args)
	})

	t.Run("system user is not narrowed", func(t *testing.T) {
		t.Parallel()

		query, args := neighborQuery(neighbor(systemContext(t, tenantID1)))

		assert.NotContains(t, query, "WHERE")
		assert.Empty(t, args)
	})

	t.Run("unauthenticated context matches nothing", func(t *testing.T) {
		t.Parallel()

		query, _ := neighborQuery(neighbor(t.Context()))

		require.Contains(t, query, "WHERE")
		assert.Contains(t, query, "FALSE")
		assert.NotContains(t, query, "tenant_id")
	})

	t.Run("acting tenant without reader role matches nothing", func(t *testing.T) {
		t.Parallel()

		query, args := neighborQuery(neighbor(requestContext(t, authn.ROLE_NONE, userID, tenantID1)))

		assert.Contains(t, query, "FALSE")
		assert.Empty(t, args)
	})

	t.Run("Allow decision without a user matches nothing", func(t *testing.T) {
		t.Parallel()

		ctx := entprivacy.DecisionContext(t.Context(), entprivacy.Allow)
		query, args := neighborQuery(neighbor(ctx))

		require.Contains(t, query, "WHERE")
		assert.Contains(t, query, "FALSE")
		assert.NotContains(t, query, "tenant_id")
		assert.Empty(t, args)
	})

	t.Run("Allow decision does not widen a tenant user", func(t *testing.T) {
		t.Parallel()

		ctx := entprivacy.DecisionContext(requestContext(t, authn.ROLE_READER, userID, tenantID1), entprivacy.Allow)
		query, args := neighborQuery(neighbor(ctx))

		assert.Contains(t, query, `"users"."tenant_id" IN ($1)`)
		assert.Equal(t, []any{tenantID1}, args)
	})

	t.Run("system user under Allow is not narrowed", func(t *testing.T) {
		t.Parallel()

		ctx := entprivacy.DecisionContext(systemContext(t, tenantID1), entprivacy.Allow)
		query, args := neighborQuery(neighbor(ctx))

		assert.NotContains(t, query, "WHERE")
		assert.Empty(t, args)
	})

	t.Run("Allow decision keeps every readable acting tenant", func(t *testing.T) {
		t.Parallel()

		ctx := entprivacy.DecisionContext(requestContext(t, authn.ROLE_READER, userID, tenantID1, tenantID2), entprivacy.Allow)
		query, args := neighborQuery(neighbor(ctx))

		assert.Contains(t, query, `"users"."tenant_id" IN ($1, $2)`)
		assert.Equal(t, []any{tenantID1, tenantID2}, args)
	})

	t.Run("Allow decision drops an acting tenant without reader role", func(t *testing.T) {
		t.Parallel()

		user := &authn.User{
			ID:       userID,
			TenantID: tenantID1,
			Roles:    map[uuid.UUID]authn.Role{tenantID1: authn.ROLE_READER, tenantID2: authn.ROLE_NONE},
		}
		ctx := entprivacy.DecisionContext(request.Context(t.Context(), user, tenantID1, tenantID2, tenantID3), entprivacy.Allow)
		query, args := neighborQuery(neighbor(ctx))

		assert.Contains(t, query, `"users"."tenant_id" IN ($1)`)
		assert.Equal(t, []any{tenantID1}, args)
	})

	t.Run("Deny decision does not lift the scope", func(t *testing.T) {
		t.Parallel()

		ctx := entprivacy.DecisionContext(t.Context(), entprivacy.Deny)
		query, _ := neighborQuery(neighbor(ctx))

		assert.Contains(t, query, "FALSE")
	})
}

func TestScopeNeighborToLive(t *testing.T) {
	t.Parallel()

	userID := uuid.New()

	t.Run("soft-deleted rows are dropped for a tenant user", func(t *testing.T) {
		t.Parallel()

		query, _ := neighborLiveQuery(neighbor(requestContext(t, authn.ROLE_READER, userID, tenantID1)))

		assert.Contains(t, query, `"users"."deleted_at" IS NULL OR "users"."deleted_at" = $1`)
	})

	t.Run("soft-deleted rows are dropped for the system user too", func(t *testing.T) {
		t.Parallel()

		query, _ := neighborLiveQuery(neighbor(systemContext(t, tenantID1)))

		assert.Contains(t, query, `"users"."deleted_at" IS NULL`)
	})

	t.Run("FEATURE_SHOW_DELETED lifts the filter", func(t *testing.T) {
		t.Parallel()

		ctx := feature.Context(requestContext(t, authn.ROLE_READER, userID, tenantID1), feature.FEATURE_SHOW_DELETED)
		query, args := neighborLiveQuery(neighbor(ctx))

		assert.NotContains(t, query, "deleted_at")
		assert.Empty(t, args)
	})

	t.Run("Allow decision lifts the filter", func(t *testing.T) {
		t.Parallel()

		ctx := entprivacy.DecisionContext(t.Context(), entprivacy.Allow)
		query, args := neighborLiveQuery(neighbor(ctx))

		assert.NotContains(t, query, "deleted_at")
		assert.Empty(t, args)
	})

	t.Run("Deny decision does not lift the filter", func(t *testing.T) {
		t.Parallel()

		ctx := entprivacy.DecisionContext(t.Context(), entprivacy.Deny)
		query, _ := neighborLiveQuery(neighbor(ctx))

		assert.Contains(t, query, `"users"."deleted_at" IS NULL`)
	})
}

// neighborLiveQuery applies the soft-delete scope and returns the rendered SQL.
func neighborLiveQuery(s *sql.Selector) (string, []any) {
	mixin.ScopeNeighborToLive(s)
	return s.Query()
}
