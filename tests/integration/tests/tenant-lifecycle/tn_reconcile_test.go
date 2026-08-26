//go:build integration

package tenants

import (
	"time"

	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TnReconcileSuite walks a single tenant through both directions of
// out-of-band drift and verifies the periodic tenant-reconcile sweeper
// heals each one. Phases are ordered s.Run subtests inside
// TestReconcile so each shows up as its own row in the test output and
// a failure short-circuits the rest, mirroring ProvisioningSuite.
//
// Coverage:
//   - register tenant (active in DB, ACTIVE in Zitadel)
//   - out-of-band deactivate in Zitadel → reconcile dispatches
//     RestoreTenantWorkflow → org returns to ACTIVE
//   - disable through pyck (DB.deleted_at set, Zitadel INACTIVE)
//   - out-of-band reactivate in Zitadel → reconcile dispatches
//     DisableTenantWorkflow → org returns to INACTIVE
//
// We never trigger the reconcile workflow ourselves. The sweep cadence
// is controlled by PYCK_TENANT_RECONCILE_INTERVAL
// (s.Cfg.TenantReconcileInterval; service default 5m, local .env
// override 5s); the heal budget must comfortably cover one interval +
// workflow latency + Zitadel projection lag.
type TnReconcileSuite struct {
	tests.Base
}

func (s *TnReconcileSuite) TestReconcile() {
	tenant := fixtures.NewTenant()
	s.T().Logf("tenant=%s admin=%s", tenant.Name, tenant.AdminUsername)

	// reconcileHealBudget bounds each drift-heal wait: at most one full
	// reconcile interval until the sweeper notices the drift, plus
	// margin for the dispatched workflow and Zitadel projection lag. At
	// the service-default cadence (5m) two heal waits blow past go
	// test's own timeout, so the test only runs against a test-cadence
	// stack (local/CI .env sets 5s; envrc.sh forwards it).
	if s.Cfg.TenantReconcileInterval > 30*time.Second {
		s.T().Skipf("drift-heal waits need a test-cadence stack: reconcile interval is %s, want ≤30s (set PYCK_TENANT_RECONCILE_INTERVAL)", s.Cfg.TenantReconcileInterval)
	}
	reconcileHealBudget := s.Cfg.TenantReconcileInterval + 30*time.Second

	var registered *gateway.RegisteredTenant

	// Creates the Zitadel sub-org + admin user + management.tenants row.
	// Baseline: org is ACTIVE in Zitadel and DB has no deleted_at, so
	// the reconciler considers the tenant in steady state. The cleanup
	// is registered here — not as a final phase — so an early bail-out
	// in any later phase still disables the tenant; mid-phase Zitadel
	// drift left behind by a failure is healed by the reconciler itself
	// within one interval.
	if !s.Run("register tenant", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err)
		r.NotEmpty(rt.IdpOrgRef)
		registered = rt
		s.DeferTenantCleanup(rt.ID)
		s.T().Logf("tenantID=%s idpOrgRef=%s", rt.ID, rt.IdpOrgRef)
	}) {
		return
	}

	// Active-but-Zitadel-inactive drift: someone deactivates the org
	// directly in Zitadel (admin UI / gRPC) while pyck still considers
	// the tenant live. ToRestore = I \ D set; reconciler must dispatch
	// RestoreTenantWorkflow to bring the org back to ACTIVE.
	if !s.Run("reconcile heals out-of-band deactivation", func() {
		r := s.Require()
		c := org_pb.NewOrganizationServiceClient(s.ZConn)
		_, err := c.DeactivateOrganization(s.Ctx, &org_pb.DeactivateOrganizationRequest{OrganizationId: registered.IdpOrgRef})
		r.NoError(err)
		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "INACTIVE", 10*time.Second)
		r.NoError(err)
		s.T().Logf("out-of-band deactivate: Zitadel INACTIVE after %s", took.Round(time.Millisecond))

		took, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "ACTIVE", reconcileHealBudget)
		r.NoError(err)
		s.T().Logf("reconcile reactivated after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	// Disable through the proper pyck path so we land in
	// (DB.deleted_at set, Zitadel INACTIVE) — the steady state the next
	// phase will perturb.
	if !s.Run("disable tenant via pyck", func() {
		r := s.Require()
		r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, registered.ID))
		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "INACTIVE", 30*time.Second)
		r.NoError(err)
		s.T().Logf("Zitadel INACTIVE after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	// Disabled-but-Zitadel-active drift: someone reactivates the org
	// directly in Zitadel while DB still has deleted_at set.
	// ToDisable = D \ I set; reconciler must dispatch
	// DisableTenantWorkflow to flip the org back to INACTIVE.
	s.Run("reconcile heals out-of-band reactivation", func() {
		r := s.Require()
		c := org_pb.NewOrganizationServiceClient(s.ZConn)
		_, err := c.ActivateOrganization(s.Ctx, &org_pb.ActivateOrganizationRequest{OrganizationId: registered.IdpOrgRef})
		r.NoError(err)
		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "ACTIVE", 10*time.Second)
		r.NoError(err)
		s.T().Logf("out-of-band reactivate: Zitadel ACTIVE after %s", took.Round(time.Millisecond))

		took, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, registered.IdpOrgRef, "INACTIVE", reconcileHealBudget)
		r.NoError(err)
		s.T().Logf("reconcile re-disabled after %s", took.Round(time.Millisecond))
	})
}
