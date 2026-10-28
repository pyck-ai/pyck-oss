package authn

import "errors"

// ErrUnauthorized is the single rejection every authentication failure
// collapses into: the caller learns that the token was refused, never which
// of introspection, token state or the tenant liveness gate refused it.
var ErrUnauthorized = errors.New("unauthorized")

// ErrSystemCallerRequired refuses a surface the platform reserves for
// itself (tenant-lifecycle decisions and machine-owned projections) to
// every caller that is not the system user, a tenant's own admin
// included. Holding ROLE_ADMIN on a tenant must not be enough to reach
// one. Call sites wrap it with the operation name, so the refusal reads
// identically across services.
var ErrSystemCallerRequired = errors.New("system caller required")
