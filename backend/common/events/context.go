package events

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/trace"
)

// ErrNoCorrelationID is returned when no OTel trace context is available.
var ErrNoCorrelationID = errors.New("no correlation ID: missing OTel trace context")

// Context keys for event middleware values.
type (
	expectReplyKey           struct{}
	extraSearchAttributesKey struct{}
)

// CorrelationIDFromContext extracts the correlation ID from the context.
// The correlation ID is derived from the OpenTelemetry trace ID to ensure
// end-to-end observability correlation across services.
// Returns ErrNoCorrelationID if no trace context is available.
func CorrelationIDFromContext(ctx context.Context) (string, error) {
	spanCtx := trace.SpanFromContext(ctx).SpanContext()
	if spanCtx.HasTraceID() {
		return spanCtx.TraceID().String(), nil
	}

	return "", ErrNoCorrelationID
}

// WithExpectReply sets whether the mutation should wait for a reply containing workflow IDs.
// When true, the resolver will block until the outbox handler delivers workflow details
// or the timeout expires.
func WithExpectReply(ctx context.Context, expect bool) context.Context {
	return context.WithValue(ctx, expectReplyKey{}, expect)
}

// ExpectsReply returns whether the mutation should wait for a reply.
// Defaults to false if not set.
func ExpectsReply(ctx context.Context) bool {
	if expect, ok := ctx.Value(expectReplyKey{}).(bool); ok {
		return expect
	}
	return false
}

// WithExtraSearchAttribute adds a workflow search attribute to the context.
// These attributes are merged with auto-computed attributes from entity mixin fields.
// Use this to add custom search attributes that cannot be derived from the entity.
func WithExtraSearchAttribute(ctx context.Context, name, value string) context.Context {
	existing := ExtraSearchAttributesFromContext(ctx)
	merged := make(map[string]string, len(existing)+1)

	for k, v := range existing {
		merged[k] = v
	}
	merged[name] = value

	return context.WithValue(ctx, extraSearchAttributesKey{}, merged)
}

// ExtraSearchAttributesFromContext returns the extended search attributes from the context.
// Returns nil if no attributes were set.
func ExtraSearchAttributesFromContext(ctx context.Context) map[string]string {
	if attrs, ok := ctx.Value(extraSearchAttributesKey{}).(map[string]string); ok {
		return attrs
	}
	return nil
}
