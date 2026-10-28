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
// disable/reconcile and zitadel-sync workflows must tolerate. The tenant row
// is the liveness truth: an org removed out of band while pyck still holds
// the tenant row leaves the tenant active (nothing soft-deletes it), the
// per-tenant user sync skips it instead of wedging, DeactivateZitadelOrgActivity
// treats NotFound as done, and the reconcile sweeper does not re-dispatch
// the disable workflow for it.
//
// Each test deletes the Zitadel org directly via the v2 OrganizationService
// (DeleteOrg) — the same out-of-band removal a cleanup job performs.
//
// Sweep-driven windows are sized from the stack's configured cadences
// (s.Cfg.TenantReconcileInterval, s.Cfg.ZitadelSyncInterval); the remaining
// timeouts are fixed bounds on Zitadel projection and Temporal dispatch lag.
const (
	// orgGoneTimeout covers Zitadel applying the org removal to its
	// projection so ListOrganizations stops returning it ACTIVE/INACTIVE.
	// Pure Zitadel projection lag — independent of any pyck sweep cadence.
	orgGoneTimeout = 30 * time.Second
	// workflowOutcomeTimeout covers NATS trigger → Temporal dispatch latency
	// plus the activity retry budget and visibility indexing lag — also
	// independent of any sweep cadence.
	workflowOutcomeTimeout = 60 * time.Second
)

// Mirror of backend/management/workflows/identity.go, worker.go and
// zitadel-sync/workflow.go. Kept as local literals so the test stays
// decoupled from the worker package's dependency graph.
const (
	lifecycleWorkflowIDPrefix  = "tenant-lifecycle-"
	disableWorkflowType        = "DisableTenantWorkflow"
	tenantSyncWorkflowIDPrefix = "tenant-sync-"
	tenantSyncWorkflowType     = "TenantSyncWorkflow"
)

func lifecycleWorkflowID(tenantID string) string {
	return lifecycleWorkflowIDPrefix + tenantID
}

func tenantSyncWorkflowID(orgID string) string {
	return tenantSyncWorkflowIDPrefix + orgID
}

// TestADeletedOrgLeavesTenantActive: the tenant row is the liveness truth.
// An active tenant whose Zitadel org is deleted out of band stays listed;
// the per-tenant user sync takes the ErrZitadelOrgNotFound skip path
// (FetchZitadelUsersActivity) and completes instead of wedging on retries
// or soft-deleting the tenant.
func (s *DeletedOrgSuite) TestADeletedOrgLeavesTenantActive() {
	// Both waits below span sync ticks. At the service-default cadence
	// (10m) the 3-interval stability window is 30 minutes, past go test's
	// own timeout, so this test only runs against a test-cadence stack
	// (local/CI .env sets 5s; envrc.sh forwards it). Skipping loudly beats
	// a vacuous or timed-out run, and skipping before RegisterTenant leaves
	// no abandoned tenant behind.
	if s.Cfg.ZitadelSyncInterval > 30*time.Second {
		s.T().Skipf("stability window needs a test-cadence stack: zitadel-sync interval is %s, want ≤30s (set PYCK_ZITADEL_SYNC_EVERY)", s.Cfg.ZitadelSyncInterval)
	}

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
	// orgGoneAt is the moment the org was observed gone from Zitadel's
	// projection. Runs started before the projection caught up may still
	// have listed the org's users and completed normally; they prove
	// nothing about the skip path and are excluded from the assertion on
	// purpose.
	orgGoneAt := time.Now()
	s.T().Logf("org gone from Zitadel after %s", took.Round(time.Millisecond))

	// A per-tenant sync run started after orgGoneAt must COMPLETE: that is
	// the NotFound skip path.
	syncWFID := tenantSyncWorkflowID(rt.IdpOrgRef)
	runID, took, err := waitForRunStatus(s.Ctx, s.Temporal, syncWFID, tenantSyncWorkflowType,
		enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, orgGoneAt, s.Cfg.ZitadelSyncInterval+workflowOutcomeTimeout)
	r.NoError(err, "TenantSyncWorkflow started after the org deletion should COMPLETE via the NotFound skip")
	s.T().Logf("post-deletion TenantSyncWorkflow %s completed after %s", runID, took.Round(time.Millisecond))

	// COMPLETED alone is also satisfied by the reconcile path treating the
	// missing org as "no users" and soft-deleting the tenant's whole
	// projection. The history shows which branch ran: the skip schedules
	// only the Zitadel listing, never the DB listing or the reconcile.
	// Checked before the stability window so a wrong-branch run fails now,
	// not after the window has been sat out.
	scheduled, err := scheduledActivityTypes(s.Ctx, s.Temporal, syncWFID, runID)
	r.NoError(err)
	r.Equal([]string{"FetchZitadelUsersActivity"}, scheduled,
		"the post-deletion run must stop after the Zitadel listing (NotFound skip), not reconcile users against an empty org")

	// The tenant must stay listed across at least two further sync ticks.
	// A regressed sync soft-deletes within one tick, so PollStable surfaces
	// it as the check's error and the test fails fast; the green path holds
	// through every tick and exhausts the window. Transient gateway faults
	// say nothing about the invariant and skip the sample; the authoritative
	// check below covers a window that errored throughout.
	window := 3 * s.Cfg.ZitadelSyncInterval
	r.NoError(tests.PollStable(s.Ctx, window, s.Cfg.ZitadelSyncInterval/2, func() error {
		present, err := tests.TenantInList(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
		if err != nil {
			s.T().Logf("stability sample skipped (tenant list: %v)", err)
			return nil
		}
		if !present {
			return fmt.Errorf("tenant %s disappeared from the list after its org was deleted out of band", rt.ID)
		}
		return nil
	}), "tenant must stay listed for the whole %s window", window)

	present, err = tests.TenantInList(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.True(present, "tenant must stay active after its org was deleted out of band")
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
	_, took, err := waitForRunStatus(s.Ctx, s.Temporal, wfID, disableWorkflowType,
		enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, time.Time{}, workflowOutcomeTimeout)
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
	// The stability window spans 3 reconcile intervals so that at least two
	// sweep ticks are guaranteed to fire inside it — proving the sweeper saw
	// the (soft-deleted row, deleted org) pair and skipped it, rather than the
	// window falling between ticks. At the service-default cadence (5m) that
	// window is 15 minutes — past go test's own timeout — so this test only
	// runs against a test-cadence stack (local/CI .env sets 5s; envrc.sh
	// forwards it). Skipping loudly beats a vacuous or timed-out run, and
	// skipping before RegisterTenant leaves no abandoned tenant behind.
	if s.Cfg.TenantReconcileInterval > 30*time.Second {
		s.T().Skipf("stability window needs a test-cadence stack: reconcile interval is %s, want ≤30s (set PYCK_TENANT_RECONCILE_INTERVAL)", s.Cfg.TenantReconcileInterval)
	}

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
