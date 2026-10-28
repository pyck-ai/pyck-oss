package events_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"
)

const testPrefix = "pyck"

var testLimits = events.StreamLimits{MaxAge: 72 * time.Hour, TenantMaxMsgs: 100_000, SystemMaxMsgs: 200_000, AggregateMaxMsgs: 2_000_000}

func newJetStream(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()

	srv := natstest.Start(t, nil)
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	return nc, js
}

func streamConfig(t *testing.T, ctx context.Context, js jetstream.JetStream, name string) jetstream.StreamConfig {
	t.Helper()

	stream, err := js.Stream(ctx, name)
	require.NoError(t, err)

	return stream.CachedInfo().Config
}

func sourceNames(cfg jetstream.StreamConfig) []string {
	names := make([]string, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		names = append(names, source.Name)
	}

	return names
}

// awaitMsgs waits until the stream holds want messages and asserts it holds no
// more.
func awaitMsgs(t *testing.T, ctx context.Context, js jetstream.JetStream, name string, want uint64) {
	t.Helper()

	stream, err := js.Stream(ctx, name)
	require.NoError(t, err)

	var got uint64
	require.Eventually(t, func() bool {
		info, err := stream.Info(ctx)
		if err != nil {
			return false
		}
		got = info.State.Msgs

		return got >= want
	}, 10*time.Second, 20*time.Millisecond, "stream %s never held %d messages", name, want)

	// Sourcing is asynchronous, and "no surplus message" is a quiet period
	// that cannot be polled for: wait a fixed grace period, then recount.
	time.Sleep(100 * time.Millisecond)

	info, err := stream.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, info.State.Msgs, "messages in stream %s", name)
}

func publish(t *testing.T, ctx context.Context, js jetstream.JetStream, subject, msgID string) *jetstream.PubAck {
	t.Helper()

	opts := []jetstream.PublishOpt{}
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}

	ack, err := js.Publish(ctx, subject, []byte("{}"), opts...)
	require.NoError(t, err, "publish %s", subject)

	return ack
}

func tenantSubject(tenantID uuid.UUID) string {
	return events.WorkflowEventTopic{StreamName: testPrefix, TenantID: tenantID, WorkflowID: uuid.New(), WorkflowName: "w"}.String()
}

func TestSetupJetStream(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	system := streamConfig(t, ctx, js, topic.SystemStreamName(testPrefix))
	assert.Equal(t, []string{"pyck-system.>"}, system.Subjects)
	assert.Equal(t, events.StreamDuplicateWindow, system.Duplicates)
	assert.Positive(t, system.MaxMsgs, "the system stream must be capped")
	assert.Equal(t, int64(-1), system.MaxBytes, "a byte cap reserves file store")

	aggregate := streamConfig(t, ctx, js, testPrefix)
	assert.Greater(t, aggregate.MaxMsgs, system.MaxMsgs, "the aggregate holds every stream's events")
	assert.Equal(t, int64(-1), aggregate.MaxBytes, "a byte cap reserves file store")
	assert.Empty(t, aggregate.Subjects)
	assert.Equal(t, []string{"pyck-system"}, sourceNames(aggregate))
	assert.Zero(t, aggregate.Duplicates, "the aggregate must not detect duplicates")

	// Another service starting later leaves the sources management added.
	tenantID := uuid.New()
	require.NoError(t, events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits).EnsureTenant(ctx, tenantID))

	_, err = events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	assert.Equal(t,
		[]string{"pyck-system", topic.TenantStreamName(testPrefix, tenantID)},
		sourceNames(streamConfig(t, ctx, js, testPrefix)),
	)

	// A service configured with other limits creates nothing and leaves the
	// existing streams' limits to management.
	other := events.StreamLimits{MaxAge: time.Hour, TenantMaxMsgs: 1, SystemMaxMsgs: 2, AggregateMaxMsgs: 3}
	_, err = events.SetupJetStream(ctx, nc, testPrefix, 1, other)
	require.NoError(t, err)

	assert.Equal(t, system.MaxMsgs, streamConfig(t, ctx, js, topic.SystemStreamName(testPrefix)).MaxMsgs)
	assert.Equal(t, system.MaxAge, streamConfig(t, ctx, js, topic.SystemStreamName(testPrefix)).MaxAge)
	assert.Equal(t, aggregate.MaxMsgs, streamConfig(t, ctx, js, testPrefix).MaxMsgs)
}

// The server stores a cap of 0 or less as no cap, so it fails at startup
// instead, named.
func TestSetupJetStream_RefusesANonPositiveMaxMsgs(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	for knob, set := range map[string]func(*events.StreamLimits, int64){
		"PYCK_NATS_TENANT_STREAM_MAX_MSGS":    func(l *events.StreamLimits, n int64) { l.TenantMaxMsgs = n },
		"PYCK_NATS_SYSTEM_STREAM_MAX_MSGS":    func(l *events.StreamLimits, n int64) { l.SystemMaxMsgs = n },
		"PYCK_NATS_AGGREGATE_STREAM_MAX_MSGS": func(l *events.StreamLimits, n int64) { l.AggregateMaxMsgs = n },
	} {
		for _, n := range []int64{0, -1} {
			limits := testLimits
			set(&limits, n)

			_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, limits)
			require.ErrorIs(t, err, events.ErrInvalidStreamMaxMsgs, "%s %d", knob, n)
			assert.Contains(t, err.Error(), knob, "the error must name the env var to fix")
		}
	}

	_, err := js.Stream(ctx, topic.SystemStreamName(testPrefix))
	require.ErrorIs(t, err, jetstream.ErrStreamNotFound, "a refused cap must create nothing")
}

// The server refuses a duplicate window larger than the max age, so a max age
// below it would fail every stream; it fails at startup instead, named.
func TestSetupJetStream_RefusesAMaxAgeBelowTheDuplicateWindow(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	for _, maxAge := range []time.Duration{-time.Second, time.Second, events.StreamDuplicateWindow - time.Second} {
		limits := testLimits
		limits.MaxAge = maxAge

		_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, limits)
		require.ErrorIs(t, err, events.ErrInvalidStreamMaxAge, "max age %s", maxAge)
		assert.Contains(t, err.Error(), "PYCK_NATS_STREAM_MAX_AGE", "the error must name the env var to fix")
	}

	_, err := js.Stream(ctx, topic.SystemStreamName(testPrefix))
	require.ErrorIs(t, err, jetstream.ErrStreamNotFound, "a refused max age must create nothing")

	for _, maxAge := range []time.Duration{0, events.StreamDuplicateWindow} {
		limits := testLimits
		limits.MaxAge = maxAge

		_, err := events.SetupJetStream(ctx, nc, testPrefix+strconv.Itoa(int(maxAge.Seconds())), 1, limits)
		require.NoError(t, err, "max age %s", maxAge)
	}
}

//nolint:tparallel,paralleltest // the subtests build on each other's streams, in order
func TestStreamTopology_EnsureTenant(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)

	t.Run("rejects the nil tenant", func(t *testing.T) {
		require.ErrorIs(t, topology.EnsureTenant(ctx, uuid.Nil), events.ErrNilTenantID)
	})

	tenantID := uuid.New()
	require.NoError(t, topology.EnsureTenant(ctx, tenantID))
	require.NoError(t, topology.EnsureTenant(ctx, tenantID), "EnsureTenant must be idempotent")

	tenantStream := topic.TenantStreamName(testPrefix, tenantID)

	t.Run("creates the tenant stream over the tenant's subjects", func(t *testing.T) {
		cfg := streamConfig(t, ctx, js, tenantStream)
		assert.Equal(t, []string{"pyck." + tenantID.String() + ".>"}, cfg.Subjects)
		assert.Equal(t, events.StreamDuplicateWindow, cfg.Duplicates)
		assert.Positive(t, cfg.MaxMsgs, "a tenant must not fill the aggregate")
		assert.Equal(t, int64(-1), cfg.MaxBytes, "a byte cap reserves file store")
	})

	t.Run("the aggregate sources it", func(t *testing.T) {
		assert.Equal(t, []string{"pyck-system", tenantStream}, sourceNames(streamConfig(t, ctx, js, testPrefix)))

		ack := publish(t, ctx, js, tenantSubject(tenantID), "")
		assert.Equal(t, tenantStream, ack.Stream, "a tenant subject must land in the tenant's stream")
		awaitMsgs(t, ctx, js, testPrefix, 1)
	})

	t.Run("a source added late delivers the stream from its start", func(t *testing.T) {
		late := uuid.New()
		_, err := js.CreateStream(ctx, events.TenantStreamConfig(testPrefix, 1, testLimits, late))
		require.NoError(t, err)

		publish(t, ctx, js, tenantSubject(late), "")
		publish(t, ctx, js, tenantSubject(late), "")

		require.NoError(t, topology.EnsureTenant(ctx, late))
		awaitMsgs(t, ctx, js, testPrefix, 3)
	})

	t.Run("concurrent registrations keep every source", func(t *testing.T) {
		tenants := make([]uuid.UUID, 16)

		var wg sync.WaitGroup
		for i := range tenants {
			tenants[i] = uuid.New()
			wg.Go(func() { assert.NoError(t, topology.EnsureTenant(ctx, tenants[i])) })
		}
		wg.Wait()

		sources := sourceNames(streamConfig(t, ctx, js, testPrefix))
		for _, tenantID := range tenants {
			assert.Contains(t, sources, topic.TenantStreamName(testPrefix, tenantID))
		}
	})
}

// TestStreamTopology_EnsureTenantOnEmptyServer pins that a tenant registered on
// a NATS that lost its streams gets an aggregate whose system source exists:
// the aggregate is created there, and sourcing a missing stream fails silently.
func TestStreamTopology_EnsureTenantOnEmptyServer(t *testing.T) {
	t.Parallel()

	_, js := newJetStream(t)
	ctx := t.Context()

	tenantID := uuid.New()
	require.NoError(t, events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits).EnsureTenant(ctx, tenantID))

	assert.Equal(t, []string{"pyck-system.>"}, streamConfig(t, ctx, js, topic.SystemStreamName(testPrefix)).Subjects)
	assert.Equal(t,
		[]string{"pyck-system", topic.TenantStreamName(testPrefix, tenantID)},
		sourceNames(streamConfig(t, ctx, js, testPrefix)),
	)

	publish(t, ctx, js, events.CustomEventTopic{StreamName: topic.SystemStreamName(testPrefix)}.String(), "")
	awaitMsgs(t, ctx, js, testPrefix, 1)
}

// TestStreamTopology_DuplicatesStayInTheirStream pins the point of the layout:
// a message ID detects duplicates within one stream only, so a tenant cannot
// suppress another tenant's or the system's message, and the aggregate keeps
// every message its sources stored.
func TestStreamTopology_DuplicatesStayInTheirStream(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)

	attacker, victim := uuid.New(), uuid.New()
	require.NoError(t, topology.EnsureTenant(ctx, attacker))
	require.NoError(t, topology.EnsureTenant(ctx, victim))

	const msgID = "predictable-id"

	// The attacker publishes first, under its own subjects.
	first := publish(t, ctx, js, tenantSubject(attacker), msgID)
	require.False(t, first.Duplicate)

	victimChange := events.TemporalWorkflowStateChangeTopic{
		StreamName: testPrefix, Namespace: victim.String(),
		TaskQueue: "q", WorkflowTypeName: "t", WorkflowID: "w", RunID: "r", Status: "s",
	}.String()
	assert.False(t, publish(t, ctx, js, victimChange, msgID).Duplicate, "another tenant's message was suppressed")

	systemChange := events.TemporalWorkflowStateChangeTopic{
		StreamName: topic.SystemStreamName(testPrefix), Namespace: "default",
		TaskQueue: "q", WorkflowTypeName: "t", WorkflowID: "w", RunID: "r", Status: "s",
	}.String()
	assert.False(t, publish(t, ctx, js, systemChange, msgID).Duplicate, "a system message was suppressed")

	// Within one stream the ID still detects duplicates.
	assert.True(t, publish(t, ctx, js, tenantSubject(attacker), msgID).Duplicate)

	awaitMsgs(t, ctx, js, topic.TenantStreamName(testPrefix, attacker), 1)
	awaitMsgs(t, ctx, js, topic.TenantStreamName(testPrefix, victim), 1)
	awaitMsgs(t, ctx, js, topic.SystemStreamName(testPrefix), 1)
	awaitMsgs(t, ctx, js, testPrefix, 3)
}

func TestStreamTopology_DeleteTenant(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	_, err := events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)

	deleted, kept := uuid.New(), uuid.New()
	require.NoError(t, topology.EnsureTenant(ctx, deleted))
	require.NoError(t, topology.EnsureTenant(ctx, kept))

	publish(t, ctx, js, tenantSubject(deleted), "")
	publish(t, ctx, js, tenantSubject(kept), "")
	awaitMsgs(t, ctx, js, testPrefix, 2)

	require.ErrorIs(t, topology.DeleteTenant(ctx, uuid.Nil), events.ErrNilTenantID)
	require.NoError(t, topology.DeleteTenant(ctx, deleted))
	require.NoError(t, topology.DeleteTenant(ctx, deleted), "DeleteTenant must be idempotent")

	_, err = js.Stream(ctx, topic.TenantStreamName(testPrefix, deleted))
	require.ErrorIs(t, err, jetstream.ErrStreamNotFound)

	assert.Equal(t,
		[]string{"pyck-system", topic.TenantStreamName(testPrefix, kept)},
		sourceNames(streamConfig(t, ctx, js, testPrefix)),
	)

	// The deleted tenant's events are purged from the aggregate too.
	awaitMsgs(t, ctx, js, testPrefix, 1)

	_, err = js.Publish(ctx, tenantSubject(deleted), []byte("{}"))
	require.ErrorIs(t, err, jetstream.ErrNoStreamResponse, "a deleted tenant's events have no stream")
}

// TestStreamTopology_ReconcileMigratesSharedStream starts from the previous
// layout, one stream capturing every subject, with a durable consumer midway.
func TestStreamTopology_ReconcileMigratesSharedStream(t *testing.T) {
	t.Parallel()

	nc, js := newJetStream(t)
	ctx := t.Context()

	// The configuration the services wrote before the per-tenant layout.
	legacy := jetstream.StreamConfig{
		Name:         testPrefix,
		Subjects:     []string{testPrefix + ".>"},
		Replicas:     1,
		MaxConsumers: 200,
		MaxAge:       72 * time.Hour,
		Duplicates:   events.StreamDuplicateWindow,
		Retention:    jetstream.LimitsPolicy,
		Discard:      jetstream.DiscardOld,
		ConsumerLimits: jetstream.StreamConsumerLimits{
			InactiveThreshold: 72 * time.Hour,
			MaxAckPending:     10000,
		},
	}
	_, err := js.CreateStream(ctx, legacy)
	require.NoError(t, err)

	active, disabled := uuid.New(), uuid.New()
	publish(t, ctx, js, tenantSubject(active), "")
	publish(t, ctx, js, tenantSubject(disabled), "")

	consumer, err := js.CreateOrUpdateConsumer(ctx, testPrefix, jetstream.ConsumerConfig{
		Durable:       "router",
		FilterSubject: testPrefix + ".*.workflows.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)
	reconcile(t, ctx, topology, active, disabled, uuid.Nil)

	// Every management start reconciles: once the streams are in place, it
	// must not write them again.
	writes, err := nc.SubscribeSync("$JS.EVENT.ADVISORY.STREAM.>")
	require.NoError(t, err)
	reconcile(t, ctx, topology, active, disabled)
	require.NoError(t, nc.Flush())

	if advisory, err := writes.NextMsg(200 * time.Millisecond); err == nil {
		t.Errorf("a second Reconcile wrote a stream: %s", advisory.Subject)
	}

	aggregate := streamConfig(t, ctx, js, testPrefix)
	assert.Empty(t, aggregate.Subjects, "the shared stream keeps no subjects")
	assert.Zero(t, aggregate.Duplicates)
	assert.ElementsMatch(t, []string{
		"pyck-system",
		topic.TenantStreamName(testPrefix, active),
		topic.TenantStreamName(testPrefix, disabled),
	}, sourceNames(aggregate))

	// Events published after the migration land in the tenant streams and
	// reach the existing consumer behind the events it had yet to read.
	ack := publish(t, ctx, js, tenantSubject(active), "")
	assert.Equal(t, topic.TenantStreamName(testPrefix, active), ack.Stream)

	awaitMsgs(t, ctx, js, testPrefix, 3)

	batch, err := consumer.Fetch(3, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)

	var fetched int
	for msg := range batch.Messages() {
		require.NoError(t, msg.Ack())
		fetched++
	}
	require.NoError(t, batch.Error())
	assert.Equal(t, 3, fetched, "the consumer must read the shared stream's events and the sourced ones")
}

// A page of full stream infos outgrows max_payload long before the tenant
// count does; Reconcile must not depend on one.
func TestStreamTopology_ReconcileManyTenantsUnderSmallMaxPayload(t *testing.T) {
	t.Parallel()

	srv := natstest.Start(t, nil, map[string]any{"max_payload": 16 * 1024})
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	tenants := make([]uuid.UUID, 60)
	for i := range tenants {
		tenants[i] = uuid.New()
	}

	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)
	reconcile(t, ctx, topology, tenants...)
	reconcile(t, ctx, topology, tenants...) // a restart reconciles existing streams too

	assert.Len(t, sourceNames(streamConfig(t, ctx, js, testPrefix)), len(tenants)+1)
}

// A tenant stream that cannot be put must not keep the others unsourced.
func TestStreamTopology_ReconcileSourcesTheStreamsThatWerePut(t *testing.T) {
	t.Parallel()

	_, js := newJetStream(t)
	ctx := t.Context()

	good, bad := uuid.New(), uuid.New()

	// A stream over one of bad's subjects makes its tenant stream overlap.
	_, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "blocker",
		Subjects: []string{"pyck." + bad.String() + ".blocked"},
	})
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)
	repair, err := topology.Reconcile(ctx, []uuid.UUID{bad, good})
	require.ErrorIs(t, err, events.ErrTenantStreams)
	assert.Equal(t, events.StreamRepair{Failed: 1}, repair)
	require.ErrorContains(t, err, bad.String())
	assert.NotContains(t, err.Error(), good.String())

	assert.Equal(t,
		[]string{"pyck-system", topic.TenantStreamName(testPrefix, good)},
		sourceNames(streamConfig(t, ctx, js, testPrefix)),
	)
}

// Past max_payload the server refuses the aggregate; the topology must say why
// and leave the aggregate as it was.
func TestStreamTopology_AggregateTooLarge(t *testing.T) {
	t.Parallel()

	srv := natstest.Start(t, nil, map[string]any{"max_payload": 2048})
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx := t.Context()
	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)

	var (
		added   int
		refused uuid.UUID
	)
	for ; added < 100; added++ {
		refused = uuid.New()
		err = topology.EnsureTenant(ctx, refused)
		if err != nil {
			break
		}
	}

	require.ErrorIs(t, err, events.ErrAggregateTooLarge)
	assert.Positive(t, added, "a 2KB max_payload holds some sources")
	assert.Len(t, sourceNames(streamConfig(t, ctx, js, testPrefix)), added+1,
		"the refused tenant must not replace the aggregate's sources")

	_, err = js.Stream(ctx, topic.TenantStreamName(testPrefix, refused))
	require.ErrorIs(t, err, jetstream.ErrStreamNotFound, "EnsureTenant must remove the stream it created")

	// Reconcile still puts the streams, so their events can be published.
	late := uuid.New()
	repair, err := topology.Reconcile(ctx, []uuid.UUID{late})
	require.ErrorIs(t, err, events.ErrAggregateTooLarge)
	assert.Equal(t, events.StreamRepair{Unsourced: 1}, repair, "the late stream is not sourced")

	_, err = js.Stream(ctx, topic.TenantStreamName(testPrefix, late))
	require.NoError(t, err, "a tenant stream must be put past max_payload")

	// A tenant being restored keeps the stream it had.
	require.ErrorIs(t, topology.EnsureTenant(ctx, late), events.ErrAggregateTooLarge)

	_, err = js.Stream(ctx, topic.TenantStreamName(testPrefix, late))
	require.NoError(t, err, "EnsureTenant must keep a stream it did not create")
}

// The server reserves a stream's MaxBytes in max_file_store when the stream is
// created; the local stack's 256MB must hold the system stream, the aggregate
// and any number of tenants.
func TestStreamTopology_FitsAFileStoreLimit(t *testing.T) {
	t.Parallel()

	srv := natstest.Start(t, nil, map[string]any{
		"jetstream": map[string]any{"store_dir": t.TempDir(), "max_file_store": int64(256 << 20)},
	})
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)
	ctx := t.Context()

	js, err := events.SetupJetStream(ctx, nc, testPrefix, 1, testLimits)
	require.NoError(t, err)

	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)
	for range 40 {
		require.NoError(t, topology.EnsureTenant(ctx, uuid.New()))
	}
}

// TestStreamTopology_EnsureTenantGivesUpWaitingForTheLock pins that a
// registration fails fast behind a long lock holder instead of queuing.
func TestStreamTopology_EnsureTenantGivesUpWaitingForTheLock(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		held := func(ctx context.Context, _ func(context.Context) error) error {
			<-ctx.Done()
			return ctx.Err()
		}
		topology := events.NewStreamTopology(nil, testPrefix, 1, held, testLimits)

		start := time.Now()
		err := topology.EnsureTenant(t.Context(), uuid.New())
		require.ErrorIs(t, err, events.ErrStreamLockNotAcquired)
		assert.Equal(t, 10*time.Second, time.Since(start))
	})
}

// TestStreamTopology_DeleteTenantGivesUpWaitingForTheLock pins that a failed
// registration's cleanup does not keep its request open behind a reconcile.
func TestStreamTopology_DeleteTenantGivesUpWaitingForTheLock(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		held := func(ctx context.Context, _ func(context.Context) error) error {
			<-ctx.Done()
			return ctx.Err()
		}
		topology := events.NewStreamTopology(nil, testPrefix, 1, held, testLimits, events.WithLockWait(3*time.Second))

		// The resolver deletes on a context that is never cancelled.
		start := time.Now()
		err := topology.DeleteTenant(context.WithoutCancel(t.Context()), uuid.New())
		require.ErrorIs(t, err, events.ErrStreamLockNotAcquired)
		assert.Equal(t, 3*time.Second, time.Since(start))
	})
}

// A tenant stream that goes missing after startup must come back on the next
// repair, sourced again, while the streams that exist are left alone.
func TestStreamTopology_RepairTenants(t *testing.T) {
	t.Parallel()

	_, js := newJetStream(t)
	ctx := t.Context()

	kept, lost := uuid.New(), uuid.New()
	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits)
	reconcile(t, ctx, topology, kept, lost)

	require.NoError(t, js.DeleteStream(ctx, topic.TenantStreamName(testPrefix, lost)))

	repair, err := topology.RepairTenants(ctx, []uuid.UUID{kept, lost, uuid.Nil})
	require.NoError(t, err)
	assert.Equal(t, events.StreamRepair{Missing: 1}, repair)

	assert.ElementsMatch(t,
		[]string{"pyck-system", topic.TenantStreamName(testPrefix, kept), topic.TenantStreamName(testPrefix, lost)},
		sourceNames(streamConfig(t, ctx, js, testPrefix)),
	)

	repair, err = topology.RepairTenants(ctx, []uuid.UUID{kept, lost})
	require.NoError(t, err)
	assert.Zero(t, repair, "nothing is missing any more")

	// A stream over one of its subjects keeps a lost stream from coming back.
	require.NoError(t, js.DeleteStream(ctx, topic.TenantStreamName(testPrefix, lost)))
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{Name: "blocker", Subjects: []string{"pyck." + lost.String() + ".x"}})
	require.NoError(t, err)

	repair, err = topology.RepairTenants(ctx, []uuid.UUID{kept, lost})
	require.ErrorIs(t, err, events.ErrTenantStreams)
	assert.Equal(t, events.StreamRepair{Missing: 1, Failed: 1}, repair)
}

// With NATS degraded every tenant can fail; the error must stay readable and
// name only the first few.
func TestStreamTopology_ReconcileCountsTheTenantFailures(t *testing.T) {
	t.Parallel()

	_, js := newJetStream(t)
	ctx := t.Context()

	bad := make([]uuid.UUID, 8)
	subjects := make([]string, len(bad))
	for i := range bad {
		bad[i] = uuid.New()
		subjects[i] = "pyck." + bad[i].String() + ".blocked"
	}

	// A stream over one subject of each makes its tenant stream overlap.
	_, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "blocker", Subjects: subjects})
	require.NoError(t, err)

	repair, err := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), testLimits).Reconcile(ctx, bad)
	require.ErrorIs(t, err, events.ErrTenantStreams)
	assert.Equal(t, events.StreamRepair{Failed: len(bad)}, repair)
	assert.Contains(t, err.Error(), "8 of 8 failed, the first 5")

	var named int
	for _, tenantID := range bad {
		if strings.Contains(err.Error(), tenantID.String()) {
			named++
		}
	}
	assert.Equal(t, 5, named)
}

func TestStreamTopology_Options(t *testing.T) {
	t.Parallel()

	nc, _ := newJetStream(t)
	ctx := t.Context()

	limits := events.StreamLimits{MaxAge: time.Hour, TenantMaxMsgs: 10, SystemMaxMsgs: 20, AggregateMaxMsgs: 30}

	js, err := events.SetupJetStream(ctx, nc, testPrefix, 1, limits)
	require.NoError(t, err)

	tenantID := uuid.New()
	topology := events.NewStreamTopology(js, testPrefix, 1, natstest.SerialLock(), limits)
	require.NoError(t, topology.EnsureTenant(ctx, tenantID))

	for name, maxMsgs := range map[string]int64{
		topic.TenantStreamName(testPrefix, tenantID): 10,
		topic.SystemStreamName(testPrefix):           20,
		topic.AggregateStreamName(testPrefix):        30,
	} {
		cfg := streamConfig(t, ctx, js, name)
		assert.Equal(t, maxMsgs, cfg.MaxMsgs, name)
		assert.Equal(t, time.Hour, cfg.MaxAge, name)
	}

	synctest.Test(t, func(t *testing.T) {
		held := func(ctx context.Context, _ func(context.Context) error) error {
			<-ctx.Done()
			return ctx.Err()
		}
		waiting := events.NewStreamTopology(nil, testPrefix, 1, held, testLimits, events.WithLockWait(3*time.Second))

		start := time.Now()
		require.ErrorIs(t, waiting.EnsureTenant(t.Context(), uuid.New()), events.ErrStreamLockNotAcquired)
		assert.Equal(t, 3*time.Second, time.Since(start))
	})
}

func reconcile(t *testing.T, ctx context.Context, topology *events.StreamTopology, tenantIDs ...uuid.UUID) {
	t.Helper()

	_, err := topology.Reconcile(ctx, tenantIDs)
	require.NoError(t, err)
}
