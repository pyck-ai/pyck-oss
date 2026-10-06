package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

var errSignalDown = errors.New("temporal signal unavailable")

// signalFailingTemporal fails every signal, so a fan-out that signals is a
// transient failure.
type signalFailingTemporal struct{ *recordingTemporal }

func (f signalFailingTemporal) SignalWorkflow(context.Context, string, string, string, any) error {
	return errSignalDown
}

type failingFactory struct{ temporal signalFailingTemporal }

func (f failingFactory) GetClient(context.Context, string) (*workflow.Client, error) {
	return workflow.NewClient("test", f.temporal)
}

func (failingFactory) Close() {}

// statusEnv is a started router consuming from an embedded JetStream, with the
// fan-out fixtures (database, fake Temporal, one workflow).
type statusEnv struct {
	*fanoutEnv

	nats *natsEnv
	js   jetstream.JetStream
}

func fastConsumer() services.ConsumerConfig {
	return services.ConsumerConfig{
		MaxDeliver: 3,
		Backoff:    []time.Duration{time.Millisecond},
		AckWait:    5 * time.Second,
	}
}

func newStatusEnv(t *testing.T, factory func(*fanoutEnv) workflow.ClientFactory) *statusEnv {
	t.Helper()

	return newStatusEnvWith(t, factory, fastConsumer())
}

// newStatusEnvWith is newStatusEnv with the consumer settings of the test.
func newStatusEnvWith(t *testing.T, factory func(*fanoutEnv) workflow.ClientFactory, consumer services.ConsumerConfig) *statusEnv {
	t.Helper()

	n := newNatsEnv(t)
	f := newFanoutEnv(t)

	js, err := jetstream.New(n.connect())
	require.NoError(t, err)

	cf := workflow.ClientFactory(recordingFactory{f.temporal})
	if factory != nil {
		cf = factory(f)
	}

	f.router = services.NewSignalRouter(f.db, services.SignalRouterConfig{
		ClientFactory:   cf,
		JetstreamClient: js,
		StreamName:      consumerStream,
		Consumer:        consumer,
	})

	require.NoError(t, f.router.Start(context.Background()))
	t.Cleanup(f.router.Stop)

	return &statusEnv{fanoutEnv: f, nats: n, js: js}
}

// mutation is one mutation event to publish.
type mutation struct {
	entity, eventID, txID uuid.UUID
	operation             string
}

func newMutation() mutation {
	return mutation{entity: uuid.New(), eventID: uuid.Must(uuid.NewV7()), txID: uuid.Must(uuid.NewV7()), operation: "create"}
}

// publishMutation publishes a create event the way the outbox does, with the
// transaction ID in the search attributes.
func (e *statusEnv) publishMutation(m mutation) {
	e.t.Helper()

	attrs := map[string]string{}
	if m.txID != uuid.Nil {
		attrs["pyck_transaction_id"] = m.txID.String()
	}

	data, err := json.Marshal(events.MutationEventMessage{
		Service: "inventory", Type: "inventoryitem", Schema: "item", Operation: m.operation,
		ID: m.entity, EventID: m.eventID, TenantID: e.tenantID,
		DataAfter: map[string]any{"status": "active"}, WfSearchAttributes: attrs,
	})
	require.NoError(e.t, err)

	subject := events.MutationEventTopic{
		StreamName: consumerStream, TenantID: e.tenantID, ServiceName: "inventory",
		SchemaName: "item", EntityID: m.entity, OperationName: m.operation,
	}.String()

	_, err = e.js.Publish(context.Background(), subject, data)
	require.NoError(e.t, err)
}

// status waits for the routing status of m and returns it.
func (e *statusEnv) status(m mutation) services.RoutingStatus {
	e.t.Helper()

	var out services.RoutingStatus

	key := services.RoutingStatusKey(e.tenantID, m.txID, m.eventID)

	require.Eventually(e.t, func() bool {
		kv, err := e.js.KeyValue(context.Background(), services.RoutingStatusBucket(consumerStream))
		if err != nil {
			return false
		}

		entry, err := kv.Get(context.Background(), key)
		if err != nil {
			return false
		}

		return json.Unmarshal(entry.Value(), &out) == nil
	}, 5*time.Second, 20*time.Millisecond, "no routing status under %s", key)

	return out
}

// noStatusKeys asserts the bucket holds no status at all.
func (e *statusEnv) noStatusKeys() {
	e.t.Helper()

	kv, err := e.js.KeyValue(context.Background(), services.RoutingStatusBucket(consumerStream))
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return
	}

	require.NoError(e.t, err)

	keys, err := kv.Keys(context.Background())
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return
	}

	require.NoError(e.t, err)
	assert.Empty(e.t, keys)
}

func kinds(targets []services.RoutingTarget) []services.RoutingTargetKind {
	out := make([]services.RoutingTargetKind, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Kind)
	}

	return out
}

func TestRoutingStatus_RecordsEveryTarget(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-a", sigInter, "SignalA", "")

	m := newMutation()
	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome)
	assert.Equal(t, e.tenantID, got.TenantID)
	assert.Equal(t, m.txID, got.TransactionID)
	assert.Equal(t, m.eventID, got.EventID)
	assert.Positive(t, got.Sequence, "the stream sequence of the event")
	assert.ElementsMatch(t, []services.RoutingTargetKind{services.RoutingTargetStarted, services.RoutingTargetSignalled}, kinds(got.Targets))

	for _, target := range got.Targets {
		switch target.Kind { //nolint:exhaustive // only these two here
		case services.RoutingTargetStarted:
			assert.Equal(t, "wf_fanout_"+m.entity.String(), target.WorkflowID)
			assert.Equal(t, "run-1", target.RunID)
		case services.RoutingTargetSignalled:
			assert.Equal(t, "wf-running", target.WorkflowID)
			assert.Equal(t, "run-running", target.RunID)
			assert.Equal(t, "SignalA", target.Signal)
		}
	}
}

func TestRoutingStatus_NothingSubscribedStillRecorded(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)

	m := newMutation()
	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome)
	assert.Empty(t, got.Targets, "finished, nothing to do: different from not routed yet")
}

func TestRoutingStatus_SignalWithoutRunningExecutionIsDropped(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.temporal.ListWorkflowFunc = nil // no running executions
	e.subscribe("worker-a", sigInter, "SignalA", "")

	m := newMutation()
	e.publishMutation(m)

	got := e.status(m)

	require.Len(t, got.Targets, 1)
	assert.Equal(t, services.RoutingTargetDropped, got.Targets[0].Kind)
	assert.Equal(t, "SignalA", got.Targets[0].Signal)
	assert.NotEmpty(t, got.Targets[0].Reason)
}

func TestRoutingStatus_PermanentFailureIsDoneWithDroppedTarget(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribe("worker-a", sigInter, "SignalA", `status = `) // does not parse: permanent
	e.subscribe("worker-b", sigInter, "SignalB", "")

	m := newMutation()
	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome, "a permanent failure is settled, not retried")
	assert.ElementsMatch(t, []services.RoutingTargetKind{services.RoutingTargetSignalled, services.RoutingTargetDropped}, kinds(got.Targets))

	for _, target := range got.Targets {
		if target.Kind == services.RoutingTargetDropped {
			assert.Contains(t, target.Reason, "filter rule", "the reason says what was wrong")
		}
	}
}

func TestRoutingStatus_FailureBeforeAnyTargetIsNotReadAsNothingToDo(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribe("worker-a", sigStart, "", "")

	m := newMutation()
	m.operation = "frobnicate" // the router knows create, update and delete

	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome, "permanent: settled")
	require.Len(t, got.Targets, 1)
	assert.Equal(t, services.RoutingTargetDropped, got.Targets[0].Kind)
	assert.Contains(t, got.Targets[0].Reason, "frobnicate")
	assert.Empty(t, e.temporal.starts)
}

func TestRoutingStatus_GiveUpListsFailedTargets(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, func(f *fanoutEnv) workflow.ClientFactory {
		return failingFactory{signalFailingTemporal{f.temporal}}
	})
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-a", sigInter, "SignalA", "")

	m := newMutation()
	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeGaveUp, got.Outcome)
	assert.ElementsMatch(t, []services.RoutingTargetKind{services.RoutingTargetStarted, services.RoutingTargetFailed}, kinds(got.Targets),
		"the start succeeded on every delivery, the signal never did")

	for _, target := range got.Targets {
		if target.Kind == services.RoutingTargetFailed {
			assert.Equal(t, "SignalA", target.Signal)
			assert.Contains(t, target.Error, errSignalDown.Error())
		}
	}
}

func TestRoutingStatus_NotRecordedWithoutIDs(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribe("worker-a", sigStart, "", "")

	noTx := newMutation()
	noTx.txID = uuid.Nil
	noEvent := newMutation()
	noEvent.eventID = uuid.Nil
	last := newMutation()

	e.publishMutation(noTx)
	e.publishMutation(noEvent)
	e.publishMutation(last) // proves the two above were settled first, in order

	e.status(last)
	require.True(t, e.nats.settled())

	kv, err := e.js.KeyValue(context.Background(), services.RoutingStatusBucket(consumerStream))
	require.NoError(t, err)

	keys, err := kv.Keys(context.Background())
	require.NoError(t, err)
	assert.Len(t, keys, 1, "only the event with a transaction ID and an event ID is recorded")
	assert.Len(t, e.temporal.starts, 3, "all three were routed regardless")
}

func TestRoutingStatus_StateChangesAreNotRecorded(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribeStateChange("worker-a", sigStart, "")

	change := events.TemporalWorkflowStateChangeMessage{
		Namespace: e.tenantID.String(), TaskQueue: fanoutTaskQueue,
		WorkflowID: "other_1", WorkflowTypeName: "Other", RunID: "run-1", Status: "COMPLETED",
	}
	data, err := json.Marshal(change)
	require.NoError(t, err)

	_, err = e.js.Publish(context.Background(), events.TemporalWorkflowStateChangeTopic{
		StreamName: consumerStream, Namespace: change.Namespace, TaskQueue: change.TaskQueue,
		WorkflowTypeName: "Other", WorkflowID: "other_1", RunID: "run-1", Status: "COMPLETED",
	}.String(), data)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(e.temporal.callsOf("start")) == 1 }, 5*time.Second, 20*time.Millisecond)
	require.True(t, e.nats.settled())

	e.noStatusKeys()
}

func TestRoutingStatus_WriteFailureStillAcknowledges(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribe("worker-a", sigStart, "", "")

	first := newMutation()
	e.publishMutation(first)
	e.status(first) // the bucket exists and works

	require.NoError(t, e.js.DeleteKeyValue(context.Background(), services.RoutingStatusBucket(consumerStream)))

	second := newMutation()
	e.publishMutation(second)

	require.Eventually(t, func() bool { return len(e.temporal.callsOf("start")) == 2 }, 5*time.Second, 20*time.Millisecond)
	require.True(t, e.nats.settled(), "the event is acknowledged although its status could not be written")

	c, err := e.js.Consumer(context.Background(), consumerStream, services.DefaultConsumerDurable)
	require.NoError(t, err)

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	assert.Len(t, e.temporal.callsOf("start"), 2, "not redelivered: handled exactly once each")
	assert.Zero(t, info.NumAckPending)
	assert.Zero(t, info.NumPending)

	// The failed write must not leave a dead bucket handle behind: the next
	// event's write looks the bucket up again (creating it if it is gone).
	third := newMutation()
	e.publishMutation(third)

	got := e.status(third)
	assert.Equal(t, third.eventID, got.EventID, "the status of a later event is written again after the bucket was lost")
}

// A start the server refuses because the workflow ID is running is recorded as
// already_running, for every operation's policy.
func TestRoutingStatus_RefusedStartIsAlreadyRunning(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.temporal.startErr = serviceerror.NewWorkflowExecutionAlreadyStarted("running", "req", "run-a")
	e.subscribe("worker-a", sigStart, "", "")

	m := newMutation()
	m.operation = "update"
	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome)
	require.Len(t, got.Targets, 1)
	assert.Equal(t, services.RoutingTargetAlreadyRunning, got.Targets[0].Kind)
}

// The start policies of update and delete ask the SDK to report a refused
// start as an error, so the router can tell it from a real start.
func TestStartPolicies_ReportRefusedStarts(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]*workflow.StartWorkflowOptions{
		"create": services.DefaultCreatePolicy, "update": services.DefaultUpdatePolicy, "delete": services.DefaultDeletePolicy,
	} {
		assert.True(t, p.WorkflowExecutionErrorWhenAlreadyStarted, name)
		assert.NotEqual(t, enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, p.WorkflowIDConflictPolicy, "%s: USE_EXISTING hides the refusal", name)
	}
}

// An event that is NAKed leaves nothing behind: its collector is discarded, and
// the redelivery starts a fresh one. Nothing is swept by age, so a slow handler
// keeps its collector however long it runs.
func TestRoutingStatus_NakedEventDiscardsItsCollector(t *testing.T) {
	t.Parallel()

	cfg := fastConsumer()
	cfg.MaxDeliver = 10
	cfg.Backoff = []time.Duration{time.Hour} // a NAKed event does not come back during the test

	e := newStatusEnvWith(t, func(f *fanoutEnv) workflow.ClientFactory {
		return failingFactory{signalFailingTemporal{f.temporal}}
	}, cfg)
	e.subscribe("worker-a", sigInter, "SignalA", "")

	m := newMutation()
	e.publishMutation(m)

	require.Eventually(t, func() bool {
		c, err := e.js.Consumer(context.Background(), consumerStream, services.DefaultConsumerDurable)
		if err != nil {
			return false
		}

		info, err := c.Info(context.Background())

		return err == nil && info.Delivered.Stream == 1 && info.NumAckPending == 1
	}, 5*time.Second, 20*time.Millisecond, "the event was handled once and NAKed")

	require.Eventually(t, func() bool { return e.router.PendingRoutingCollectors() == 0 }, 2*time.Second, 20*time.Millisecond,
		"the NAKed event's collector is discarded")
	e.noStatusKeys()
}
