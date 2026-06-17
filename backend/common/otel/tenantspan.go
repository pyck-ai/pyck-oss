package otel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/pyck-ai/pyck/backend/common/tenantid"
)

// tenantSpanProcessor stamps the tenant ID(s) found in the span's starting
// context onto the span as the tenant.id attribute, so traces are filterable
// by tenant in the observability backend.
//
// The tenant IDs are placed in the context by the tenant HTTP middleware
// (backend/common/tenant), which runs before resolver, database, and GraphQL
// operation spans are created — so those child spans inherit the attribute.
// The root server span created earlier (by otelchi) predates the middleware
// and is intentionally left untagged; its children carry the attribute, which
// is sufficient to resolve a trace by tenant.
type tenantSpanProcessor struct{}

func newTenantSpanProcessor() tenantSpanProcessor {
	return tenantSpanProcessor{}
}

func (tenantSpanProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	tenantIDs := tenantid.FromContext(parent)
	if len(tenantIDs) == 0 {
		return
	}

	s.SetAttributes(attribute.String(tenantid.AttributeKey, tenantid.String(tenantIDs)))
}

func (tenantSpanProcessor) OnEnd(sdktrace.ReadOnlySpan) {}

func (tenantSpanProcessor) Shutdown(context.Context) error { return nil }

func (tenantSpanProcessor) ForceFlush(context.Context) error { return nil }
