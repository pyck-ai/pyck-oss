package request

import (
	"context"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"go.opentelemetry.io/otel/trace"
)

func Context(ctx context.Context, user *authn.User, tenantIDs ...uuid.UUID) context.Context {
	ctx = authn.Context(ctx, user)
	ctx = tenant.Context(ctx, tenantIDs...)
	return ctx
}

func ForContext(ctx context.Context) RequestContext {
	logger := log.ForContext(ctx)
	trace := trace.SpanFromContext(ctx).SpanContext()

	if !trace.HasTraceID() {
		logger.Warn().
			Msg("request context created without trace ID")
	}

	var traceID string

	if trace.HasTraceID() {
		traceID = trace.TraceID().String()
	}

	return RequestContext{
		logger:    logger,
		user:      authn.ForContext(ctx),
		tenantIDs: tenant.ForContext(ctx),
		traceID:   traceID,
	}
}

type RequestContext struct {
	logger    *log.Logger
	user      authn.User
	tenantIDs []uuid.UUID
	traceID   string
}

// Log returns the logger for the request context.
func (rc RequestContext) Log() *log.Logger {
	return rc.logger
}

// User returns the user for the request context.
func (rc RequestContext) User() authn.User {
	return rc.user
}

// QueryTenantID returns the tenant ID for the request context.
//
// Deprecated: Use TenantIDs instead.
func (rc RequestContext) QueryTenantIDs() []uuid.UUID {
	return rc.tenantIDs
}

// TenantIDs returns the tenant IDs for the request context.
func (rc RequestContext) TenantIDs() []uuid.UUID {
	return rc.tenantIDs
}

// MutationTenantID returns the tenant ID for mutations in the request context.
func (rc RequestContext) MutationTenantID() uuid.UUID {
	if rc.HasMutationTenantID() {
		return rc.tenantIDs[0]
	}

	rc.Log().Panic().
		Any("user", rc.User()).
		Msg("cannot determine mutation tenant ID")

	panic("cannot determine mutation tenant ID.")
}

func (rc RequestContext) HasMutationTenantID() bool {
	return len(rc.tenantIDs) == 1
}

func (rc RequestContext) TraceID() string {
	return rc.traceID
}
