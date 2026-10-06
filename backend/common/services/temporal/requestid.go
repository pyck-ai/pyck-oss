package temporal

import (
	"context"

	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

type requestIDKey struct{}

// WithRequestID returns a context that makes RequestIDInterceptor stamp id as
// the Temporal RequestId on start/signal/signal-with-start calls made with it.
// Temporal deduplicates repeated calls that carry the same RequestId.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the ID set with WithRequestID. It lets callers
// and test doubles see what the interceptor would send.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)

	return id, ok && id != ""
}

// RequestIDInterceptor is a gRPC unary client interceptor that overwrites the
// SDK's random RequestId with the one set via WithRequestID. Calls whose
// context carries no ID, and all other request types, pass through untouched.
func RequestIDInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if id, ok := ctx.Value(requestIDKey{}).(string); ok && id != "" {
			switch r := req.(type) {
			case *workflowservice.StartWorkflowExecutionRequest:
				r.RequestId = id
			case *workflowservice.SignalWorkflowExecutionRequest:
				r.RequestId = id
			case *workflowservice.SignalWithStartWorkflowExecutionRequest:
				r.RequestId = id
			}
		}

		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
