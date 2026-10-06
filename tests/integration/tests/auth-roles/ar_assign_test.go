//go:build integration

package authroles

import (
	"slices"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
)

// assignRoles is a thin wrapper that issues an assignRoles mutation for the
// suite tenant against the given client and target user.
func (s *AuthRolesSuite) assignRoles(c managementapi.Client, userID uuid.UUID, roles ...string) (*managementapi.AssignRoles, error) {
	return c.AssignRoles(s.Ctx, managementapi.AssignRolesArgs{
		Input: managementmodel.AssignRolesInput{
			TenantID: s.tenantUUID(),
			UserID:   userID,
			Roles:    roles,
		},
	})
}

// TestAssignRolesAssignsAndReadsBack assigns a single service role and confirms
// the response and userServiceRoles both reflect it.
func (s *AuthRolesSuite) TestAssignRolesAssignsAndReadsBack() {
	c := s.adminClient()
	userID := s.syncedUser()

	resp, err := s.assignRoles(c, userID, serviceroles.Picking.String())
	s.Require().NoError(err, "assignRoles")
	s.Contains(resp.GetAssignRoles().GetRoles(), serviceroles.Picking.String(),
		"response should reflect the assigned role")

	s.waitForServiceRoles(c, userID, "assigned role should be readable back",
		func(roles []string) bool { return slices.Contains(roles, serviceroles.Picking.String()) })
}

// TestAssignRolesIsAdditive assigns two roles in separate calls and confirms the
// second does not drop the first.
func (s *AuthRolesSuite) TestAssignRolesIsAdditive() {
	c := s.adminClient()
	userID := s.syncedUser()

	_, err := s.assignRoles(c, userID, serviceroles.Picking.String())
	s.Require().NoError(err, "assign first role")

	resp, err := s.assignRoles(c, userID, serviceroles.Inventory.String())
	s.Require().NoError(err, "assign second role")

	roles := resp.GetAssignRoles().GetRoles()
	s.Contains(roles, serviceroles.Picking.String(), "first role must be preserved")
	s.Contains(roles, serviceroles.Inventory.String(), "second role must be added")
}

// TestAssignRolesIsIdempotent assigns the same role twice and confirms it is not
// duplicated.
func (s *AuthRolesSuite) TestAssignRolesIsIdempotent() {
	c := s.adminClient()
	userID := s.syncedUser()

	_, err := s.assignRoles(c, userID, serviceroles.Picking.String())
	s.Require().NoError(err, "first assign")
	resp, err := s.assignRoles(c, userID, serviceroles.Picking.String())
	s.Require().NoError(err, "second assign")

	count := 0
	for _, r := range resp.GetAssignRoles().GetRoles() {
		if r == serviceroles.Picking.String() {
			count++
		}
	}
	s.Equal(1, count, "re-assigning the same role must not duplicate it")
}

// TestAssignRolesRejectsInvalidRoleKeys covers every input-validation
// rejection of assignRoles: role keys outside the per-service catalog (ladder
// keys, arbitrary strings), an empty list, and a mix of valid and invalid
// keys. All cases target the same synced user; after each rejected call the
// user's service roles are read back and must still be empty, proving a
// rejected request applies nothing — in particular that a valid key cannot
// slip through alongside an invalid one.
//
// Ladder, unknown and mixed keys share one resolver error (any key outside
// the service-role catalog trips the same validation); only the empty list
// has its own.
func (s *AuthRolesSuite) TestAssignRolesRejectsInvalidRoleKeys() {
	c := s.adminClient()
	userID := s.syncedUser()

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
			_, err := s.assignRoles(c, userID, tc.roles...)
			s.Require().ErrorContains(err, tc.wantErr)
			s.Empty(s.userServiceRoles(c, userID),
				"a rejected assignRoles call must not have applied any role")
		})
	}
}

// TestAssignRolesRequiresAuth confirms an unauthenticated caller cannot assign
// roles. The invalid token is rejected with 401 before the resolver runs (see
// unauthedClient), so the pinned substring is the gateway's fetch error rather
// than the resolver's own authentication-guard message.
func (s *AuthRolesSuite) TestAssignRolesRequiresAuth() {
	_, err := s.assignRoles(s.unauthedClient(), uuid.New(), serviceroles.Picking.String())
	s.Require().ErrorContains(err, "401: Unauthorized",
		"an unauthenticated caller must not assign service roles")
}

// TestAssignRolesRequiresAdmin confirms a non-admin caller (writer) cannot
// assign roles.
func (s *AuthRolesSuite) TestAssignRolesRequiresAdmin() {
	writer := s.provisionReadyWriter()
	c := gateway.NewClientForTenant(s.Cfg, writer, s.tenant.ID)

	_, err := s.assignRoles(c, s.syncedUser(), serviceroles.Picking.String())
	s.Require().ErrorContains(err, "admin role required for assignRoles",
		"a writer must not be able to assign service roles")
}

// TestAssignRolesUnknownTenant confirms assigning against a non-existent tenant
// is rejected.
func (s *AuthRolesSuite) TestAssignRolesUnknownTenant() {
	c := s.adminClient()
	_, err := c.AssignRoles(s.Ctx, managementapi.AssignRolesArgs{
		Input: managementmodel.AssignRolesInput{
			TenantID: uuid.New(), // no such tenant
			UserID:   uuid.New(),
			Roles:    []string{serviceroles.Picking.String()},
		},
	})
	s.Require().ErrorContains(err, "tenant not found", "unknown tenant must be rejected")
}

// TestAssignRolesUserNotInTenant confirms assigning to a user that is not a
// member of the tenant is rejected.
func (s *AuthRolesSuite) TestAssignRolesUserNotInTenant() {
	c := s.adminClient()
	_, err := c.AssignRoles(s.Ctx, managementapi.AssignRolesArgs{
		Input: managementmodel.AssignRolesInput{
			TenantID: s.tenantUUID(),
			UserID:   uuid.New(), // not a member of the tenant
			Roles:    []string{serviceroles.Picking.String()},
		},
	})
	s.Require().ErrorContains(err, "user not found in tenant",
		"a user outside the tenant must be rejected")
}
