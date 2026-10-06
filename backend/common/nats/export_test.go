package nats

// Test-only exports of the tenant access decisions.
var (
	ResolveTenant          = resolveTenant
	StateChangeDenyPattern = stateChangeDenyPattern
	ErrTenantNotSingle     = errTenantNotSingle
)
