//go:build integration

package tenants

import (
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TestRegisterTenant_AdminGateAndLifecycleGuards proves the tenant-admin
// contract against a live stack:
//
//  1. A fresh tenant's org is delegated the admin project role, so an
//     admin principal is provisionable inside it at all (a user grant
//     can only carry roles the org's project grant holds, and without
//     the delegation the grant fails with Errors.Project.Role.NotFound)
//     and its token passes the admin-gated surfaces on its own tenant:
//     management's userServiceRoles and inventory's
//     rebuildInventoryStock.
//  2. Holding admin on the tenant is not enough to undo or postpone an
//     operator's decision: restoreTenant and setTenantExpiry require a
//     system caller, so the tenant's own admin is refused and nothing
//     is written.
//  3. Holding admin on the tenant IS enough to disable it: deleteTenant
//     admits the tenant's own admin, and the disable reaches Zitadel.
//
// The order matters: proving the admin gate open first guarantees a
// refusal in step 2 is the system-caller guard, not the ROLE_ADMIN gate
// firing on a grant whose projection has not caught up yet; and step 3
// runs last because it retires the tenant every earlier step reads.
func (s *TnRegisterSuite) TestRegisterTenant_AdminGateAndLifecycleGuards() {
	r := s.Require()
	tenant := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status)

	// The service-gate roles ride along because the admin-gated surfaces
	// this asserts on span two subgraphs, and inventory refuses a token
	// without inventory_service before any resolver runs.
	admin, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates(authn.ROLE_ADMIN.String()))
	r.NoError(err, "an admin principal must be provisionable in a fresh tenant: its org's project grant must carry the admin role")

	// Pin the registration-granted user grant itself, not only its side
	// effects: rt.Status "done" proves Zitadel accepted *a* grant, and the
	// gate probe below runs as a self-provisioned machine user: a workflow
	// regression granting writer-only would pass both. The human admin's
	// OIDC token is unobtainable in this harness (CheckPassword yields a
	// session token), so assert on Zitadel's read side instead. Polled:
	// the authorization projection is eventually consistent.
	err = tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		keys, kerr := zitadelclient.GrantedRoleKeys(s.Ctx, s.ZConn, rt.IdpOrgRef, s.Cfg.ZitadelProjectID, tenant.AdminUsername)
		if kerr != nil {
			return kerr
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{authn.ROLE_ADMIN.String()}) {
			return fmt.Errorf("registration admin grant carries %v, want [admin]", keys)
		}
		return nil
	})
	r.NoError(err, "the registration workflow must grant the tenant's admin exactly admin: the ladder is cumulative on every consumer")

	// The registration workflow granted the tenant's human admin admin
	// and triggered the sync that projects it into management.users; its
	// row is the subject of the gated read below.
	syncedID, err := tests.WaitForSyncedUser(s.Ctx, s.Cfg, rt.ID, tenant.AdminUsername)
	r.NoError(err, "the tenant's admin user must be projected into management")
	adminUserID, err := uuid.Parse(syncedID)
	r.NoError(err, "parse synced admin user id")
	tenantID, err := uuid.Parse(rt.ID)
	r.NoError(err, "parse tenant id")

	// The admin-gated read must accept the tenant's own admin token.
	// Polled: ProvisionUserInTenant barriers on the token authenticating,
	// not on the admin grant being visible in its introspected roles.
	c := gateway.NewClientForTenant(s.Cfg, admin.PAT, rt.ID)
	var roles []string
	_, err = tests.WaitUntilAccepted(s.Ctx, "admin gate", 30*time.Second, func() error {
		resp, err := c.GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
			Input: managementmodel.UserServiceRolesInput{TenantID: tenantID, UserID: adminUserID},
		})
		if err != nil {
			return err
		}
		roles = resp.GetUserServiceRoles()
		return nil
	})
	r.NoError(err, "userServiceRoles must accept a tenant-admin token on its own tenant")
	r.Empty(roles, "the registration admin holds ladder roles only, never service roles")

	// The admin role reaches beyond management: rebuildInventoryStock is
	// gated on it too, so a fresh tenant's admin must be admitted there as
	// well. Asserted against this tenant, whose movement history is empty
	// and disposable: the rebuild deletes and recomputes every stock row
	// of whatever tenant it runs in.
	inv := gateway.NewInventoryClientForTenant(s.Cfg, admin.PAT, rt.ID)
	rebuild, err := inv.RebuildInventoryStock(s.Ctx)
	r.NoError(err, "rebuildInventoryStock must accept a tenant-admin token on its own tenant")
	r.True(rebuild.GetRebuildInventoryStock().GetSuccess(), "the rebuild of an empty history must succeed")

	// The two operator-owned lifecycle mutations: the expiry is the
	// operator's deadline and the restore undoes the operator's disable,
	// so a tenant admin able to move either would outlive the operator's
	// call. Both are asserted through the token that just passed the
	// admin gate, so the refusal is attributable to the system-caller
	// guard alone, and against an active tenant, so the refusal comes
	// from the resolver and not from the tenant middleware's denial of a
	// disabled tenant in the header.
	expiry := time.Now().UTC().Add(24 * time.Hour)
	err = gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, admin.PAT, rt.ID, &expiry)
	r.Error(err, "a tenant admin must not be able to schedule its own tenant's disable")
	r.ErrorContains(err, "system caller required")

	err = gateway.RestoreTenantAs(s.Ctx, s.Cfg, admin.PAT, rt.ID, nil)
	r.Error(err, "a tenant admin must not be able to undo an operator's disable")
	r.ErrorContains(err, "system caller required")

	// Neither refusal wrote: no expiry was scheduled and the tenant is
	// still the active, never-expiring row the registration left behind.
	expiresAt, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.Nil(expiresAt, "a refused setTenantExpiry must leave the tenant without an expiry")
	status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.Equal("done", status, "the refused mutations must leave the tenant active")

	// A tenant may disable itself: the soft-delete design keeps only the
	// way back with the operator. The disable is real once its event has
	// deactivated the org: the second vantage point, since the row alone
	// would also read deleted if the event never dispatched.
	err = gateway.DeleteTenantAs(s.Ctx, s.Cfg, admin.PAT, rt.ID)
	r.NoError(err, "a tenant admin must be able to disable its own tenant")
	_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "INACTIVE", 30*time.Second)
	r.NoError(err, "the admin's disable must dispatch DisableTenantWorkflow and deactivate the org")
}
