package authn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/authn/mocks"
	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
)

// errSubscribe is the JetStream refusal the subscribe-failure tests inject.
var errSubscribe = errors.New("nats: stream not found")

// subscribeJetStream embeds the interface so only CreateOrUpdateConsumer
// needs overriding; any other method panics on the nil embedded value.
// consumer is handed back when err is nil.
type subscribeJetStream struct {
	jetstream.JetStream

	consumer jetstream.Consumer
	err      error
}

func (f subscribeJetStream) CreateOrUpdateConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.consumer, nil
}

// consumeConsumer answers Consume with cc, or with err when it is set.
type consumeConsumer struct {
	jetstream.Consumer

	cc  jetstream.ConsumeContext
	err error
}

func (c consumeConsumer) Consume(jetstream.MessageHandler, ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.cc, nil
}

// stoppedConsumeContext records whether the caller stopped it.
type stoppedConsumeContext struct {
	jetstream.ConsumeContext

	stopped bool
}

func (s *stoppedConsumeContext) Stop() { s.stopped = true }

// providerConfig is a ZitadelConfig NewZitadelAuthProvider accepts.
func providerConfig() config.ZitadelConfig {
	return config.ZitadelConfig{
		ZitadelAudience:           "https://test.example.com",
		ZitadelOrganizationId:     "123456789",
		ZitadelPATCacheTTL:        time.Hour,
		ZitadelPATCacheTTLOverlap: time.Minute,
	}
}

// newProviderWithTenantEvents runs the helper against js and returns what
// it returned. The liveness cache is the caller's (it outlives the helper
// in every service main and runs its own expiry goroutine), so the test
// builds it before taking its goroutine snapshot and closes it in cleanup.
func newProviderWithTenantEvents(t *testing.T, cache *liveness.Cache, js jetstream.JetStream) (*authn.ZitadelAuthProvider, jetstream.ConsumeContext, error) {
	t.Helper()
	return authn.NewProviderWithTenantEvents(t.Context(), mocks.NewMockZitadelClient(), providerConfig(),
		livenessmocks.NoDisabledTenants, cache, js, "pyck", "inventory")
}

// The provider is built before the subscribe, and its token cache starts
// a janitor goroutine. When the subscribe fails the caller receives no
// provider to close, so the helper must close it itself; otherwise every
// failed boot attempt leaves a goroutine behind. Both subscribe steps can
// fail: creating the consumer and starting its consume loop.
//
//nolint:paralleltest // goleak compares the process's goroutines before and after the call; a parallel sibling would start its own in between
func TestNewProviderWithTenantEvents_FailedSubscribeClosesTheProvider(t *testing.T) {
	cases := []struct {
		name string
		js   jetstream.JetStream
	}{
		{"creating the consumer fails", subscribeJetStream{err: errSubscribe}},
		{"starting the consume loop fails", subscribeJetStream{consumer: consumeConsumer{err: errSubscribe}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := newLivenessCache(t, providerConfig())
			before := goleak.IgnoreCurrent()

			provider, cc, err := newProviderWithTenantEvents(t, cache, tc.js)

			require.ErrorIs(t, err, errSubscribe)
			assert.Contains(t, err.Error(), "subscribe revocations")
			assert.Nil(t, provider, "a failed setup hands back no provider")
			assert.Nil(t, cc, "a failed setup hands back no consume handle")
			goleak.VerifyNone(t, before)
		})
	}
}

// A provider the helper refuses to build (here: a PAT cache overlap longer
// than its TTL) never reaches the subscribe and leaves nothing running.
//
//nolint:paralleltest // goleak compares the process's goroutines before and after the call
func TestNewProviderWithTenantEvents_RefusedProviderSubscribesNothing(t *testing.T) {
	cfg := providerConfig()
	cfg.ZitadelPATCacheTTLOverlap = 2 * cfg.ZitadelPATCacheTTL
	cache := newLivenessCache(t, cfg)
	before := goleak.IgnoreCurrent()

	// Any JetStream call would panic on the nil embedded interface.
	provider, cc, err := authn.NewProviderWithTenantEvents(t.Context(), mocks.NewMockZitadelClient(), cfg,
		livenessmocks.NoDisabledTenants, cache, subscribeJetStream{}, "pyck", "inventory")

	require.ErrorIs(t, err, authn.ErrPATCacheOverlap)
	assert.Nil(t, provider)
	assert.Nil(t, cc)
	goleak.VerifyNone(t, before)
}

// On success the caller owns both halves: the provider (whose Close stops
// the token-cache janitor) and the consume handle. Once it has closed and
// stopped them, nothing the helper started is left running.
//
//nolint:paralleltest // goleak compares the process's goroutines before and after the call
func TestNewProviderWithTenantEvents_SuccessHandsBackProviderAndConsumeHandle(t *testing.T) {
	cache := newLivenessCache(t, providerConfig())
	before := goleak.IgnoreCurrent()
	stopper := &stoppedConsumeContext{}

	provider, cc, err := newProviderWithTenantEvents(t, cache, subscribeJetStream{consumer: consumeConsumer{cc: stopper}})

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Same(t, stopper, cc, "the consume handle is the subscription's own")
	cc.Stop()
	provider.Close()
	assert.True(t, stopper.stopped)
	goleak.VerifyNone(t, before)
}
