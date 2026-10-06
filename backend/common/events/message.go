package events

import (
	"strings"

	"github.com/google/uuid"
)

type CustomEventMessage struct {
	Type      string
	Operation string
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Data      any
	DataID    uuid.UUID
}

type WorkflowEventMessage struct {
	TenantID           uuid.UUID
	WorkflowID         uuid.UUID
	WorkflowName       string
	TaskQueue          string
	WfSearchAttributes map[string]string
}

type UpdateEventMessage struct {
	Service   string
	Type      string
	Schema    string
	Operation string
	ID        uuid.UUID
	TenantID  uuid.UUID
	Attribute string
	Data      any
}

type UpdateAttributeDetails struct {
	OldValue any `json:"old_value"`
	NewValue any `json:"new_value"`
}

// stateChangeNamespace is the UUIDv5 namespace of state-change event IDs. It
// is fixed forever: changing it would give a redelivered event a new ID.
var stateChangeNamespace = uuid.MustParse("d0005538-6693-4943-ad45-f4b684716d13") //nolint:gochecknoglobals

// TemporalWorkflowStateChangeMessage announces that a workflow run entered a
// status.
//
// It is published with a JetStream message ID derived from EventID, so the
// copies that several Temporal pods publish for one change collapse into one
// stored message within the stream's duplicate window. The ID is a hash of
// public fields and so predictable: a client allowed to publish to the stream
// could suppress a real state change within that window by sending a message
// with the ID the real one would get. That risk is accepted until each tenant
// has its own stream (#1656). Beyond the window, or when a publish is retried,
// a change can still be stored twice; the signal router absorbs that, because
// its Temporal request IDs are derived from the same (namespace, workflow ID,
// run ID, status) fields.
type TemporalWorkflowStateChangeMessage struct {
	Namespace        string `json:"namespace"`
	TaskQueue        string `json:"task_queue"`
	WorkflowID       string `json:"workflow_id"`
	WorkflowTypeName string `json:"workflow_type_name"`
	RunID            string `json:"run_id"`
	Status           string `json:"status"`
}

// EventID is the event ID of a state change. Unlike a mutation event, a state
// change has no outbox row to take an ID from, so it is derived: the same
// (namespace, workflow ID, run ID, status) always yields the same ID, whichever
// replica handles it and however often it is redelivered. Fields are
// NUL-separated so that no two different changes share an input.
//
// It is a UUIDv5 because the event ID must be a UUID (the pyck-event-id header
// and workflowsdk carry a uuid.UUID). The publisher derives the JetStream
// message ID from it, and the signal router uses it as the event ID and as the
// suffix of the workflow ID a state change starts.
func (m *TemporalWorkflowStateChangeMessage) EventID() uuid.UUID {
	return uuid.NewSHA1(stateChangeNamespace, []byte(strings.Join(
		[]string{m.Namespace, m.WorkflowID, m.RunID, m.Status}, "\x00",
	)))
}
