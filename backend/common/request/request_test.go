package request_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/requestid"
)

func TestRequestContextReadsRequestIDFromBaggage(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050505"

	ctx, err := requestid.WithRequestID(context.Background(), id)
	require.NoError(t, err)

	rc := request.ForContext(ctx)
	assert.Equal(t, id, rc.RequestID())
}

func TestRequestContextRequestIDEmptyWhenAbsent(t *testing.T) {
	t.Parallel()

	rc := request.ForContext(context.Background())
	assert.Empty(t, rc.RequestID())
}

func TestRequestContextCarriesUserAndTenants(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050508"
	tenantID := uuid.New()

	ctx := request.Context(context.Background(), authn.SystemUser(), tenantID)
	ctx, err := requestid.WithRequestID(ctx, id)
	require.NoError(t, err)

	rc := request.ForContext(ctx)

	assert.Equal(t, id, rc.RequestID())
	assert.Equal(t, []uuid.UUID{tenantID}, rc.TenantIDs())
	assert.True(t, rc.HasMutationTenantID())
	mutationTenantID, err := rc.MutationTenantID()
	require.NoError(t, err)
	assert.Equal(t, tenantID, mutationTenantID)
}

func TestMutationTenantIDRequiresSingleTenant(t *testing.T) {
	t.Parallel()

	for name, tenantIDs := range map[string][]uuid.UUID{
		"zero tenants":     {},
		"multiple tenants": {uuid.New(), uuid.New()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := request.Context(t.Context(), authn.SystemUser(), tenantIDs...)
			rc := request.ForContext(ctx)

			assert.False(t, rc.HasMutationTenantID())
			mutationTenantID, err := rc.MutationTenantID()
			require.ErrorIs(t, err, request.ErrSingleTenantRequired)
			assert.Equal(t, uuid.Nil, mutationTenantID)
		})
	}
}

func TestRequireRole(t *testing.T) {
	t.Parallel()

	tenantA, tenantB := uuid.New(), uuid.New()
	writer := &authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_WRITER, tenantB: authn.ROLE_WRITER},
	}

	for name, tc := range map[string]struct {
		user      *authn.User
		tenantIDs []uuid.UUID
		wantErr   error
	}{
		"unauthenticated":  {&authn.User{}, []uuid.UUID{tenantA}, authn.ErrUnauthorized},
		"zero tenants":     {writer, nil, request.ErrSingleTenantRequired},
		"multiple tenants": {writer, []uuid.UUID{tenantA, tenantB}, request.ErrSingleTenantRequired},
		"missing role":     {writer, []uuid.UUID{uuid.New()}, authn.ErrUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rc := request.ForContext(request.Context(t.Context(), tc.user, tc.tenantIDs...))
			tenantID, err := rc.RequireRole(authn.ROLE_WRITER, "op")
			require.ErrorIs(t, err, tc.wantErr)
			assert.Contains(t, err.Error(), "op")
			assert.Equal(t, uuid.Nil, tenantID)
		})
	}

	t.Run("role held", func(t *testing.T) {
		t.Parallel()

		rc := request.ForContext(request.Context(t.Context(), writer, tenantA))
		tenantID, err := rc.RequireRole(authn.ROLE_WRITER, "op")
		require.NoError(t, err)
		assert.Equal(t, tenantA, tenantID)
	})
}
