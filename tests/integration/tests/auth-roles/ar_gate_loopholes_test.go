//go:build integration

package authroles

import (
	"net/http"
	"time"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TestServiceRoleAloneIsInsufficient closes the "I hold inventory_service, so I
// can reach inventory" loophole. Service roles are tracked separately from the
// reader/writer/admin ladder: a service role grants no ladder access, so a user
// holding only a service role is stopped before the gate matters.
//
// Two layers enforce this, depending on how the request names its tenant:
//   - With an explicit X-Pyck-Tenant-Id, the tenant middleware rejects the
//     request (400) because the user lacks even reader in that tenant.
//   - With no tenant header, the operative set defaults to the user's ladder
//     tenants — empty here — and the gate fails closed (403) on a non-system
//     user with no operative tenant.
//
// Either way: a service role without a ladder role opens nothing.
func (s *AuthRolesSuite) TestServiceRoleAloneIsInsufficient() {
	u := s.provisionUserDetailed(serviceroles.Inventory.String()) // no ladder role

	s.Run("explicit tenant rejected at tenant layer", func() {
		status := s.callService(s.Cfg.InventoryURL, u.token, s.tenant.ID)
		s.Equal(http.StatusBadRequest, status,
			"a service role grants no tenant access, so the tenant middleware rejects the explicit tenant")
	})

	s.Run("no tenant header fails closed at the gate", func() {
		status := s.callService(s.Cfg.InventoryURL, u.token, "")
		s.Equal(http.StatusForbidden, status,
			"with no ladder tenant the operative set is empty; the gate denies a non-system user")
	})
}

// TestNoRolesDenied is the baseline: a user with NO roles at all — no ladder
// role and no service role, not even a project grant — cannot reach a gated
// service, however it names the tenant. With an explicit tenant the request is
// rejected at the tenant layer (the user lacks even reader); with no tenant
// header the operative set is empty and the gate fails closed. Access is denied
// either way — a role-less authenticated user gets nothing.
func (s *AuthRolesSuite) TestNoRolesDenied() {
	u := s.provisionUserNoRoles()

	s.Run("explicit tenant rejected at tenant layer", func() {
		s.Equal(http.StatusBadRequest, s.callService(s.Cfg.InventoryURL, u.token, s.tenant.ID),
			"a user with no roles has no tenant access, so the tenant middleware rejects it")
	})

	s.Run("no tenant header fails closed at the gate", func() {
		s.Equal(http.StatusForbidden, s.callService(s.Cfg.InventoryURL, u.token, ""),
			"a role-less authenticated user has an empty operative set; the gate denies it")
	})
}

// TestSystemTokenBypassesGateWithEmptyTenantSet covers the gate branch where a
// system caller is allowed even with no operative tenant — the fail-closed
// empty-set rule applies only to non-system users. Sending no tenant header
// yields an empty operative set for the system token (its ladder map is empty).
func (s *AuthRolesSuite) TestSystemTokenBypassesGateWithEmptyTenantSet() {
	for _, role := range serviceroles.All {
		role := role
		s.Run(role.String(), func() {
			status := s.callService(s.serviceURL(role), s.Cfg.ServiceToken, "")
			s.Equal(http.StatusOK, status,
				"system token must bypass the %s gate even with no operative tenant", role)
		})
	}
}

// TestServiceRoleIsTenantScoped closes the cross-tenant loophole: a service role
// held in one tenant must not grant access to a gated service scoped to a
// different tenant, and the "all"/multi-tenant header expansion must not be
// usable to smuggle partial coverage past the gate (the gate requires the role
// in EVERY operative tenant).
//
// Setup: one user, member of two tenants — writer + inventory_service in the
// suite tenant (A), writer only in a second tenant (B). The token is minted
// after both grants so its introspection observes both orgs.
func (s *AuthRolesSuite) TestServiceRoleIsTenantScoped() {
	tenantB := s.secondaryTenant()

	u := s.provisionUserInTenant(s.tenant, "writer", serviceroles.Inventory.String())
	s.grantInTenant(tenantB, u.idpID, "writer")
	token := s.freshToken(u.idpID)

	// The tenant-B writer grant projects into introspection asynchronously.
	// Wait until the fresh token can see tenant B before probing: otherwise the
	// tenant middleware rejects tenant B with 400 (grant not yet visible) and
	// "all" collapses to tenant A only — both masking the gate's 403 that these
	// cases assert. Tenant A was granted during provisioning and is ready.
	_, err := tests.WaitTokenReady(s.Ctx, s.Cfg, token, tenantB.ID, 15*time.Second)
	s.Require().NoError(err, "tenant-B writer grant never projected into token")

	inv := s.Cfg.InventoryURL

	s.Run("tenant holding the role -> allowed", func() {
		s.Equal(http.StatusOK, s.callService(inv, token, s.tenant.ID),
			"inventory_service in tenant A grants access scoped to tenant A")
	})

	s.Run("tenant lacking the role -> denied", func() {
		s.Equal(http.StatusForbidden, s.callService(inv, token, tenantB.ID),
			"inventory_service in tenant A must not grant access scoped to tenant B")
	})

	s.Run("both tenants -> denied (missing in one)", func() {
		s.Equal(http.StatusForbidden, s.callService(inv, token, s.tenant.ID+","+tenantB.ID),
			"the gate requires the role in every operative tenant")
	})

	s.Run("all tenants -> denied (missing in one)", func() {
		s.Equal(http.StatusForbidden, s.callService(inv, token, "all"),
			`"all" expands to every tenant the user belongs to; a gap in one denies`)
	})
}

// TestServiceRoleCoveredInBothTenantsAllowsMultiHeader is the positive control
// for TestServiceRoleIsTenantScoped: it proves the "every operative tenant"
// check isn't simply "deny whenever more than one tenant is named" — a user
// holding the role in BOTH tenants must be let through on "A,B" and "all",
// exactly as it is for a single tenant. Without this, a gate bug that denied
// all multi-tenant requests unconditionally would pass every case in the
// sibling test (all of them assert 403) while silently breaking every
// legitimate multi-tenant caller.
func (s *AuthRolesSuite) TestServiceRoleCoveredInBothTenantsAllowsMultiHeader() {
	tenantB := s.secondaryTenant()

	u := s.provisionUserInTenant(s.tenant, "writer", serviceroles.Inventory.String())
	s.grantInTenant(tenantB, u.idpID, "writer", serviceroles.Inventory.String())
	token := s.freshToken(u.idpID)

	_, err := tests.WaitTokenReady(s.Ctx, s.Cfg, token, tenantB.ID, 15*time.Second)
	s.Require().NoError(err, "tenant-B grant never projected into token")

	inv := s.Cfg.InventoryURL

	s.Run("both tenants explicit -> allowed", func() {
		s.Equal(http.StatusOK, s.callService(inv, token, s.tenant.ID+","+tenantB.ID),
			"the role is held in every operative tenant, so the gate must allow it")
	})

	s.Run("all tenants -> allowed", func() {
		s.Equal(http.StatusOK, s.callService(inv, token, "all"),
			`"all" must allow when the role is held in every tenant it expands to`)
	})
}
