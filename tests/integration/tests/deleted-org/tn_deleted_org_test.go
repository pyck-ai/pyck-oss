//go:build integration

package deletedorg

import (
	"fmt"
	"time"

	enums "go.temporal.io/api/enums/v1"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// These tests cover the "Zitadel org no longer exists" paths the tenant
// disable/restore/reconcile and zitadel-sync workflows must tolerate. They
// reproduce the production bug where the org-cleanup job removed orgs out of
// band while pyck still held tenant rows, causing DeactivateZitadelOrgActivity
// to fail NotFound and the reconcile sweeper to re-dispatch the disable
// workflow forever.
//
// Each test deletes the Zitadel org directly via the v2 OrganizationService
// (DeleteOrg) — the same out-of-band removal a cleanup job performs.
//
// The tenant-reconcile cadence comes from s.Cfg.TenantReconcileInterval, so
// reconcile-driven windows scale with the stack's actual configuration. The
// zitadel-sync cadence is not exposed through config, so sync-driven timeouts
// below are fixed bounds sized generously over the local 5s interval
// (pyck/.env) plus workflow dispatch and visibility-indexing lag.
const (
	// syncSoftDeleteTimeout must cover at least one zitadel-sync interval
	// plus reconcile + projection lag. Fixed because config does not expose
	// the zitadel-sync cadence; 120s spans many local 5s intervals.
	syncSoftDeleteTimeout = 120 * time.Second
	// orgGoneTimeout covers Zitadel applying the org removal to its
	// projection so ListOrganizations stops returning it ACTIVE/INACTIVE.
	// Pure Zitadel projection lag — independent of any pyck sweep cadence.
	orgGoneTimeout = 30 * time.Second
	// workflowOutcomeTimeout covers NATS trigger → Temporal dispatch latency
	// plus the activity retry budget and visibility indexing lag — also
	// independent of any sweep cadence.
	workflowOutcomeTimeout = 60 * time.Second
)

// Mirror of backend/management/workflows/identity.go and worker.go. Kept as
// local literals so the test stays decoupled from the worker package's
// dependency graph.
const (
	lifecycleWorkflowIDPrefix = "tenant-lifecycle-"
	disableWorkflowType       = "DisableTenantWorkflow"
)

func lifecycleWorkflowID(tenantID string) string {
	return lifecycleWorkflowIDPrefix + tenantID
}

// TestADeletedActiveTenantIsSoftDeleted: an active tenant whose Zitadel org
// is deleted out of band must be soft-deleted by the zitadel-sync reconcile
// (Zitadel is the SSOT). This also exercises the per-tenant user-sync
// NotFound skip (FetchZitadelUsersActivity) — a deleted org must not wedge
// the sync.
func (s *DeletedOrgSuite) TestADeletedActiveTenantIsSoftDeleted() {
	r := s.Require()
	tenant := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	r.NotEmpty(rt.IdpOrgRef)
	s.DeferTenantCleanup(rt.ID)
	s.T().Logf("registered tenantID=%s idpOrgRef=%s", rt.ID, rt.IdpOrgRef)

	present, err := tests.TenantInList(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.True(present, "tenant should be listed before org deletion")

	// Out-of-band org removal — the cleanup-job scenario.
	r.NoError(zitadelclient.DeleteOrg(s.Ctx, s.ZConn, rt.IdpOrgRef))
	took, err := waitUntilOrgGone(s.Ctx, s.ZConn, rt.IdpOrgRef, orgGoneTimeout)
	r.NoError(err)
	s.T().Logf("org gone from Zitadel after %s", took.Round(time.Millisecond))

	// zitadel-sync must reconcile the missing org into a DB soft-delete.
	took, err = waitUntilTenantAbsent(s.Ctx, s.Cfg, rt.ID, syncSoftDeleteTimeout)
	r.NoError(err)
	s.T().Logf("tenant soft-deleted by zitadel-sync after %s", took.Round(time.Millisecond))
}

// TestBDisableWithMissingOrgSucceeds: disabling a tenant whose Zitadel org is
// already gone must succeed rather than fail/retry. The resolver soft-deletes
// the row and dispatches DisableTenantWorkflow, whose
// DeactivateZitadelOrgActivity treats NotFound as a successful no-op, so the
// workflow run reaches COMPLETED (not FAILED).
func (s *DeletedOrgSuite) TestBDisableWithMissingOrgSucceeds() {
	r := s.Require()
	tenant := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	r.NotEmpty(rt.IdpOrgRef)
	s.DeferTenantCleanup(rt.ID)
	s.T().Logf("registered tenantID=%s idpOrgRef=%s", rt.ID, rt.IdpOrgRef)

	// Remove the org first, so the disable side-effect targets a missing org.
	r.NoError(zitadelclient.DeleteOrg(s.Ctx, s.ZConn, rt.IdpOrgRef))
	_, err = waitUntilOrgGone(s.Ctx, s.ZConn, rt.IdpOrgRef, orgGoneTimeout)
	r.NoError(err)

	// Disable through pyck. The mutation soft-deletes synchronously and fires
	// the workflow async via NATS.
	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID))

	wfID := lifecycleWorkflowID(rt.ID)
	took, err := waitForRunStatus(s.Ctx, s.Temporal, wfID, disableWorkflowType,
		enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, workflowOutcomeTimeout)
	r.NoError(err, "DisableTenantWorkflow should COMPLETE despite the missing org")
	s.T().Logf("DisableTenantWorkflow completed after %s", took.Round(time.Millisecond))
}

// TestCReconcileSkipsDeletedOrg is the regression guard for the original bug.
// A soft-deleted tenant whose org is then deleted must be treated by the
// tenant-reconcile sweeper as consistent (terminal), NOT re-dispatched as
// drift. A regressed sweeper emits a fresh DisableTenantWorkflow every
// reconcile interval, so the disable run count grows within one tick; the
// guard asserts it stays flat across several intervals.
func (s *DeletedOrgSuite) TestCReconcileSkipsDeletedOrg() {
	r := s.Require()
	tenant := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	r.NotEmpty(rt.IdpOrgRef)
	s.DeferTenantCleanup(rt.ID)
	s.T().Logf("registered tenantID=%s idpOrgRef=%s", rt.ID, rt.IdpOrgRef)

	// Disable through pyck so we land in (DB.deleted_at set, org INACTIVE).
	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID))
	_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "INACTIVE", 30*time.Second)
	r.NoError(err)

	wfID := lifecycleWorkflowID(rt.ID)

	// Now delete the org out of band: (DB.disabled, org DELETED).
	r.NoError(zitadelclient.DeleteOrg(s.Ctx, s.ZConn, rt.IdpOrgRef))
	before, err := countRuns(s.Ctx, s.Temporal, wfID, disableWorkflowType)
	r.NoError(err)
	s.T().Logf("disable runs before stability window: %d", before)

	// The stability window spans 3 reconcile intervals so that at least two
	// sweep ticks are guaranteed to fire inside it — proving the sweeper saw
	// the (soft-deleted row, deleted org) pair and skipped it, rather than the
	// window falling between ticks. At the service-default cadence (5m) that
	// window is 15 minutes — past go test's own timeout — so this test only
	// runs against a test-cadence stack (`task up:integration` applies
	// config/compose/integration.yaml, which sets 5s; envrc.sh reads it from the
	// running management container). Skipping loudly beats a vacuous or
	// timed-out run.
	if s.Cfg.TenantReconcileInterval > 30*time.Second {
		s.T().Skipf("stability window needs a test-cadence stack: reconcile interval is %s, want ≤30s (set PYCK_TENANT_RECONCILE_INTERVAL)", s.Cfg.TenantReconcileInterval)
	}
	window := 3 * s.Cfg.TenantReconcileInterval

	// Poll the invariant across the window instead of sleeping it out: a
	// regressed sweeper re-dispatches within one tick, so the count grows
	// almost immediately and PollStable surfaces it as the check's error —
	// the test fails fast. The green path holds the invariant through every
	// tick and exhausts the window. Transient visibility faults say nothing
	// about the invariant and skip the sample; the authoritative count
	// below covers a window that errored throughout.
	r.NoError(tests.PollStable(s.Ctx, window, s.Cfg.TenantReconcileInterval/2, func() error {
		n, err := countRuns(s.Ctx, s.Temporal, wfID, disableWorkflowType)
		if err != nil {
			s.T().Logf("stability sample skipped (count runs: %v)", err)
			return nil
		}
		if n > before {
			return fmt.Errorf("tenant-reconcile re-dispatched DisableTenantWorkflow for a deleted org: runs grew %d → %d inside the stability window", before, n)
		}
		return nil
	}), "disable-run count must stay at %d for the whole %s window", before, window)

	after, err := countRuns(s.Ctx, s.Temporal, wfID, disableWorkflowType)
	r.NoError(err)
	r.Equal(before, after,
		"tenant-reconcile must not re-dispatch DisableTenantWorkflow for a deleted org")

	// And the tenant must remain soft-deleted (not spuriously restored).
	present, err := tests.TenantInList(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.False(present, "tenant must stay soft-deleted")
}
