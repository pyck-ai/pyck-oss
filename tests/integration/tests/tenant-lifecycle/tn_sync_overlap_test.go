//go:build integration

package tenants

import (
	"fmt"
	"time"

	"go.temporal.io/api/workflowservice/v1"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// zitadelSyncWorkflowType mirrors workflows.ZitadelSyncWorkflow in the
// management worker package.
const zitadelSyncWorkflowType = "ZitadelSyncWorkflow"

// TestRegisterTenant_SurvivesZitadelSyncTicks registers a tenant on the
// fast-cadence stack and waits until at least two ZitadelSyncWorkflow runs
// that started at or after the registration have completed, the first of
// them usually overlapping the registration itself. The tenant must come out
// of them untouched: still listed (a soft-deleted row is not), still done,
// its Zitadel org still ACTIVE, and its admin still able to log in. A sync
// tick that judged DB rows against a Zitadel org snapshot would read a
// tenant created between its two reads as "org gone", soft-delete it and
// deactivate the brand-new org through the delete event.
func (s *TnRegisterSuite) TestRegisterTenant_SurvivesZitadelSyncTicks() {
	r := s.Require()
	if s.Cfg.ZitadelSyncInterval > time.Minute {
		s.T().Skipf("needs the integration sync cadence (task up:integration); PYCK_ZITADEL_SYNC_EVERY is %s", s.Cfg.ZitadelSyncInterval)
	}

	startedAt := time.Now().UTC().Add(-time.Second)
	tenant := fixtures.NewTenant()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status)

	query := fmt.Sprintf(`WorkflowType = %q AND StartTime >= %q AND ExecutionStatus = "Completed"`,
		zitadelSyncWorkflowType, startedAt.Format(time.RFC3339))
	err = tests.PollUntil(s.Ctx, 2*s.Cfg.ZitadelSyncInterval+time.Minute, time.Second, func() error {
		resp, err := s.Temporal.ListWorkflow(s.Ctx, &workflowservice.ListWorkflowExecutionsRequest{Query: query})
		if err != nil {
			return fmt.Errorf("list sync runs: %w", err)
		}
		if n := len(resp.GetExecutions()); n < 2 {
			return fmt.Errorf("%d completed sync runs since the registration, want 2", n)
		}
		return nil
	})
	r.NoError(err, "two sync ticks must complete after the registration started")

	status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err, "the tenant must still be listed: a soft-deleted row is not")
	r.Equal("done", status, "a sync tick must not change the registration status")

	state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, rt.IdpOrgRef)
	r.NoError(err)
	r.Equal("ACTIVE", state, "a sync tick must not deactivate the new tenant's org")

	_, err = zitadelclient.CheckPassword(s.Ctx, s.ZConn, rt.IdpOrgRef, tenant.AdminUsername, tenant.AdminPassword)
	r.NoError(err, "the new tenant's admin must still authenticate after the sync ticks")
}
