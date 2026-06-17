package events

import (
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

type TemporalWorkflowStateChangeMessage struct {
	Namespace        string `json:"namespace"`
	TaskQueue        string `json:"task_queue"`
	WorkflowID       string `json:"workflow_id"`
	WorkflowTypeName string `json:"workflow_type_name"`
	RunID            string `json:"run_id"`
	Status           string `json:"status"`
}
