package services_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"

	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
)

// subscribeStateChangeFiltered registers a live state-change subscription with a FEEL filter.
func (e *fanoutEnv) subscribeStateChangeFiltered(typ entworkflowsignal.TemporalSignalType, signal, filter string) {
	e.t.Helper()

	e.db.WorkflowSignal.Create().
		SetTenantID(e.tenantID).
		SetWorkflowID(e.wf.ID).
		SetNatsTopic(events.TemporalWorkflowStateChangeTopic{StreamName: "pyck"}.String()).
		SetTemporalSignalType(typ).
		SetTemporalSignal(signal).
		SetFilterRule(filter).
		SetWorkerID("worker-a").
		SetExpiresAt(time.Now().UTC().Add(time.Hour)).
		SaveX(e.ctx)
}

func stateChange(status string) events.TemporalWorkflowStateChangeMessage {
	return events.TemporalWorkflowStateChangeMessage{
		WorkflowID:       "other_1",
		WorkflowTypeName: "Other",
		RunID:            "run-1",
		Status:           status,
	}
}

func TestRouter_StateChange_FilterSeesTheEvent(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChangeFiltered(sigStart, "", `status = "COMPLETED"`)

	require.NoError(t, e.publishStateChange(stateChange("RUNNING")))
	assert.Empty(t, e.temporal.starts, "a RUNNING event must not match a COMPLETED filter")

	require.NoError(t, e.publishStateChange(stateChange("COMPLETED")))
	assert.Len(t, e.temporal.starts, 1, "a COMPLETED event must match")
}

func TestRouter_StateChange_FilterSeesEveryEventField(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChangeFiltered(sigInter, "SignalA",
		`workflow_type_name = "Other" and workflow_id = "other_1" and run_id = "run-1" and task_queue = "`+fanoutTaskQueue+`" and namespace = "`+e.tenantID.String()+`"`)

	require.NoError(t, e.publishStateChange(stateChange("COMPLETED")))

	assert.Equal(t, []string{"SignalA"}, e.temporal.signals)
}

func TestRouter_StateChange_StartInputIsTheMessage(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChange("worker-a", sigStart, "")

	require.NoError(t, e.publishStateChange(stateChange("COMPLETED")))

	require.Len(t, e.temporal.startInputs, 1)
	assert.Equal(t, wantStateChangeJSON(e, "COMPLETED"), toJSON(t, e.temporal.startInputs[0]))
}

func TestRouter_StateChange_SignalPayloadIsTheMessage(t *testing.T) {
	t.Parallel()

	e := newFanoutEnv(t)
	e.subscribeStateChange("worker-a", sigInter, "SignalA")

	require.NoError(t, e.publishStateChange(stateChange("FAILED")))

	require.Len(t, e.temporal.signalPayloads, 1)
	assert.Equal(t, wantStateChangeJSON(e, "FAILED"), toJSON(t, e.temporal.signalPayloads[0]))
}

// wantStateChangeJSON is the message publishStateChange puts on the wire.
func wantStateChangeJSON(e *fanoutEnv, status string) string {
	msg := stateChange(status)
	msg.Namespace = e.tenantID.String()
	msg.TaskQueue = fanoutTaskQueue

	data, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}

	return string(data)
}

func toJSON(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)

	return string(data)
}
