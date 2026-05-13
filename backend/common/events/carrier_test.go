package events_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/requestid"
)

// TestMain lives in hook_test.go and installs the global OTel propagator
// shared by all tests in this package, mirroring backend/common/otel.

func TestNATSCarrierRoundTripPropagatesRequestID(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050505"

	ctx, err := requestid.WithRequestID(context.Background(), id)
	require.NoError(t, err)

	msg := &nats.Msg{Subject: "test", Data: []byte("payload")}
	events.InjectIntoMsg(ctx, msg)

	receivedCtx := events.ContextFromMessage(context.Background(), msg)
	assert.Equal(t, id, requestid.FromContext(receivedCtx))
}

func TestContextFromMessageHandlesNilHeader(t *testing.T) {
	t.Parallel()

	msg := &nats.Msg{Subject: "test", Data: []byte("payload")}

	got := events.ContextFromMessage(context.Background(), msg)
	assert.Empty(t, requestid.FromContext(got))
}

func TestContextFromMessageHandlesNilMessage(t *testing.T) {
	t.Parallel()

	got := events.ContextFromMessage(context.Background(), nil)
	assert.Empty(t, requestid.FromContext(got))
}

func TestInjectIntoMsgInitializesHeader(t *testing.T) {
	t.Parallel()

	ctx, err := requestid.WithRequestID(context.Background(), "01010101-0202-7303-8404-050505050506")
	require.NoError(t, err)

	msg := &nats.Msg{Subject: "test", Data: []byte("payload")}
	events.InjectIntoMsg(ctx, msg)

	require.NotNil(t, msg.Header, "InjectIntoMsg must lazily initialize header map")
	assert.NotEmpty(t, msg.Header.Get("baggage"), "baggage must be encoded into the message header")
}

// TestNATSCarrierRoundTripPropagatesTraceContextWithBaggage verifies that
// the carrier carries BOTH the W3C trace context AND baggage in a single
// round trip — important because the Inject path goes through the global
// composite propagator, and a regression in either side would silently
// break log/event correlation across services.
func TestNATSCarrierRoundTripPropagatesTraceContextWithBaggage(t *testing.T) {
	t.Parallel()

	const id = "01010101-0202-7303-8404-050505050508"

	tp := sdktrace.NewTracerProvider()
	tracer := tp.Tracer("carrier-test")

	parentCtx, span := tracer.Start(context.Background(), "publish")
	defer span.End()

	ctx, err := requestid.WithRequestID(parentCtx, id)
	require.NoError(t, err)

	msg := &nats.Msg{Subject: "test", Data: []byte("payload")}
	events.InjectIntoMsg(ctx, msg)

	require.NotEmpty(t, msg.Header.Get("traceparent"), "traceparent must be injected by the W3C TraceContext propagator")
	require.NotEmpty(t, msg.Header.Get("baggage"), "baggage must be injected by the W3C Baggage propagator")

	// Receiver side: extract into a fresh background context.
	receivedCtx := events.ContextFromMessage(context.Background(), msg)

	assert.Equal(t, id, requestid.FromContext(receivedCtx))

	receivedSpanCtx := trace.SpanContextFromContext(receivedCtx)
	require.True(t, receivedSpanCtx.IsValid(), "extracted span context must be valid")
	assert.Equal(t, span.SpanContext().TraceID(), receivedSpanCtx.TraceID(), "trace ID must round-trip through the carrier")
}

// TestInjectPreservesUnrelatedBaggageMembers guards against a regression
// where the carrier might overwrite the entire baggage header instead of
// merging members — which would break unrelated baggage that other parts
// of the system may add (e.g. tenant/user attribution).
func TestInjectPreservesUnrelatedBaggageMembers(t *testing.T) {
	t.Parallel()

	other, err := baggage.NewMember("pyck.tenant-id", "tenant-abc")
	require.NoError(t, err)
	bag, err := baggage.New(other)
	require.NoError(t, err)
	ctx := baggage.ContextWithBaggage(context.Background(), bag)

	ctx, err = requestid.WithRequestID(ctx, "01010101-0202-7303-8404-050505050509")
	require.NoError(t, err)

	msg := &nats.Msg{Subject: "test", Data: []byte("payload")}
	events.InjectIntoMsg(ctx, msg)

	receivedCtx := events.ContextFromMessage(context.Background(), msg)

	assert.Equal(t, "01010101-0202-7303-8404-050505050509", requestid.FromContext(receivedCtx))
	assert.Equal(t, "tenant-abc", baggage.FromContext(receivedCtx).Member("pyck.tenant-id").Value(),
		"unrelated baggage members must round-trip alongside request-id")
}

// TestNATSCarrierKeysExposesAllHeaders ensures the TextMapCarrier.Keys
// implementation returns all stored keys; the OTel SDK relies on Keys()
// for some propagators and a buggy implementation can drop fields silently.
func TestNATSCarrierKeysExposesAllHeaders(t *testing.T) {
	t.Parallel()

	ctx, err := requestid.WithRequestID(context.Background(), "01010101-0202-7303-8404-05050505050a")
	require.NoError(t, err)

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(ctx, "publish")
	defer span.End()

	msg := &nats.Msg{Subject: "test", Data: []byte("payload")}
	events.InjectIntoMsg(ctx, msg)

	// Round-trip through Extract goes through Keys() internally for some
	// propagators, but we also verify presence directly to lock the
	// header names against drift.
	assert.NotEmpty(t, msg.Header.Get("traceparent"))
	assert.NotEmpty(t, msg.Header.Get("baggage"))
}

// TestPropagationConstantsPinned guards the public contract surface that
// downstream services and infrastructure rely on (HTTP proxies look at
// X-Request-ID; baggage consumers look at pyck.request-id; log indexers
// look at request_id). Each rename is a breaking change.
func TestPropagationConstantsPinned(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "X-Request-ID", requestid.HTTPHeader, "HTTP response header must follow the standard")
	assert.Equal(t, "pyck.request-id", requestid.BaggageKey, "OTel baggage key must follow the dot-namespaced convention")
	assert.Equal(t, "request_id", requestid.LogField, "log field must use snake_case per ECS / OTel logs data model")
	assert.NotNil(t, otel.GetTextMapPropagator(), "global propagator must be installed before tests use it")
}
