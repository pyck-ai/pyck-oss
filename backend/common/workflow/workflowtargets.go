package workflow

import (
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
)

// WorkflowTargetsUpdaterInput is the payload accepted by the
// SetWorkflowTargets update handler. Targets are passed as enum values so the
// JSON Schema (and downstream GraphQL) constrains valid surface names.
type WorkflowTargetsUpdaterInput struct {
	Targets []WorkflowTarget `json:"targets"`
}

var workflowTargetsUpdaterSchema = json_schema.MustReflect(WorkflowTargetsUpdaterInput{})

func (s WorkflowTargetsUpdaterInput) JSONSchema() *json_schema.Schema {
	return workflowTargetsUpdaterSchema
}
