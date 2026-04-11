package authz

import "context"

// Authorizer defines the interface for authorization checks
type Authorizer interface {
	Enforce(ctx context.Context, resource, action string) (bool, error)
	MustEnforce(ctx context.Context, resource, action string) error
}