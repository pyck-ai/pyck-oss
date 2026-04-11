package workflow

import json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"

type WorkflowDetails struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	RunID string `json:"runID"`
}

type WorkflowUpdateType struct {
	ID     string              `json:"id"`
	Schema *json_schema.Schema `json:"schema,omitempty"`
}

type UserDataInput struct {
	ActivityIndex uint64              `json:"activityIndex"`
	ActivityCount uint64              `json:"activityCount,omitempty"`
	Type          *WorkflowUpdateType `json:"type,omitempty"`
	Data          any                 `json:"data,omitempty"`
	Errors        []string            `json:"errors,omitempty"`
}

func (u *UserDataInput) Update(input *UserDataInput) {
	if input == nil {
		u.Type = nil
		u.Data = nil
		u.Errors = nil

		return
	}

	u.Type = input.Type
	u.Data = input.Data
	u.Errors = input.Errors

	u.ActivityIndex++ // Increment index to indicate a change
}
