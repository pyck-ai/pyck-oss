package workflowsdk

import (
	"encoding/json"
	"fmt"
	"reflect"

	"go.temporal.io/sdk/workflow"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

type WorkflowUpdateJSONSchemaer interface {
	JSONSchema() *json_schema.Schema
}

type WorkflowUpdateValuer[T any] interface {
	json.Marshaler
	json.Unmarshaler

	Value() T
	SetValue(value T)
	UnsetValue()
	HasValue() bool
}

type WorkflowUpdater[T, R any] interface {
	WorkflowUpdateValuer[T]

	Type() *common_workflow.WorkflowUpdateType
	Validate(ctx workflow.Context, ref R, value T) error
	Update(ctx workflow.Context, input *common_workflow.UserDataInput, ref R, value T) (any, error)
	Await(ctx workflow.Context, input *common_workflow.UserDataInput, ref R) error
}

type WorkflowUpdate[T, R any] struct {
	value      T
	valueIsSet bool
}

func (u *WorkflowUpdate[T, R]) Value() T {
	return u.value
}

func (u *WorkflowUpdate[T, R]) SetValue(value T) {
	u.value = value
	u.valueIsSet = true
}

func (u *WorkflowUpdate[T, R]) UnsetValue() {
	var zero T
	u.value = zero
	u.valueIsSet = false
}

func (u *WorkflowUpdate[T, R]) HasValue() bool {
	return u.valueIsSet
}

func (u *WorkflowUpdate[T, R]) MarshalJSON() ([]byte, error) {
	return json.Marshal(u.value)
}

func (u *WorkflowUpdate[T, R]) UnmarshalJSON(data []byte) error {
	var v T

	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	u.SetValue(v)

	return nil
}

func (WorkflowUpdate[T, R]) DefaultType(u WorkflowUpdater[T, R]) *common_workflow.WorkflowUpdateType {
	var (
		t   T
		typ common_workflow.WorkflowUpdateType
	)

	typ.ID = reflect.TypeOf(u).Elem().Name()

	switch v := any(t).(type) {
	case WorkflowUpdateJSONSchemaer:
		typ.Schema = v.JSONSchema()
	default:
		typ.Schema = nil
	}

	return &typ
}

func (WorkflowUpdate[T, R]) DefaultAwait(ctx workflow.Context, u WorkflowUpdater[T, R], input *common_workflow.UserDataInput, ref R) error {
	u.UnsetValue()

	// Rejections are logged here, where every update passes, because a
	// validator rejection never reaches the workflow history.
	updateType := u.Type().ID

	if err := workflow.SetUpdateHandlerWithOptions(ctx, updateType,
		func(ctx workflow.Context, value T) (any, error) {
			result, err := u.Update(ctx, input, ref, value)
			if err != nil {
				logUpdateRejection(ctx, "update rejected", updateType, value, err)
			}

			return result, err
		},
		workflow.UpdateHandlerOptions{
			Validator: func(ctx workflow.Context, value T) error {
				if err := u.Validate(ctx, ref, value); err != nil {
					logUpdateRejection(ctx, "update rejected by validator", updateType, value, err)

					return err
				}

				return nil
			},
		},
	); err != nil {
		return err
	}

	input.Update(&common_workflow.UserDataInput{
		Type: u.Type(),
		Data: ref,
	})

	if err := workflow.Await(ctx, u.HasValue); err != nil {
		return err
	}

	return nil
}

// logUpdateRejection logs a rejected update with the client's update ID, so
// the line can be matched with the caller's request.
func logUpdateRejection(ctx workflow.Context, msg, updateType string, value any, err error) {
	var updateID string
	if info := workflow.GetCurrentUpdateInfo(ctx); info != nil {
		updateID = info.ID
	}

	workflow.GetLogger(ctx).Warn(msg,
		"update", updateType,
		"updateID", updateID,
		"input", value,
		"error", err)
}

func (WorkflowUpdate[T, R]) DefaultValidate(ctx workflow.Context, u WorkflowUpdater[T, R], _ R, value T) error {
	if u.HasValue() {
		return ErrAlreadySet
	}

	if schema := u.Type().Schema; schema != nil {
		valueJSON, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value for validation: %w", err)
		}

		var valueMap map[string]any
		if err := json.Unmarshal(valueJSON, &valueMap); err != nil {
			return fmt.Errorf("failed to unmarshal value to map: %w", err)
		}

		return schema.Validate(valueMap)
	}

	return nil
}

func (WorkflowUpdate[T, R]) DefaultUpdate(ctx workflow.Context, u WorkflowUpdater[T, R], input *common_workflow.UserDataInput, value T) (any, error) {
	u.SetValue(value)
	input.Update(nil)

	return u.Value(), nil
}
