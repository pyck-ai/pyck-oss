package tenant

import "errors"

var (
	ErrInvalidTenantID    = errors.New("invalid tenant ID")
	ErrNoUser             = errors.New("no user found")
	ErrNoAccessToTenantID = errors.New("no access to tenant ID")
	// ErrTenantDisabled is returned (403) when a tenant explicitly named in
	// X-Pyck-Tenant-Id is disabled.
	ErrTenantDisabled = errors.New("tenant is disabled")
)
