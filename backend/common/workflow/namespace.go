package workflow

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
)

// NamespaceGetter provides methods for resolving tenant IDs and Temporal namespaces.
type NamespaceGetter interface {
	GetNamespace(tenantID uuid.UUID) string
	GetTenantID(orgID string) uuid.UUID
	GetUserID(userID string) uuid.UUID
}

// NewNamespaceGetter creates a new NamespaceGetter that uses the provided
// Zitadel audience to compute tenant UUIDs from organization IDs.
//
//nolint:ireturn,iface // Returns interface by design — consumers depend on NamespaceGetter interface.
func NewNamespaceGetter(zitadelAudience string) NamespaceGetter {
	return &namespaceGetter{
		zitadelAudience: zitadelAudience,
	}
}

type namespaceGetter struct {
	zitadelAudience string
}

// GetNamespace returns the Temporal namespace for a tenant. Currently returns
// the tenant UUID as-is; this will be updated when multi-tenancy support for
// Temporal namespaces is implemented.
func (g *namespaceGetter) GetNamespace(tenantID uuid.UUID) string {
	return tenantID.String()
}

// GetTenantID derives a deterministic tenant UUID from a Zitadel organization ID.
// Currently uses a single-level mapping; this will be updated when multi-tenancy
// support is implemented.
func (g *namespaceGetter) GetTenantID(orgID string) uuid.UUID {
	return authn.ComputeUUID(g.zitadelAudience, orgID)
}

// GetUserID derives a deterministic user UUID from a Zitadel user ID.
func (g *namespaceGetter) GetUserID(userID string) uuid.UUID {
	return authn.ComputeUUID(g.zitadelAudience, userID)
}
