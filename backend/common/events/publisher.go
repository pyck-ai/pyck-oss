package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/std"
)

const (
	OpCreate = "create"
	OpUpdate = "update"
	OpDelete = "delete"
)

var (
	ErrReplyFailed = errors.New("reply indicates failure")

	mutationEventRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mutation_event_requests_total",
			Help: "Total number of mutation event requests with reply",
		},
		[]string{"service", "schema", "operation"},
	)

	mutationEventTimeouts = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mutation_event_reply_timeouts_total",
			Help: "Total number of mutation event replies that timed out",
		},
		[]string{"service", "schema", "operation"},
	)
)

func NewEventPublisher(js jetstream.JetStream, natsClient *nats.Conn, streamName string, replyTimeout time.Duration) (*EventPublisher, error) {
	return &EventPublisher{
		jetstream:    js,
		streamName:   streamName,
		natsClient:   natsClient,
		replyTimeout: replyTimeout,
	}, nil
}

type Publisher interface {
	SendCustomEvent(ctx context.Context, msg *CustomEventMessage) error
	SendMutationEvent(ctx context.Context, msg *MutationEventMessage) error
	SendMutationEventWithReply(ctx context.Context, msg *MutationEventMessage) ([]byte, error)
	SendTemporalWorkflowEvent(ctx context.Context, msg *TemporalWorkflowStateChangeMessage) error
	SendUpdateEvent(ctx context.Context, msg *UpdateEventMessage) error
	SendWorkflowEvent(ctx context.Context, msg *WorkflowEventMessage) error

	// Raw methods for pre-serialized payloads (used by OutboxHandler)
	PublishRaw(ctx context.Context, topic string, payload []byte, msgID string) error
	RequestRaw(ctx context.Context, topic string, payload []byte, timeout time.Duration) (*EventReply, error)
}

type EventReply struct {
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Msg     *nats.Msg       `json:"-"`
}

type EventPublisher struct {
	jetstream    jetstream.JetStream
	natsClient   *nats.Conn
	streamName   string
	replyTimeout time.Duration
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

// SendMutationEventWithReply sends a mutation event using the request/reply pattern and waits for a response.
//
// This method combines request/reply semantics with fire-and-forget event publishing:
//
// 1. Sends a request to the request/reply topic and waits for a response (subject to timeout)
// 2. If successful, publishes the same event to the fire-and-forget topic for other subscribers
//
// Timeout Behavior (IMPORTANT):
// The timeout (configured in EventPublisher.replyTimeout) only affects waiting for the REPLY,
// not the actual processing of the request. This means:
//
// - If the timeout expires, this method returns (nil, nil) - NOT an error
// - The request handler may still be processing the event in the background
// - Workflows may have been started even though no reply was received
// - The caller should NOT retry immediately, as this could cause duplicate workflows
//
// Why timeout returns nil instead of error:
// Timeouts are often caused by slow workflow startup or database queries, not failures.
// Returning nil allows the HTTP request to complete successfully while workflows continue
// processing asynchronously. A timeout metric is incremented for monitoring.
//
// Use Cases:
// - Creating/updating entities where you want confirmation before responding to the client
// - Operations that need to know which workflows were started
// - Synchronous-style operations in an async architecture
//
// Return Values:
// - ([]byte, nil): Success - returns the reply data from the handler
// - (nil, nil): Timeout - request sent but no reply received within timeout period
// - (nil, error): Failure - request failed to send, reply parsing failed, or handler returned error
//
// Metrics:
// - mutation_event_requests_total: Incremented on every call
// - mutation_event_reply_timeouts_total: Incremented when timeout occurs
//
// Example:
//
//	data, err := publisher.SendMutationEventWithReply(ctx, &events.MutationEventMessage{
//	    Service: "inventory",
//	    Schema: "item",
//	    Operation: "create",
//	    ID: itemID,
//	    TenantID: tenantID,
//	    Data: itemData,
//	})
//	if err != nil {
//	    // Handler explicitly returned an error
//	    return err
//	}
//	if data == nil {
//	    // Timeout - workflow may still be processing
//	    log.Warn("workflow start timed out, processing continues in background")
//	}
//	// data contains response from workflow handler (e.g., workflow IDs)
func (e *EventPublisher) SendMutationEventWithReply(ctx context.Context, msg *MutationEventMessage) (data []byte, err error) {
	mutationEventRequests.WithLabelValues(msg.Service, msg.Schema, msg.Operation).Inc()

	topic := &MutationEventWithReplyTopic{
		StreamName:    e.streamName,
		TenantID:      msg.TenantID,
		ServiceName:   msg.Service,
		SchemaName:    msg.Schema,
		EntityID:      msg.ID,
		OperationName: msg.Operation,
	}

	response, err := e.request(ctx, topic.String(), msg, e.replyTimeout)
	if err != nil {
		if errors.Is(err, nats.ErrTimeout) {
			mutationEventTimeouts.WithLabelValues(msg.Service, msg.Schema, msg.Operation).Inc()
			log.ForContext(ctx).Warn().
				Str("topic", topic.String()).
				Msg("mutation event reply timed out")
			return nil, nil
		}
		return nil, err
	}

	reply := &EventReply{
		Msg: response,
	}

	if response != nil {
		if err := json.Unmarshal(response.Data, &reply); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response: %w", err)
		}

		if !reply.Success {
			return nil, fmt.Errorf("%w: %s", ErrReplyFailed, reply.Error)
		}
	}

	if err := e.SendMutationEvent(ctx, msg); err != nil {
		return nil, err
	}

	return reply.Data, nil
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

	return e.publish(ctx, topic.String(), msg)
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

func (e *EventPublisher) publish(ctx context.Context, topic string, msg any) (err error) {
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
	_, err = e.jetstream.PublishMsg(context.WithoutCancel(ctx), natsMsg)

	log.ForContext(ctx).Err(err).
		Str("topic", topic).
		Any("payload", msg).
		Msg("publish nats message")

	return err
}

func (e *EventPublisher) request(ctx context.Context, topic string, msg any, timeout time.Duration) (*nats.Msg, error) {
	payload, err := std.MarshalJson(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request payload: %w", err)
	}

	natsMsg := &nats.Msg{Subject: topic, Data: payload}
	injectIntoMsg(ctx, natsMsg)

	return e.natsClient.RequestMsg(natsMsg, timeout)
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

// RequestRaw sends a pre-serialized payload using NATS request/reply pattern.
// Returns the parsed EventReply or an error.
func (e *EventPublisher) RequestRaw(ctx context.Context, topic string, payload []byte, timeout time.Duration) (*EventReply, error) {
	natsMsg := &nats.Msg{Subject: topic, Data: payload}
	injectIntoMsg(ctx, natsMsg)

	msg, err := e.natsClient.RequestMsg(natsMsg, timeout)
	if err != nil {
		return nil, err
	}

	var reply EventReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return nil, fmt.Errorf("failed to unmarshal reply: %w", err)
	}

	reply.Msg = msg
	return &reply, nil
}
