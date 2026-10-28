//go:build integration

package tenants

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TnLifecycleRolesSuite pins who may drive the three tenant-lifecycle
// mutations (deleteTenant, restoreTenant, setTenantExpiry) from every
// vantage point a real caller has: a writer and an admin homed in the
// tenant, an admin holding the role through a cross-tenant grant, and
// the system principal, against an active tenant, a tenant the admin
// disabled itself, and a tenant the expiry sweep disabled. The contract
// under test:
//
//   - ROLE_ADMIN on the target is required for all three; a writer is
//     refused everywhere, and an admin of another tenant cannot even
//     name the target: the header axis refuses a tenant the caller
//     holds no grant into.
//   - deleteTenant admits any ROLE_ADMIN holder on the target (the
//     tenant's own admin and a cross-tenant admin alike) and the
//     disable reaches Zitadel.
//   - restoreTenant and setTenantExpiry are system-only: an admin is
//     refused on an active tenant by the resolver, and on a disabled
//     tenant earlier still, by the liveness axis that refuses every
//     non-system token naming a disabled tenant.
//   - Only the system principal brings a disabled tenant back, after
//     which the same admin tokens work again.
//
// Each test registers its own tenants: the mutations under test retire
// the tenant they run against, so nothing here may touch a shared one.
type TnLifecycleRolesSuite struct {
	tests.Base
}

// lifecycleTokenTimeout bounds the propagation waits on token acceptance
// and denial: the liveness verdict is cached per service, so a flip is
// only observable once the eviction fan-out or the TTL backstop lands.
const lifecycleTokenTimeout = 90 * time.Second

// requireSystemCallerRefusal is the poll predicate for "the admin gate is
// open": a fresh PAT authenticates before its introspected roles carry
// the admin grant, and until then the operator-owned mutations refuse it
// with the same "admin role required" a writer gets. Only once the
// refusal is the system-caller guard's is every later refusal
// attributable to its own rule. A success would mean the guard is gone.
func requireSystemCallerRefusal(err error) error {
	switch {
	case err == nil:
		return errors.New("admin was admitted to a system-only mutation")
	case strings.Contains(err.Error(), "system caller required"):
		return nil
	default:
		return fmt.Errorf("admin gate not open yet: %w", err)
	}
}

// provisionAdmin mints an admin PAT (plus the service gates) in the tenant
// and waits for it to authenticate.
func (s *TnLifecycleRolesSuite) provisionAdmin(rt *gateway.RegisteredTenant) string {
	s.T().Helper()
	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates(authn.ROLE_ADMIN.String()))
	s.Require().NoError(err, "provision an admin PAT in tenant %s", rt.ID)
	return p.PAT
}

// requireRefusal asserts a lifecycle call failed with the given resolver
// message: the refusal the caller reads, so it must be the resolver's
// own and not a transport fault that happens to fail the call.
func (s *TnLifecycleRolesSuite) requireRefusal(err error, want, what string) {
	s.T().Helper()
	s.Require().Error(err, "%s must be refused", what)
	s.Require().ErrorContains(err, want, "%s must be refused by the resolver with %q", what, want)
}

// requireLivenessDenial asserts a lifecycle call was refused before any
// resolver ran, by the axis that denies a non-system token naming a
// disabled tenant.
func (s *TnLifecycleRolesSuite) requireLivenessDenial(err error, what string) {
	s.T().Helper()
	s.Require().Error(err, "%s must be denied", what)
	s.Require().True(tests.IsAuthDenial(err.Error()), "%s must be an auth denial of the disabled tenant, got: %v", what, err)
}

// TestOwnTenantRoles walks one tenant through the matrix with tokens
// homed in it: a writer is refused everywhere, the admin may only
// disable, and the system principal is the only way back.
func (s *TnLifecycleRolesSuite) TestOwnTenantRoles() {
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status)

	writer, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision a writer PAT")
	adminPAT := s.provisionAdmin(rt)
	expiry := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	// Polled: the PAT authenticates before its introspected roles are
	// guaranteed to carry the grant, and a not-yet-visible role reads as
	// the same "admin role required" a writer gets. Waiting for the
	// admin gate to open first makes every refusal below attributable
	// to its own rule.
	_, err = tests.WaitUntilAccepted(s.Ctx, "admin gate", lifecycleTokenTimeout, func() error {
		return requireSystemCallerRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, &expiry))
	})
	r.NoError(err, "the admin PAT must pass the role gate and stop at the system-caller guard")

	s.Run("writer is refused every lifecycle mutation", func() {
		s.requireRefusal(gateway.DeleteTenantAs(s.Ctx, s.Cfg, writer.PAT, rt.ID), "admin role required", "writer deleteTenant")
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, writer.PAT, rt.ID, &expiry), "admin role required", "writer setTenantExpiry")
		s.requireRefusal(gateway.RestoreTenantAs(s.Ctx, s.Cfg, writer.PAT, rt.ID, nil), "admin role required", "writer restoreTenant")
	})

	s.Run("admin is refused the operator-owned mutations on its active tenant", func() {
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, &expiry), "system caller required", "admin setTenantExpiry")
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "system caller required", "admin clearing the expiry")
		s.requireRefusal(gateway.RestoreTenantAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "system caller required", "admin restoreTenant")
	})

	s.Run("nothing was written by the refusals", func() {
		status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
		r.NoError(err)
		r.Equal("done", status)
		got, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
		r.NoError(err)
		r.Nil(got, "no refused call may have scheduled an expiry")
	})

	s.Run("system sets the expiry and the admin cannot move it", func() {
		r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, &expiry))
		later := expiry.Add(24 * time.Hour)
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, &later), "system caller required", "admin postponing the expiry")
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "system caller required", "admin clearing the expiry")
		got, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
		r.NoError(err)
		r.NotNil(got)
		r.True(got.Equal(expiry), "the operator's expiry %v must survive the admin's attempts, got %v", expiry, got)
	})

	s.Run("admin disables its own tenant and every home token is denied", func() {
		r.NoError(gateway.DeleteTenantAs(s.Ctx, s.Cfg, adminPAT, rt.ID), "a tenant may disable itself")
		_, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "INACTIVE", lifecycleTokenTimeout)
		r.NoError(err, "the admin's disable must deactivate the org")
		_, err = tests.WaitUntilTokenDenied(s.Ctx, s.Cfg, adminPAT, rt.ID, lifecycleTokenTimeout)
		r.NoError(err, "the admin's own token must be denied once its home tenant is disabled")
		_, err = tests.WaitUntilTokenDenied(s.Ctx, s.Cfg, writer.PAT, rt.ID, lifecycleTokenTimeout)
		r.NoError(err, "the writer's token must be denied once its home tenant is disabled")
	})

	s.Run("admin cannot bring its disabled tenant back or touch its expiry", func() {
		s.requireLivenessDenial(gateway.RestoreTenantAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "admin restoreTenant on its disabled tenant")
		s.requireLivenessDenial(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "admin setTenantExpiry on its disabled tenant")
		s.requireLivenessDenial(gateway.DeleteTenantAs(s.Ctx, s.Cfg, adminPAT, rt.ID), "admin deleteTenant on its disabled tenant")
		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, rt.IdpOrgRef)
		r.NoError(err)
		r.Equal("INACTIVE", state, "no denied call may have restored the org")
	})

	s.Run("only the system principal restores, and the admin works again", func() {
		r.NoError(gateway.RestoreTenant(s.Ctx, s.Cfg, rt.ID))
		_, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "ACTIVE", lifecycleTokenTimeout)
		r.NoError(err, "the restore must reactivate the org")
		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, adminPAT, rt.ID, lifecycleTokenTimeout)
		r.NoError(err, "the admin's token must be accepted again after the restore")
		got, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
		r.NoError(err)
		r.NotNil(got, "a restore without expiresAt must keep the operator's expiry")
		r.True(got.Equal(expiry), "restored tenant must still carry %v, got %v", expiry, got)

		// The restored admin is back to exactly where it was: it may
		// disable again, but the operator's expiry is still not its.
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "system caller required", "admin clearing the expiry after restore")
		r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, nil), "the system principal clears the expiry")
		got, err = tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
		r.NoError(err)
		r.Nil(got)
	})
}

// waitDeniedUnderHeader polls a tenants read that names tenantID in the
// X-Pyck-Tenant-Id header until it is refused. The explicit header is
// the point: a token's "all" expansion silently drops a disabled tenant
// and answers 200, so only a request that names the tenant observes the
// denial a cross-tenant grant into it must get.
func (s *TnLifecycleRolesSuite) waitDeniedUnderHeader(token, tenantID string) {
	s.T().Helper()
	c := gateway.NewClientForTenant(s.Cfg, token, tenantID)
	first := 1
	_, err := tests.WaitUntilDenied(s.Ctx, "header "+tenantID, lifecycleTokenTimeout, func() error {
		_, err := c.GetTenants(s.Ctx, managementapi.GetTenantsArgs{
			First: &first,
			Where: &managementapi.TenantWhereInput{ID: &tenantID},
		})
		return err
	})
	s.Require().NoError(err, "naming the disabled tenant %s must be denied", tenantID)
}

// requireNoAccess asserts a lifecycle call naming a tenant the caller
// holds no grant into was refused by the tenant middleware: the header
// axis, which rejects the selection before any role or liveness check
// and long before a resolver runs.
func (s *TnLifecycleRolesSuite) requireNoAccess(err error, what string) {
	s.T().Helper()
	s.Require().Error(err, "%s must be refused", what)
	s.Require().ErrorContains(err, "no access to tenant ID", "%s must be refused by the header axis, got: %v", what, err)
}

// TestForeignTenantAdmin proves that ROLE_ADMIN is scoped to the tenant it
// was granted on: an admin homed in A with no grant into B cannot disable,
// restore or expire B, and cannot even name B: the request is refused at
// the header axis, so the lifecycle resolvers never see it.
func (s *TnLifecycleRolesSuite) TestForeignTenantAdmin() {
	r := s.Require()

	tenantA, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant A")
	s.DeferTenantCleanup(tenantA.ID)
	tenantB, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant B")
	s.DeferTenantCleanup(tenantB.ID)

	adminA := s.provisionAdmin(tenantA)
	expiry := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	// The admin gate is proven open on A first, so a refusal on B cannot
	// be a role grant that has not projected yet.
	_, err = tests.WaitUntilAccepted(s.Ctx, "admin gate on A", lifecycleTokenTimeout, func() error {
		return requireSystemCallerRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminA, tenantA.ID, &expiry))
	})
	r.NoError(err, "the admin PAT must hold admin on its own tenant A")

	s.Run("A's admin cannot name B at all", func() {
		s.requireNoAccess(gateway.DeleteTenantAs(s.Ctx, s.Cfg, adminA, tenantB.ID), "foreign admin deleteTenant on B")
		s.requireNoAccess(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminA, tenantB.ID, &expiry), "foreign admin setTenantExpiry on B")
		s.requireNoAccess(gateway.RestoreTenantAs(s.Ctx, s.Cfg, adminA, tenantB.ID, nil), "foreign admin restoreTenant on B")
	})

	s.Run("B is untouched", func() {
		status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, tenantB.ID)
		r.NoError(err)
		r.Equal("done", status, "B must still be active")
		got, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, tenantB.ID)
		r.NoError(err)
		r.Nil(got, "B must carry no expiry")
		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, tenantB.IdpOrgRef)
		r.NoError(err)
		r.Equal("ACTIVE", state, "B's org must stay active")
	})

	// The same admin, once B is disabled by the operator, still cannot
	// reach B: the header axis refuses the selection whether B is
	// active or disabled, so a foreign admin can neither undo nor
	// pile onto an operator's disable.
	s.Run("A's admin cannot reach a disabled B either", func() {
		r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, tenantB.ID), "the operator disables B")
		_, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, tenantB.IdpOrgRef, "INACTIVE", lifecycleTokenTimeout)
		r.NoError(err)
		s.requireNoAccess(gateway.RestoreTenantAs(s.Ctx, s.Cfg, adminA, tenantB.ID, nil), "foreign admin restoreTenant on disabled B")
		s.requireNoAccess(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminA, tenantB.ID, nil), "foreign admin setTenantExpiry on disabled B")
		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, tenantB.IdpOrgRef)
		r.NoError(err)
		r.Equal("INACTIVE", state, "no refused call may have restored B")
	})
}

// TestCrossTenantAdmin proves the gates key on the role held on the
// TARGET tenant, not on where the caller is homed: an admin in A with an
// admin grant into B may disable B and nothing else, and loses B, but
// not A, while B is disabled.
func (s *TnLifecycleRolesSuite) TestCrossTenantAdmin() {
	r := s.Require()

	tenantA, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant A")
	s.DeferTenantCleanup(tenantA.ID)
	tenantB, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant B")
	s.DeferTenantCleanup(tenantB.ID)

	// Both grants are created before the PAT is first used: the first
	// request caches the introspected role set for the PAT cache TTL,
	// and a grant added afterwards would stay invisible for that long.
	roles := tests.RolesWithServiceGates(authn.ROLE_ADMIN.String())
	uid, err := zitadelclient.EnsureMachineUser(s.Ctx, s.ZConn, tenantA.IdpOrgRef, fixtures.NewMachineUser())
	r.NoError(err, "ensure machine user in A")
	r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, tenantA.IdpOrgRef, s.Cfg.ZitadelProjectID, uid, roles), "admin grant in A")
	r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, tenantB.IdpOrgRef, s.Cfg.ZitadelProjectID, uid, roles), "admin grant into B")
	_, pat, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, uid)
	r.NoError(err, "add PAT")
	_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantA.ID, lifecycleTokenTimeout)
	r.NoError(err, "PAT must see its home tenant A")
	_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantB.ID, lifecycleTokenTimeout)
	r.NoError(err, "PAT must see the granted tenant B")

	expiry := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	// Polled like the own-tenant admin: the role gate must be open on B
	// before a refusal can be pinned on the system-caller guard.
	_, err = tests.WaitUntilAccepted(s.Ctx, "admin gate on B", lifecycleTokenTimeout, func() error {
		return requireSystemCallerRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, pat, tenantB.ID, &expiry))
	})
	r.NoError(err, "the cross-tenant admin PAT must pass the role gate on B and stop at the system-caller guard")

	s.Run("cross-tenant admin is refused the operator-owned mutations on B", func() {
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, pat, tenantB.ID, &expiry), "system caller required", "cross-tenant admin setTenantExpiry on B")
		s.requireRefusal(gateway.RestoreTenantAs(s.Ctx, s.Cfg, pat, tenantB.ID, nil), "system caller required", "cross-tenant admin restoreTenant on B")
		got, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, tenantB.ID)
		r.NoError(err)
		r.Nil(got)
	})

	s.Run("cross-tenant admin disables B and keeps A", func() {
		r.NoError(gateway.DeleteTenantAs(s.Ctx, s.Cfg, pat, tenantB.ID), "ROLE_ADMIN on the target is what deleteTenant requires, wherever the caller is homed")
		_, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, tenantB.IdpOrgRef, "INACTIVE", lifecycleTokenTimeout)
		r.NoError(err, "the disable must deactivate B's org")
		s.waitDeniedUnderHeader(pat, tenantB.ID)
		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantA.ID, lifecycleTokenTimeout)
		r.NoError(err, "the caller's home tenant A must be unaffected")
		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, tenantA.IdpOrgRef)
		r.NoError(err)
		r.Equal("ACTIVE", state, "A's org must stay active")
	})

	s.Run("cross-tenant admin cannot bring B back", func() {
		s.requireLivenessDenial(gateway.RestoreTenantAs(s.Ctx, s.Cfg, pat, tenantB.ID, nil), "cross-tenant admin restoreTenant on disabled B")
		s.requireLivenessDenial(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, pat, tenantB.ID, nil), "cross-tenant admin setTenantExpiry on disabled B")
		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, tenantB.IdpOrgRef)
		r.NoError(err)
		r.Equal("INACTIVE", state, "no denied call may have restored B")
	})

	s.Run("system restores B and the cross-tenant grant works again", func() {
		r.NoError(gateway.RestoreTenant(s.Ctx, s.Cfg, tenantB.ID))
		_, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, tenantB.IdpOrgRef, "ACTIVE", lifecycleTokenTimeout)
		r.NoError(err)
		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantB.ID, lifecycleTokenTimeout)
		r.NoError(err, "the grant into B must be usable again after the restore")
	})
}

// TestExpirySweepAndRoles covers the disable nobody calls: the operator's
// expiry, found by the tenant-expiry-check sweep. The tenant's admin must
// not be able to postpone it, is locked out once the sweep fires exactly
// like after a manual disable, and is back once the system principal
// restores with a fresh expiry.
func (s *TnLifecycleRolesSuite) TestExpirySweepAndRoles() {
	if s.Cfg.TenantExpiryCheckInterval > 30*time.Second {
		s.T().Skipf("sweep-driven disable needs a test-cadence stack: expiry-check interval is %s, want ≤30s (set PYCK_TENANT_EXPIRY_CHECK_INTERVAL)", s.Cfg.TenantExpiryCheckInterval)
	}
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)
	adminPAT := s.provisionAdmin(rt)

	// The admin gate must be open before the expiry is set: once the
	// sweep fires, every refusal turns into a liveness denial and the
	// system-caller guard can no longer be told apart from it.
	farFuture := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	_, err = tests.WaitUntilAccepted(s.Ctx, "admin gate", lifecycleTokenTimeout, func() error {
		return requireSystemCallerRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, &farFuture))
	})
	r.NoError(err, "the admin PAT must pass the role gate and stop at the system-caller guard")

	s.Run("admin cannot postpone the operator's imminent expiry", func() {
		soon := time.Now().UTC().Add(5 * time.Second)
		r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, &soon), "the operator schedules the disable")
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, &farFuture), "system caller required", "admin postponing the expiry")
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "system caller required", "admin clearing the expiry")
	})

	s.Run("the sweep disables the tenant and locks the admin out", func() {
		budget := s.Cfg.TenantExpiryCheckInterval + revocationTimeout
		_, err := tests.WaitUntilTokenDenied(s.Ctx, s.Cfg, adminPAT, rt.ID, budget)
		r.NoError(err, "the admin's token must be denied once the sweep disabled its tenant")
		_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "INACTIVE", budget)
		r.NoError(err, "the sweep's disable must deactivate the org")
		s.requireLivenessDenial(gateway.RestoreTenantAs(s.Ctx, s.Cfg, adminPAT, rt.ID, &farFuture), "admin restoreTenant after the sweep")
		s.requireLivenessDenial(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "admin setTenantExpiry after the sweep")
	})

	s.Run("system restores with a fresh expiry and the admin is back", func() {
		// The past expiry is overwritten in the same mutation: a restore
		// that left it in place would be sweep-eligible again the moment
		// deleted_at clears.
		r.NoError(gateway.RestoreTenantWithExpiry(s.Ctx, s.Cfg, rt.ID, &farFuture))
		_, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "ACTIVE", lifecycleTokenTimeout)
		r.NoError(err)
		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, adminPAT, rt.ID, lifecycleTokenTimeout)
		r.NoError(err, "the admin's token must be accepted again after the restore")
		got, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
		r.NoError(err)
		r.NotNil(got)
		r.True(got.Equal(farFuture), "the restore must carry the fresh expiry %v, got %v", farFuture, got)
		s.requireRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminPAT, rt.ID, nil), "system caller required", "admin clearing the expiry after restore")
	})
}

// TestForeignTenantAdminOnAdminGatedSurfaces extends the foreign-admin
// check to every other surface this change opened to tenant admins or
// reserved for the system: the service-role trio (userServiceRoles,
// assignRoles, removeRoles), the user-projection CRUD and the inventory
// rebuild. An admin homed in A with no grant into B is refused on B from
// both request shapes a client can produce: naming B in the header,
// which the header axis refuses outright, and naming B only in the
// input while acting under its own header A, which each resolver refuses
// on its own rule; and B's admin ends up with the roles it started with.
func (s *TnLifecycleRolesSuite) TestForeignTenantAdminOnAdminGatedSurfaces() {
	r := s.Require()

	tenantA, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant A")
	s.DeferTenantCleanup(tenantA.ID)
	tenantBFixture := fixtures.NewTenant()
	tenantB, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenantBFixture)
	r.NoError(err, "register tenant B")
	s.DeferTenantCleanup(tenantB.ID)

	adminA := s.provisionAdmin(tenantA)
	expiry := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	_, err = tests.WaitUntilAccepted(s.Ctx, "admin gate on A", lifecycleTokenTimeout, func() error {
		return requireSystemCallerRefusal(gateway.SetTenantExpiryAs(s.Ctx, s.Cfg, adminA, tenantA.ID, &expiry))
	})
	r.NoError(err, "the admin PAT must hold admin on its own tenant A")

	// B's own admin is the target every call names: registration
	// projected it into management.users, so it is a real, addressable
	// row, the one a foreign admin would most want to touch.
	syncedID, err := tests.WaitForSyncedUser(s.Ctx, s.Cfg, tenantB.ID, tenantBFixture.AdminUsername)
	r.NoError(err, "B's admin must be projected into management")
	targetUser, err := uuid.Parse(syncedID)
	r.NoError(err)
	targetTenant, err := uuid.Parse(tenantB.ID)
	r.NoError(err)
	roles := []string{serviceroles.Inventory.String()}
	newUser := managementapi.CreateUserInput{
		IdpID: "foreign-" + uuid.NewString(), Username: "foreign-" + uuid.NewString(),
		Email: "foreign@example.com", FirstName: "Foreign", LastName: "Admin", TenantID: tenantB.ID,
	}

	s.Run("naming B in the header is refused before any resolver", func() {
		c := gateway.NewClientForTenant(s.Cfg, adminA, tenantB.ID)
		_, err := c.GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
			Input: managementmodel.UserServiceRolesInput{TenantID: targetTenant, UserID: targetUser},
		})
		s.requireNoAccess(err, "foreign admin userServiceRoles under header B")
		_, err = c.AssignRoles(s.Ctx, managementapi.AssignRolesArgs{
			Input: managementmodel.AssignRolesInput{TenantID: targetTenant, UserID: targetUser, Roles: roles},
		})
		s.requireNoAccess(err, "foreign admin assignRoles under header B")
		_, err = c.RemoveRoles(s.Ctx, managementapi.RemoveRolesArgs{
			Input: managementmodel.RemoveRolesInput{TenantID: targetTenant, UserID: targetUser, Roles: roles},
		})
		s.requireNoAccess(err, "foreign admin removeRoles under header B")
		_, err = c.CreateUser(s.Ctx, managementapi.CreateUserArgs{Input: newUser})
		s.requireNoAccess(err, "foreign admin createUser under header B")
		_, err = c.DeleteUser(s.Ctx, managementapi.DeleteUserArgs{Id: syncedID})
		s.requireNoAccess(err, "foreign admin deleteUser under header B")
		_, err = gateway.NewInventoryClientForTenant(s.Cfg, adminA, tenantB.ID).RebuildInventoryStock(s.Ctx)
		s.requireNoAccess(err, "foreign admin rebuildInventoryStock under header B")
	})

	s.Run("naming B only in the input is refused by each resolver's own rule", func() {
		c := gateway.NewClientForTenant(s.Cfg, adminA, tenantA.ID)
		_, err := c.GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
			Input: managementmodel.UserServiceRolesInput{TenantID: targetTenant, UserID: targetUser},
		})
		s.requireRefusal(err, "admin role required for userServiceRoles", "foreign admin userServiceRoles on B")
		_, err = c.AssignRoles(s.Ctx, managementapi.AssignRolesArgs{
			Input: managementmodel.AssignRolesInput{TenantID: targetTenant, UserID: targetUser, Roles: roles},
		})
		s.requireRefusal(err, "admin role required for assignRoles", "foreign admin assignRoles on B")
		_, err = c.RemoveRoles(s.Ctx, managementapi.RemoveRolesArgs{
			Input: managementmodel.RemoveRolesInput{TenantID: targetTenant, UserID: targetUser, Roles: roles},
		})
		s.requireRefusal(err, "admin role required for removeRoles", "foreign admin removeRoles on B")
		// The projection writes are system-only for everyone, so the
		// foreign admin is stopped by that guard before the target's
		// tenant is even considered.
		_, err = c.CreateUser(s.Ctx, managementapi.CreateUserArgs{Input: newUser})
		s.requireRefusal(err, "system caller required", "foreign admin createUser targeting B")
		_, err = c.UpdateUser(s.Ctx, managementapi.UpdateUserArgs{Id: syncedID, Input: managementapi.UpdateUserInput{Username: ptr("hijacked")}})
		s.requireRefusal(err, "system caller required", "foreign admin updateUser on B's admin")
		_, err = c.DeleteUser(s.Ctx, managementapi.DeleteUserArgs{Id: syncedID})
		s.requireRefusal(err, "system caller required", "foreign admin deleteUser on B's admin")
	})

	s.Run("B's admin is untouched", func() {
		resp, err := gateway.NewClientForTenant(s.Cfg, s.Cfg.ServiceToken, tenantB.ID).GetUserServiceRoles(s.Ctx, managementapi.GetUserServiceRolesArgs{
			Input: managementmodel.UserServiceRolesInput{TenantID: targetTenant, UserID: targetUser},
		})
		r.NoError(err)
		r.Empty(resp.GetUserServiceRoles(), "no refused call may have granted B's admin a service role")
		status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, tenantB.ID)
		r.NoError(err)
		r.Equal("done", status)
	})
}

// ptr returns a pointer to v, for optional input fields.
func ptr[T any](v T) *T { return &v }
