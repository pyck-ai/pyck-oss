//go:build integration

package tenants

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
)

// tenantListCap bounds the tenants listing. The connection caps `first` at
// 200 rows, which is well above what a test stack holds.
const tenantListCap = 200

// TestTenantIdentity_DerivesFromOrgRefForEveryTenant checks the one
// invariant that decides whether a token reaches its tenant:
// tenant.id == ComputeUUID(audience, tenant.idp_org_ref). Every service
// derives the tenant of a request from the token's organization alone
// (none of them can read the management database) so a row that breaks it
// is a tenant nobody can authenticate into.
//
// It runs over every tenant the stack holds, not only the one this test
// registers: rows created before pyck began choosing organization IDs
// carry the numeric IDs Zitadel minted, and the derivation must treat both
// shapes as the opaque strings they are. Running this on a stack built
// from an older revision and again after the change is the before/after
// proof that existing tenants are untouched.
func (s *TnRegisterSuite) TestTenantIdentity_DerivesFromOrgRefForEveryTenant() {
	r := s.Require()

	fresh, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(fresh.ID)

	c := gateway.NewClient(s.Cfg, s.Cfg.ServiceToken)
	first := tenantListCap
	resp, err := c.GetTenants(s.Ctx, managementapi.GetTenantsArgs{First: &first})
	r.NoError(err)

	var chosen, minted int
	seenFresh := false
	for _, edge := range resp.GetTenants().GetEdges() {
		if edge == nil || edge.GetNode() == nil {
			continue
		}
		node := edge.GetNode()

		r.Equal(authn.ComputeUUID(s.Cfg.ZitadelAudience, node.GetIdpOrgRef()).String(), node.GetID(),
			"tenant %q must be derivable from its organization %q", node.GetName(), node.GetIdpOrgRef())

		if _, err := uuid.Parse(node.GetIdpOrgRef()); err == nil {
			chosen++
		} else {
			minted++
		}
		seenFresh = seenFresh || node.GetID() == fresh.ID
	}

	r.True(seenFresh, "the tenant registered by this test must be in the listing")
	r.Positive(chosen, "a registration must produce a pyck-chosen organization id")
	s.T().Logf("tenant identities verified: %d with a pyck-chosen organization id, %d with a Zitadel-minted one", chosen, minted)
}
