package adapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/temporal/event"
	"github.com/pyck-ai/pyck/backend/temporal/event/adapter"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	commandpb "go.temporal.io/api/command/v1"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	tokenspb "go.temporal.io/server/api/token/v1"
	"go.temporal.io/server/common/primitives/timestamp"
	"go.temporal.io/server/common/tasktoken"
	"google.golang.org/grpc"
)

// createMockPublisher creates a mock publisher for testing
func createMockPublisher(t *testing.T) *mocks.MockPublisher {
	t.Helper()

	mockPub := &mocks.MockPublisher{}
	// Make sure mock calls don't block - return immediately
	mockPub.Test(t)
	return mockPub
}

// createHandler creates a properly initialized Handler with the given publisher for testing
func createHandler(t *testing.T, publisher events.Publisher) *event.Handler {
	t.Helper()

	return event.NewHandlerWithPoolSize(t.Context(), publisher, 10, 100)
}

// createValidTaskToken creates a valid serialized task token for testing
func createValidTaskToken(namespaceID, workflowID, runID string) []byte {
	serializer := tasktoken.NewSerializer()
	task := &tokenspb.Task{
		NamespaceId:      namespaceID,
		WorkflowId:       workflowID,
		RunId:            runID,
		ScheduledEventId: 1,
		Attempt:          1,
		StartedTime:      timestamp.TimePtr(time.Now()),
	}
	token, err := serializer.Serialize(task)
	if err != nil {
		panic(err) // Should never happen in tests with valid data
	}
	return token
}

// Test basic interceptor creation
func TestNewGRPCInterceptor(t *testing.T) {
	ctx := context.Background()
	mockPub := createMockPublisher(t)
	handler := createHandler(t, mockPub)
	defer handler.Close()

	interceptor := adapter.NewGRPCInterceptor(ctx, handler)

	require.NotNil(t, interceptor)
	assert.IsType(t, grpc.UnaryServerInterceptor(nil), interceptor)
}

// Test that the interceptor passes requests through to the next handler (no events for non-workflow methods)
func TestGRPCInterceptor_PassThrough(t *testing.T) {
	ctx := context.Background()
	mockPub := createMockPublisher(t)
	handler := createHandler(t, mockPub)
	defer handler.Close()

	interceptor := adapter.NewGRPCInterceptor(ctx, handler)

	// Test with a simple request
	request := &workflowservice.GetClusterInfoRequest{}
	expectedResponse := &workflowservice.GetClusterInfoResponse{
		ClusterName: "test-cluster",
	}

	var actualRequest interface{}
	nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
		actualRequest = req
		return expectedResponse, nil
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/GetClusterInfo",
	}

	response, err := interceptor(ctx, request, info, nextHandler)

	require.NoError(t, err)
	assert.Equal(t, expectedResponse, response)
	assert.Equal(t, request, actualRequest)

	// Verify no events were sent for non-workflow method
	mockPub.AssertNotCalled(t, "SendTemporalWorkflowEvent")
}

// Test StartWorkflowExecution with various scenarios
func TestGRPCInterceptor_StartWorkflowExecution(t *testing.T) {
	taskQueueName := "test-task-queue"
	workflowTypeName := "test-workflow-type"

	testCases := []struct {
		name          string
		request       *workflowservice.StartWorkflowExecutionRequest
		response      *workflowservice.StartWorkflowExecutionResponse
		expectedEvent *events.TemporalWorkflowStateChangeMessage
		shouldNotify  bool
	}{
		{
			name: "Started_WithAllFields",
			request: &workflowservice.StartWorkflowExecutionRequest{
				Namespace:  "test-namespace",
				WorkflowId: "test-workflow-id",
				TaskQueue: &taskqueue.TaskQueue{
					Name: taskQueueName,
				},
				WorkflowType: &commonpb.WorkflowType{
					Name: workflowTypeName,
				},
			},
			response: &workflowservice.StartWorkflowExecutionResponse{
				RunId:   "test-run-id",
				Started: true,
				Status:  enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			},
			expectedEvent: &events.TemporalWorkflowStateChangeMessage{
				Namespace:  "test-namespace",
				RunID:      "test-run-id",
				Status:     "Running",
				WorkflowID: "test-workflow-id",
			},
			shouldNotify: true,
		},
		{
			name: "Started_MinimalFields",
			request: &workflowservice.StartWorkflowExecutionRequest{
				Namespace:  "test-namespace",
				WorkflowId: "test-workflow-id",
			},
			response: &workflowservice.StartWorkflowExecutionResponse{
				RunId:   "test-run-id",
				Started: true,
				Status:  enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			},
			expectedEvent: &events.TemporalWorkflowStateChangeMessage{
				Namespace:  "test-namespace",
				RunID:      "test-run-id",
				Status:     "Running",
				WorkflowID: "test-workflow-id",
			},
			shouldNotify: true,
		},
		{
			name: "NotStarted_AlreadyExists",
			request: &workflowservice.StartWorkflowExecutionRequest{
				Namespace:  "test-namespace",
				WorkflowId: "test-workflow-id",
			},
			response: &workflowservice.StartWorkflowExecutionResponse{
				RunId:   "existing-run-id",
				Started: false,
			},
			expectedEvent: nil,
			shouldNotify:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockPub := createMockPublisher(t)

			if tc.shouldNotify {
				mockPub.ExpectTemporalWorkflowEvent().Return(nil).Run(func(args mock.Arguments) {
					evt := args.Get(1).(*events.TemporalWorkflowStateChangeMessage)
					assert.Equal(t, tc.expectedEvent.Namespace, evt.Namespace)
					assert.Equal(t, tc.expectedEvent.WorkflowID, evt.WorkflowID)
					assert.Equal(t, tc.expectedEvent.RunID, evt.RunID)
					assert.Equal(t, tc.expectedEvent.Status, evt.Status)
				})
			}

			handler := createHandler(t, mockPub)

			interceptor := adapter.NewGRPCInterceptor(ctx, handler)

			nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
				return tc.response, nil
			}

			info := &grpc.UnaryServerInfo{
				FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/StartWorkflowExecution",
			}

			result, err := interceptor(ctx, tc.request, info, nextHandler)

			require.NoError(t, err)
			assert.Equal(t, tc.response, result)

			// Close handler to drain queue and wait for async event publication
			handler.Close()

			// Wait for all expected mock calls to complete using WaitGroup
			if tc.shouldNotify {
				mockPub.WaitForCalls()
				mockPub.AssertNumberOfCalls(t, "SendTemporalWorkflowEvent", 1)
			} else {
				mockPub.AssertNotCalled(t, "SendTemporalWorkflowEvent")
			}
		})
	}
}

// Test RespondWorkflowTaskCompleted with various scenarios
func TestGRPCInterceptor_RespondWorkflowTaskCompleted(t *testing.T) {
	testCases := []struct {
		name         string
		taskToken    []byte
		commands     []*commandpb.Command
		shouldNotify bool
	}{
		{
			name:      "InvalidTaskToken_WithTerminalCommand",
			taskToken: []byte("invalid-task-token"),
			commands: []*commandpb.Command{
				{
					CommandType: enumspb.COMMAND_TYPE_COMPLETE_WORKFLOW_EXECUTION,
				},
			},
			shouldNotify: false,
		},
		{
			name:      "NonTerminalCommands",
			taskToken: createValidTaskToken("test-namespace-id", "test-workflow-id", "test-run-id"),
			commands: []*commandpb.Command{
				{
					CommandType: enumspb.COMMAND_TYPE_SCHEDULE_ACTIVITY_TASK,
				},
				{
					CommandType: enumspb.COMMAND_TYPE_START_TIMER,
				},
			},
			shouldNotify: false,
		},
		{
			name:         "NoCommands",
			taskToken:    createValidTaskToken("test-namespace-id", "test-workflow-id", "test-run-id"),
			commands:     []*commandpb.Command{},
			shouldNotify: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockPub := createMockPublisher(t)

			defer mockPub.AssertNotCalled(t, "SendTemporalWorkflowEvent")

			handler := createHandler(t, mockPub)
			defer handler.Close()

			interceptor := adapter.NewGRPCInterceptor(ctx, handler)

			request := &workflowservice.RespondWorkflowTaskCompletedRequest{
				Namespace: "test-namespace",
				TaskToken: tc.taskToken,
				Commands:  tc.commands,
			}

			response := &workflowservice.RespondWorkflowTaskCompletedResponse{}

			nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
				return response, nil
			}

			info := &grpc.UnaryServerInfo{
				FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/RespondWorkflowTaskCompleted",
			}

			result, err := interceptor(ctx, request, info, nextHandler)

			require.NoError(t, err)
			assert.Equal(t, response, result)
		})
	}
}

// Test that errors from the underlying service are properly propagated (no events sent on error)
func TestGRPCInterceptor_ServiceError_Propagated(t *testing.T) {
	ctx := context.Background()
	mockPub := createMockPublisher(t)

	defer mockPub.AssertNotCalled(t, "SendTemporalWorkflowEvent")

	handler := createHandler(t, mockPub)
	defer handler.Close()

	interceptor := adapter.NewGRPCInterceptor(ctx, handler)

	request := &workflowservice.StartWorkflowExecutionRequest{
		Namespace:  "test-namespace",
		WorkflowId: "test-workflow-id",
	}

	expectedError := assert.AnError

	nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, expectedError
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/StartWorkflowExecution",
	}

	result, err := interceptor(ctx, request, info, nextHandler)

	assert.Nil(t, result)
	assert.Equal(t, expectedError, err)
}

// Test command type detection for all terminal workflow states
func TestGRPCInterceptor_RespondWorkflowTaskCompleted_CommandTypes(t *testing.T) {
	testCases := []struct {
		name           string
		commandType    enumspb.CommandType
		expectedStatus string
	}{
		{
			name:           "CompleteWorkflow",
			commandType:    enumspb.COMMAND_TYPE_COMPLETE_WORKFLOW_EXECUTION,
			expectedStatus: "Completed",
		},
		{
			name:           "FailWorkflow",
			commandType:    enumspb.COMMAND_TYPE_FAIL_WORKFLOW_EXECUTION,
			expectedStatus: "Failed",
		},
		{
			name:           "CancelWorkflow",
			commandType:    enumspb.COMMAND_TYPE_CANCEL_WORKFLOW_EXECUTION,
			expectedStatus: "Canceled",
		},
		{
			name:           "ContinueAsNewWorkflow",
			commandType:    enumspb.COMMAND_TYPE_CONTINUE_AS_NEW_WORKFLOW_EXECUTION,
			expectedStatus: "ContinuedAsNew",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockPub := createMockPublisher(t)

			namespaceID := "test-namespace-id"
			namespace := "test-namespace"
			workflowID := "test-workflow-id"
			runID := "test-run-id"

			// Create a valid task token
			taskToken := createValidTaskToken(namespaceID, workflowID, runID)

			// Set up expectation for the event
			// Note: The event uses the request's namespace (not the token's namespace ID)
			mockPub.ExpectTemporalWorkflowEvent().Return(nil).Run(func(args mock.Arguments) {
				evt := args.Get(1).(*events.TemporalWorkflowStateChangeMessage)
				assert.Equal(t, namespace, evt.Namespace)
				assert.Equal(t, workflowID, evt.WorkflowID)
				assert.Equal(t, runID, evt.RunID)
				assert.Equal(t, tc.expectedStatus, evt.Status)
			})

			handler := createHandler(t, mockPub)

			interceptor := adapter.NewGRPCInterceptor(ctx, handler)

			// Create request with terminal command
			request := &workflowservice.RespondWorkflowTaskCompletedRequest{
				Namespace: namespace,
				TaskToken: taskToken,
				Commands: []*commandpb.Command{
					{
						CommandType: tc.commandType,
					},
				},
			}

			response := &workflowservice.RespondWorkflowTaskCompletedResponse{}

			nextHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
				return response, nil
			}

			info := &grpc.UnaryServerInfo{
				FullMethod: "/temporal.api.workflowservice.v1.WorkflowService/RespondWorkflowTaskCompleted",
			}

			result, err := interceptor(ctx, request, info, nextHandler)

			require.NoError(t, err)
			assert.Equal(t, response, result)

			// Close handler to drain queue and wait for async event publication
			handler.Close()

			// Wait for all expected mock calls to complete using WaitGroup
			mockPub.WaitForCalls()
			mockPub.AssertNumberOfCalls(t, "SendTemporalWorkflowEvent", 1)
		})
	}
}
