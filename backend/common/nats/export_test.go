package nats

// Test-only exports of the tenant access decisions and the auth callout.
var (
	NewAuthHandler         = newAuthService
	ResolveTenant          = resolveTenant
	StateChangeDenyPattern = stateChangeDenyPattern
	TenantPermissions      = tenantPermissions
	ErrTenantNotSingle     = errTenantNotSingle
)
