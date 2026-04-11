package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"

	"github.com/pyck-ai/pyck/backend/common/log"
)

var ErrIndexOverflow = errors.New("index overflow: value exceeds int64 max")

// StartWorkflow starts a new workflow execution with the given parameters.
//
// Deprecated: Use StartWorkflowWithOptions() instead.
func (c *Client) StartWorkflow(ctx context.Context, workflowName, taskQueue string, input interface{}, searchAttributes map[string]interface{}) (string, string, error) {
	log.ForContext(ctx).Warn().
		Msg("Client.StartWorkflow() is deprecated, use Client.StartWorkflowWithOptions() instead")

	var (
		typedSearchAttributes []temporal.SearchAttributeUpdate
		workflowIDSuffix      = uuid.New().String()
	)

	for key, value := range searchAttributes {
		valueStr := fmt.Sprintf("%v", value)

		switch key {
		case PyckDataIDKey:
			workflowIDSuffix = valueStr
			typedSearchAttributes = append(typedSearchAttributes, PyckDataID.ValueSet(valueStr))
		case PyckDataTypeKey:
			typedSearchAttributes = append(typedSearchAttributes, PyckDataType.ValueSet(valueStr))
		case PyckTenantIDKey:
			typedSearchAttributes = append(typedSearchAttributes, PyckTenantID.ValueSet(valueStr))
		case PyckWorkflowNameKey:
			typedSearchAttributes = append(typedSearchAttributes, PyckWorkflowName.ValueSet(valueStr))
		case PyckWorkflowAssigneeKey:
			typedSearchAttributes = append(typedSearchAttributes, PyckWorkflowAssignee.ValueSet(valueStr))
		case PyckServiceKey:
			typedSearchAttributes = append(typedSearchAttributes, PyckService.ValueSet(valueStr))
		default:
			log.ForContext(ctx).Warn().
				Str("key", key).
				Msg("unknown search attribute key, skipping")
			continue
		}
	}

	options := &temporalclient.StartWorkflowOptions{
		ID:                    workflowName + "_" + workflowIDSuffix,
		TaskQueue:             taskQueue,
		TypedSearchAttributes: temporal.NewSearchAttributes(typedSearchAttributes...),
	}

	we, err := c.StartWorkflowWithOptions(ctx, workflowName, input, options)
	if err != nil {
		log.ForContext(ctx).Info().
			Str("workflowName", workflowName).
			Err(err).
			Msg("Unable to start workflow")
		return "", "", err
	}

	return we.GetID(), we.GetRunID(), nil
}

// GetNextPendingActivityProperties queries the workflow for the next pending activity.
//
// Deprecated: Use GetCurrentUserDataInput() instead.
func (c *Client) GetNextPendingActivityProperties(ctx context.Context, workflowID, runID string) (*NextActivityProperties, error) {
	log.ForContext(ctx).Warn().
		Msg("Client.GetNextPendingActivityProperties() is deprecated, use Client.GetCurrentUserDataInput() instead")

	userDataInput, err := c.GetCurrentUserDataInput(ctx, workflowID, runID)
	if err != nil {
		return nil, err
	}

	dataStr, err := json.Marshal(userDataInput.Data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user data input data: %w", err)
	}

	var dataMap map[string]string
	if err := json.Unmarshal(dataStr, &dataMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user data input data to map: %w", err)
	}

	activityProperties := make([]*ActivityProperties, 0, len(dataMap))
	for key, value := range dataMap {
		activityProperties = append(activityProperties, &ActivityProperties{
			FormKey: key,
			Value:   value,
		})
	}

	return &NextActivityProperties{
		NextActivityName:       &userDataInput.Type.ID,
		NextActivityProperties: activityProperties,
	}, nil
}

// GetWorkflowInfo retrieves basic workflow execution information.
//
// Deprecated: Use GetWorkflowExecutionInfo() instead.
func (c *Client) GetWorkflowInfo(ctx context.Context, workflowID, runID string) (string, string, map[string]string, error) {
	log.ForContext(ctx).Warn().
		Msg("Client.GetWorkflowInfo() is deprecated, use Client.GetWorkflowExecutionInfo() instead")

	executionInfo, err := c.GetWorkflowExecutionInfo(ctx, workflowID, runID)
	if err != nil {
		return "", "", nil, err
	}

	searchAttributes := make(map[string]string, len(executionInfo.GetSearchAttributes().GetIndexedFields()))
	dataConverter := converter.GetDefaultDataConverter()

	for key, value := range executionInfo.GetSearchAttributes().GetIndexedFields() {
		searchAttributes[key] = dataConverter.ToString(value)
	}

	return executionInfo.GetStatus().String(), executionInfo.GetType().GetName(), searchAttributes, nil
}

// GetActivityIndex queries the workflow for its current activity index.
//
// Deprecated: Use GetCurrentUserDataInput() instead.
func (c *Client) GetActivityIndex(ctx context.Context, workflowID, runID string) (int64, error) {
	log.ForContext(ctx).Warn().
		Msg("Client.GetActivityIndex() is deprecated, use Client.GetCurrentUserDataInput() instead")

	resp, err := c.GetCurrentUserDataInput(ctx, workflowID, runID)
	if err != nil {
		return 0, err
	}

	if resp.ActivityIndex > uint64(int64(^uint64(0)>>1)) {
		return 0, ErrIndexOverflow
	}
	return int64(resp.ActivityIndex), nil
}
