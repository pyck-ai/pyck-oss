//go:build integration

package tenants

import (
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// ProvisioningSuite walks a single tenant through every observable
// step of the disable / restore / delete lifecycle. Phases are run as
// ordered s.Run subtests inside TestLifecycle so each shows up as its
// own row in the test output and a failure short-circuits the rest.
//
// Coverage:
//   - register tenant + machine user + project grant + usable PAT
//   - baseline: PAT accepted by every probed subgraph
//   - disable evicts the tenant's tokens from every service's
//     introspection cache via the NATS revocation subscriber; next
//     request re-introspects, Zitadel returns active=false, PAT rejected
//   - Zitadel org transitions to INACTIVE (disable-tenant Temporal
//     workflow ran)
//   - disable is idempotent: re-disabling preserves all of the above
//   - restore: Zitadel org reactivates, next request re-introspects
//     successfully, PAT accepted again
//   - restore is idempotent: re-restoring preserves the active state
//   - final delete: tenants list hides the row (NotDeletedFilter),
//     Zitadel org INACTIVE
type ProvisioningSuite struct {
	tests.Base
}

func (s *ProvisioningSuite) TestLifecycle() {
	tenant := fixtures.NewTenant()
	s.T().Logf("tenant=%s admin=%s", tenant.Name, tenant.AdminUsername)

	var (
		registered *gateway.RegisteredTenant
		pat        string
	)

	// Creates the Zitadel sub-org + admin user + management.tenants row
	// via the gateway's registerTenant mutation. Captures the tenant ID
	// and IdP org reference for downstream phases. The cleanup is
	// registered here — not as a final phase — so an early bail-out in
	// any later phase still tears the tenant down; the final phase below
	// asserts the delete behaviour on its own and this safety net is
	// idempotent on top of it.
	if !s.Run("register tenant", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err)
		r.NotEmpty(rt.ID, "tenant.ID")
		r.NotEmpty(rt.IdpOrgRef, "tenant.IdpOrgRef")
		registered = rt
		s.DeferTenantCleanup(rt.ID)
		s.T().Logf("tenantID=%s idpOrgRef=%s", rt.ID, rt.IdpOrgRef)
	}) {
		return
	}

	// Provisions a machine user in the new sub-org, grants it the writer
	// role plus every per-service gate on the Pyck project, mints a PAT,
	// and blocks until Zitadel's projections accept the PAT and it sees
	// its own tenant — so no later phase races the PAT projection.
	if !s.Run("provision machine user and PAT", func() {
		r := s.Require()
		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, registered, tests.RolesWithServiceGates("writer"))
		r.NoError(err)
		pat = p.PAT
		s.T().Logf("userID=%s", p.UserID)
	}) {
		return
	}

	// Baseline: with the tenant active, the PAT must be accepted by
	// every service whose auth middleware has the tenantrevocation
	// cache wired in. Provisioning proved management accepts the PAT;
	// the other services introspect it independently on first contact,
	// so this is a bounded wait rather than a single shot. If this phase
	// fails, the test setup is broken (wrong query name, wrong grant, …)
	// and any subsequent rejection after disable would be a false
	// positive.
	if !s.Run("baseline: PAT accepted by all services", func() {
		r := s.Require()
		_, err := waitUntilAllAccept(s.Ctx, s.Cfg, pat, 30*time.Second)
		r.NoError(err)
	}) {
		return
	}

	// Disable the tenant and wait for every service's in-process cache
	// to learn about it. Per-service rejection latency is logged so a
	// regression in JetStream propagation surfaces as a slow run rather
	// than silently passing.
	if !s.Run("disable propagates to all services", func() {
		r := s.Require()
		r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, registered.ID))

		latencies, err := waitUntilAllReject(s.Ctx, s.Cfg, pat, revocationTimeout)
		r.NoError(err)
		for name, d := range latencies {
			s.T().Logf("%s rejected after %s", name, d.Round(time.Millisecond))
		}
	}) {
		return
	}

	// The disable-tenant Temporal workflow runs asynchronously and
	// deactivates the tenant's Zitadel sub-org. Polled separately
	// because it lags the cache propagation by a few seconds (workflow
	// scheduling + activity execution). Without this check, a broken
	// or unregistered workflow would let the org stay ACTIVE while
	// pyck believes the tenant is disabled.
	if !s.Run("Zitadel org deactivated after disable", func() {
		r := s.Require()
		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "INACTIVE", 30*time.Second)
		r.NoError(err)
		s.T().Logf("Zitadel org INACTIVE after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	// deleteTenant on an already-deleted tenant must be a no-op:
	// the resolver should return success and the post-conditions from
	// the previous phases must still hold. A regression here (e.g. the
	// resolver wiping deleted_at, or the workflow erroring on
	// AlreadyDeactivated and rolling back state) would surface as a
	// flipped Zitadel state below.
	if !s.Run("disable is idempotent", func() {
		r := s.Require()
		r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, registered.ID))

		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, registered.IdpOrgRef)
		r.NoError(err)
		r.Equal("INACTIVE", state, "Zitadel org flipped state after second disable")
	}) {
		return
	}

	// Restore the tenant. The org is reactivated by the asynchronous
	// RestoreTenantWorkflow, so wait for Zitadel to report ACTIVE before
	// expecting any service to accept the PAT again — no service can
	// accept while its re-introspection still sees an inactive org.
	if !s.Run("Zitadel org reactivated after restore", func() {
		r := s.Require()
		r.NoError(gateway.RestoreTenant(s.Ctx, s.Cfg, registered.ID))

		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "ACTIVE", 30*time.Second)
		r.NoError(err)
		s.T().Logf("Zitadel org ACTIVE after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	// With the org ACTIVE, acceptance follows quickly: negative
	// org-active verdicts are not cached, so each service's next
	// introspection sees the reactivated org. Mirror of the disable
	// phase — exercises the restore path through the same propagation
	// channel, with per-service latency logged.
	if !s.Run("restore propagates to all services", func() {
		r := s.Require()
		latencies, err := waitUntilAllAccept(s.Ctx, s.Cfg, pat, 30*time.Second)
		r.NoError(err)
		for name, d := range latencies {
			s.T().Logf("%s accepted after %s", name, d.Round(time.Millisecond))
		}
	}) {
		return
	}

	// restoreTenant on an already-active tenant must be a no-op:
	// resolver returns success and Zitadel org stays ACTIVE.
	if !s.Run("restore is idempotent", func() {
		r := s.Require()
		r.NoError(gateway.RestoreTenant(s.Ctx, s.Cfg, registered.ID))

		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, registered.IdpOrgRef)
		r.NoError(err)
		r.Equal("ACTIVE", state, "Zitadel org flipped state after second restore")
	}) {
		return
	}

	// Final delete: disable once more and confirm both views agree the
	// tenant is gone — hidden from the system-token tenants listing
	// (NotDeletedFilter at the resolver) and INACTIVE in Zitadel (the
	// workflow re-ran). "Delete" here is the soft-delete via
	// deleteTenant; there is no hard-delete mutation.
	s.Run("delete tenant", func() {
		r := s.Require()
		r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, registered.ID))

		visible, err := tests.TenantInList(s.Ctx, s.Cfg, s.Cfg.ServiceToken, registered.ID)
		r.NoError(err)
		r.False(visible, "tenant still listed after disable — NotDeletedFilter is not applied")

		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "INACTIVE", 30*time.Second)
		r.NoError(err)
		s.T().Logf("Zitadel org INACTIVE after %s", took.Round(time.Millisecond))
	})
}
