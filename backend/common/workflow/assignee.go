package workflow

import (
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
)

type WorkflowAssignee *string

type WorkflowAssigneeUpdaterInput struct {
	Assignee WorkflowAssignee `json:"assignee"`
}

var workflowAssigneeUpdaterSchema = json_schema.MustReflect(WorkflowAssigneeUpdaterInput{})

func (s WorkflowAssigneeUpdaterInput) JSONSchema() *json_schema.Schema {
	return workflowAssigneeUpdaterSchema
}
