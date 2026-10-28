package resolvers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A cancelled request context belongs to the HTTP request: SendQuery hands
// its error back to the test instead of failing it while warming the
// liveness cache, which also keeps SendQuery safe off the test goroutine
// (execAsync).
func TestSendQueryReturnsTheErrorOfACancelledContext(t *testing.T) {
	t.Parallel()
	te := setup(t)
	defer te.Close(t)

	ctx, cancel := context.WithCancel(te.ctx(userA))
	cancel()

	closeResp, _, err := te.SendQuery(t, ctx, executionsWithoutRemoteUIQuery, nil)
	defer closeResp()
	require.ErrorIs(t, err, context.Canceled)
}
