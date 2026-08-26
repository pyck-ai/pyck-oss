//go:build integration

package authroles

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
)

// removeRoles is a thin wrapper that issues a removeRoles mutation for the suite
// tenant against the given client and target user.
func (s *AuthRolesSuite) removeRoles(c managementapi.Client, userID uuid.UUID, roles ...string) (*managementapi.RemoveRoles, error) {
	return c.RemoveRoles(s.Ctx, managementapi.RemoveRolesArgs{
		Input: managementmodel.RemoveRolesInput{
			TenantID: s.tenantUUID(),
			UserID:   userID,
			Roles:    roles,
		},
	})
}

// userServiceRoles reads the target user's per-service roles in the suite
// tenant via the management endpoint.
func (s *AuthRolesSuite) userServiceRoles(c managementapi.Client, userID uuid.UUID) []string {
	s.T().Helper()
	read, err := c.GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
		Input: managementmodel.UserServiceRolesInput{TenantID: s.tenantUUID(), UserID: userID},
	})
	s.Require().NoError(err, "userServiceRoles")
	return read.GetUserServiceRoles()
}

// TestRemoveRoleRevokesGateAccess exercises the full add → access → remove →
// denied lifecycle through the management endpoint and the gate: a user holding
// inventory_service reaches the inventory service, and once the role is removed
// via removeRoles a fresh token is denied.
func (s *AuthRolesSuite) TestRemoveRoleRevokesGateAccess() {
	// Add the role (via provisioning) and confirm the gate lets the user in.
	u := s.provisionUserDetailed("writer", serviceroles.Inventory.String())
	s.Require().Equal(http.StatusOK, s.callService(s.Cfg.InventoryURL, u.token, s.tenant.ID),
		"user holding inventory_service should pass the inventory gate")

	mgmtUserID, err := uuid.Parse(s.waitForSyncedUser(u.username))
	s.Require().NoError(err)

	// Remove the role through the management endpoint (tenant-admin context).
	c := s.adminClient()
	resp, err := s.removeRoles(c, mgmtUserID, serviceroles.Inventory.String())
	s.Require().NoError(err, "removeRoles")
	s.NotContains(resp.GetRemoveRoles().GetRoles(), serviceroles.Inventory.String(),
		"response should no longer list inventory_service")

	// A fresh token (fresh introspection) is now denied by the gate. Poll with a
	// new token each attempt to absorb Zitadel projection lag on the revoked
	// grant — a token introspected mid-lag would cache the stale role.
	s.Eventually(func() bool {
		return s.callService(s.Cfg.InventoryURL, s.freshToken(u.idpID), s.tenant.ID) == http.StatusForbidden
	}, 15*time.Second, 1*time.Second,
		"inventory gate must deny the user once inventory_service is removed")
}

// TestRemoveRolesPreservesOtherServiceRoles confirms removeRoles is surgical:
// removing one service role leaves the user's other service roles intact.
func (s *AuthRolesSuite) TestRemoveRolesPreservesOtherServiceRoles() {
	c := s.adminClient()
	userID := s.syncedUser()

	_, err := s.assignRoles(c, userID, serviceroles.Inventory.String(), serviceroles.Picking.String())
	s.Require().NoError(err, "assign two roles")

	resp, err := s.removeRoles(c, userID, serviceroles.Inventory.String())
	s.Require().NoError(err, "removeRoles")

	remaining := resp.GetRemoveRoles().GetRoles()
	s.NotContains(remaining, serviceroles.Inventory.String(), "removed role must be gone")
	s.Contains(remaining, serviceroles.Picking.String(), "other service role must be preserved")
}

// TestRemoveRolesLastRoleLeavesEmpty confirms removing the only remaining role
// leaves the user with no service roles (the underlying Zitadel authorization
// is deleted rather than left empty).
func (s *AuthRolesSuite) TestRemoveRolesLastRoleLeavesEmpty() {
	c := s.adminClient()
	userID := s.syncedUser()

	_, err := s.assignRoles(c, userID, serviceroles.Picking.String())
	s.Require().NoError(err, "assign one role")

	resp, err := s.removeRoles(c, userID, serviceroles.Picking.String())
	s.Require().NoError(err, "removeRoles")
	s.Empty(resp.GetRemoveRoles().GetRoles(), "removing the last role leaves none")

	s.Empty(s.userServiceRoles(c, userID), "read-back confirms no service roles remain")
}

// TestRemoveRolesIdempotent confirms removing a role the user does not hold is a
// no-op rather than an error.
func (s *AuthRolesSuite) TestRemoveRolesIdempotent() {
	c := s.adminClient()
	userID := s.syncedUser() // holds no service roles

	resp, err := s.removeRoles(c, userID, serviceroles.Receiving.String())
	s.Require().NoError(err, "removing an unheld role must be a no-op, not an error")
	s.NotContains(resp.GetRemoveRoles().GetRoles(), serviceroles.Receiving.String())
}

// TestRemoveRolesRejectsInvalidRoleKeys covers every input-validation
// rejection of removeRoles, mirroring TestAssignRolesRejectsInvalidRoleKeys.
// The target user holds picking beforehand and must still hold it after every
// rejected call — critically for the mixed case: without that guarantee a
// caller could smuggle a real removal past validation by pairing it with a
// deliberately-invalid key if the resolver validated after mutating.
func (s *AuthRolesSuite) TestRemoveRolesRejectsInvalidRoleKeys() {
	c := s.adminClient()
	userID := s.syncedUser()

	_, err := s.assignRoles(c, userID, serviceroles.Picking.String())
	s.Require().NoError(err, "assign the role that must survive every rejected call")

	for _, tc := range []struct {
		name    string
		roles   []string
		wantErr string
	}{
		{"ladder key", []string{"admin"}, "only per-service roles may be assigned"},
		{"unknown key", []string{"totally_made_up_service"}, "only per-service roles may be assigned"},
		{"empty list", []string{}, "at least one role is required"},
		{"mixed valid and ladder", []string{serviceroles.Picking.String(), "admin"}, "only per-service roles may be assigned"},
	} {
		s.Run(tc.name, func() {
			_, err := s.removeRoles(c, userID, tc.roles...)
			s.Require().ErrorContains(err, tc.wantErr)
			s.Contains(s.userServiceRoles(c, userID), serviceroles.Picking.String(),
				"a rejected removeRoles call must not have removed anything")
		})
	}
}

// TestRemoveRolesUserNotInTenant confirms removeRoles validates tenant
// membership too (parity with assignRoles): a user outside the tenant is
// rejected before any Zitadel mutation.
func (s *AuthRolesSuite) TestRemoveRolesUserNotInTenant() {
	c := s.adminClient()
	_, err := c.RemoveRoles(s.Ctx, managementapi.RemoveRolesArgs{
		Input: managementmodel.RemoveRolesInput{
			TenantID: s.tenantUUID(),
			UserID:   uuid.New(), // not a member of the tenant
			Roles:    []string{serviceroles.Picking.String()},
		},
	})
	s.Require().ErrorContains(err, "user not found in tenant",
		"a user outside the tenant must be rejected")
}

// TestRemoveRolesRequiresAdmin confirms a non-admin caller (writer) cannot
// remove roles.
func (s *AuthRolesSuite) TestRemoveRolesRequiresAdmin() {
	writer := s.provisionReadyWriter()
	c := gateway.NewClientForTenant(s.Cfg, writer, s.tenant.ID)

	_, err := s.removeRoles(c, s.syncedUser(), serviceroles.Picking.String())
	s.Require().ErrorContains(err, "admin role required for removeRoles",
		"a writer must not be able to remove service roles")
}

// TestRemoveRolesRequiresAuth confirms an unauthenticated caller cannot remove
// roles — mirrors TestAssignRolesRequiresAuth for the remove endpoint. The
// pinned substring is the gateway's 401 fetch error (see unauthedClient).
func (s *AuthRolesSuite) TestRemoveRolesRequiresAuth() {
	_, err := s.removeRoles(s.unauthedClient(), uuid.New(), serviceroles.Picking.String())
	s.Require().ErrorContains(err, "401: Unauthorized",
		"an unauthenticated caller must not remove service roles")
}

// TestRemoveRolesUnknownTenant confirms removeRoles against a non-existent
// tenant is rejected — mirrors TestAssignRolesUnknownTenant.
func (s *AuthRolesSuite) TestRemoveRolesUnknownTenant() {
	c := s.adminClient()
	_, err := c.RemoveRoles(s.Ctx, managementapi.RemoveRolesArgs{
		Input: managementmodel.RemoveRolesInput{
			TenantID: uuid.New(), // no such tenant
			UserID:   uuid.New(),
			Roles:    []string{serviceroles.Picking.String()},
		},
	})
	s.Require().ErrorContains(err, "tenant not found", "unknown tenant must be rejected")
}
