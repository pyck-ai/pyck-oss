package events

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/std"
)

const (
	// StateChangeMsgIDPrefix prefixes the JetStream message ID of a state change.
	StateChangeMsgIDPrefix = "statechange-"

	// JSErrCodeDuplicateMsgIDInProcess is the JetStream error code (10158) for
	// "duplicate message id is in process". Another publisher's copy with the
	// same Nats-Msg-Id is still being stored, so the server cannot yet answer
	// with the normal duplicate ack. The message is not lost: a retry gets the
	// duplicate ack once the other copy is stored. nats.go has no named
	// constant for it.
	JSErrCodeDuplicateMsgIDInProcess jetstream.ErrorCode = 10158
)

// IsDuplicateMsgIDInProcess reports whether err wraps a JetStream API error
// with code JSErrCodeDuplicateMsgIDInProcess. It is expected when several
// publishers send the same message ID at the same moment and is safe to retry.
func IsDuplicateMsgIDInProcess(err error) bool {
	var apiErr *jetstream.APIError

	return errors.As(err, &apiErr) && apiErr != nil && apiErr.ErrorCode == JSErrCodeDuplicateMsgIDInProcess
}

func NewEventPublisher(js jetstream.JetStream, natsClient *nats.Conn, streamName string) *EventPublisher {
	return &EventPublisher{
		jetstream:  js,
		streamName: streamName,
		natsClient: natsClient,
	}
}

type Publisher interface {
	SendCustomEvent(ctx context.Context, msg *CustomEventMessage) error
	SendMutationEvent(ctx context.Context, msg *MutationEventMessage) error
	SendTemporalWorkflowEvent(ctx context.Context, msg *TemporalWorkflowStateChangeMessage) error
	SendUpdateEvent(ctx context.Context, msg *UpdateEventMessage) error
	SendWorkflowEvent(ctx context.Context, msg *WorkflowEventMessage) error

	// PublishRaw publishes a pre-serialized payload (used by OutboxHandler).
	PublishRaw(ctx context.Context, topic string, payload []byte, msgID string) error
}

type EventPublisher struct {
	jetstream  jetstream.JetStream
	natsClient *nats.Conn
	streamName string
}

var _ Publisher = (*EventPublisher)(nil)

func (e *EventPublisher) SendCustomEvent(ctx context.Context, msg *CustomEventMessage) error {
	topic := &CustomEventTopic{
		StreamName: e.streamName,
	}

	return e.publish(ctx, topic.String(), msg)
}

func (e *EventPublisher) SendMutationEvent(ctx context.Context, msg *MutationEventMessage) error {
	topic := &MutationEventTopic{
		StreamName:    e.streamName,
		TenantID:      msg.TenantID,
		ServiceName:   msg.Service,
		SchemaName:    msg.Schema,
		EntityID:      msg.ID,
		OperationName: msg.Operation,
	}

	return e.publish(ctx, topic.String(), msg)
}

func (e *EventPublisher) SendTemporalWorkflowEvent(ctx context.Context, msg *TemporalWorkflowStateChangeMessage) error {
	topic := &TemporalWorkflowStateChangeTopic{
		StreamName:       e.streamName,
		Namespace:        msg.Namespace,
		TaskQueue:        msg.TaskQueue,
		WorkflowTypeName: msg.WorkflowTypeName,
		WorkflowID:       msg.WorkflowID,
		RunID:            msg.RunID,
		Status:           msg.Status,
	}

	// Every Temporal pod that receives the database notification publishes the
	// change, so the message ID lets JetStream keep one copy per duplicate
	// window. The ID is predictable, so a client that can publish to the stream
	// could suppress the change by guessing it; that is accepted until each
	// tenant has its own stream (#1656). The signal router deduplicates by
	// Temporal request ID beyond the window.
	return e.publish(ctx, topic.String(), msg, jetstream.WithMsgID(StateChangeMsgIDPrefix+msg.EventID().String()))
}

func (e *EventPublisher) SendUpdateEvent(ctx context.Context, msg *UpdateEventMessage) error {
	topic := &UpdateEventTopic{
		StreamName:    e.streamName,
		TenantID:      msg.TenantID,
		ServiceName:   msg.Service,
		SchemaName:    msg.Schema,
		EntityID:      msg.ID,
		OperationName: msg.Operation,
		AttributeName: msg.Attribute,
	}

	return e.publish(ctx, topic.String(), msg)
}

func (e *EventPublisher) SendWorkflowEvent(ctx context.Context, msg *WorkflowEventMessage) error {
	topic := &WorkflowEventTopic{
		StreamName:   e.streamName,
		TenantID:     msg.TenantID,
		WorkflowID:   msg.WorkflowID,
		WorkflowName: msg.WorkflowName,
	}

	return e.publish(ctx, topic.String(), msg)
}

func (e *EventPublisher) publish(ctx context.Context, topic string, msg any, opts ...jetstream.PublishOpt) (err error) {
	payload, err := std.MarshalJson(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal publish payload: %w", err)
	}

	natsMsg := &nats.Msg{Subject: topic, Data: payload}
	injectIntoMsg(ctx, natsMsg)

	// We use context.WithoutCancel() here to ensure event publishing is not
	// cancelled when the parent context is cancelled. This is critical because
	// the parent context typically comes from an HTTP request, which is
	// automatically cancelled once the request body is fully sent to the client.
	// If we used the parent context directly, events could be lost when the
	// HTTP response completes but before NATS has acknowledged the publish.
	_, err = e.jetstream.PublishMsg(context.WithoutCancel(ctx), natsMsg, opts...)

	logger := log.ForContext(ctx)

	// A concurrent publisher of the same message ID is expected and the caller
	// retries, so it is not an error worth alerting on.
	if IsDuplicateMsgIDInProcess(err) {
		logger.Debug().Err(err).
			Str("topic", topic).
			Any("payload", msg).
			Msg("publish nats message")

		return err
	}

	logger.Err(err).
		Str("topic", topic).
		Any("payload", msg).
		Msg("publish nats message")

	return err
}

// PublishRaw publishes a pre-serialized payload to the given topic via JetStream.
// This is used by OutboxHandler which already has serialized payloads.
func (e *EventPublisher) PublishRaw(ctx context.Context, topic string, payload []byte, msgID string) error {
	opts := []jetstream.PublishOpt{}
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}

	natsMsg := &nats.Msg{Subject: topic, Data: payload}
	injectIntoMsg(ctx, natsMsg)

	_, err := e.jetstream.PublishMsg(context.WithoutCancel(ctx), natsMsg, opts...)
	return err
}
