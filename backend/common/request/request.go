package request

import (
	"context"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/requestid"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

func Context(ctx context.Context, user *authn.User, tenantIDs ...uuid.UUID) context.Context {
	ctx = authn.Context(ctx, user)
	ctx = tenant.Context(ctx, tenantIDs...)
	return ctx
}

func ForContext(ctx context.Context) RequestContext {
	return RequestContext{
		logger:    log.ForContext(ctx),
		user:      authn.ForContext(ctx),
		tenantIDs: tenant.ForContext(ctx),
		requestID: requestid.FromContext(ctx),
	}
}

type RequestContext struct {
	logger    *log.Logger
	user      authn.User
	tenantIDs []uuid.UUID
	requestID string
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

// RequestID returns the server-generated request ID propagated via OTel
// baggage. Stable across retries and independent of trace sampling.
func (rc RequestContext) RequestID() string {
	return rc.requestID
}
