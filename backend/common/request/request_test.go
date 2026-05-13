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
	assert.Equal(t, tenantID, rc.MutationTenantID())
}
