package services_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/eventid"
	"github.com/pyck-ai/pyck/backend/common/events"
	commontemporal "github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/model"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

const (
	fanoutTaskQueue = "fanout-queue"

	sigStart = entworkflowsignal.TemporalSignalTypeStart
	sigInter = entworkflowsignal.TemporalSignalTypeIntermediate
)

// recordingTemporal records the starts and signals the router issues.
type recordingTemporal struct {
	*mocks.SimpleMockTemporalClient

	mu      sync.Mutex
	starts  []string
	signals []string

	// startInputs and signalPayloads record what was delivered, in call order.
	startInputs    []any
	signalPayloads []any
	startErr       error
	signalErr      error

	// swsErr fails SignalWithStartWorkflow. byIDMissing makes a signal with no
	// run ID (a signal by workflow ID) find no execution.
	swsErr      error
	byIDMissing bool
	swsOptions  []temporalclient.StartWorkflowOptions

	// describe answers DescribeWorkflowExecution, which the router calls before
	// a redelivered Signal-With-Start. Nil means the workflow is not found.
	// describes records the workflow IDs asked about.
	describe  func(workflowID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	describes []string

	// calls records every start and signal with the IDs that reached the
	// client call's context, in call order.
	calls []recordedCall
}

// recordedCall is one start or signal as the Temporal client saw it.
type recordedCall struct {
	Kind         string // "start" or "signal"
	WorkflowID   string
	RunID        string
	Signal       string
	RequestID    string
	HasRequestID bool
	EventID      uuid.UUID
	HasEventID   bool
}

func recordCall(ctx context.Context, call recordedCall) recordedCall {
	call.RequestID, call.HasRequestID = commontemporal.RequestIDFromContext(ctx)
	call.EventID, call.HasEventID = eventid.FromContext(ctx)

	return call
}

// callsOf returns the recorded calls of one kind.
func (r *recordingTemporal) callsOf(kind string) []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []recordedCall

	for _, c := range r.calls {
		if c.Kind == kind {
			out = append(out, c)
		}
	}

	return out
}

func newRecordingTemporal() *recordingTemporal {
	r := &recordingTemporal{SimpleMockTemporalClient: mocks.NewSimpleMockTemporalClient()}
	// One running execution, so broadcast signals have a target.
	r.ListWorkflowFunc = func(context.Context, *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{{
				Execution: &commonpb.WorkflowExecution{WorkflowId: "wf-running", RunId: "run-running"},
				Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
				TaskQueue: fanoutTaskQueue,
			}},
		}, nil
	}

	return r
}

func (r *recordingTemporal) ExecuteWorkflow(ctx context.Context, opts temporalclient.StartWorkflowOptions, _ any, args ...any) (temporalclient.WorkflowRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, recordCall(ctx, recordedCall{Kind: "start", WorkflowID: opts.ID}))
	r.starts = append(r.starts, opts.ID)
	r.startInputs = append(r.startInputs, args...)

	if r.startErr != nil {
		return nil, r.startErr
	}

	return mocks.NewMockWorkflowRun(opts.ID, "run-1", nil, nil), nil
}

func (r *recordingTemporal) SignalWithStartWorkflow(ctx context.Context, workflowID, signalName string, signalArg any, options temporalclient.StartWorkflowOptions, _ any, workflowArgs ...any) (temporalclient.WorkflowRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, recordCall(ctx, recordedCall{Kind: "signal-with-start", WorkflowID: workflowID, Signal: signalName}))
	r.swsOptions = append(r.swsOptions, options)
	r.startInputs = append(r.startInputs, workflowArgs...)
	r.signalPayloads = append(r.signalPayloads, signalArg)

	if r.swsErr != nil {
		return nil, r.swsErr
	}

	return mocks.NewMockWorkflowRun(workflowID, "run-sws", nil, nil), nil
}

func (r *recordingTemporal) DescribeWorkflowExecution(_ context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.describes = append(r.describes, workflowID+"/"+runID)

	if r.describe == nil {
		return nil, serviceerror.NewNotFound("workflow execution not found")
	}

	return r.describe(workflowID)
}

func (r *recordingTemporal) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if runID == "" {
		r.calls = append(r.calls, recordCall(ctx, recordedCall{Kind: "signal-by-id", WorkflowID: workflowID, Signal: signalName}))

		if r.byIDMissing {
			return serviceerror.NewNotFound("workflow execution not found")
		}

		r.signalPayloads = append(r.signalPayloads, arg)

		return r.signalErr
	}

	r.calls = append(r.calls, recordCall(ctx, recordedCall{Kind: "signal", WorkflowID: workflowID, RunID: runID, Signal: signalName}))
	r.signals = append(r.signals, signalName)
	r.signalPayloads = append(r.signalPayloads, arg)

	return r.signalErr
}

type recordingFactory struct{ temporal *recordingTemporal }

func (f recordingFactory) GetClient(context.Context, string) (*workflow.Client, error) {
	return workflow.NewClient("test", f.temporal)
}

func (recordingFactory) Close() {}

type fanoutEnv struct {
	t        *testing.T
	ctx      context.Context //nolint:containedctx // test helper
	tenantID uuid.UUID
	db       *ent.Client
	temporal *recordingTemporal
	router   *services.SignalRouter
	wf       *ent.Workflow

	// delivery is the JetStream delivery count the next publishes run under; 0
	// is a direct call, like a first delivery.
	delivery uint64
}

func newFanoutEnv(t *testing.T) *fanoutEnv {
	t.Helper()

	tenantID := uuid.New()
	db := newExpiryTestClient(t)
	ctx := seedCtx(tenantID)
	temporal := newRecordingTemporal()

	return &fanoutEnv{
		t:        t,
		ctx:      ctx,
		tenantID: tenantID,
		db:       db,
		temporal: temporal,
		router:   services.NewSignalRouter(db, services.SignalRouterConfig{ClientFactory: recordingFactory{temporal}}),
		wf:       db.Workflow.Create().SetTenantID(tenantID).SetName("wf_fanout").SetTaskQueue(fanoutTaskQueue).SaveX(ctx),
	}
}

// trackRouting makes the next publishes collect their routing targets, which
// services.CollectedTargets reads from the returned context.
func (e *fanoutEnv) trackRouting() context.Context {
	e.ctx = services.TrackRouting(e.ctx)

	return e.ctx
}

// subscribe registers a live subscription matching every mutation event.
func (e *fanoutEnv) subscribe(worker string, typ entworkflowsignal.TemporalSignalType, signal, filter string) {
	e.t.Helper()

	e.db.WorkflowSignal.Create().
		SetTenantID(e.tenantID).
		SetWorkflowID(e.wf.ID).
		SetNatsTopic("*.*.*.*.*.*.*").
		SetTemporalSignalType(typ).
		SetTemporalSignal(signal).
		SetFilterRule(filter).
		SetWorkerID(worker).
		SetExpiresAt(time.Now().UTC().Add(time.Hour)).
		SaveX(e.ctx)
}

// publish routes one mutation event and returns the entity ID. The event has
// no event ID, like one from a publisher that predates event_id.
func (e *fanoutEnv) publish() (uuid.UUID, error) {
	e.t.Helper()

	id := uuid.New()
	_, err := e.publishEvent("create", id, uuid.Nil)

	return id, err
}

// publishEvent routes one mutation event for an entity with an event ID.
func (e *fanoutEnv) publishEvent(operation string, entityID, eventID uuid.UUID) ([]*model.TemporalWorkflow, error) {
	e.t.Helper()

	return e.publishEventWithAttrs(operation, entityID, eventID, nil)
}

// publishEventWithAttrs is publishEvent with search attributes on the event.
func (e *fanoutEnv) publishEventWithAttrs(operation string, entityID, eventID uuid.UUID, attrs map[string]string) ([]*model.TemporalWorkflow, error) {
	e.t.Helper()

	data, err := json.Marshal(events.MutationEventMessage{
		Service:   "inventory",
		Type:      "inventoryitem",
		Schema:    "item",
		Operation: operation,
		ID:        entityID,
		EventID:   eventID,
		TenantID:  e.tenantID,
		DataAfter: map[string]any{"status": "active"},

		WfSearchAttributes: attrs,
	})
	require.NoError(e.t, err)

	ctx := e.ctx
	if e.delivery > 0 {
		ctx = services.WithDelivery(ctx, e.delivery)
	}

	return e.router.HandleMutationEvent(ctx, &nats.Msg{
		Subject: events.MutationEventTopic{
			StreamName: "pyck", TenantID: e.tenantID, ServiceName: "inventory",
			SchemaName: "item", EntityID: entityID, OperationName: operation,
		}.String(),
		Data: data,
	})
}

func TestRouter_FanOut_DistinctSignalNamesAllSignalled(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigInter, "SignalA", "")
	e.subscribe("worker-b", sigInter, "SignalB", "")

	_, err := e.publish()
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"SignalA", "SignalB"}, e.temporal.signals)
	assert.Empty(t, e.temporal.starts)
}

func TestRouter_FanOut_SameSignalNameSignalledOnce(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigInter, "SignalA", "")
	e.subscribe("worker-b", sigInter, "SignalA", "")

	_, err := e.publish()
	require.NoError(t, err)

	assert.Equal(t, []string{"SignalA"}, e.temporal.signals)
}

func TestRouter_FanOut_FilterFalseDoesNotShadowLaterMatch(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigInter, "SignalA", `status = "inactive"`)
	e.subscribe("worker-b", sigInter, "SignalA", `status = "active"`)

	_, err := e.publish()
	require.NoError(t, err)

	assert.Equal(t, []string{"SignalA"}, e.temporal.signals)
}

func TestRouter_FanOut_FilterFalseForAllDeliversNothing(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigInter, "SignalA", `status = "inactive"`)
	e.subscribe("worker-b", sigStart, "", `status = "inactive"`)

	_, err := e.publish()
	require.NoError(t, err)

	assert.Empty(t, e.temporal.signals)
	assert.Empty(t, e.temporal.starts)
}

func TestRouter_FanOut_StartAndSignalBothDelivered(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-a", sigInter, "SignalA", "")

	id, err := e.publish()
	require.NoError(t, err)

	assert.Equal(t, []string{"wf_fanout_" + id.String()}, e.temporal.starts)
	assert.Equal(t, []string{"SignalA"}, e.temporal.signals)
}

func TestRouter_FanOut_TwoStartsStartOnce(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-b", sigStart, "", "")

	id, err := e.publish()
	require.NoError(t, err)

	assert.Equal(t, []string{"wf_fanout_" + id.String()}, e.temporal.starts)
}

func TestRouter_FanOut_FilterErrorDoesNotLoseOtherDeliveries(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigInter, "SignalA", `status = `) // does not parse
	e.subscribe("worker-b", sigInter, "SignalB", "")

	_, err := e.publish()
	require.Error(t, err, "the broken filter is still reported")

	assert.Equal(t, []string{"SignalB"}, e.temporal.signals)
}

// subscribeStateChange registers a live subscription on every Temporal state change.
func (e *fanoutEnv) subscribeStateChange(worker string, typ entworkflowsignal.TemporalSignalType, signal string) {
	e.t.Helper()

	e.db.WorkflowSignal.Create().
		SetTenantID(e.tenantID).
		SetWorkflowID(e.wf.ID).
		SetNatsTopic(events.TemporalWorkflowStateChangeTopic{StreamName: "pyck"}.String()).
		SetTemporalSignalType(typ).
		SetTemporalSignal(signal).
		SetWorkerID(worker).
		SetExpiresAt(time.Now().UTC().Add(time.Hour)).
		SaveX(e.ctx)
}

// publishStateChange routes one state-change event.
func (e *fanoutEnv) publishStateChange(msg events.TemporalWorkflowStateChangeMessage) error {
	e.t.Helper()

	msg.Namespace = e.tenantID.String()
	msg.TaskQueue = fanoutTaskQueue

	data, err := json.Marshal(msg)
	require.NoError(e.t, err)

	_, err = e.router.HandleTemporalWorkflowStateChange(e.ctx, &nats.Msg{
		Subject: events.TemporalWorkflowStateChangeTopic{
			StreamName: "pyck", Namespace: msg.Namespace, TaskQueue: msg.TaskQueue,
			WorkflowTypeName: "Other", WorkflowID: msg.WorkflowID, RunID: msg.RunID, Status: msg.Status,
		}.String(),
		Data: data,
	})

	return err
}

func TestRouter_FanOut_StateChangeDeliversEveryMatch(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChange("worker-a", sigStart, "")
	e.subscribeStateChange("worker-b", sigStart, "")
	e.subscribeStateChange("worker-a", sigInter, "SignalA")
	e.subscribeStateChange("worker-b", sigInter, "SignalB")
	e.subscribeStateChange("worker-c", sigInter, "SignalB")

	require.NoError(t, e.publishStateChange(events.TemporalWorkflowStateChangeMessage{
		WorkflowID: "other_1", RunID: "run-1", Status: "COMPLETED",
	}))

	assert.Len(t, e.temporal.starts, 1, "two start subscriptions start the workflow once")
	assert.ElementsMatch(t, []string{"SignalA", "SignalB"}, e.temporal.signals)
}

func TestRouter_StateChangeStartIDIsStable(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChange("worker-a", sigStart, "")

	change := events.TemporalWorkflowStateChangeMessage{WorkflowID: "other_1", RunID: "run-1", Status: "COMPLETED"}

	require.NoError(t, e.publishStateChange(change))
	require.NoError(t, e.publishStateChange(change), "a redelivered event")

	other := change
	other.Status = "FAILED"
	require.NoError(t, e.publishStateChange(other))

	require.Len(t, e.temporal.starts, 3)
	assert.Equal(t, e.temporal.starts[0], e.temporal.starts[1], "the same state change must start under the same ID")
	assert.NotEqual(t, e.temporal.starts[0], e.temporal.starts[2], "a different status is a different state change")
	assert.Contains(t, e.temporal.starts[0], "wf_fanout_")
}
