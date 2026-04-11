package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/sdk/converter"
)

// ErrEventIDOverflow is returned when a Temporal event ID exceeds the GraphQL Int range.
var ErrEventIDOverflow = errors.New("event ID exceeds GraphQL Int range")

// HistoryFromProto converts Temporal protobuf history events to GraphQL WorkflowEvent types.
func HistoryFromProto(hist []*historypb.HistoryEvent) ([]*WorkflowEvent, error) {
	if len(hist) == 0 {
		return []*WorkflowEvent{}, nil
	}

	events := make([]*WorkflowEvent, 0, len(hist))
	for _, event := range hist {
		b, err := json.Marshal(event.GetAttributes())
		if err != nil {
			return nil, fmt.Errorf("failed to marshal event attributes: %w", err)
		}

		var attribs map[string]any
		if err := json.Unmarshal(b, &attribs); err != nil {
			return nil, fmt.Errorf("failed to unmarshal event attributes: %w", err)
		}

		// Bounds check: GraphQL Int is 32-bit signed, Temporal EventId is int64
		eventID := event.GetEventId()
		if eventID > math.MaxInt32 || eventID < math.MinInt32 {
			return nil, fmt.Errorf("%w: %d", ErrEventIDOverflow, eventID)
		}

		events = append(events, &WorkflowEvent{
			EventID:   int(eventID),
			EventType: event.GetEventType().String(),
			EventTime: event.GetEventTime().AsTime().Format(time.RFC3339),
			Extra:     attribs,
		})
	}

	return events, nil
}

// FromProto converts a Temporal protobuf WorkflowExecutionInfo into the internal model.
//
// This method populates the WorkflowExecutionInfo struct with data from Temporal's
// protobuf representation, performing necessary type conversions and data extraction.
func (info *WorkflowExecutionInfo) FromProto(
	proto *workflowpb.WorkflowExecutionInfo,
	hist []*historypb.HistoryEvent,
) error {
	dataConverter := converter.GetDefaultDataConverter()

	executionInfo := WorkflowExecutionInfo{
		Execution: &WorkflowExecution{
			WorkflowID: proto.GetExecution().GetWorkflowId(),
			ID:         proto.GetExecution().GetRunId(),
		},
		Type: &WorkflowType{
			Name: proto.GetType().GetName(),
		},
		StartTime:            proto.GetStartTime().AsTime().Format(time.RFC3339),
		Status:               proto.GetStatus().String(),
		HistoryLength:        int(proto.GetHistoryLength()),
		TaskQueue:            proto.GetTaskQueue(),
		StateTransitionCount: int(proto.GetStateTransitionCount()),
		HistorySizeBytes:     int(proto.GetHistorySizeBytes()),
		SearchAttributes:     make([]*TemporalMetadata, 0),
	}

	// Add close time if the workflow has completed
	if proto.GetCloseTime() != nil {
		closeTime := proto.GetCloseTime().AsTime().Format(time.RFC3339)
		executionInfo.CloseTime = &closeTime
	}

	// Add execution time if available
	if proto.GetExecutionTime() != nil {
		executionTime := proto.GetExecutionTime().AsTime().Format(time.RFC3339)
		executionInfo.ExecutionTime = &executionTime
	}

	// Add execution duration if available
	if proto.GetExecutionDuration() != nil {
		executionDuration := proto.GetExecutionDuration().AsDuration().String()
		executionInfo.ExecutionDuration = &executionDuration
	}

	// Add parent namespace ID if available
	if parentNsID := proto.GetParentNamespaceId(); parentNsID != "" {
		executionInfo.ParentNamespaceID = &parentNsID
	}

	// Add parent execution if available
	if proto.GetParentExecution() != nil {
		executionInfo.ParentExecution = &WorkflowExecution{
			WorkflowID: proto.GetParentExecution().GetWorkflowId(),
			ID:         proto.GetParentExecution().GetRunId(),
		}
	}

	// Add root execution if available
	if proto.GetRootExecution() != nil {
		executionInfo.RootExecution = &WorkflowExecution{
			WorkflowID: proto.GetRootExecution().GetWorkflowId(),
			ID:         proto.GetRootExecution().GetRunId(),
		}
	}

	// Extract search attributes as metadata
	if proto.GetSearchAttributes() != nil {
		for key, payload := range proto.GetSearchAttributes().GetIndexedFields() {
			var value interface{}
			if err := dataConverter.FromPayload(payload, &value); err != nil {
				// If deserialization fails, skip this attribute
				continue
			}
			executionInfo.SearchAttributes = append(executionInfo.SearchAttributes, &TemporalMetadata{
				Key:   key,
				Value: fmt.Sprintf("%v", value),
			})
		}
	}

	// Add memo if available
	if proto.GetMemo() != nil && len(proto.GetMemo().GetFields()) > 0 {
		var memo WorkflowMemo
		for key, payload := range proto.GetMemo().GetFields() {
			var value interface{}
			if err := dataConverter.FromPayload(payload, &value); err != nil {
				// If deserialization fails, skip this field
				continue
			}

			switch strings.ToLower(key) {
			case "title":
				if v, ok := value.(string); ok && v != "" {
					memo.Title = &v
				}
			case "subtitle":
				if v, ok := value.(string); ok && v != "" {
					memo.Subtitle = &v
				}
			case "data":
				if v, ok := value.(map[string]interface{}); ok && len(v) > 0 {
					memo.Data = v
				}
			}
		}

		executionInfo.Memo = &memo
	}

	// Add auto reset points if available
	if proto.GetAutoResetPoints() != nil && len(proto.GetAutoResetPoints().GetPoints()) > 0 {
		autoResetPoints := make(map[string]interface{})
		for i, point := range proto.GetAutoResetPoints().GetPoints() {
			autoResetPoints[fmt.Sprintf("point_%d", i)] = map[string]interface{}{
				"runId":               point.GetRunId(),
				"firstWorkflowTaskId": point.GetFirstWorkflowTaskCompletedId(),
				"createTime":          point.GetCreateTime().AsTime().Format(time.RFC3339),
				"expireTime":          point.GetExpireTime().AsTime().Format(time.RFC3339),
				"resettable":          point.GetResettable(),
			}
		}
		executionInfo.AutoResetPoints = autoResetPoints
	}

	*info = executionInfo

	return nil
}
