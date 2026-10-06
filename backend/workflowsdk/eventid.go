package workflowsdk

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/eventid"
)

// ErrSignalChannelClosed is returned by ReceiveEvent when the channel is
// closed and empty.
var ErrSignalChannelClosed = errors.New("signal channel closed")

// signalEventIDMetadataKey is the payload metadata key the worker interceptor
// stamps the event ID under on a received signal's first payload. The data
// converters ignore unknown metadata keys, so ordinary Receive is unaffected.
const signalEventIDMetadataKey = "pyck-event-id"

type (
	startEventIDKey  struct{}
	dataConverterKey struct{}
)

// EventID returns the ID of the event that started this workflow run, as sent
// by the signal router in the pyck-event-id header. It reports false when the
// run was not started by the router (or the header is missing or malformed).
//
// For a signal's event ID use ReceiveEvent. The event ID plus the signal name
// is the key for deduplicating redelivered events inside a workflow.
func EventID(ctx workflow.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(startEventIDKey{}).(uuid.UUID)

	return id, ok
}

// ReceivedEvent is a signal value together with the ID of the event that
// delivered it.
type ReceivedEvent[T any] struct {
	Value T
	// ID is the event ID, or uuid.Nil if the signal was sent without one.
	ID uuid.UUID
}

// HasID reports whether the signal carried an event ID.
func (e ReceivedEvent[T]) HasID() bool { return e.ID != uuid.Nil }

// ReceiveEvent receives the next signal from ch, like ch.Receive, and returns
// it with the event ID it was sent with. It blocks until a signal arrives.
// Use it in place of Receive for signals sent by the router.
//
// Deduplicate redelivered events on (ev.ID, signal name), for example by
// remembering the pairs already handled in workflow state.
func ReceiveEvent[T any](ctx workflow.Context, ch workflow.ReceiveChannel) (ReceivedEvent[T], error) {
	var (
		ev  ReceivedEvent[T]
		raw converter.RawValue
	)

	if more := ch.Receive(ctx, &raw); !more && raw.Payload() == nil {
		return ev, ErrSignalChannelClosed
	}

	payload := raw.Payload()
	if payload == nil {
		return ev, nil
	}

	if b, ok := payload.GetMetadata()[signalEventIDMetadataKey]; ok {
		if id, err := uuid.ParseBytes(b); err == nil {
			ev.ID = id
		}
	}

	dc, _ := ctx.Value(dataConverterKey{}).(converter.DataConverter)
	if dc == nil {
		dc = converter.GetDefaultDataConverter()
	}

	if err := dc.FromPayload(payload, &ev.Value); err != nil {
		return ev, fmt.Errorf("decode signal payload: %w", err)
	}

	return ev, nil
}

// NewEventIDInterceptor returns the worker interceptor behind EventID and
// ReceiveEvent. dc is the worker's data converter (nil for the default), used
// by ReceiveEvent to decode the signal value. NewWorker installs it; it is
// exported for tests and custom workers.
//
// It works from the headers the SDK puts on the workflow context before the
// interceptors run: the start header in ExecuteWorkflow, and each signal's
// header in HandleSignal, which it copies into the payload metadata so the ID
// stays attached to its signal in the channel.
//
//nolint:ireturn // interceptor.Interceptor is the type Temporal worker and client options take.
func NewEventIDInterceptor(dc converter.DataConverter) interceptor.Interceptor {
	return &eventIDInterceptor{dc: dc}
}

// WorkerClientInterceptors returns the client interceptors workflowsdk's worker
// adds to its Temporal client options so workflows can read event IDs. dc is
// the client's data converter (nil for the default).
func WorkerClientInterceptors(dc converter.DataConverter) []interceptor.ClientInterceptor {
	return []interceptor.ClientInterceptor{NewEventIDInterceptor(dc)}
}

type eventIDInterceptor struct {
	interceptor.InterceptorBase

	dc converter.DataConverter
}

//nolint:ireturn // satisfies interceptor.WorkerInterceptor, which returns the WorkflowInboundInterceptor interface.
func (i *eventIDInterceptor) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &eventIDInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, dc: i.dc}
}

type eventIDInbound struct {
	interceptor.WorkflowInboundInterceptorBase

	dc converter.DataConverter
}

func (w *eventIDInbound) ExecuteWorkflow(ctx workflow.Context, in *interceptor.ExecuteWorkflowInput) (any, error) {
	if w.dc != nil {
		ctx = workflow.WithValue(ctx, dataConverterKey{}, w.dc)
	}

	if id, ok := eventid.Parse(interceptor.WorkflowHeader(ctx)[eventid.HeaderKey]); ok {
		ctx = workflow.WithValue(ctx, startEventIDKey{}, id)
	}

	return w.Next.ExecuteWorkflow(ctx, in)
}

func (w *eventIDInbound) HandleSignal(ctx workflow.Context, in *interceptor.HandleSignalInput) error {
	id, ok := eventid.Parse(interceptor.WorkflowHeader(ctx)[eventid.HeaderKey])
	if !ok || len(in.Arg.GetPayloads()) == 0 {
		return w.Next.HandleSignal(ctx, in)
	}

	// Copy rather than mutate: the payloads belong to the history event.
	first := in.Arg.GetPayloads()[0]
	metadata := make(map[string][]byte, len(first.GetMetadata())+1)

	for k, v := range first.GetMetadata() {
		metadata[k] = v
	}

	metadata[signalEventIDMetadataKey] = []byte(id.String())

	payloads := make([]*commonpb.Payload, len(in.Arg.GetPayloads()))
	copy(payloads, in.Arg.GetPayloads())
	payloads[0] = &commonpb.Payload{Metadata: metadata, Data: first.GetData()}

	stamped := *in
	stamped.Arg = &commonpb.Payloads{Payloads: payloads}

	return w.Next.HandleSignal(ctx, &stamped)
}
