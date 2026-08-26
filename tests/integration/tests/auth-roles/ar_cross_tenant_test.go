//go:build integration

package authroles

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
)

// This file closes the write-path counterpart to TestServiceRoleIsTenantScoped
// (ar_gate_loopholes_test.go), which proves the *gate* is tenant-scoped using a
// cross-tenant AUTHORIZATION (a user granted a role in a second tenant without
// being a home-org member there). The gate can do that because it reads the
// token's introspected ProjectRoles directly. assignRoles/removeRoles/
// userServiceRoles cannot: they resolve the target user via a
// management.users row scoped to the tenant (resolveTenantUser), and the
// zitadel-sync workflow only ever projects a user into their HOME org's
// tenant (FetchZitadelUsersActivity lists org membership, not project
// authorizations). So a user who merely holds an authorization in tenant B —
// never having been synced there — cannot be a valid mutation target in B,
// however real that authorization is. TestAssignRolesCannotTargetNonHomeTenantMember
// and its removeRoles counterpart prove exactly that: the write endpoints are
// tenant-isolated for a stronger reason than an explicit check — the target
// identity structurally does not resolve outside its home tenant.

// adminClientForTenant returns a management client scoped to tenantID, using
// the system service token (ROLE_SYSTEM satisfies the admin gate for any
// tenant). Mirrors adminClient() but for a tenant other than the suite's own.
func (s *AuthRolesSuite) adminClientForTenant(tenantID string) managementapi.Client {
	return gateway.NewClientForTenant(s.Cfg, s.Cfg.ServiceToken, tenantID)
}

// TestAssignRolesCannotTargetNonHomeTenantMember provisions a user whose home
// org is tenant A, grants them a real authorization (ladder + service role) in
// tenant B, and confirms assignRoles scoped to tenant B still cannot target
// them by their real (tenant-A) management id — proving a genuine cross-tenant
// authorization is not enough to make resolveTenantUser find them in B. Using
// their real id, rather than an arbitrary uuid, additionally rules out a
// resolver that resolved the target by id alone while ignoring the tenant
// scope.
func (s *AuthRolesSuite) TestAssignRolesCannotTargetNonHomeTenantMember() {
	tenantB := s.secondaryTenant()

	u := s.provisionUserInTenant(s.tenant, "writer") // home org = suite tenant (A)
	s.grantInTenant(tenantB, u.idpID, "writer", serviceroles.Inventory.String())

	userInA, err := uuid.Parse(s.waitForSyncedUser(u.username))
	s.Require().NoError(err, "parse tenant-A management user id")

	tenantBID, err := uuid.Parse(tenantB.ID)
	s.Require().NoError(err, "parse tenant B id")

	_, err = s.adminClientForTenant(tenantB.ID).AssignRoles(s.Ctx, managementapi.AssignRolesArgs{
		Input: managementmodel.AssignRolesInput{
			TenantID: tenantBID,
			UserID:   userInA, // real id, but never synced into tenant B
			Roles:    []string{serviceroles.Picking.String()},
		},
	})
	s.Require().ErrorContains(err, "user not found in tenant",
		"assignRoles must not be able to target a user who only holds an authorization in tenant B, never having been synced there")
}

// TestRemoveRolesCannotTargetNonHomeTenantMember is the removeRoles mirror of
// TestAssignRolesCannotTargetNonHomeTenantMember.
func (s *AuthRolesSuite) TestRemoveRolesCannotTargetNonHomeTenantMember() {
	tenantB := s.secondaryTenant()

	u := s.provisionUserInTenant(s.tenant, "writer", serviceroles.Inventory.String()) // home org = A
	s.grantInTenant(tenantB, u.idpID, "writer", serviceroles.Inventory.String())      // real authorization in B

	userInA, err := uuid.Parse(s.waitForSyncedUser(u.username))
	s.Require().NoError(err, "parse tenant-A management user id")

	tenantBID, err := uuid.Parse(tenantB.ID)
	s.Require().NoError(err, "parse tenant B id")

	_, err = s.adminClientForTenant(tenantB.ID).RemoveRoles(s.Ctx, managementapi.RemoveRolesArgs{
		Input: managementmodel.RemoveRolesInput{
			TenantID: tenantBID,
			UserID:   userInA, // real id, but never synced into tenant B
			Roles:    []string{serviceroles.Inventory.String()},
		},
	})
	s.Require().ErrorContains(err, "user not found in tenant",
		"removeRoles must not be able to target a user who only holds an authorization in tenant B, never having been synced there")
}

// TestAssignRolesRejectsSoftDeletedTenant confirms assignRoles cannot be used
// against a disabled (soft-deleted) tenant — resolveTenantUser's
// DeletedAtIsNil filter must make it behave as "tenant not found", closing off
// a path to keep granting access after a tenant has been disabled.
func (s *AuthRolesSuite) TestAssignRolesRejectsSoftDeletedTenant() {
	// This test soft-deletes its tenant as part of the assertion, so it must
	// NOT use the suite's shared secondary tenant — it registers a disposable
	// one of its own.
	victim, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	s.Require().NoError(err, "register victim tenant")
	s.DeferTenantCleanup(victim.ID)

	u := s.provisionUserInTenant(victim, "writer")
	userID, err := uuid.Parse(s.waitForSyncedUserInTenant(victim.ID, u.username))
	s.Require().NoError(err, "parse victim management user id")

	s.Require().NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, victim.ID), "soft-delete victim tenant")

	victimID, err := uuid.Parse(victim.ID)
	s.Require().NoError(err, "parse victim tenant id")

	_, err = s.adminClientForTenant(victim.ID).AssignRoles(s.Ctx, managementapi.AssignRolesArgs{
		Input: managementmodel.AssignRolesInput{
			TenantID: victimID,
			UserID:   userID,
			Roles:    []string{serviceroles.Inventory.String()},
		},
	})
	// Deliberately the same error a never-existing tenant yields: a
	// soft-deleted tenant must be indistinguishable from an unknown one.
	s.Require().ErrorContains(err, "tenant not found",
		"assignRoles must reject a soft-deleted tenant")
}
