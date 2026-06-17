// Package tenantid provides the canonical context storage and telemetry field
// names for the tenant ID(s) a request operates on. It is intentionally a leaf
// package (no dependencies on other backend/common packages) so it can be
// imported from low-level packages such as log and otel without import cycles,
// mirroring the requestid package.
package tenantid

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

const (
	// AttributeKey is the OTel span attribute key carrying the tenant ID(s)
	// the request operates on. It is set on spans by the tenant span
	// processor in backend/common/otel so traces are filterable by tenant.
	AttributeKey = "tenant.id"

	// LogField is the structured log field name written by the zerolog hook
	// in backend/common/log. Snake_case follows the Elastic Common Schema and
	// the OTel logs data model — the convention used by most observability
	// tooling.
	LogField = "tenant_id"
)

type contextKey struct{}

// Context returns a context carrying the given tenant IDs.
func Context(ctx context.Context, tenantIDs ...uuid.UUID) context.Context {
	return context.WithValue(ctx, contextKey{}, tenantIDs)
}

// FromContext returns the tenant IDs stored in the context, or nil if none.
func FromContext(ctx context.Context) []uuid.UUID {
	if ctx == nil {
		return nil
	}

	if tenantIDs, ok := ctx.Value(contextKey{}).([]uuid.UUID); ok {
		return tenantIDs
	}

	return nil
}

// String renders the tenant IDs as a comma-separated string for use as a
// telemetry attribute or log field value. It returns an empty string when
// there are no tenant IDs.
func String(tenantIDs []uuid.UUID) string {
	switch len(tenantIDs) {
	case 0:
		return ""
	case 1:
		return tenantIDs[0].String()
	default:
		parts := make([]string, len(tenantIDs))
		for i, id := range tenantIDs {
			parts[i] = id.String()
		}

		return strings.Join(parts, ",")
	}
}
