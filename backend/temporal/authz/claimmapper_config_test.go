package authz_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/server/common/authorization"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"

	"github.com/pyck-ai/pyck/backend/temporal/authz"
)

// A claim mapper wired without its liveness pair would deny or panic on the
// first authenticated request; the constructor returns the mistake so the
// server fails at boot with an error, not a panic.
func TestNewClaimMapper_RefusesMissingLivenessDependencies(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		cache  *liveness.Cache
		source liveness.TenantSource
	}{
		"nil cache":  {nil, livenessmocks.NoDisabledTenants},
		"nil source": {livenessmocks.NewTestCache(t), nil},
		"both nil":   {nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var mapper *authz.ClaimMapper
			var err error
			require.NotPanics(t, func() {
				mapper, err = authz.NewClaimMapper(t.Context(), &mocks.MockAuthProvider{}, tc.cache, tc.source, 2*time.Second)
			})
			require.ErrorIs(t, err, liveness.ErrMissingDeps)
			assert.Nil(t, mapper)
		})
	}
}

// The liveness lookup runs inside the claims budget after introspection
// has spent part of it. A budget at or below the configured lookup timeout
// cuts every cold lookup short and denies the namespace, so it is refused
// at construction for any configuration, not only the shipped defaults.
func TestNewClaimMapper_RefusesAClaimsTimeoutNotAboveTheLookup(t *testing.T) {
	t.Parallel()

	const lookup = 3 * time.Second
	for name, claims := range map[string]time.Duration{
		"below":    2 * time.Second,
		"equal":    lookup,
		"zero":     0,
		"negative": -time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cache := liveness.NewCache(time.Minute, liveness.WithLookupTimeout(lookup))
			t.Cleanup(cache.Close)

			mapper, err := authz.NewClaimMapper(t.Context(), &mocks.MockAuthProvider{}, cache, livenessmocks.NoDisabledTenants, claims)

			require.ErrorIs(t, err, authz.ErrClaimsTimeoutBelowLookup)
			assert.Nil(t, mapper)
		})
	}
}

// A configured claims timeout above the lookup is accepted and is the
// deadline GetClaims actually runs under: a stalled introspection ends on
// it, not on a built-in constant.
func TestNewClaimMapper_AppliesTheConfiguredClaimsTimeout(t *testing.T) {
	t.Parallel()

	const lookup = 50 * time.Millisecond
	const claims = 300 * time.Millisecond
	cache := liveness.NewCache(time.Minute, liveness.WithLookupTimeout(lookup))
	t.Cleanup(cache.Close)

	var deadlineIn time.Duration
	mockAuth := &mocks.MockAuthProvider{}
	mockAuth.On("Authenticate", mock.Anything, "user-token").
		Run(func(args mock.Arguments) {
			callCtx, ok := args.Get(0).(context.Context)
			require.True(t, ok)
			deadline, has := callCtx.Deadline()
			require.True(t, has)
			deadlineIn = time.Until(deadline)
			<-callCtx.Done()
		}).
		Return(authn.User{}, context.DeadlineExceeded)

	mapper, err := authz.NewClaimMapper(t.Context(), mockAuth, cache, livenessmocks.NoDisabledTenants, claims)
	require.NoError(t, err)

	start := time.Now()
	_, err = mapper.GetClaims(&authorization.AuthInfo{AuthToken: "Bearer user-token"})
	waited := time.Since(start)

	require.Error(t, err)
	assert.LessOrEqual(t, deadlineIn, claims, "the mapping runs under the configured budget")
	assert.Greater(t, deadlineIn, claims/2)
	assert.GreaterOrEqual(t, waited, claims)
	assert.Less(t, waited, time.Second, "the configured budget, not the old 2s constant, ends the call")
}
