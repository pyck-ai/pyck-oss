package liveness_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/liveness"
)

// The two lookup budgets moved into configuration so a deployment can tune
// them; what must not move with them is the guarantee they encode. These
// tests exercise the options through the behaviour they change (how long a
// caller waits on a stalled source) rather than through a getter.

// stalledSource accepts the call and never answers until the test releases
// it, which is the failure mode the lookup timeout exists for: a source
// that is reachable but slow, not one that is down.
func stalledSource(release <-chan struct{}) liveness.TenantSource {
	return func(callCtx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
		select {
		case <-release:
			return nil, nil
		case <-callCtx.Done():
			return nil, callCtx.Err()
		}
	}
}

// cacheWith builds a cache with the given options and closes it with the test.
func cacheWith(t *testing.T, opts ...liveness.Option) *liveness.Cache {
	t.Helper()
	c := liveness.NewCache(time.Hour, opts...)
	t.Cleanup(c.Close)
	return c
}

// A configured lookup timeout is the one the caller actually gets: an
// option that never reached LookupTenants would leave every deployment on
// the one-second default whatever it set.
func TestNewCache_WithLookupTimeoutBoundsTheCaller(t *testing.T) {
	t.Parallel()

	const configured = 120 * time.Millisecond
	c := cacheWith(t, liveness.WithLookupTimeout(configured))

	release := make(chan struct{})
	defer close(release)

	tenant := uuid.New()
	start := time.Now()
	live, err := c.LookupTenants(t.Context(), []uuid.UUID{tenant}, stalledSource(release))
	waited := time.Since(start)

	require.Error(t, err, "a caller that stopped waiting must report the fault")
	assert.False(t, live[tenant], "a tenant that could not be verified is not served")
	assert.GreaterOrEqual(t, waited, configured, "the configured wait must be honoured in full")
	assert.Less(t, waited, liveness.DefaultLookupTimeout,
		"a shorter configured timeout must actually shorten the wait, not be ignored")
}

// A non-positive configured value must leave the bound in place. An unset
// or mis-typed environment variable would otherwise turn "one second" into
// "no deadline", which is exactly the wedge the bound prevents: every
// in-flight request would hold a goroutine for the source's whole budget.
func TestNewCache_NonPositiveTimeoutsKeepTheDefaults(t *testing.T) {
	t.Parallel()

	for name, d := range map[string]time.Duration{"zero": 0, "negative": -time.Second} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := cacheWith(t, liveness.WithLookupTimeout(d), liveness.WithSourceTimeout(d))

			release := make(chan struct{})
			defer close(release)

			start := time.Now()
			_, err := c.LookupTenants(t.Context(), []uuid.UUID{uuid.New()}, stalledSource(release))
			waited := time.Since(start)

			require.Error(t, err)
			assert.GreaterOrEqual(t, waited, liveness.DefaultLookupTimeout,
				"a non-positive override must keep the default bound, not remove it")
			assert.Less(t, waited, 5*time.Second, "and must not fall back to the source budget either")
		})
	}
}

// The source timeout bounds the detached call, which is what keeps a
// stalled source from holding a goroutine forever after every caller has
// given up. It is configurable for the same reason as the lookup timeout,
// and a configured value has to reach the detached call.
func TestNewCache_WithSourceTimeoutBoundsTheDetachedCall(t *testing.T) {
	t.Parallel()

	const configured = 150 * time.Millisecond
	c := cacheWith(t,
		liveness.WithLookupTimeout(30*time.Millisecond),
		liveness.WithSourceTimeout(configured))

	done := make(chan error, 1)
	source := func(callCtx context.Context, _ []uuid.UUID) ([]uuid.UUID, error) {
		<-callCtx.Done() // only the source timeout can end this call
		done <- callCtx.Err()
		return nil, callCtx.Err()
	}

	_, err := c.LookupTenants(t.Context(), []uuid.UUID{uuid.New()}, source)
	require.Error(t, err, "the caller gives up on its own, shorter budget")

	select {
	case callErr := <-done:
		require.ErrorIs(t, callErr, context.DeadlineExceeded,
			"the detached call must end on the configured source timeout, not run forever")
	case <-time.After(5 * time.Second):
		t.Fatal("the detached source call was not bounded by the configured source timeout")
	}
}

// Options are additive and independent: configuring one must not reset the
// other to its default.
func TestNewCache_OptionsDoNotOverwriteEachOther(t *testing.T) {
	t.Parallel()

	const lookup = 80 * time.Millisecond
	c := cacheWith(t, liveness.WithSourceTimeout(2*time.Second), liveness.WithLookupTimeout(lookup))

	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	_, err := c.LookupTenants(t.Context(), []uuid.UUID{uuid.New()}, stalledSource(release))
	waited := time.Since(start)

	require.Error(t, err)
	assert.GreaterOrEqual(t, waited, lookup)
	assert.Less(t, waited, liveness.DefaultLookupTimeout,
		"the lookup timeout must survive a source-timeout option next to it")
}

// A cache built without options behaves exactly as before: the defaults are
// the contract for every caller that has nothing to configure.
func TestNewCache_WithoutOptionsKeepsTheDefaultLookupBound(t *testing.T) {
	t.Parallel()

	c := cacheWith(t)

	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	_, err := c.LookupTenants(t.Context(), []uuid.UUID{uuid.New()}, stalledSource(release))
	waited := time.Since(start)

	require.Error(t, err)
	assert.GreaterOrEqual(t, waited, liveness.DefaultLookupTimeout)
	assert.Less(t, waited, 5*time.Second)
}
