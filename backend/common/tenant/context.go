package tenant

import (
	"context"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/tenantid"
)

// Context adds tenant IDs to the context.
//
// Storage is delegated to the leaf tenantid package so low-level packages
// (log, otel) can read the tenant IDs for telemetry enrichment without
// importing this package (which would create an import cycle).
func Context(ctx context.Context, tenantIDs ...uuid.UUID) context.Context {
	return tenantid.Context(ctx, tenantIDs...)
}

// ForContext retrieves tenant IDs from the context.
//
// Returns an empty UUID list if no tenant IDs are found.
func ForContext(ctx context.Context) []uuid.UUID {
	if tenantIDs := tenantid.FromContext(ctx); tenantIDs != nil {
		return tenantIDs
	}

	return []uuid.UUID{}
}
