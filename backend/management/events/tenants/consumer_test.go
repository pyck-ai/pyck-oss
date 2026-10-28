//nolint:testpackage // the consumer config under test is built by an unexported path; tests live alongside.
package tenants

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The durable rename and DeliverNewPolicy are deploy-time decisions with no
// runtime symptom: get them wrong and the service still starts, still
// consumes, and silently swallows registrations (old durable, old filter)
// or re-dispatches years of lifecycle history (DeliverAll). Nothing but
// this test reads them back.

// The name the durable had before the create subject joined the filter. A
// deploy that reuses it hands the new binaries a consumer the old ones are
// still reading, and a create event delivered to an old replica is ACKed
// without a registration ever starting.
const retiredConsumerName = "management-tenant-lifecycle-trigger"

// fakeConsumeContext is the handle Consume returns; the trigger only ever
// passes it back to its caller.
type fakeConsumeContext struct{ jetstream.ConsumeContext }

// fakeConsumer accepts the callback and hands back a handle.
type fakeConsumer struct {
	jetstream.Consumer
	consumeErr error
	// opts are the pull options Consume was called with.
	opts []jetstream.PullConsumeOpt
}

func (c *fakeConsumer) Consume(_ jetstream.MessageHandler, opts ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	c.opts = opts
	if c.consumeErr != nil {
		return nil, c.consumeErr
	}
	return &fakeConsumeContext{}, nil
}

// capturingJetStream records the consumer configuration the trigger asks
// for. Only the two methods SubscribeTrigger calls are implemented; the
// embedded interface makes any other call a loud panic rather than a silent
// pass.
type capturingJetStream struct {
	jetstream.JetStream

	stream     string
	cfg        jetstream.ConsumerConfig
	consumer   *fakeConsumer
	consumeErr error
	createErr  error
}

func (js *capturingJetStream) CreateOrUpdateConsumer(
	_ context.Context, stream string, cfg jetstream.ConsumerConfig,
) (jetstream.Consumer, error) {
	js.stream = stream
	js.cfg = cfg
	if js.createErr != nil {
		return nil, js.createErr
	}
	js.consumer = &fakeConsumer{consumeErr: js.consumeErr}
	return js.consumer, nil
}

// subscribe runs SubscribeTrigger against the capturing JetStream and
// returns it for inspection.
func subscribe(t *testing.T, js *capturingJetStream, cfg TriggerConfig) error {
	t.Helper()
	cc, err := SubscribeTrigger(t.Context(), js, nil, "pyck", cfg)
	if err == nil {
		require.NotNil(t, cc, "a successful subscribe must return the cancel handle")
	}
	return err
}

func TestSubscribeTrigger_UsesTheV2DurableAndDeliversOnlyNewEvents(t *testing.T) {
	t.Parallel()

	js := &capturingJetStream{}
	require.NoError(t, subscribe(t, js, testCfg()))

	assert.Equal(t, "pyck", js.stream)
	assert.Equal(t, "management-tenant-lifecycle-trigger-v2", js.cfg.Name)
	assert.Equal(t, js.cfg.Name, js.cfg.Durable, "the consumer must be durable under its own name")
	assert.NotEqual(t, retiredConsumerName, js.cfg.Durable,
		"reusing the retired durable makes a rolling deploy swallow create events")

	assert.Equal(t, jetstream.DeliverNewPolicy, js.cfg.DeliverPolicy,
		"replaying retained history would re-dispatch every disable and restore ever performed")
}

// The consumer's delivery cap and its ACK policy are what make a failed
// dispatch retryable at all: without explicit ACKs a message is gone on
// delivery, and without a cap a permanently failing one is redelivered
// forever. The consumer carries no BackOff (it would shrink AckWait to
// its first entry) and its AckWait outlasts the handler's Temporal call.
func TestSubscribeTrigger_CarriesTheConfiguredDeliveryBounds(t *testing.T) {
	t.Parallel()

	cfg := TriggerConfig{MaxRedeliver: 7, TemporalCallTimeout: 3 * time.Second}
	js := &capturingJetStream{}
	require.NoError(t, subscribe(t, js, cfg))

	assert.Equal(t, jetstream.AckExplicitPolicy, js.cfg.AckPolicy)
	assert.Equal(t, cfg.MaxRedeliver, js.cfg.MaxDeliver,
		"the delivery cap comes from configuration, not from a constant")
	assert.Empty(t, js.cfg.BackOff, "a consumer BackOff sets AckWait to its first entry")
	assert.Greater(t, js.cfg.AckWait, cfg.TemporalCallTimeout)
}

// The create subject is the one the retired durable did not carry; it is
// the whole reason for the rename, so the filter must name it.
func TestSubscribeTrigger_FiltersTheTenantLifecycleSubjects(t *testing.T) {
	t.Parallel()

	js := &capturingJetStream{}
	require.NoError(t, subscribe(t, js, testCfg()))

	require.NotEmpty(t, js.cfg.FilterSubjects)
	assert.ElementsMatch(t, triggerSubjects("pyck"), js.cfg.FilterSubjects)

	var create string
	for _, subject := range js.cfg.FilterSubjects {
		assert.True(t, strings.HasPrefix(subject, "pyck."), "subjects are scoped to the configured stream: %s", subject)
		if strings.HasSuffix(subject, ".create") {
			create = subject
		}
	}
	assert.NotEmpty(t, create, "the create subject is what the v2 durable exists for")
	assert.Contains(t, create, "tenant", "and it is the tenant schema's create")
}

// A consumer that cannot be created or consumed must abort startup: a
// service that came up without this subscription would leave every
// registration pending and every disable undispatched, with nothing but the
// reconcile sweep behind it.
func TestSubscribeTrigger_FailuresAbortStartup(t *testing.T) {
	t.Parallel()

	createErr := errors.New("consumer creation refused")
	consumeErr := errors.New("consume refused")

	cases := map[string]struct {
		js   *capturingJetStream
		want error
	}{
		"consumer creation": {js: &capturingJetStream{createErr: createErr}, want: createErr},
		"consume":           {js: &capturingJetStream{consumeErr: consumeErr}, want: consumeErr},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := subscribe(t, tc.js, testCfg())
			require.ErrorIs(t, err, tc.want,
				"startup must not continue without the trigger subscription, and must say why")
		})
	}
}

// AckWait is derived from the Temporal call budget for every configuration,
// not pinned to the default: a deployment that raises
// PYCK_TENANT_EVENT_TEMPORAL_CALL_TIMEOUT must not get a consumer that
// redelivers an event while its handler is still waiting on Temporal.
func TestSubscribeTrigger_AckWaitOutlastsTheTemporalCall(t *testing.T) {
	t.Parallel()

	for _, callTimeout := range []time.Duration{time.Second, 10 * time.Second, 45 * time.Second} {
		t.Run(callTimeout.String(), func(t *testing.T) {
			t.Parallel()

			js := &capturingJetStream{}
			require.NoError(t, subscribe(t, js, TriggerConfig{MaxRedeliver: 5, TemporalCallTimeout: callTimeout}))

			assert.Empty(t, js.cfg.BackOff, "with a BackOff nats-server forces AckWait to BackOff[0]")
			assert.Greater(t, js.cfg.AckWait, callTimeout,
				"an ACK-timeout redelivery must not fire while the handler is inside the Temporal call")
		})
	}
}

// JetStream starts AckWait when it delivers a message, and the callback runs
// serially. A prefetched event queued behind a handler blocked in Temporal
// would time out while it waits and be redelivered at once, outside the
// NakWithDelay schedule, spending a delivery: during an outage a few queued
// events use up MaxDeliver and a create event is dropped. The consumer must
// therefore hold one message at a time.
func TestSubscribeTrigger_PullsOneEventAtATime(t *testing.T) {
	t.Parallel()

	js := &capturingJetStream{}
	require.NoError(t, subscribe(t, js, testCfg()))

	require.NotNil(t, js.consumer)
	assert.Contains(t, js.consumer.opts, jetstream.PullMaxMessages(1),
		"a prefetched event's AckWait runs while it waits behind the handler ahead of it")
}
