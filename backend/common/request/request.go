package request

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/requestid"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// ErrSingleTenantRequired is returned when a request must act on exactly one
// tenant but carries none or several.
var ErrSingleTenantRequired = errors.New("a single tenant must be selected")

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

// MutationTenantID returns the single tenant a mutation acts on: the
// acting set resolved by tenant.Middleware, which a mutation requires to
// hold exactly one tenant. That set is the X-Pyck-Tenant-Id selection
// after the role and liveness axes, so the target can be implicit — a
// caller whose expansion resolves to one live tenant mutates it without
// ever naming it, including when a second tenant was dropped for being
// disabled. The full header x tenant-state matrix is in
// docs/tenant-access-control.md.
//
// It returns ErrSingleTenantRequired when the set is not exactly one: an
// empty set (a system token under the "all" expansion, which holds no
// per-tenant roles) or several tenants.
func (rc RequestContext) MutationTenantID() (uuid.UUID, error) {
	if !rc.HasMutationTenantID() {
		return uuid.Nil, ErrSingleTenantRequired
	}

	return rc.tenantIDs[0], nil
}

// HasMutationTenantID reports whether the acting set names exactly one
// tenant, i.e. whether MutationTenantID has an unambiguous answer.
func (rc RequestContext) HasMutationTenantID() bool {
	return len(rc.tenantIDs) == 1
}

// RequestID returns the server-generated request ID propagated via OTel
// baggage. Stable across retries and independent of trace sampling.
func (rc RequestContext) RequestID() string {
	return rc.requestID
}

// RequireRole asserts the caller is authenticated AND holds role on
// the mutation tenant. Returns the tenant ID on success; otherwise an
// error wrapping ErrSingleTenantRequired or authn.ErrUnauthorized. op
// names the caller for error-message diagnosis (e.g. "deleteTenant").
func (rc RequestContext) RequireRole(role authn.Role, op string) (uuid.UUID, error) {
	if !rc.User().IsAuthenticated() {
		return uuid.Nil, fmt.Errorf("%w: authentication required for %s", authn.ErrUnauthorized, op)
	}
	tenantID, err := rc.MutationTenantID()
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w for %s", err, op)
	}
	if !rc.User().HasRole(role, tenantID) {
		return uuid.Nil, fmt.Errorf("%w: %s role required for %s", authn.ErrUnauthorized, role, op)
	}
	return tenantID, nil
}
