package tenant

import "errors"

var (
	ErrInvalidTenantID    = errors.New("invalid tenant ID")
	ErrNoUser             = errors.New("no user found")
	ErrNoAccessToTenantID = errors.New("no access to tenant ID")
)
