package nats

import (
	"os"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
)

// natsDefaults parses NatsConfig with only the required fields supplied, so
// every duration below is the envDefault a deployment that sets nothing
// gets: the value the hierarchy assertions must hold for.
func natsDefaults(t *testing.T) envconfig.NatsConfig {
	t.Helper()
	t.Setenv("PYCK_NATS_URL", "nats://localhost:4222")
	t.Setenv("PYCK_NATS_STREAM_NAME", "pyck")
	t.Setenv("PYCK_NATS_WS_URL", "ws://localhost:8080")
	t.Setenv("PYCK_NATS_REPLICAS_NO", "1")

	cfg, err := env.ParseAs[envconfig.NatsConfig]()
	require.NoError(t, err)
	return cfg
}

// The library default and the environment default are two spellings of the
// same number, in two packages, with nothing tying them together: a change
// to one is silent until a deployment that sets no environment behaves
// differently from a unit test that reads the constant.
//
//nolint:paralleltest // t.Setenv (in natsDefaults) is incompatible with t.Parallel.
func TestCalloutDefaultsMatchTheEnvironmentDefaults(t *testing.T) {
	cfg := natsDefaults(t)

	assert.Equal(t, defaultCalloutTimeout, cfg.NatsCalloutTimeout,
		"PYCK_NATS_CALLOUT_TIMEOUT's default must equal the package default")
	assert.Equal(t, defaultUserJWTTTL, cfg.NatsUserJWTTTL,
		"PYCK_NATS_USER_JWT_TTL's default must equal the package default")
}

// The hierarchy server > callout > lookup, for the two defaults and the
// server's own configured timeout. It pins the shipped values only: a
// deployment that overrides either environment variable is caught at
// startup instead, by the NewAuthService refusal above.
//
//nolint:paralleltest // t.Setenv (in natsDefaults) is incompatible with t.Parallel.
func TestConfiguredCalloutBudgetKeepsTheHierarchy(t *testing.T) {
	raw, err := os.ReadFile(natsServerConfPath)
	if err != nil {
		t.Skipf("cannot read %s: %v", natsServerConfPath, err)
	}
	serverTimeout := authorizationTimeout(t, string(raw))

	natsCfg := natsDefaults(t)
	for _, key := range []string{
		"PYCK_ZITADEL_OAUTH_URL", "PYCK_ZITADEL_GRPC_ADDR", "PYCK_ZITADEL_AUDIENCE",
		"PYCK_ZITADEL_ORG_ID", "PYCK_ZITADEL_PROJECT_ID", "PYCK_ZITADEL_APP_KEYFILE",
	} {
		t.Setenv(key, "set")
	}
	zitadelCfg, err := env.ParseAs[envconfig.ZitadelConfig]()
	require.NoError(t, err)

	assert.Greater(t, serverTimeout, natsCfg.NatsCalloutTimeout,
		"the configured callout budget (%s) must fit inside the server's authorization timeout (%s)",
		natsCfg.NatsCalloutTimeout, serverTimeout)
	assert.Greater(t, natsCfg.NatsCalloutTimeout, zitadelCfg.LivenessLookupTimeout,
		"the configured callout budget (%s) must leave room for the liveness lookup (%s)",
		natsCfg.NatsCalloutTimeout, zitadelCfg.LivenessLookupTimeout)
	assert.Equal(t, liveness.DefaultLookupTimeout, zitadelCfg.LivenessLookupTimeout,
		"PYCK_LIVENESS_LOOKUP_TIMEOUT's default must equal the liveness package default")
}

// The options are how a deployment's configured budgets reach the service.
func TestAuthServiceOptions_OverrideTheDefaults(t *testing.T) {
	t.Parallel()

	service := newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil,
		livenessmocks.NewTestCache(t), nil,
		WithCalloutTimeout(750*time.Millisecond),
		WithUserJWTTTL(9*time.Minute))

	assert.Equal(t, 750*time.Millisecond, service.calloutTimeout)
	assert.Equal(t, 9*time.Minute, service.userJWTTTL)
}

// A configured zero must not remove the bound: an unset or mis-typed
// environment value would otherwise turn "two seconds" into "no deadline
// at all", which is the failure the budget exists to prevent.
func TestAuthServiceOptions_NonPositiveValueKeepsTheDefault(t *testing.T) {
	t.Parallel()

	for name, d := range map[string]time.Duration{"zero": 0, "negative": -time.Second} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service := newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil,
				livenessmocks.NewTestCache(t), nil,
				WithCalloutTimeout(d), WithUserJWTTTL(d))

			assert.Equal(t, defaultCalloutTimeout, service.calloutTimeout)
			assert.Equal(t, defaultUserJWTTTL, service.userJWTTTL)
		})
	}
}

// A service constructed without options keeps the package defaults, so a
// caller that has no configuration to pass is bounded exactly as before.
func TestAuthServiceOptions_DefaultWhenUnset(t *testing.T) {
	t.Parallel()

	service := newTestAuthService(t, nil)

	assert.Equal(t, defaultCalloutTimeout, service.calloutTimeout)
	assert.Equal(t, defaultUserJWTTTL, service.userJWTTTL)
}

// The configured TTL is what a minted tenant credential actually expires
// on: an override that never reached mintUserClaims would leave every
// deployment on the five-minute default whatever it configured.
func TestMintUserClaims_UsesTheConfiguredTTL(t *testing.T) {
	t.Parallel()

	const ttl = 30 * time.Minute
	service := newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil,
		livenessmocks.NewTestCache(t), nil, WithUserJWTTTL(ttl))

	before := time.Now()
	claims := service.mintUserClaims("UAUSERNKEY", uuid.New(), false)

	require.NotZero(t, claims.Expires)
	assert.GreaterOrEqual(t, claims.Expires, before.Add(ttl-userJWTTTLJitter/2).Unix())
	assert.LessOrEqual(t, claims.Expires, time.Now().Add(ttl+userJWTTTLJitter/2).Unix())
}

// accountSeed is an issuer seed for a service under construction; its value
// does not reach the assertions below.
func accountSeed(t *testing.T) string {
	t.Helper()

	kp, err := nkeys.CreateAccount()
	require.NoError(t, err)
	seed, err := kp.Seed()
	require.NoError(t, err)
	return string(seed)
}

// The lower half of the hierarchy is two independently set environment
// variables, so the only place that can hold it is the constructor that
// sees both: a lookup that cannot finish inside the callout refuses every
// uncached tenant at connect time, which is a deployment-wide connect
// failure and not something to serve.
//
// The boundary is strict, and the equality case is the one that reads as
// acceptable and is not: a lookup allowed to spend the whole callout
// budget leaves nothing to sign the answer with, so the callout times out
// rather than returning a refusal. The two cases on either side of the
// boundary pin it in both directions.
func TestNewAuthService_RefusesACalloutBudgetBelowTheLookup(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		callout, lookup time.Duration
		wantRefused     bool
	}{
		"a callout shorter than the lookup is refused": {
			callout: 2 * time.Second, lookup: 5 * time.Second, wantRefused: true,
		},
		"equal budgets are refused": {
			callout: 5 * time.Second, lookup: 5 * time.Second, wantRefused: true,
		},
		"one nanosecond of headroom is accepted": {
			callout: 5*time.Second + time.Nanosecond, lookup: 5 * time.Second, wantRefused: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cache := liveness.NewCache(time.Hour, liveness.WithLookupTimeout(tc.lookup))
			t.Cleanup(cache.Close)

			_, err := NewAuthService(t.Context(), "pyck-nats", "pyck-stream", "pyck", nil,
				accountSeed(t), cache, livenessmocks.NoDisabledTenants,
				WithCalloutTimeout(tc.callout))

			if tc.wantRefused {
				require.ErrorIs(t, err, errCalloutBudgetBelowLookup)
				assert.Contains(t, err.Error(), tc.callout.String(),
					"the refusal must name the budgets an operator has to change")
				return
			}
			require.NoError(t, err, "a callout budget that strictly exceeds the lookup must be served")
		})
	}
}

// A callout wired without its liveness cache or tenant source would grant
// tenant-scoped permissions with the liveness axis absent; the constructor
// refuses it at boot.
func TestNewAuthService_MissingLivenessDepsIsAnError(t *testing.T) {
	t.Parallel()

	_, err := NewAuthService(t.Context(), "pyck-nats", "pyck-stream", "pyck", nil,
		accountSeed(t), nil, livenessmocks.NoDisabledTenants)
	require.ErrorIs(t, err, liveness.ErrMissingDeps)

	_, err = NewAuthService(t.Context(), "pyck-nats", "pyck-stream", "pyck", nil,
		accountSeed(t), livenessmocks.NewTestCache(t), nil)
	require.ErrorIs(t, err, liveness.ErrMissingDeps)
}

// The same wiring with the shipped defaults constructs, so the guard bounds
// a misconfiguration and not the deployment.
func TestNewAuthService_AcceptsTheDefaultBudgets(t *testing.T) {
	t.Parallel()

	cache := livenessmocks.NewTestCache(t)

	_, err := NewAuthService(t.Context(), "pyck-nats", "pyck-stream", "pyck", nil,
		accountSeed(t), cache, livenessmocks.NoDisabledTenants)

	require.NoError(t, err)
}

// mintUserClaims must never hand out a credential that is already expired
// when it is signed. The jitter is drawn per credential, so the assertion
// is over many draws at a TTL just above the jitter, the worst case for the
// negative half of the draw.
func TestMintUserClaims_NeverMintsAnExpiredCredential(t *testing.T) {
	t.Parallel()

	ttl := userJWTTTLJitter + time.Second
	_, err := NewAuthService(t.Context(), "pyck-nats", "pyck-stream", "pyck", nil,
		accountSeed(t), livenessmocks.NewTestCache(t), livenessmocks.NoDisabledTenants,
		WithUserJWTTTL(ttl))
	require.NoError(t, err)

	service := newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil,
		livenessmocks.NewTestCache(t), nil, WithUserJWTTTL(ttl))

	for range 200 {
		claims := service.mintUserClaims("UAUSERNKEY", uuid.New(), false)
		require.Greater(t, claims.Expires, time.Now().Unix(),
			"a minted credential must still be valid when it is handed out")
	}
}

// PYCK_NATS_USER_JWT_TTL is configurable, and a TTL at or below a fixed
// jitter would mint a credential that is already expired. The spread
// therefore scales down with a short TTL: every credential lives at least
// three quarters and at most five quarters of the TTL, and a cohort is
// still spread rather than expiring in lockstep.
func TestMintUserClaims_ShortTTLKeepsAtLeastThreeQuartersOfIt(t *testing.T) {
	t.Parallel()

	for _, ttl := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute} {
		t.Run(ttl.String(), func(t *testing.T) {
			t.Parallel()

			_, err := NewAuthService(t.Context(), "pyck-nats", "pyck-stream", "pyck", nil,
				accountSeed(t), livenessmocks.NewTestCache(t), livenessmocks.NoDisabledTenants,
				WithUserJWTTTL(ttl))
			require.NoError(t, err, "a short TTL is a valid configuration")

			service := newAuthService(t.Context(), "pyck-stream", "pyck", nil, nil,
				livenessmocks.NewTestCache(t), nil, WithUserJWTTTL(ttl))

			distinct := map[int64]struct{}{}
			for range 300 {
				before := time.Now()
				claims := service.mintUserClaims("UAUSERNKEY", uuid.New(), false)
				after := time.Now()
				require.GreaterOrEqual(t, claims.Expires, before.Add(ttl*3/4).Unix(),
					"a credential must live at least 3/4 of the TTL")
				require.LessOrEqual(t, claims.Expires, after.Add(ttl*5/4).Unix(),
					"a credential must live at most 5/4 of the TTL")
				distinct[claims.Expires-before.Unix()] = struct{}{}
			}
			assert.Greater(t, len(distinct), 1, "expiries must still be spread across a cohort")
		})
	}
}
