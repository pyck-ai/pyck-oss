package nats_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	natsgo "github.com/pyck-ai/pyck/backend/common/nats"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"
)

func TestRequireSeparateTenantAccount(t *testing.T) {
	t.Parallel()

	srv := natstest.Start(t, nil)
	service := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	t.Run("accepts another account", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, natsgo.RequireSeparateTenantAccount(t.Context(), service, natstest.TenantAccount))
	})

	t.Run("refuses the services' account", func(t *testing.T) {
		t.Parallel()

		err := natsgo.RequireSeparateTenantAccount(t.Context(), service, natstest.StreamAccount)
		require.ErrorIs(t, err, natsgo.ErrTenantAccountHoldsStreams)
	})
}
