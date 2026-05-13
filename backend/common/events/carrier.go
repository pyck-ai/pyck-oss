package events

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// natsHeaderCarrier adapts nats.Header to the OTel TextMapCarrier interface so
// the global text map propagator can inject and extract trace context and
// baggage (including pyck.request-id) through NATS message headers.
type natsHeaderCarrier nats.Header

var _ propagation.TextMapCarrier = (*natsHeaderCarrier)(nil)

func (c natsHeaderCarrier) Get(key string) string {
	return nats.Header(c).Get(key)
}

func (c natsHeaderCarrier) Set(key, value string) {
	nats.Header(c).Set(key, value)
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// injectIntoMsg writes the OTel propagation context (trace + baggage) from ctx
// into the message headers. The message header map is initialized lazily.
func injectIntoMsg(ctx context.Context, msg *nats.Msg) {
	if msg.Header == nil {
		msg.Header = nats.Header{}
	}
	otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier(msg.Header))
}

// ContextFromMessage returns ctx enriched with OTel trace context and baggage
// extracted from msg.Header. Subscribers should call this at the entry of
// their handler so request-id and trace context flow through.
func ContextFromMessage(ctx context.Context, msg *nats.Msg) context.Context {
	if msg == nil || msg.Header == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, natsHeaderCarrier(msg.Header))
}

// ContextFromJetstreamMessage is the jetstream.Msg counterpart of
// ContextFromMessage for pull/push consumer handlers.
func ContextFromJetstreamMessage(ctx context.Context, msg jetstream.Msg) context.Context {
	if msg == nil {
		return ctx
	}
	headers := msg.Headers()
	if headers == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, natsHeaderCarrier(headers))
}
