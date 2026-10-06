package services_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/pyck-ai/pyck/backend/common/events"
)

// twoRunningExecutions makes a broadcast signal have two targets.
func (e *fanoutEnv) twoRunningExecutions() {
	e.temporal.ListWorkflowFunc = func(context.Context, *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		running := func(id, run string) *workflowpb.WorkflowExecutionInfo {
			return &workflowpb.WorkflowExecutionInfo{
				Execution: &commonpb.WorkflowExecution{WorkflowId: id, RunId: run},
				Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
				TaskQueue: fanoutTaskQueue,
			}
		}

		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{running("wf-a", "run-a"), running("wf-b", "run-b")},
		}, nil
	}
}

func TestRouter_RequestIDs_ReachTheClientCall(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-a", sigInter, "SignalA", "")

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())
	_, err := e.publishEvent("create", entityID, eventID)
	require.NoError(t, err)

	starts, signals := e.temporal.callsOf("start"), e.temporal.callsOf("signal")
	require.Len(t, starts, 1)
	require.Len(t, signals, 1)

	for _, c := range []recordedCall{starts[0], signals[0]} {
		assert.True(t, c.HasRequestID, "%s carries a request ID", c.Kind)
		_, err := uuid.Parse(c.RequestID)
		require.NoError(t, err, "the request ID is a UUID")
		assert.True(t, c.HasEventID, "%s carries the event ID", c.Kind)
		assert.Equal(t, eventID, c.EventID, "%s carries the event's own ID, not the entity ID", c.Kind)
	}

	assert.NotEqual(t, starts[0].RequestID, signals[0].RequestID, "a start and a signal of one event differ")
}

func TestRouter_RequestIDs_SameEventTwiceGivesSameIDs(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-a", sigInter, "SignalA", "")
	e.subscribe("worker-b", sigInter, "SignalB", "")

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())

	for range 2 {
		_, err := e.publishEvent("create", entityID, eventID)
		require.NoError(t, err)
	}

	starts := e.temporal.callsOf("start")
	require.Len(t, starts, 2)
	assert.Equal(t, starts[0].RequestID, starts[1].RequestID, "a redelivered start repeats its request ID")

	// Two signals per delivery, in either order: compare them by signal name.
	byName := map[string][]string{}
	for _, c := range e.temporal.callsOf("signal") {
		byName[c.Signal] = append(byName[c.Signal], c.RequestID)
	}

	require.Len(t, byName["SignalA"], 2)
	require.Len(t, byName["SignalB"], 2)
	assert.Equal(t, byName["SignalA"][0], byName["SignalA"][1])
	assert.Equal(t, byName["SignalB"][0], byName["SignalB"][1])
	assert.NotEqual(t, byName["SignalA"][0], byName["SignalB"][0], "two signal names are two calls")
}

func TestRouter_RequestIDs_DifferentEventsGiveDifferentIDs(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-a", sigInter, "SignalA", "")

	entityID := uuid.New() // the same entity, two events (an update then another)

	_, err := e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
	_, err = e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	starts, signals := e.temporal.callsOf("start"), e.temporal.callsOf("signal")
	require.Len(t, starts, 2)
	require.Len(t, signals, 2)
	assert.Equal(t, starts[0].WorkflowID, starts[1].WorkflowID, "both updates target the entity's workflow ID")
	assert.NotEqual(t, starts[0].RequestID, starts[1].RequestID)
	assert.NotEqual(t, signals[0].RequestID, signals[1].RequestID)
}

func TestRouter_RequestIDs_BroadcastTargetsGetOwnIDs(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.twoRunningExecutions()
	e.subscribe("worker-a", sigInter, "SignalA", "")

	_, err := e.publishEvent("update", uuid.New(), uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	signals := e.temporal.callsOf("signal")
	require.Len(t, signals, 2)
	assert.NotEqual(t, signals[0].WorkflowID, signals[1].WorkflowID)
	assert.NotEqual(t, signals[0].RequestID, signals[1].RequestID, "each target execution gets its own request ID")
	assert.Equal(t, signals[0].EventID, signals[1].EventID, "both carry the same event ID")
}

func TestRouter_RequestIDs_TargetIncludesRunID(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.temporal.ListWorkflowFunc = func(context.Context, *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		running := func(run string) *workflowpb.WorkflowExecutionInfo {
			return &workflowpb.WorkflowExecutionInfo{
				Execution: &commonpb.WorkflowExecution{WorkflowId: "wf-same", RunId: run},
				Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
				TaskQueue: fanoutTaskQueue,
			}
		}

		return &workflowservice.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{running("run-1"), running("run-2")}}, nil
	}
	e.subscribe("worker-a", sigInter, "SignalA", "")

	_, err := e.publishEvent("update", uuid.New(), uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	signals := e.temporal.callsOf("signal")
	require.Len(t, signals, 2)
	assert.NotEqual(t, signals[0].RequestID, signals[1].RequestID, "two runs of one workflow ID are two targets")
}

func TestRouter_RequestIDs_NoEventIDMeansNoIDs(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribe("worker-a", sigStart, "", "")
	e.subscribe("worker-a", sigInter, "SignalA", "")

	// A publisher that predates event_id: the event decodes with a nil EventID.
	_, err := e.publish()
	require.NoError(t, err)

	calls := append(e.temporal.callsOf("start"), e.temporal.callsOf("signal")...)
	require.Len(t, calls, 2)

	for _, c := range calls {
		assert.False(t, c.HasRequestID, "%s: no ID is invented", c.Kind)
		assert.False(t, c.HasEventID, "%s: no header without an event ID", c.Kind)
	}
}

func TestRouter_RequestIDs_StateChange(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChange("worker-a", sigStart, "")
	e.subscribeStateChange("worker-a", sigInter, "SignalA")

	change := events.TemporalWorkflowStateChangeMessage{WorkflowID: "other_1", RunID: "run-1", Status: "COMPLETED"}

	require.NoError(t, e.publishStateChange(change))
	require.NoError(t, e.publishStateChange(change), "a redelivery")

	other := change
	other.Status = "FAILED"
	require.NoError(t, e.publishStateChange(other))

	starts, signals := e.temporal.callsOf("start"), e.temporal.callsOf("signal")
	require.Len(t, starts, 3)
	require.Len(t, signals, 3)

	for _, c := range append(append([]recordedCall{}, starts...), signals...) {
		assert.True(t, c.HasRequestID, "%s carries a request ID", c.Kind)
		assert.True(t, c.HasEventID, "%s carries an event ID", c.Kind)
	}

	assert.Equal(t, starts[0].RequestID, starts[1].RequestID, "a redelivered state change repeats its IDs")
	assert.Equal(t, signals[0].RequestID, signals[1].RequestID)
	assert.Equal(t, starts[0].EventID, starts[1].EventID)
	assert.NotEqual(t, starts[0].RequestID, starts[2].RequestID, "another status is another state change")
	assert.NotEqual(t, starts[0].EventID, starts[2].EventID)
}
