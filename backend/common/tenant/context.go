package tenant

import (
	"context"

	"github.com/google/uuid"
)

type contextKeyTenantIDs struct{}

// Context adds tenant IDs to the context.
func Context(ctx context.Context, tenantIDs ...uuid.UUID) context.Context {
	return context.WithValue(ctx, contextKeyTenantIDs{}, &tenantIDs)
}

// ForContext retrieves tenant IDs from the context.
//
// Returns an empty UUID list if no tenant IDs are found.
func ForContext(ctx context.Context) []uuid.UUID {
	if tenantIDs, ok := ctx.Value(contextKeyTenantIDs{}).(*[]uuid.UUID); ok {
		return *tenantIDs
	}

	return []uuid.UUID{}
}
