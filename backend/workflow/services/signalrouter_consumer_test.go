package services_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"

	"github.com/pyck-ai/pyck/backend/common/events"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

const consumerStream = "pyck"

var errTransient = errors.New("temporal unavailable")

// natsEnv is an embedded JetStream server with the event stream on it. The
// stream also takes request.> so the tests can prove the consumer's filter
// keeps those subjects out.
type natsEnv struct {
	t   *testing.T
	url string
	js  jetstream.JetStream
	nc  *nats.Conn
}

func newNatsEnv(t *testing.T) *natsEnv {
	t.Helper()

	srv, err := server.NewServer(&server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	})
	require.NoError(t, err)

	go srv.Start()

	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded NATS did not start")
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })

	e := &natsEnv{t: t, url: srv.ClientURL()}
	e.nc = e.connect()
	e.js, err = jetstream.New(e.nc)
	require.NoError(t, err)

	_, err = e.js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     consumerStream,
		Subjects: []string{consumerStream + ".>", "request.>"},
	})
	require.NoError(t, err)

	return e
}

func (e *natsEnv) connect() *nats.Conn {
	e.t.Helper()

	nc, err := nats.Connect(e.url)
	require.NoError(e.t, err)
	e.t.Cleanup(nc.Close)

	return nc
}

// router builds a started router whose handler is fn.
func (e *natsEnv) router(cfg services.ConsumerConfig, fn func(context.Context, *nats.Msg) error) *services.SignalRouter {
	e.t.Helper()

	js, err := jetstream.New(e.connect())
	require.NoError(e.t, err)

	r := services.NewSignalRouter(nil, services.SignalRouterConfig{
		JetstreamClient: js,
		StreamName:      consumerStream,
		Consumer:        cfg,
	})
	if fn != nil {
		r.SetDispatch(fn)
	}

	require.NoError(e.t, r.Start(context.Background()))
	e.t.Cleanup(r.Stop)

	return r
}

func (e *natsEnv) publish(subject string) {
	e.t.Helper()

	_, err := e.js.Publish(context.Background(), subject, []byte(`{}`))
	require.NoError(e.t, err)
}

// settled polls until the router's durable consumer has nothing pending or unacknowledged and
// reports whether it got there.
func (e *natsEnv) settled() bool {
	e.t.Helper()

	return assert.Eventually(e.t, func() bool {
		c, err := e.js.Consumer(context.Background(), consumerStream, services.DefaultConsumerDurable)
		if err != nil {
			return false
		}

		info, err := c.Info(context.Background())

		return err == nil && info.NumAckPending == 0 && info.NumPending == 0 && info.NumWaiting >= 0 && info.Delivered.Consumer > 0
	}, 5*time.Second, 20*time.Millisecond)
}

func mutationSubject() string {
	return events.MutationEventTopic{
		StreamName: consumerStream, TenantID: uuid.New(), ServiceName: "inventory",
		SchemaName: "item", EntityID: uuid.New(), OperationName: "create",
	}.String()
}

func stateChangeSubject() string {
	return events.TemporalWorkflowStateChangeTopic{
		StreamName: consumerStream, Namespace: uuid.NewString(), TaskQueue: "q",
		WorkflowTypeName: "Wf", WorkflowID: "wf-1", RunID: "run-1", Status: "completed",
	}.String()
}

// fastCfg shrinks the timing so a test finishes in a second or two.
func fastCfg() services.ConsumerConfig {
	return services.ConsumerConfig{
		AckWait:     time.Second,
		Backoff:     []time.Duration{50 * time.Millisecond},
		Concurrency: 4,
	}
}

func TestConsumer_HandledOnceAndAcked(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	var calls atomic.Int32

	e.router(fastCfg(), func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return nil
	})

	e.publish(mutationSubject())
	e.publish(stateChangeSubject())

	require.Eventually(t, func() bool { return calls.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
	e.settled()

	time.Sleep(1500 * time.Millisecond) // past AckWait: an unacked event would return
	assert.EqualValues(t, 2, calls.Load(), "acked events are not redelivered")
}

func TestConsumer_TransientErrorIsRedeliveredWithBackoff(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	var (
		mu    sync.Mutex
		times []time.Time
	)

	cfg := fastCfg()
	cfg.Backoff = []time.Duration{200 * time.Millisecond}

	e.router(cfg, func(context.Context, *nats.Msg) error {
		mu.Lock()
		defer mu.Unlock()

		times = append(times, time.Now())
		if len(times) <= 2 {
			return errTransient
		}

		return nil
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(times) == 3 }, 5*time.Second, 10*time.Millisecond)
	e.settled()

	mu.Lock()
	defer mu.Unlock()

	assert.GreaterOrEqual(t, times[1].Sub(times[0]), 150*time.Millisecond, "first NAK delay is respected")
	assert.GreaterOrEqual(t, times[2].Sub(times[1]), 150*time.Millisecond, "second NAK delay is respected")

	time.Sleep(1200 * time.Millisecond)
	assert.Len(t, times, 3, "a successful handling is acked")
}

func TestConsumer_PermanentErrorIsAckedAfterOneAttempt(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	var calls atomic.Int32

	e.router(fastCfg(), func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return services.Permanent(errors.New("malformed"))
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	e.settled()

	time.Sleep(1500 * time.Millisecond)
	assert.EqualValues(t, 1, calls.Load())
}

// TestConsumer_MalformedEventIsAckedByRealHandler runs the router's own
// dispatch: a payload that is not JSON is permanent and must not come back.
func TestConsumer_MalformedEventIsAckedByRealHandler(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)
	e.router(fastCfg(), nil)

	_, err := e.js.Publish(context.Background(), mutationSubject(), []byte("not-json"))
	require.NoError(t, err)
	_, err = e.js.Publish(context.Background(), stateChangeSubject(), []byte("not-json"))
	require.NoError(t, err)

	e.settled()

	c, err := e.js.Consumer(context.Background(), consumerStream, services.DefaultConsumerDurable)
	require.NoError(t, err)

	time.Sleep(1500 * time.Millisecond)

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 2, info.Delivered.Stream, "both malformed events delivered once")
	assert.Zero(t, info.NumRedelivered, "a malformed event is never redelivered")
	assert.Zero(t, info.NumAckPending)
}

//nolint:paralleltest // asserts a delta of the process-wide give-up counter, which parallel tests also move
func TestConsumer_GivesUpAfterMaxDeliver(t *testing.T) {
	e := newNatsEnv(t)

	var calls atomic.Int32

	cfg := fastCfg()
	cfg.MaxDeliver = 3
	cfg.Backoff = []time.Duration{20 * time.Millisecond}

	before := testutil.ToFloat64(services.EventsGivenUp())

	e.router(cfg, func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return errTransient
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() == 3 }, 5*time.Second, 10*time.Millisecond)
	e.settled()

	time.Sleep(1500 * time.Millisecond)
	assert.EqualValues(t, 3, calls.Load(), "no delivery after MaxDeliver")
	assert.InDelta(t, before+1, testutil.ToFloat64(services.EventsGivenUp()), 0.001, "the give-up is counted once")
}

func TestConsumer_FilterExcludesOtherSubjects(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)
	tenant := uuid.New()

	var (
		mu  sync.Mutex
		got []string
	)

	r := e.router(fastCfg(), func(_ context.Context, msg *nats.Msg) error {
		mu.Lock()
		defer mu.Unlock()

		got = append(got, msg.Subject)

		return nil
	})
	_ = r

	c, err := e.js.Consumer(context.Background(), consumerStream, services.DefaultConsumerDurable)
	require.NoError(t, err)

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"pyck.*.crud.*.*.*.*", "pyck.*.temporal.*.*.*.*.*"}, info.Config.FilterSubjects)
	assert.Equal(t, jetstream.DeliverNewPolicy, info.Config.DeliverPolicy)
	assert.Equal(t, jetstream.AckExplicitPolicy, info.Config.AckPolicy)

	mutation, stateChange := mutationSubject(), stateChangeSubject()
	excluded := []string{
		events.CustomEventTopic{StreamName: consumerStream}.String(),
		events.WorkflowEventTopic{StreamName: consumerStream, TenantID: tenant, WorkflowID: uuid.New(), WorkflowName: "wf"}.String(),
		events.UpdateEventTopic{
			StreamName: consumerStream, TenantID: tenant, ServiceName: "inventory", SchemaName: "item",
			EntityID: uuid.New(), OperationName: "update", AttributeName: "quantity",
		}.String(),
		events.DeadLetterEventTopic{
			StreamName: consumerStream, TenantID: tenant, ServiceName: "inventory", SchemaName: "item",
			EntityID: uuid.New(), OperationName: "create",
		}.String(),
		events.MutationEventWithReplyTopic{
			StreamName: consumerStream, TenantID: tenant, ServiceName: "inventory", SchemaName: "item",
			EntityID: uuid.New(), OperationName: "create",
		}.String(),
	}

	for _, s := range excluded {
		e.publish(s)
	}

	e.publish(mutation)
	e.publish(stateChange)

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= 2 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{mutation, stateChange}, got, "excluded: %v", excluded)
}

func TestConsumer_TwoRoutersShareTheDurable(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	var (
		mu     sync.Mutex
		counts = map[string]int{}
	)

	handler := func(_ context.Context, msg *nats.Msg) error {
		mu.Lock()
		defer mu.Unlock()

		counts[msg.Subject]++

		return nil
	}

	e.router(fastCfg(), handler)
	e.router(fastCfg(), handler)

	const total = 40

	for range total {
		e.publish(mutationSubject())
	}

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(counts) == total }, 10*time.Second, 10*time.Millisecond)
	e.settled()
	time.Sleep(1200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	for subject, n := range counts {
		assert.Equal(t, 1, n, "event %s handled %d times", subject, n)
	}
}

func TestConsumer_StopWaitsForInFlightHandler(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	var (
		started = make(chan struct{})
		release = make(chan struct{})
		calls   atomic.Int32
	)

	r := e.router(fastCfg(), func(context.Context, *nats.Msg) error {
		calls.Add(1)
		close(started)
		<-release

		return nil
	})

	e.publish(mutationSubject())
	<-started

	stopped := make(chan struct{})

	go func() { r.Stop(); close(stopped) }()

	select {
	case <-stopped:
		t.Fatal("Stop returned while a handler was running")
	case <-time.After(300 * time.Millisecond):
	}

	close(release)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the handler finished")
	}

	e.settled()
	assert.EqualValues(t, 1, calls.Load(), "the in-flight event was handled and acked")
}

// TestConsumer_InProgressKeepsALongHandlerFromRedelivery runs a handler longer
// than AckWait: without the InProgress pings the event would be redelivered
// under it.
func TestConsumer_InProgressKeepsALongHandlerFromRedelivery(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	var calls atomic.Int32

	cfg := fastCfg()
	cfg.AckWait = 600 * time.Millisecond

	e.router(cfg, func(context.Context, *nats.Msg) error {
		calls.Add(1)
		time.Sleep(2 * time.Second)

		return nil
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() >= 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(2500 * time.Millisecond)
	e.settled()
	assert.EqualValues(t, 1, calls.Load(), "handled %d times", calls.Load())
}

func TestIsPermanent(t *testing.T) {
	t.Parallel()

	perm := services.Permanent(errors.New("bad"))
	wrapped := fmt.Errorf("failed to trigger workflow: %w", perm)

	assert.True(t, services.IsPermanent(perm))
	assert.True(t, services.IsPermanent(wrapped))
	assert.True(t, services.IsPermanent(errors.Join(perm, wrapped)), "all parts permanent")
	assert.False(t, services.IsPermanent(errTransient))
	assert.False(t, services.IsPermanent(errors.Join(perm, errTransient)), "one transient part needs a redelivery")
	assert.False(t, services.IsPermanent(nil))
}

// TestIsPermanent_TemporalErrors: Temporal's InvalidArgument (the signal count
// limit, the blob size limit) fails the same way on every redelivery. A
// NamespaceNotFound stays transient: a namespace created a moment ago is
// reported as missing for about a second.
func TestIsPermanent_TemporalErrors(t *testing.T) {
	t.Parallel()

	permanentErrs := map[string]error{
		"invalid argument":         serviceerror.NewInvalidArgument("exceeded workflow execution limit for signals"),
		"wrapped invalid argument": fmt.Errorf("failed to signal: %w", serviceerror.NewInvalidArgument("blob size exceeds limit")),
		"joined, all permanent":    errors.Join(serviceerror.NewInvalidArgument("a"), services.Permanent(errors.New("b"))),
	}
	for name, err := range permanentErrs {
		assert.True(t, services.IsPermanent(err), name)
	}

	transient := map[string]error{
		"namespace not found":        serviceerror.NewNamespaceNotFound("ns"),
		"unavailable":                serviceerror.NewUnavailable("down"),
		"joined with transient part": errors.Join(serviceerror.NewInvalidArgument("a"), errTransient),
	}
	for name, err := range transient {
		assert.False(t, services.IsPermanent(err), name)
	}
}

// TestRouter_OutcomeClassification drives the real handlers and checks which
// outcomes are errors (redelivered) and which are not (acked).
func TestRouter_OutcomeClassification(t *testing.T) {
	t.Parallel()

	t.Run("start refused because the ID exists is success", func(t *testing.T) {
		t.Parallel()

		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigStart, "", "")
		e.temporal.startErr = serviceerror.NewWorkflowExecutionAlreadyStarted("exists", "req", "run")

		_, err := e.publish()
		require.NoError(t, err)
	})

	t.Run("signal to an execution that closed is success", func(t *testing.T) {
		t.Parallel()

		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigInter, "SignalA", "")
		e.temporal.signalErr = serviceerror.NewNotFound("workflow execution already completed")

		_, err := e.publish()
		require.NoError(t, err)
	})

	t.Run("a Temporal outage is transient", func(t *testing.T) {
		t.Parallel()

		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigInter, "SignalA", "")
		e.temporal.signalErr = serviceerror.NewUnavailable("temporal down")

		_, err := e.publish()
		require.Error(t, err)
		assert.False(t, services.IsPermanent(err))
	})

	t.Run("a Signal-With-Start without a signal name is permanent", func(t *testing.T) {
		t.Parallel()

		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigSWS, "", "")

		_, err := e.publish()
		require.Error(t, err)
		assert.True(t, services.IsPermanent(err))
	})

	t.Run("a signal by ID without a signal name is permanent", func(t *testing.T) {
		t.Parallel()

		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigByID, "", "")

		_, err := e.publish()
		require.Error(t, err)
		assert.True(t, services.IsPermanent(err))
	})

	t.Run("a broken filter is permanent", func(t *testing.T) {
		t.Parallel()

		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigInter, "SignalA", `status = `)

		_, err := e.publish()
		require.Error(t, err)
		assert.True(t, services.IsPermanent(err))
	})

	t.Run("a broken filter plus a Temporal outage is transient", func(t *testing.T) {
		t.Parallel()

		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigInter, "SignalA", `status = `)
		e.subscribe("worker-b", sigInter, "SignalB", "")
		e.temporal.signalErr = serviceerror.NewUnavailable("temporal down")

		_, err := e.publish()
		require.Error(t, err)
		assert.False(t, services.IsPermanent(err))
	})
}

// A durable that exists with a config the server will not update (DeliverPolicy
// is immutable) must not keep the router from starting: a crash loop would stop
// all routing. The router binds the existing durable instead.
func TestConsumer_StartBindsAnExistingIncompatibleDurable(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	_, err := e.js.CreateOrUpdateConsumer(context.Background(), consumerStream, jetstream.ConsumerConfig{
		Durable:        services.DefaultConsumerDurable,
		FilterSubjects: []string{consumerStream + ".*.crud.*.*.*.*"},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        time.Second,
	})
	require.NoError(t, err)

	var calls atomic.Int32

	e.router(fastCfg(), func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return nil
	})

	e.publish(mutationSubject())
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the router consumes through the durable it found")
}
