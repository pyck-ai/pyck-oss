// nolint:staticcheck
package mocks

import (
	"context"
	"io"
	"reflect"
	"sync"

	"github.com/pyck-ai/pyck/backend/common/workflow"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/operatorservice/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

type MockTemporalClient struct {
	mock.Mock
}

// ExecuteWorkflow mocks the ExecuteWorkflow method.
func (m *MockTemporalClient) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error) {
	callArgs := m.Called(ctx, options, workflow, args)
	return callArgs.Get(0).(client.WorkflowRun), callArgs.Error(1)
}

// GetWorkflow mocks the GetWorkflow method.
func (m *MockTemporalClient) GetWorkflow(ctx context.Context, workflowID string, runID string) client.WorkflowRun {
	callArgs := m.Called(ctx, workflowID, runID)
	return callArgs.Get(0).(client.WorkflowRun)
}

// SignalWorkflow mocks the SignalWorkflow method.
func (m *MockTemporalClient) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error {
	args := m.Called(ctx, workflowID, runID, signalName, arg)
	return args.Error(0)
}

// SignalWithStartWorkflow mocks the SignalWithStartWorkflow method.
func (m *MockTemporalClient) SignalWithStartWorkflow(ctx context.Context, workflowID, signalName string, signalArg interface{}, options client.StartWorkflowOptions, workflow interface{}, workflowArgs ...interface{}) (client.WorkflowRun, error) {
	args := m.Called(ctx, workflowID, signalName, signalArg, options, workflow, workflowArgs)
	return args.Get(0).(client.WorkflowRun), args.Error(1)
}

// NewWithStartWorkflowOperation implements client.Client.
func (m *MockTemporalClient) NewWithStartWorkflowOperation(options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) client.WithStartWorkflowOperation {
	mArgs := m.Called(options, workflow, args)
	return mArgs.Get(0).(client.WithStartWorkflowOperation)
}

// CancelWorkflow mocks the CancelWorkflow method.
func (m *MockTemporalClient) CancelWorkflow(ctx context.Context, workflowID, runID string) error {
	args := m.Called(ctx, workflowID, runID)
	return args.Error(0)
}

// TerminateWorkflow mocks the TerminateWorkflow method.
func (m *MockTemporalClient) TerminateWorkflow(ctx context.Context, workflowID, runID, reason string, details ...interface{}) error {
	args := m.Called(ctx, workflowID, runID, reason, details)
	return args.Error(0)
}

// GetWorkflowHistory mocks the GetWorkflowHistory method.
func (m *MockTemporalClient) GetWorkflowHistory(ctx context.Context, workflowID, runID string, isLongPoll bool, filterType enums.HistoryEventFilterType) client.HistoryEventIterator {
	args := m.Called(ctx, workflowID, runID, isLongPoll, filterType)
	return args.Get(0).(client.HistoryEventIterator)
}

// CompleteActivity mocks the CompleteActivity method.
func (m *MockTemporalClient) CompleteActivity(ctx context.Context, taskToken []byte, result interface{}, err error) error {
	args := m.Called(ctx, taskToken, result, err)
	return args.Error(0)
}

// CompleteActivityWithOptions mocks the CompleteActivityWithOptions method.
func (m *MockTemporalClient) CompleteActivityWithOptions(ctx context.Context, opts client.CompleteActivityOptions) error {
	args := m.Called(ctx, opts)
	return args.Error(0)
}

// CompleteActivityByID mocks the CompleteActivityByID method.
func (m *MockTemporalClient) CompleteActivityByID(ctx context.Context, namespace, workflowID, runID, activityID string, result interface{}, err error) error {
	args := m.Called(ctx, namespace, workflowID, runID, activityID, result, err)
	return args.Error(0)
}

// CompleteActivityByIDWithOptions mocks the CompleteActivityByIDWithOptions method.
func (m *MockTemporalClient) CompleteActivityByIDWithOptions(ctx context.Context, opts client.CompleteActivityByIDOptions) error {
	args := m.Called(ctx, opts)
	return args.Error(0)
}

// CompleteActivityByActivityID mocks the CompleteActivityByActivityID method.
func (m *MockTemporalClient) CompleteActivityByActivityID(ctx context.Context, namespace, activityID, activityRunID string, result interface{}, err error) error {
	args := m.Called(ctx, namespace, activityID, activityRunID, result, err)
	return args.Error(0)
}

// CompleteActivityByActivityIDWithOptions mocks the CompleteActivityByActivityIDWithOptions method.
func (m *MockTemporalClient) CompleteActivityByActivityIDWithOptions(ctx context.Context, opts client.CompleteActivityByActivityIDOptions) error {
	args := m.Called(ctx, opts)
	return args.Error(0)
}

// RecordActivityHeartbeat mocks the RecordActivityHeartbeat method.
func (m *MockTemporalClient) RecordActivityHeartbeat(ctx context.Context, taskToken []byte, details ...interface{}) error {
	args := m.Called(ctx, taskToken, details)
	return args.Error(0)
}

// RecordActivityHeartbeatWithOptions mocks the RecordActivityHeartbeatWithOptions method.
func (m *MockTemporalClient) RecordActivityHeartbeatWithOptions(ctx context.Context, opts client.RecordActivityHeartbeatOptions) error {
	args := m.Called(ctx, opts)
	return args.Error(0)
}

// RecordActivityHeartbeatByID mocks the RecordActivityHeartbeatByID method.
func (m *MockTemporalClient) RecordActivityHeartbeatByID(ctx context.Context, namespace, workflowID, runID, activityID string, details ...interface{}) error {
	args := m.Called(ctx, namespace, workflowID, runID, activityID, details)
	return args.Error(0)
}

// RecordActivityHeartbeatByIDWithOptions mocks the RecordActivityHeartbeatByIDWithOptions method.
func (m *MockTemporalClient) RecordActivityHeartbeatByIDWithOptions(ctx context.Context, opts client.RecordActivityHeartbeatByIDOptions) error {
	args := m.Called(ctx, opts)
	return args.Error(0)
}

// ExecuteActivity mocks the ExecuteActivity method.
func (m *MockTemporalClient) ExecuteActivity(ctx context.Context, options client.StartActivityOptions, activity any, activityArgs ...any) (client.ActivityHandle, error) {
	args := m.Called(ctx, options, activity, activityArgs)
	return args.Get(0).(client.ActivityHandle), args.Error(1)
}

// GetActivityHandle mocks the GetActivityHandle method.
func (m *MockTemporalClient) GetActivityHandle(options client.GetActivityHandleOptions) client.ActivityHandle {
	args := m.Called(options)
	return args.Get(0).(client.ActivityHandle)
}

// ListActivities mocks the ListActivities method.
func (m *MockTemporalClient) ListActivities(ctx context.Context, options client.ListActivitiesOptions) (client.ListActivitiesResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.ListActivitiesResult), args.Error(1)
}

// CountActivities mocks the CountActivities method.
func (m *MockTemporalClient) CountActivities(ctx context.Context, options client.CountActivitiesOptions) (*client.CountActivitiesResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(*client.CountActivitiesResult), args.Error(1)
}

// ListClosedWorkflow mocks the ListClosedWorkflow method.
func (m *MockTemporalClient) ListClosedWorkflow(ctx context.Context, request *workflowservice.ListClosedWorkflowExecutionsRequest) (*workflowservice.ListClosedWorkflowExecutionsResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*workflowservice.ListClosedWorkflowExecutionsResponse), args.Error(1)
}

// ListOpenWorkflow mocks the ListOpenWorkflow method.
func (m *MockTemporalClient) ListOpenWorkflow(ctx context.Context, request *workflowservice.ListOpenWorkflowExecutionsRequest) (*workflowservice.ListOpenWorkflowExecutionsResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*workflowservice.ListOpenWorkflowExecutionsResponse), args.Error(1)
}

// ListWorkflow mocks the ListWorkflow method.
func (m *MockTemporalClient) ListWorkflow(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*workflowservice.ListWorkflowExecutionsResponse), args.Error(1)
}

// ListArchivedWorkflow mocks the ListArchivedWorkflow method.
func (m *MockTemporalClient) ListArchivedWorkflow(ctx context.Context, request *workflowservice.ListArchivedWorkflowExecutionsRequest) (*workflowservice.ListArchivedWorkflowExecutionsResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*workflowservice.ListArchivedWorkflowExecutionsResponse), args.Error(1)
}

// ScanWorkflow mocks the ScanWorkflow method.
func (m *MockTemporalClient) ScanWorkflow(ctx context.Context, request *workflowservice.ScanWorkflowExecutionsRequest) (*workflowservice.ScanWorkflowExecutionsResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*workflowservice.ScanWorkflowExecutionsResponse), args.Error(1)
}

// CountWorkflow mocks the CountWorkflow method.
func (m *MockTemporalClient) CountWorkflow(ctx context.Context, request *workflowservice.CountWorkflowExecutionsRequest) (*workflowservice.CountWorkflowExecutionsResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*workflowservice.CountWorkflowExecutionsResponse), args.Error(1)
}

// GetSearchAttributes mocks the GetSearchAttributes method.
func (m *MockTemporalClient) GetSearchAttributes(ctx context.Context) (*workflowservice.GetSearchAttributesResponse, error) {
	args := m.Called(ctx)
	return args.Get(0).(*workflowservice.GetSearchAttributesResponse), args.Error(1)
}

// QueryWorkflow mocks the QueryWorkflow method.
func (m *MockTemporalClient) QueryWorkflow(ctx context.Context, workflowID, runID, queryType string, args ...interface{}) (converter.EncodedValue, error) {
	args2 := m.Called(ctx, workflowID, runID, queryType, args)
	return args2.Get(0).(converter.EncodedValue), args2.Error(1)
}

// QueryWorkflowWithOptions mocks the QueryWorkflowWithOptions method.
func (m *MockTemporalClient) QueryWorkflowWithOptions(ctx context.Context, request *client.QueryWorkflowWithOptionsRequest) (*client.QueryWorkflowWithOptionsResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*client.QueryWorkflowWithOptionsResponse), args.Error(1)
}

// DescribeWorkflowExecution mocks the DescribeWorkflowExecution method.
func (m *MockTemporalClient) DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	args := m.Called(ctx, workflowID, runID)
	return args.Get(0).(*workflowservice.DescribeWorkflowExecutionResponse), args.Error(1)
}

// DescribeWorkflow mocks the DescribeWorkflow method.
func (m *MockTemporalClient) DescribeWorkflow(ctx context.Context, workflowID, runID string) (*client.WorkflowExecutionDescription, error) {
	args := m.Called(ctx, workflowID, runID)
	return args.Get(0).(*client.WorkflowExecutionDescription), args.Error(1)
}

// DescribeTaskQueue mocks the DescribeTaskQueue method.
func (m *MockTemporalClient) DescribeTaskQueue(ctx context.Context, taskqueue string, taskqueueType enums.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
	args := m.Called(ctx, taskqueue, taskqueueType)
	return args.Get(0).(*workflowservice.DescribeTaskQueueResponse), args.Error(1)
}

// ResetWorkflowExecution mocks the ResetWorkflowExecution method.
func (m *MockTemporalClient) ResetWorkflowExecution(ctx context.Context, request *workflowservice.ResetWorkflowExecutionRequest) (*workflowservice.ResetWorkflowExecutionResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*workflowservice.ResetWorkflowExecutionResponse), args.Error(1)
}

// UpdateWorkerBuildIdCompatibility mocks the UpdateWorkerBuildIdCompatibility method.
func (m *MockTemporalClient) UpdateWorkerBuildIdCompatibility(ctx context.Context, options *client.UpdateWorkerBuildIdCompatibilityOptions) error {
	args := m.Called(ctx, options)
	return args.Error(0)
}

// GetWorkerBuildIdCompatibility mocks the GetWorkerBuildIdCompatibility method.
func (m *MockTemporalClient) GetWorkerBuildIdCompatibility(ctx context.Context, options *client.GetWorkerBuildIdCompatibilityOptions) (*client.WorkerBuildIDVersionSets, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(*client.WorkerBuildIDVersionSets), args.Error(1)
}

// GetWorkerTaskReachability mocks the GetWorkerTaskReachability method.
func (m *MockTemporalClient) GetWorkerTaskReachability(ctx context.Context, options *client.GetWorkerTaskReachabilityOptions) (*client.WorkerTaskReachability, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(*client.WorkerTaskReachability), args.Error(1)
}

// CheckHealth mocks the CheckHealth method.
func (m *MockTemporalClient) CheckHealth(ctx context.Context, request *client.CheckHealthRequest) (*client.CheckHealthResponse, error) {
	args := m.Called(ctx, request)
	return args.Get(0).(*client.CheckHealthResponse), args.Error(1)
}

// UpdateWorkflow mocks the UpdateWorkflow method.
func (m *MockTemporalClient) UpdateWorkflow(ctx context.Context, options client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.WorkflowUpdateHandle), args.Error(1)
}

// UpdateWithStartWorkflow implements client.Client.
func (m *MockTemporalClient) UpdateWithStartWorkflow(ctx context.Context, options client.UpdateWithStartWorkflowOptions) (client.WorkflowUpdateHandle, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.WorkflowUpdateHandle), args.Error(1)
}

// GetWorkflowUpdateHandle mocks the GetWorkflowUpdateHandle method.
func (m *MockTemporalClient) GetWorkflowUpdateHandle(ref client.GetWorkflowUpdateHandleOptions) client.WorkflowUpdateHandle {
	args := m.Called(ref)
	return args.Get(0).(client.WorkflowUpdateHandle)
}

// WorkflowService mocks the WorkflowService method.
func (m *MockTemporalClient) WorkflowService() workflowservice.WorkflowServiceClient {
	args := m.Called()
	return args.Get(0).(workflowservice.WorkflowServiceClient)
}

// OperatorService mocks the OperatorService method.
func (m *MockTemporalClient) OperatorService() operatorservice.OperatorServiceClient {
	args := m.Called()
	return args.Get(0).(operatorservice.OperatorServiceClient)
}

// UpdateWorkerVersioningRules mocks the UpdateWorkerVersioningRules method.
func (m *MockTemporalClient) UpdateWorkerVersioningRules(ctx context.Context, options client.UpdateWorkerVersioningRulesOptions) (*client.WorkerVersioningRules, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(*client.WorkerVersioningRules), args.Error(1)
}

// GetWorkerVersioningRules mocks the GetWorkerVersioningRules method.
func (m *MockTemporalClient) GetWorkerVersioningRules(ctx context.Context, options client.GetWorkerVersioningOptions) (*client.WorkerVersioningRules, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(*client.WorkerVersioningRules), args.Error(1)
}

// DescribeTaskQueueEnhanced mocks the DescribeTaskQueueEnhanced method.
func (m *MockTemporalClient) DescribeTaskQueueEnhanced(ctx context.Context, options client.DescribeTaskQueueEnhancedOptions) (client.TaskQueueDescription, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.TaskQueueDescription), args.Error(1)
}

// ScheduleClient mocks the ScheduleClient method.
func (m *MockTemporalClient) ScheduleClient() client.ScheduleClient {
	args := m.Called()
	return args.Get(0).(client.ScheduleClient)
}

// DeploymentClient mocks the DeploymentClient method.
func (m *MockTemporalClient) DeploymentClient() client.DeploymentClient {
	args := m.Called()
	return args.Get(0).(client.DeploymentClient)
}

// WorkerDeploymentClient mocks the WorkerDeploymentClient method.
func (m *MockTemporalClient) WorkerDeploymentClient() client.WorkerDeploymentClient {
	args := m.Called()
	return args.Get(0).(client.WorkerDeploymentClient)
}

// UpdateWorkflowExecutionOptions mocks the UpdateWorkflowExecutionOptions method.
func (m *MockTemporalClient) UpdateWorkflowExecutionOptions(ctx context.Context, options client.UpdateWorkflowExecutionOptionsRequest) (client.WorkflowExecutionOptions, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.WorkflowExecutionOptions), args.Error(1)
}

// Close mocks the Close method.
func (m *MockTemporalClient) Close() {
	m.Called()
}

// SimpleMockTemporalClient is a simple mock that doesn't use testify/mock complications.
// It's useful for testing UpdateWorkflow and DescribeWorkflowExecution with thread-safe state.
type SimpleMockTemporalClient struct {
	MockTemporalClient // embed for other methods
	mu                 sync.Mutex
	workflows          map[string]map[string]string

	// ListWorkflowFunc allows tests to customize ListWorkflow behavior
	ListWorkflowFunc func(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error)

	// QueryWorkflowFunc allows tests to customize QueryWorkflow behavior
	QueryWorkflowFunc func(ctx context.Context, workflowID, runID, queryType string, args ...interface{}) (converter.EncodedValue, error)

	// CancelWorkflowFunc allows tests to customize CancelWorkflow behavior
	CancelWorkflowFunc func(ctx context.Context, workflowID, runID string) error
}

// NewSimpleMockTemporalClient creates a new SimpleMockTemporalClient.
func NewSimpleMockTemporalClient() *SimpleMockTemporalClient {
	return &SimpleMockTemporalClient{
		workflows: make(map[string]map[string]string),
	}
}

// UpdateWorkflow implements a simple mock for UpdateWorkflow that stores state.
func (m *SimpleMockTemporalClient) UpdateWorkflow(ctx context.Context, opts client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error) {
	var assigneeID string

	// Handle both old (string) and new (WorkflowAssigneeUpdaterInput) formats
	if len(opts.Args) > 0 {
		switch arg := opts.Args[0].(type) {
		case string:
			assigneeID = arg
		case workflow.WorkflowAssigneeUpdaterInput:
			if arg.Assignee != nil {
				assigneeID = *arg.Assignee
			}
		}
	}

	// Store in state with mutex protection
	m.mu.Lock()
	if m.workflows[opts.WorkflowID] == nil {
		m.workflows[opts.WorkflowID] = make(map[string]string)
	}
	m.workflows[opts.WorkflowID]["pyck_workflow_assignee"] = assigneeID
	m.mu.Unlock()

	// Return handle with the assignee
	return &MockUpdateHandle{assigneeID: assigneeID}, nil
}

// ListWorkflow implements a customizable mock for ListWorkflow.
// If ListWorkflowFunc is set, it will be called. Otherwise returns empty result.
func (m *SimpleMockTemporalClient) ListWorkflow(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	if m.ListWorkflowFunc != nil {
		return m.ListWorkflowFunc(ctx, request)
	}
	// Default: return empty list
	return &workflowservice.ListWorkflowExecutionsResponse{
		Executions: []*workflowpb.WorkflowExecutionInfo{},
	}, nil
}

// QueryWorkflow implements a customizable mock for QueryWorkflow.
// If QueryWorkflowFunc is set, it will be called. Otherwise returns an empty encoded value.
func (m *SimpleMockTemporalClient) QueryWorkflow(ctx context.Context, workflowID, runID, queryType string, args ...interface{}) (converter.EncodedValue, error) {
	if m.QueryWorkflowFunc != nil {
		return m.QueryWorkflowFunc(ctx, workflowID, runID, queryType, args...)
	}
	// Default: return nil encoded value (will cause "no result" on Get)
	return nil, nil
}

// CancelWorkflow implements a customizable mock for CancelWorkflow.
// If CancelWorkflowFunc is set, it will be called. Otherwise returns nil (success).
func (m *SimpleMockTemporalClient) CancelWorkflow(ctx context.Context, workflowID, runID string) error {
	if m.CancelWorkflowFunc != nil {
		return m.CancelWorkflowFunc(ctx, workflowID, runID)
	}
	return nil
}

// GetWorkflowHistory returns an empty history iterator for SimpleMockTemporalClient.
func (m *SimpleMockTemporalClient) GetWorkflowHistory(_ context.Context, _, _ string, _ bool, _ enums.HistoryEventFilterType) client.HistoryEventIterator {
	return &EmptyHistoryIterator{}
}

// EmptyHistoryIterator is a mock iterator that returns no events.
type EmptyHistoryIterator struct{}

// HasNext returns false as there are no events.
func (e *EmptyHistoryIterator) HasNext() bool {
	return false
}

// Next returns io.EOF to signal end of iteration.
func (e *EmptyHistoryIterator) Next() (*historypb.HistoryEvent, error) {
	return nil, io.EOF
}

// DescribeWorkflowExecution implements a simple mock that returns workflow info from stored state.
func (m *SimpleMockTemporalClient) DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	m.mu.Lock()
	searchAttrs := m.workflows[workflowID]
	if searchAttrs == nil {
		searchAttrs = make(map[string]string)
	}
	// Copy to avoid race when iterating after unlock
	searchAttrsCopy := make(map[string]string, len(searchAttrs))
	for k, v := range searchAttrs {
		searchAttrsCopy[k] = v
	}
	m.mu.Unlock()

	indexedFields := make(map[string]*common.Payload)
	for key, value := range searchAttrsCopy {
		data := []byte(`"` + value + `"`)
		indexedFields[key] = &common.Payload{Data: data}
	}

	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Type: &common.WorkflowType{
				Name: "mock-workflow",
			},
			SearchAttributes: &common.SearchAttributes{
				IndexedFields: indexedFields,
			},
		},
	}, nil
}

// MockUpdateHandle implements the workflow update handle interface.
type MockUpdateHandle struct {
	assigneeID string
}

// Get returns the assignee ID that was sent in the request.
func (m *MockUpdateHandle) Get(ctx context.Context, valuePtr interface{}) error {
	if strPtr, ok := valuePtr.(*string); ok {
		*strPtr = m.assigneeID
	}
	return nil
}

// RunID returns a mock run ID.
func (m *MockUpdateHandle) RunID() string {
	return "mock-run-id"
}

// WorkflowID returns a mock workflow ID.
func (m *MockUpdateHandle) WorkflowID() string {
	return "mock-workflow-id"
}

// UpdateID returns a mock update ID.
func (m *MockUpdateHandle) UpdateID() string {
	return "mock-update-id"
}

// ConfigurableUpdateHandle is a configurable mock update handle for testing.
// It allows tests to set the value to be returned and error to be thrown.
type ConfigurableUpdateHandle struct {
	ReturnValue interface{}
	ReturnError error
	runID       string
	workflowID  string
	updateID    string
}

// NewConfigurableUpdateHandle creates a new configurable update handle with default IDs.
func NewConfigurableUpdateHandle(value interface{}, err error) *ConfigurableUpdateHandle {
	return &ConfigurableUpdateHandle{
		ReturnValue: value,
		ReturnError: err,
		runID:       "test-run-id",
		workflowID:  "test-workflow-id",
		updateID:    "test-update-id",
	}
}

// Get returns the configured value or error.
func (m *ConfigurableUpdateHandle) Get(ctx context.Context, valuePtr interface{}) error {
	if m.ReturnError != nil {
		return m.ReturnError
	}

	// Handle different types of value pointers
	switch v := valuePtr.(type) {
	case *int64:
		if intVal, ok := m.ReturnValue.(int64); ok {
			*v = intVal
		}
	case *string:
		if strVal, ok := m.ReturnValue.(string); ok {
			*v = strVal
		}
	}

	return nil
}

// RunID returns the run ID.
func (m *ConfigurableUpdateHandle) RunID() string {
	return m.runID
}

// WorkflowID returns the workflow ID.
func (m *ConfigurableUpdateHandle) WorkflowID() string {
	return m.workflowID
}

// UpdateID returns the update ID.
func (m *ConfigurableUpdateHandle) UpdateID() string {
	return m.updateID
}

// MockWorkflowRun is a mock implementation of client.WorkflowRun for testing.
type MockWorkflowRun struct {
	workflowID  string
	runID       string
	ReturnValue interface{}
	ReturnError error
}

// NewMockWorkflowRun creates a new MockWorkflowRun with the given IDs.
func NewMockWorkflowRun(workflowID, runID string, returnValue interface{}, returnError error) *MockWorkflowRun {
	return &MockWorkflowRun{
		workflowID:  workflowID,
		runID:       runID,
		ReturnValue: returnValue,
		ReturnError: returnError,
	}
}

// GetID returns the workflow ID.
func (m *MockWorkflowRun) GetID() string {
	return m.workflowID
}

// GetRunID returns the run ID.
func (m *MockWorkflowRun) GetRunID() string {
	return m.runID
}

// Get fills valuePtr with the return value or returns the error.
func (m *MockWorkflowRun) Get(ctx context.Context, valuePtr interface{}) error {
	if m.ReturnError != nil {
		return m.ReturnError
	}

	if m.ReturnValue != nil && valuePtr != nil {
		// Use reflection to copy the value
		srcVal := reflect.ValueOf(m.ReturnValue)
		dstVal := reflect.ValueOf(valuePtr)

		if dstVal.Kind() == reflect.Ptr && !dstVal.IsNil() {
			if srcVal.Kind() == reflect.Ptr {
				dstVal.Elem().Set(srcVal.Elem())
			} else {
				dstVal.Elem().Set(srcVal)
			}
		}
	}

	return nil
}

// GetWithOptions fills valuePtr with the return value or returns the error.
func (m *MockWorkflowRun) GetWithOptions(ctx context.Context, valuePtr interface{}, options client.WorkflowRunGetOptions) error {
	return m.Get(ctx, valuePtr)
}
