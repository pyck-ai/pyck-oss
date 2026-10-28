//go:build integration

package tenants

import (
	"time"

	"github.com/pyck-ai/pyck/backend/common/authn"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// The three tenant-lifecycle mutations name their target in the input
// (input.tenantId) instead of deriving it from the X-Pyck-Tenant-Id header.
// That moves the whole authorization question: the role check is now about
// the named tenant, and nothing but the resolver stands between an
// authenticated caller and a tenant it does not own; the header used to be
// validated by the tenant middleware before any resolver ran.
//
// Two tenants are registered so the two axes can be separated: a caller's
// own tenant (the header) and the tenant it names (the input).

// TestLifecycleTarget_IsTheInputNotTheHeader proves both halves against a
// live stack: a tenant admin naming a foreign tenant is refused and the
// foreign tenant is untouched, while an operator naming it succeeds with a
// header that points somewhere else entirely.
func (s *TnRegisterSuite) TestLifecycleTarget_IsTheInputNotTheHeader() {
	r := s.Require()

	// The caller's own tenant: the one its token and its header name.
	caller := fixtures.NewTenant()
	callerTenant, err := gateway.RegisterTenant(s.Ctx, s.Cfg, caller)
	r.NoError(err)
	s.DeferTenantCleanup(callerTenant.ID)
	r.Equal("done", callerTenant.Status)

	// The victim: a live tenant the caller holds no role on at all.
	victimTenant, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(victimTenant.ID)
	r.Equal("done", victimTenant.Status)

	admin, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, callerTenant,
		[]string{authn.ROLE_ADMIN.String()})
	r.NoError(err, "an admin principal must be provisionable in the caller's tenant")

	s.Run("a tenant admin cannot retire a tenant it does not own", func() {
		r := s.Require()

		err := gateway.DeleteTenantFrom(s.Ctx, s.Cfg, admin.PAT, callerTenant.ID, victimTenant.ID)
		r.Error(err, "naming a foreign tenant must not retire it")
		r.ErrorContains(err, "admin role required",
			"the role gate must be evaluated against input.tenantId, not against the caller's header")

		// Two vantage points: the row is still live, and the org behind it
		// was never deactivated: a refusal that dispatched the disable
		// workflow anyway would pass the first assertion alone.
		status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, victimTenant.ID)
		r.NoError(err, "the victim tenant must still be visible")
		r.Equal("done", status)

		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, victimTenant.IdpOrgRef)
		r.NoError(err)
		r.Equal("ACTIVE", state, "a refused delete must not deactivate the org")
	})

	s.Run("a tenant admin cannot move a foreign tenant's expiry", func() {
		r := s.Require()

		future := time.Now().UTC().Add(72 * time.Hour)
		err := gateway.SetTenantExpiryFrom(s.Ctx, s.Cfg, admin.PAT, callerTenant.ID, victimTenant.ID, &future)
		r.Error(err, "naming a foreign tenant must not schedule its disable")
		r.ErrorContains(err, "admin role required")

		expiresAt, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, victimTenant.ID)
		r.NoError(err)
		r.Nil(expiresAt, "a refused setTenantExpiry must leave the foreign tenant without an expiry")
	})

	s.Run("a tenant admin cannot restore a foreign tenant", func() {
		r := s.Require()

		err := gateway.RestoreTenantFrom(s.Ctx, s.Cfg, admin.PAT, callerTenant.ID, victimTenant.ID, nil)
		r.Error(err, "restore is an operator action on a tenant the caller does not own")
		r.ErrorContains(err, "admin role required",
			"the role gate answers before the system-caller gate")
	})

	s.Run("an operator writes the tenant the input names, not the one in the header", func() {
		r := s.Require()

		// The header names the caller's tenant; the input names the victim.
		// Only the victim may be written.
		future := time.Now().UTC().Add(96 * time.Hour)
		err := gateway.SetTenantExpiryFrom(s.Ctx, s.Cfg, s.Cfg.ServiceToken,
			callerTenant.ID, victimTenant.ID, &future)
		r.NoError(err, "a system caller may name any tenant")

		got, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, victimTenant.ID)
		r.NoError(err)
		r.NotNil(got, "the named tenant must carry the expiry")
		r.WithinDuration(future, *got, time.Second)

		untouched, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, callerTenant.ID)
		r.NoError(err)
		r.Nil(untouched, "the tenant in the header must not be written instead")
	})

	s.Run("an operator retires and restores the tenant the input names", func() {
		r := s.Require()

		err := gateway.DeleteTenantFrom(s.Ctx, s.Cfg, s.Cfg.ServiceToken, callerTenant.ID, victimTenant.ID)
		r.NoError(err, "a system caller may retire the tenant it names")

		_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, victimTenant.IdpOrgRef, "INACTIVE", 30*time.Second)
		r.NoError(err, "the disable must reach Zitadel for the named tenant")

		// The caller's own tenant, which the header named throughout, is
		// untouched: its org is still active.
		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, callerTenant.IdpOrgRef)
		r.NoError(err)
		r.Equal("ACTIVE", state,
			"the header tenant must not be retired by a mutation aimed elsewhere")

		// Restoring a disabled tenant is the case that needs the decoupled
		// header at all: naming a disabled tenant in the header is refused
		// by the tenant middleware before the resolver runs.
		err = gateway.RestoreTenantFrom(s.Ctx, s.Cfg, s.Cfg.ServiceToken, callerTenant.ID, victimTenant.ID, nil)
		r.NoError(err, "a system caller may restore the tenant it names")

		_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, victimTenant.IdpOrgRef, "ACTIVE", 30*time.Second)
		r.NoError(err, "the restore must reach Zitadel for the named tenant")
	})
}

// TestLifecycleStatusGates_ErrorRowRetiresButDoesNotRestore proves the two
// registration-status gates on a live stack, using the one non-done row the
// public API can produce deterministically: a registration whose admin
// username is already held instance-wide ends marked error with its org
// left behind.
//
// deleteTenant must accept that row (retiring a failed registration is the
// operator's only way to stop paying for the org it left) and restoreTenant
// must refuse the retired row, because it lost its registration data at the
// delete and bringing it back would produce a tenant nothing ever finished
// provisioning. While the row is still live, restoreTenant is the idempotent
// no-op it is for every live row.
//
// The pending gate cannot be reached from outside: registerTenant returns
// only once the run has settled the row, so there is no window in which a
// client holds a pending tenant id. It is covered decisively in the resolver
// unit tests (TestDeleteTenant_RefusesARunningRegistration) and, from the
// run's own side, by the workflow's mid-run re-check.
func (s *TnRegisterSuite) TestLifecycleStatusGates_ErrorRowRetiresButDoesNotRestore() {
	r := s.Require()

	first := fixtures.NewTenant()
	firstTenant, err := gateway.RegisterTenant(s.Ctx, s.Cfg, first)
	r.NoError(err)
	s.DeferTenantCleanup(firstTenant.ID)

	// The same admin username under a second tenant: Zitadel's default
	// domain policy makes usernames unique instance-wide, so the import
	// fails after the org exists and the row is marked error.
	second := fixtures.NewTenant()
	second.AdminUsername = first.AdminUsername

	_, err = gateway.RegisterTenant(s.Ctx, s.Cfg, second)
	r.Error(err, "a username held by another org's user cannot be provisioned")

	failed, err := gateway.FindTenantByName(s.Ctx, s.Cfg, second.Name)
	r.NoError(err)
	r.NotNil(failed)
	s.DeferTenantCleanup(failed.ID)
	r.Equal("error", failed.Status, "the row is kept, marked error")

	s.Run("restoreTenant on a live error row is a no-op, not a refusal", func() {
		r := s.Require()

		// The status gate guards rows coming back from a soft delete. A row
		// that is still live has nothing to restore, so the mutation answers
		// success without touching it, whatever its status, and the row's
		// outcome stays what the run wrote.
		err := gateway.RestoreTenantFrom(s.Ctx, s.Cfg, s.Cfg.ServiceToken,
			firstTenant.ID, failed.ID, nil)
		r.NoError(err, "restoring a row that is not deleted is idempotent")

		status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, failed.ID)
		r.NoError(err)
		r.Equal("error", status, "a no-op restore must not rewrite the registration outcome")
	})

	s.Run("deleteTenant retires the error row and keeps its outcome", func() {
		r := s.Require()

		err := gateway.DeleteTenant(s.Ctx, s.Cfg, failed.ID)
		r.NoError(err, "a failed registration must be retirable: its org is still being paid for")

		// The disable is real once the event has deactivated the org: the
		// second vantage point on a row that merely reads deleted.
		_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, failed.IdpOrgRef, "INACTIVE", 30*time.Second)
		r.NoError(err, "retiring an error row must still dispatch DisableTenantWorkflow")
	})

	s.Run("a retired error row is still not restorable", func() {
		r := s.Require()

		err := gateway.RestoreTenantFrom(s.Ctx, s.Cfg, s.Cfg.ServiceToken,
			firstTenant.ID, failed.ID, nil)
		r.Error(err, "the delete cleared the registration data; there is nothing to bring back")
		r.ErrorContains(err, "tenant registration did not finish")
	})
}
