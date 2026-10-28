package liveness_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/liveness"
)

// Every service builds its cache from configuration through one
// constructor; each of the three configured values must reach the cache,
// or a deployment that tuned it silently runs on the default. Checked
// through behaviour: the TTL by an entry expiring, the lookup timeout by
// the getter the NATS callout and the claim mapper check their budgets
// against, the source timeout by the detached call ending.
func TestNewCacheFromConfig_CarriesEveryConfiguredValue(t *testing.T) {
	t.Parallel()

	cfg := envconfig.LivenessConfig{
		LivenessCacheTTL:      80 * time.Millisecond,
		LivenessLookupTimeout: 30 * time.Millisecond,
		LivenessSourceTimeout: 150 * time.Millisecond,
	}
	c := liveness.NewCacheFromConfig(cfg)
	t.Cleanup(c.Close)

	assert.Equal(t, cfg.LivenessLookupTimeout, c.LookupTimeout())

	// TTL: a verdict cached now is gone once the configured TTL passed.
	live := uuid.New()
	_, err := c.LookupTenants(t.Context(), []uuid.UUID{live}, func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
		return nil, nil
	})
	require.NoError(t, err)
	_, cached := c.Get(liveness.TenantKey(live))
	require.True(t, cached, "the verdict is cached right after the lookup")
	require.Eventually(t, func() bool {
		_, ok := c.Get(liveness.TenantKey(live))
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "the entry must expire on the configured TTL, not the default hour")

	// Source timeout: only it can end this detached call.
	done := make(chan error, 1)
	_, err = c.LookupTenants(t.Context(), []uuid.UUID{uuid.New()}, func(callCtx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
		<-callCtx.Done()
		done <- callCtx.Err()
		return nil, callCtx.Err()
	})
	require.Error(t, err, "the caller gives up on the configured lookup timeout")
	select {
	case callErr := <-done:
		require.ErrorIs(t, callErr, context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("the detached call was not bounded by the configured source timeout")
	}
}

// An unset (zero) configuration keeps every default instead of removing
// the bounds.
func TestNewCacheFromConfig_ZeroConfigKeepsTheDefaults(t *testing.T) {
	t.Parallel()

	c := liveness.NewCacheFromConfig(envconfig.LivenessConfig{})
	t.Cleanup(c.Close)

	assert.Equal(t, liveness.DefaultLookupTimeout, c.LookupTimeout())
}
