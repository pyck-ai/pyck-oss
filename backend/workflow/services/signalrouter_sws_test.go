package services_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

const (
	sigSWS  = entworkflowsignal.TemporalSignalTypeSignalWithStart
	sigByID = entworkflowsignal.TemporalSignalTypeSignalByID
)

func TestRouter_SWS_SignalsAndStartsTheEventsWorkflow(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigSWS, "Updated", "")

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())
	started, err := e.publishEvent("update", entityID, eventID)
	require.NoError(t, err)
	assert.Empty(t, started, "the SDK does not say whether a signal-with-start started the run")

	calls := e.temporal.callsOf("signal-with-start")
	require.Len(t, calls, 1)
	assert.Equal(t, "wf_fanout_"+entityID.String(), calls[0].WorkflowID)
	assert.Equal(t, "Updated", calls[0].Signal)
	assert.Empty(t, e.temporal.callsOf("start"), "no separate start")
	assert.Empty(t, e.temporal.callsOf("signal"), "no broadcast")

	require.Len(t, e.temporal.swsOptions, 1)
	opts := e.temporal.swsOptions[0]
	assert.Equal(t, enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, opts.WorkflowIDConflictPolicy, "Temporal rejects FAIL for signal-with-start")
	assert.Equal(t, fanoutTaskQueue, opts.TaskQueue)
	assert.Equal(t, "wf_fanout_"+entityID.String(), opts.ID)
	assert.True(t, calls[0].HasEventID)
	assert.Equal(t, eventID, calls[0].EventID)

	// The event is the start input and the signal payload.
	require.Len(t, e.temporal.startInputs, 1)
	require.Len(t, e.temporal.signalPayloads, 1)
	assert.Equal(t, toJSON(t, e.temporal.startInputs[0]), toJSON(t, e.temporal.signalPayloads[0]))
}

func TestRouter_SWS_ReusePolicyFollowsTheOperation(t *testing.T) {
	t.Parallel()

	want := map[string]enums.WorkflowIdReusePolicy{
		"create": enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		"update": enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		"delete": enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}

	for op, policy := range want {
		e := newFanoutEnv(t)
		e.subscribe("worker-a", sigSWS, "X", "")

		_, err := e.publishEvent(op, uuid.New(), uuid.Must(uuid.NewV7()))
		require.NoError(t, err, op)
		require.Len(t, e.temporal.swsOptions, 1, op)
		assert.Equal(t, policy, e.temporal.swsOptions[0].WorkflowIDReusePolicy, op)
		assert.Equal(t, enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, e.temporal.swsOptions[0].WorkflowIDConflictPolicy, op)
	}
}

func TestRouter_SWS_CarriesTheTransactionID(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigSWS, "Updated", "")

	_, err := e.publishEventWithAttrs("update", uuid.New(), uuid.Must(uuid.NewV7()), map[string]string{"pyck_transaction_id": "tx-1"})
	require.NoError(t, err)

	require.Len(t, e.temporal.swsOptions, 1)

	got := map[string]any{}
	for k, v := range e.temporal.swsOptions[0].TypedSearchAttributes.GetUntypedValues() {
		got[k.GetName()] = v
	}

	assert.Equal(t, "tx-1", got["pyck_transaction_id"])
	assert.Equal(t, e.wf.Name, got["pyck_workflow_name"])
}

func TestRouter_SWS_StartAndSWSMatchStartOnceAndSignalOnce(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-b", sigSWS, "Updated", "")
	e.subscribe("worker-c", sigSWS, "Updated", "")

	_, err := e.publishEvent("create", uuid.New(), uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	assert.Len(t, e.temporal.callsOf("signal-with-start"), 1, "one SWS per distinct name")
	assert.Empty(t, e.temporal.callsOf("start"), "the SWS carries the start")
}

func TestRouter_SWS_DistinctNamesAreDistinctCalls(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigSWS, "A", "")
	e.subscribe("worker-a", sigSWS, "B", "")

	_, err := e.publishEvent("create", uuid.New(), uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	calls := e.temporal.callsOf("signal-with-start")
	require.Len(t, calls, 2)
	assert.NotEqual(t, calls[0].RequestID, calls[1].RequestID)
	assert.ElementsMatch(t, []string{"A", "B"}, []string{calls[0].Signal, calls[1].Signal})
}

func TestRouter_SWS_BroadcastOfTheSameNameSkipsTheEventsWorkflow(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.twoRunningExecutions() // wf-a and wf-b
	e.subscribe("worker-a", sigSWS, "Updated", "")
	e.subscribe("worker-b", sigInter, "Updated", "")

	entityID := uuid.New()
	_, err := e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	assert.Len(t, e.temporal.callsOf("signal-with-start"), 1)
	assert.Len(t, e.temporal.callsOf("signal"), 2, "the broadcast still reaches the other executions")
}

func TestRouter_SWS_RequestIDsAreStableAndOfTheirOwnKind(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigSWS, "Updated", "")
	e.subscribe("worker-a", sigByID, "Other", "")
	e.subscribe("worker-a", sigInter, "Updated", "")

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())

	for range 2 {
		_, err := e.publishEvent("update", entityID, eventID)
		require.NoError(t, err)
	}

	sws, byID, bc := e.temporal.callsOf("signal-with-start"), e.temporal.callsOf("signal-by-id"), e.temporal.callsOf("signal")
	require.Len(t, sws, 2)
	require.Len(t, byID, 2)
	require.Len(t, bc, 2)

	for _, pair := range [][]recordedCall{sws, byID, bc} {
		assert.True(t, pair[0].HasRequestID)
		assert.Equal(t, pair[0].RequestID, pair[1].RequestID, "a redelivery repeats the request ID")
	}

	assert.NotEqual(t, sws[0].RequestID, byID[0].RequestID)
	assert.NotEqual(t, sws[0].RequestID, bc[0].RequestID)
	assert.NotEqual(t, byID[0].RequestID, bc[0].RequestID)
}

func TestRouter_ByID_SignalsTheEventsWorkflowAndNeverStarts(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigByID, "Updated", "")
	e.subscribe("worker-b", sigByID, "Updated", "")

	entityID := uuid.New()
	started, err := e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
	assert.Empty(t, started)

	calls := e.temporal.callsOf("signal-by-id")
	require.Len(t, calls, 1, "one signal per distinct name")
	assert.Equal(t, "wf_fanout_"+entityID.String(), calls[0].WorkflowID)
	assert.Empty(t, e.temporal.starts)
	assert.Empty(t, e.temporal.callsOf("signal-with-start"))
	assert.Empty(t, e.temporal.callsOf("signal"))
}

func TestRouter_ByID_NoExecutionIsADropNotAnError(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.temporal.byIDMissing = true
	e.subscribe("worker-a", sigByID, "Updated", "")

	_, err := e.publishEvent("update", uuid.New(), uuid.Must(uuid.NewV7()))
	require.NoError(t, err, "a policy drop is acknowledged")
	assert.Empty(t, e.temporal.starts)
}

func TestRouter_ByID_TransientFailureIsAnError(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.temporal.signalErr = serviceerror.NewUnavailable("down")
	e.subscribe("worker-a", sigByID, "Updated", "")

	_, err := e.publishEvent("update", uuid.New(), uuid.Must(uuid.NewV7()))
	require.Error(t, err)
	assert.False(t, services.IsPermanent(err))
}

func TestRouter_SWS_ReuseRefusalIsADropNotAnError(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.temporal.swsErr = serviceerror.NewWorkflowExecutionAlreadyStarted("finished", "req", "run")
	e.subscribe("worker-a", sigSWS, "Updated", "")

	_, err := e.publishEvent("delete", uuid.New(), uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
}

func TestRouter_SWS_TransientFailureIsAnError(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.temporal.swsErr = serviceerror.NewUnavailable("down")
	e.subscribe("worker-a", sigSWS, "Updated", "")

	_, err := e.publishEvent("update", uuid.New(), uuid.Must(uuid.NewV7()))
	require.Error(t, err)
	assert.False(t, services.IsPermanent(err))
}

func TestRoutingStatus_SWSAndByIDTargets(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.subscribe("worker-a", sigSWS, "Updated", "")
	e.subscribe("worker-a", sigByID, "Other", "")

	m := newMutation()
	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome)
	require.Len(t, got.Targets, 2)
	assert.ElementsMatch(t, []services.RoutingTargetKind{services.RoutingTargetSignalled, services.RoutingTargetSignalled}, kinds(got.Targets))

	for _, target := range got.Targets {
		assert.Equal(t, "wf_fanout_"+m.entity.String(), target.WorkflowID)
	}
}

func TestRoutingStatus_ByIDWithNoExecutionIsDroppedWithAReason(t *testing.T) {
	t.Parallel()

	e := newStatusEnv(t, nil)
	e.temporal.byIDMissing = true
	e.subscribe("worker-a", sigByID, "Updated", "")

	m := newMutation()
	e.publishMutation(m)

	got := e.status(m)

	assert.Equal(t, services.RoutingOutcomeDone, got.Outcome)
	require.Len(t, got.Targets, 1)
	assert.Equal(t, services.RoutingTargetDropped, got.Targets[0].Kind)
	assert.Equal(t, "wf_fanout_"+m.entity.String(), got.Targets[0].WorkflowID)
	assert.Equal(t, "Updated", got.Targets[0].Signal)
	assert.NotEmpty(t, got.Targets[0].Reason)
}

// describeRun makes the fake Temporal answer a describe of the event's workflow
// with a run of the given status, started by the event whose ID memoEventID
// holds ("" for a run with no memo).
func (e *fanoutEnv) describeRun(status enums.WorkflowExecutionStatus, memoEventID string) {
	e.t.Helper()

	e.temporal.describe = func(string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		info := &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{RunId: "run-described"},
			Status:    status,
		}

		if memoEventID != "" {
			payload, err := converter.GetDefaultDataConverter().ToPayload(memoEventID)
			if err != nil {
				return nil, err
			}

			info.Memo = &commonpb.Memo{Fields: map[string]*commonpb.Payload{commonworkflow.MemoEventID: payload}}
		}

		return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info}, nil
	}
}

func TestRouter_SWS_RecordsTheEventInTheRunsMemo(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigSWS, "Updated", "")

	eventID := uuid.Must(uuid.NewV7())
	_, err := e.publishEvent("update", uuid.New(), eventID)
	require.NoError(t, err)

	require.Len(t, e.temporal.swsOptions, 1)
	assert.Equal(t, map[string]any{commonworkflow.MemoEventID: eventID.String()}, e.temporal.swsOptions[0].Memo)
	assert.Equal(t, "pyck_event_id", commonworkflow.MemoEventID, "the key is part of the workflow's contract")
}

func TestRouter_SWS_FirstDeliveryDoesNotDescribe(t *testing.T) {
	t.Parallel()

	for _, delivery := range []uint64{0, 1} {
		e := newFanoutEnv(t)
		e.delivery = delivery
		e.subscribe("worker-a", sigSWS, "Updated", "")

		_, err := e.publishEvent("update", uuid.New(), uuid.Must(uuid.NewV7()))
		require.NoError(t, err)

		assert.Empty(t, e.temporal.describes, "delivery %d", delivery)
		assert.Len(t, e.temporal.callsOf("signal-with-start"), 1, "delivery %d", delivery)
	}
}

// TestRouter_SWS_RedeliveryOfAFinishedRunsEventIsDelivered: the event started
// the run, which has closed since. Temporal would apply the reuse policy to a
// repeated call (a second run under update, a drop under create and delete), so
// the router sends nothing and records the target as delivered.
func TestRouter_SWS_RedeliveryOfAFinishedRunsEventIsDelivered(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"create", "update", "delete"} {
		for _, status := range []enums.WorkflowExecutionStatus{
			enums.WORKFLOW_EXECUTION_STATUS_COMPLETED,
			enums.WORKFLOW_EXECUTION_STATUS_FAILED,
			enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		} {
			e := newFanoutEnv(t)
			e.delivery = 2
			e.subscribe("worker-a", sigSWS, "Updated", "")

			entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())
			e.describeRun(status, eventID.String())

			ctx := e.trackRouting()

			_, err := e.publishEvent(op, entityID, eventID)
			require.NoError(t, err, "%s %s", op, status)

			assert.Equal(t, []string{"wf_fanout_" + entityID.String() + "/"}, e.temporal.describes, "%s %s: the latest run is described", op, status)
			assert.Empty(t, e.temporal.callsOf("signal-with-start"), "%s %s: nothing is sent", op, status)

			targets := services.CollectedTargets(ctx)
			require.Len(t, targets, 1, "%s %s", op, status)
			assert.Equal(t, services.RoutingTargetSignalled, targets[0].Kind, "%s %s", op, status)
			assert.Equal(t, "run-described", targets[0].RunID, "%s %s", op, status)
			assert.Equal(t, "Updated", targets[0].Signal, "%s %s", op, status)
		}
	}
}

// TestRouter_SWS_RedeliveryCallsWhenTheRunIsNotKnownToBeTheEventsOwn: in every
// case but "closed and started by this event" the call goes out as before.
func TestRouter_SWS_RedeliveryCallsWhenTheRunIsNotKnownToBeTheEventsOwn(t *testing.T) {
	t.Parallel()

	eventID := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7()).String()

	cases := map[string]func(e *fanoutEnv){
		"running, started by the event": func(e *fanoutEnv) { e.describeRun(enums.WORKFLOW_EXECUTION_STATUS_RUNNING, eventID.String()) },
		"closed, started by another event": func(e *fanoutEnv) {
			e.describeRun(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, other)
		},
		"closed, no memo": func(e *fanoutEnv) { e.describeRun(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, "") },
		"not found":       func(*fanoutEnv) {},
		"describe fails": func(e *fanoutEnv) {
			e.temporal.describe = func(string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
				return nil, serviceerror.NewUnavailable("describe is down")
			}
		},
	}

	for name, setup := range cases {
		e := newFanoutEnv(t)
		e.delivery = 3
		e.subscribe("worker-a", sigSWS, "Updated", "")
		setup(e)

		_, err := e.publishEvent("update", uuid.New(), eventID)
		require.NoError(t, err, "%s: the event does not fail", name)

		assert.Len(t, e.temporal.describes, 1, name)
		assert.Len(t, e.temporal.callsOf("signal-with-start"), 1, "%s: the call goes out", name)
	}
}
