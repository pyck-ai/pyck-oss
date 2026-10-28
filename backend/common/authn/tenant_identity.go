package authn

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// NewTenantIdentity mints the identity pair of a tenant that does not exist
// yet: the Zitadel organization ID the tenant will be created under, and
// the tenant ID derived from it.
//
// The org ID is random (a v7 UUID string); the tenant ID is
// ComputeUUID(audience, orgID), the same derivation every token-to-tenant
// mapping in this package applies to the org claim of a login. Callers
// must persist orgID as tenant.idp_org_ref and create the Zitadel org with
// exactly that ID, otherwise tokens issued for the org resolve to a tenant
// that does not exist.
func NewTenantIdentity(audience string) (orgID string, tenantID uuid.UUID) {
	orgID = uuidgql.GenerateV7UUID().String()
	return orgID, ComputeUUID(audience, orgID)
}
