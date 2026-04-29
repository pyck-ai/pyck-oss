package workflow

import (
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
)

type WorkflowIsAssignableUpdaterInput struct {
	IsAssignable bool `json:"is_assignable"`
}

var workflowIsAssignableUpdaterSchema = json_schema.MustReflect(WorkflowIsAssignableUpdaterInput{})

func (s WorkflowIsAssignableUpdaterInput) JSONSchema() *json_schema.Schema {
	return workflowIsAssignableUpdaterSchema
}
