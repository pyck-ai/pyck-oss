package adapter

import (
	"context"
	"errors"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/temporal/event"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	tokenspb "go.temporal.io/server/api/token/v1"
	"go.temporal.io/server/common/tasktoken"
	"google.golang.org/grpc"
	"lab.nexedi.com/kirr/go123/xcontext"
)

var (
	ErrInvalidToken = errors.New("invalid token")
)

func NewGRPCInterceptor(ctx context.Context, handler *event.Handler) grpc.UnaryServerInterceptor {
	return func(reqCtx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (resp any, err error) {
		// Temporal (specifically its gRPC server) does not allow defining a
		// custom base context, so we have to retroactively merge our base
		// context into the server context in order to propagate loggers,
		// traces, etc...
		// Note: xcontext.Merge returns (context.Context, context.CancelFunc).
		// The CancelFunc is not used here as context lifecycle is managed by gRPC.
		reqCtx, _ = xcontext.Merge(reqCtx, ctx)

		interceptor := &tracingFrontendInterceptor{
			handler:    handler,
			serializer: tasktoken.NewSerializer(),
		}

		return interceptor.Handle(reqCtx, req, info, next)
	}
}

type tracingFrontendInterceptor struct {
	handler    *event.Handler
	serializer *tasktoken.Serializer
}

func (i *tracingFrontendInterceptor) Handle(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	resp, err := next(ctx, req)
	if err != nil {
		return nil, err
	}

	switch req := req.(type) {
	case *workflowservice.StartWorkflowExecutionRequest:
		resp, ok := resp.(*workflowservice.StartWorkflowExecutionResponse)
		if !ok {
			break
		}

		i.handleStartWorkflowExecution(ctx, req, resp)
	case *workflowservice.RespondWorkflowTaskCompletedRequest:
		resp, ok := resp.(*workflowservice.RespondWorkflowTaskCompletedResponse)
		if !ok {
			break
		}

		i.handleRespondWorkflowTaskComplete(ctx, req, resp)
	default:
		break
	}

	return resp, err
}

func (i *tracingFrontendInterceptor) handleStartWorkflowExecution(ctx context.Context, req *workflowservice.StartWorkflowExecutionRequest, resp *workflowservice.StartWorkflowExecutionResponse) {
	if !resp.GetStarted() {
		return
	}

	i.handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
		Namespace:  req.GetNamespace(),
		WorkflowID: req.GetWorkflowId(),
		RunID:      resp.GetRunId(),
		Status:     resp.GetStatus().String(),
	})
}
func (i *tracingFrontendInterceptor) handleRespondWorkflowTaskComplete(ctx context.Context, req *workflowservice.RespondWorkflowTaskCompletedRequest, _ *workflowservice.RespondWorkflowTaskCompletedResponse) {
	var status enumspb.WorkflowExecutionStatus

	for _, cmd := range req.GetCommands() {
		switch cmd.GetCommandType() {
		case enumspb.COMMAND_TYPE_COMPLETE_WORKFLOW_EXECUTION:
			status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
		case enumspb.COMMAND_TYPE_FAIL_WORKFLOW_EXECUTION:
			status = enumspb.WORKFLOW_EXECUTION_STATUS_FAILED
		case enumspb.COMMAND_TYPE_CANCEL_WORKFLOW_EXECUTION:
			status = enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED
		case enumspb.COMMAND_TYPE_CONTINUE_AS_NEW_WORKFLOW_EXECUTION:
			status = enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW
		default:
			continue // skip to next command
		}

		break // we only handle the first matching command
	}

	if status == enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED {
		return
	}

	task, err := i.parseTaskToken(req.GetTaskToken())
	if err != nil {
		log.ForContext(ctx).Error().
			Err(err).
			Msg("failed to deserialize request task token")
		return
	}

	// Skip if required fields are missing
	if task.GetRunId() == "" || task.GetWorkflowId() == "" {
		return
	}

	i.handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
		Namespace:        req.GetNamespace(),
		WorkflowTypeName: task.GetWorkflowType(),
		WorkflowID:       task.GetWorkflowId(),
		RunID:            task.GetRunId(),
		Status:           status.String(),
	})
}

func (i *tracingFrontendInterceptor) parseTaskToken(token []byte) (*tokenspb.Task, error) {
	task, err := i.serializer.Deserialize(token)
	if err != nil {
		return nil, err
	}

	if task == nil || task.GetRunId() == "" || task.GetWorkflowId() == "" {
		return nil, ErrInvalidToken
	}

	return task, nil
}
