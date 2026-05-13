// Package requestid provides constants and helpers for the server-generated
// request ID propagated via OTel baggage. It is intentionally a leaf package
// (no dependencies on other backend/common packages) so it can be imported
// from low-level packages such as http, log, and events without import cycles.
package requestid

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/baggage"
)

const (
	// BaggageKey is the OTel baggage key carrying the server-generated
	// request ID. Propagates automatically across HTTP, NATS, and Temporal
	// because the W3C Baggage propagator is registered globally in
	// backend/common/otel.
	BaggageKey = "pyck.request-id"

	// LogField is the structured log field name written by the zerolog
	// hook in backend/common/log. Snake_case follows Elastic Common Schema
	// and the OTel logs data model — the convention used by most observability
	// tooling (Datadog, Splunk, Loki, etc.).
	LogField = "request_id"

	// HTTPHeader is the HTTP response header echoing the request ID back
	// to the client.
	HTTPHeader = "X-Request-ID"
)

// WithRequestID returns a context carrying the given request ID in OTel
// baggage. Existing baggage members are preserved.
func WithRequestID(ctx context.Context, requestID string) (context.Context, error) {
	member, err := baggage.NewMember(BaggageKey, requestID)
	if err != nil {
		return ctx, fmt.Errorf("create request-id baggage member: %w", err)
	}

	bag, err := baggage.FromContext(ctx).SetMember(member)
	if err != nil {
		return ctx, fmt.Errorf("set request-id baggage member: %w", err)
	}

	return baggage.ContextWithBaggage(ctx, bag), nil
}

// FromContext returns the request ID stored in OTel baggage, or an empty
// string if none is present.
func FromContext(ctx context.Context) string {
	return baggage.FromContext(ctx).Member(BaggageKey).Value()
}
