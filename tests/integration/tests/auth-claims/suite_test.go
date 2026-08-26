//go:build integration

// Package tenantclaim verifies that pyck_tenant_id reaches machine-user
// OIDC tokens as a plain top-level claim, end to end:
//
//	registerTenant → Zitadel sub-org + machine key
//	→ private_key_jwt token request fires Zitadel's Actions v2 Execution
//	→ pyck-management /webhook/zitadel/actions/pre-token returns append_claims
//	→ introspection shows pyck_tenant_id = ComputeUUID(audience, orgID)
//
// Consolidates myworkflow's verify-tenant-claim and
// verify-tenant-action-claim CLI checks into one automated suite. The
// interactive human-OIDC variant (myworkflow/test_authcode_claim.sh) is
// deliberately not ported — it needs a browser login and can't run
// unattended.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/auth-claims/...
package tenantclaim

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestTenantClaim is the runner for the tenant-claim suite.
func TestTenantClaim(t *testing.T) {
	suite.Run(t, new(TenantClaimSuite))
}
