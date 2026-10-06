package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/events"
)

func stateChange() *events.TemporalWorkflowStateChangeMessage {
	return &events.TemporalWorkflowStateChangeMessage{
		Namespace:        "default",
		TaskQueue:        "queue",
		WorkflowID:       "wf-1",
		WorkflowTypeName: "Type",
		RunID:            "run-1",
		Status:           "Running",
	}
}

// TestStateChangeEventID pins the derived ID. The signal router's event IDs
// and start IDs come from it, so a change here re-keys redelivered events.
func TestStateChangeEventID(t *testing.T) {
	t.Parallel()

	// Computed with the router's former stateChangeEventID.
	const want = "04bf0fd8-1273-5826-bcc8-f1616c5a2adc"

	assert.Equal(t, want, stateChange().EventID().String())
}

func TestStateChangeEventIDDeterministic(t *testing.T) {
	t.Parallel()

	base := stateChange()
	assert.Equal(t, base.EventID(), stateChange().EventID())

	// Fields outside the identity do not change the ID.
	other := stateChange()
	other.TaskQueue = "other"
	other.WorkflowTypeName = "Other"
	assert.Equal(t, base.EventID(), other.EventID())

	mutators := map[string]func(*events.TemporalWorkflowStateChangeMessage){
		"namespace":   func(m *events.TemporalWorkflowStateChangeMessage) { m.Namespace = "x" },
		"workflow id": func(m *events.TemporalWorkflowStateChangeMessage) { m.WorkflowID = "x" },
		"run id":      func(m *events.TemporalWorkflowStateChangeMessage) { m.RunID = "x" },
		"status":      func(m *events.TemporalWorkflowStateChangeMessage) { m.Status = "x" },
	}
	for name, mutate := range mutators {
		m := stateChange()
		mutate(m)
		assert.NotEqual(t, base.EventID(), m.EventID(), name)
	}

	// NUL separation: moving a character across a field boundary changes the ID.
	a := &events.TemporalWorkflowStateChangeMessage{Namespace: "ab", WorkflowID: "c"}
	b := &events.TemporalWorkflowStateChangeMessage{Namespace: "a", WorkflowID: "bc"}
	assert.NotEqual(t, a.EventID(), b.EventID())
}
