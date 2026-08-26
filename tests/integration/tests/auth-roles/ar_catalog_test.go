//go:build integration

package authroles

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
)

// TestServiceRolesCatalog confirms the assignable catalog returned by the
// serviceRoles query is exactly the serviceroles package's set — no more, no
// fewer. This keeps the API's advertised keys in lockstep with the enum that is
// the single source of truth.
func (s *AuthRolesSuite) TestServiceRolesCatalog() {
	c := s.adminClient()

	cat, err := c.GetServiceRoles(s.Ctx)
	s.Require().NoError(err, "serviceRoles")

	got := make([]string, 0, len(cat.GetServiceRoles()))
	for _, sr := range cat.GetServiceRoles() {
		got = append(got, sr.GetKey())
	}
	s.ElementsMatch(serviceroles.ServiceRoleStrings(), got,
		"catalog must advertise exactly the serviceroles enum keys")
}

// TestServiceRolesRequiresAdmin confirms the catalog is admin-only.
func (s *AuthRolesSuite) TestServiceRolesRequiresAdmin() {
	writer := s.provisionReadyWriter()
	c := gateway.NewClientForTenant(s.Cfg, writer, s.tenant.ID)

	_, err := c.GetServiceRoles(s.Ctx)
	s.Require().ErrorContains(err, "admin role required for serviceRoles",
		"a writer must not be able to read the service-role catalog")
}

// TestServiceRolesRequiresAuth confirms the catalog rejects an unauthenticated
// caller. The pinned substring is the gateway's 401 fetch error (see
// unauthedClient).
func (s *AuthRolesSuite) TestServiceRolesRequiresAuth() {
	_, err := s.unauthedClient().GetServiceRoles(s.Ctx)
	s.Require().ErrorContains(err, "401: Unauthorized",
		"an unauthenticated caller must not read the service-role catalog")
}

// TestUserServiceRolesReturnsEmptyForUserWithNoRoles confirms reading a synced
// user who holds no service roles returns an empty list, not an error — the
// "no authorization" branch.
func (s *AuthRolesSuite) TestUserServiceRolesReturnsEmptyForUserWithNoRoles() {
	userID := s.syncedUser() // writer only, no service roles
	s.Empty(s.userServiceRoles(s.adminClient(), userID),
		"a user with no service roles must read back as an empty list")
}

// TestUserServiceRolesRequiresAdmin confirms reading a user's roles is
// admin-only — a non-admin must not be able to enumerate other users' roles.
func (s *AuthRolesSuite) TestUserServiceRolesRequiresAdmin() {
	writer := s.provisionReadyWriter()
	c := gateway.NewClientForTenant(s.Cfg, writer, s.tenant.ID)

	_, err := c.GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
		Input: managementmodel.UserServiceRolesInput{TenantID: s.tenantUUID(), UserID: uuid.New()},
	})
	s.Require().ErrorContains(err, "admin role required for userServiceRoles",
		"a writer must not be able to read user service roles")
}

// TestUserServiceRolesRequiresAuth confirms an unauthenticated caller cannot
// read a user's service roles — mirrors TestServiceRolesRequiresAuth, which
// only covers the catalog query, not the per-user lookup. The pinned substring
// is the gateway's 401 fetch error (see unauthedClient).
func (s *AuthRolesSuite) TestUserServiceRolesRequiresAuth() {
	_, err := s.unauthedClient().GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
		Input: managementmodel.UserServiceRolesInput{TenantID: s.tenantUUID(), UserID: uuid.New()},
	})
	s.Require().ErrorContains(err, "401: Unauthorized",
		"an unauthenticated caller must not read user service roles")
}

// TestUserServiceRolesUnknownTenant confirms the read endpoint validates the
// tenant exists too — mirrors TestAssignRolesUnknownTenant for the read path.
func (s *AuthRolesSuite) TestUserServiceRolesUnknownTenant() {
	c := s.adminClient()
	_, err := c.GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
		Input: managementmodel.UserServiceRolesInput{TenantID: uuid.New(), UserID: uuid.New()},
	})
	s.Require().ErrorContains(err, "tenant not found", "unknown tenant must be rejected")
}

// TestUserServiceRolesUserNotInTenant closes an information-disclosure
// loophole: an admin of tenant A must not be able to read another tenant's
// user's service roles by pairing their own TenantID with a UserID that
// belongs elsewhere (or does not exist at all). resolveTenantUser looks the
// user up scoped to the given tenant, so a cross-tenant id must 404 rather
// than silently returning that user's roles in some other org.
func (s *AuthRolesSuite) TestUserServiceRolesUserNotInTenant() {
	c := s.adminClient()
	_, err := c.GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
		Input: managementmodel.UserServiceRolesInput{TenantID: s.tenantUUID(), UserID: uuid.New()},
	})
	s.Require().ErrorContains(err, "user not found in tenant",
		"a user id outside the tenant must be rejected, not silently resolved")
}

// TestUserServiceRolesFiltersLadder confirms userServiceRoles reports only
// per-service roles, never the privilege-ladder role the user also holds.
func (s *AuthRolesSuite) TestUserServiceRolesFiltersLadder() {
	u := s.provisionUserDetailed("writer", serviceroles.Inventory.String())
	userID, err := uuid.Parse(s.waitForSyncedUser(u.username))
	s.Require().NoError(err)

	roles := s.userServiceRoles(s.adminClient(), userID)
	s.Contains(roles, serviceroles.Inventory.String(), "service role must be reported")
	s.NotContains(roles, "writer", "ladder role must be filtered out")
}

// TestServiceCatalogHasURLs is a guard rather than a behavior test: every role
// in the serviceroles catalog must have a direct service URL wired into the
// test config, so adding a sixth gate role can never silently escape the gate
// coverage in gate_test.go.
func (s *AuthRolesSuite) TestServiceCatalogHasURLs() {
	for _, role := range serviceroles.All {
		s.NotEmpty(s.serviceURL(role), "service role %q must have a direct URL configured", role)
	}
}
