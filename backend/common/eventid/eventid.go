// Package eventid carries the ID of the event that triggered a Temporal
// start or signal, from the signal router to the workflow.
//
// The router puts the ID on the context with WithEventID. ClientInterceptor,
// installed on the Temporal connection, then copies it into the Temporal
// header HeaderKey of every start, signal and signal-with-start request. The
// worker side (workflowsdk) reads it back for workflow code.
//
// It is a leaf package so both sides can import it without a cycle.
package eventid

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

// HeaderKey is the Temporal header that carries the event ID, encoded as a
// JSON string UUID so it also reads well in the Temporal UI.
const HeaderKey = "pyck-event-id"

type ctxKey struct{}

// WithEventID returns a context that makes ClientInterceptor send id in the
// HeaderKey header of start, signal and signal-with-start calls.
func WithEventID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the event ID set with WithEventID.
func FromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(ctxKey{}).(uuid.UUID)

	return id, ok && id != uuid.Nil
}

// Payload encodes id as a header payload.
func Payload(id uuid.UUID) (*commonpb.Payload, error) {
	data, err := json.Marshal(id.String())
	if err != nil {
		return nil, err
	}

	return &commonpb.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     data,
	}, nil
}

// Parse decodes a payload written by Payload. It reports false for a nil
// payload or anything that is not a JSON string holding a UUID.
func Parse(p *commonpb.Payload) (uuid.UUID, bool) {
	if p == nil {
		return uuid.Nil, false
	}

	var s string
	if err := json.Unmarshal(p.GetData(), &s); err != nil {
		return uuid.Nil, false
	}

	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}

	return id, true
}

// ClientInterceptor is a gRPC unary client interceptor that adds the
// HeaderKey header to start, signal and signal-with-start requests whose
// context carries an event ID. Other calls, and calls without an ID, pass
// through untouched. Install it with grpc.WithChainUnaryInterceptor in
// client.ConnectionOptions.DialOptions; clients derived from that client
// share the connection and so carry it too.
func ClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if id, ok := FromContext(ctx); ok {
			if err := stampHeader(req, id); err != nil {
				return err
			}
		}

		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// stampHeader writes id into the Header of the request types ClientInterceptor
// targets, keeping any fields already there. Other request types are left
// untouched.
func stampHeader(req any, id uuid.UUID) error {
	var header **commonpb.Header

	switch r := req.(type) {
	case *workflowservice.StartWorkflowExecutionRequest:
		header = &r.Header
	case *workflowservice.SignalWorkflowExecutionRequest:
		header = &r.Header
	case *workflowservice.SignalWithStartWorkflowExecutionRequest:
		header = &r.Header
	default:
		return nil
	}

	payload, err := Payload(id)
	if err != nil {
		return err
	}

	if *header == nil {
		*header = &commonpb.Header{}
	}

	if (*header).Fields == nil {
		(*header).Fields = map[string]*commonpb.Payload{}
	}

	(*header).Fields[HeaderKey] = payload

	return nil
}
