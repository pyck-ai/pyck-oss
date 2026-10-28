//go:build integration

package tenants

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/management/ent/types"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TnDeletePendingSuite proves deleteTenant works for a row whose
// registration never finished: with no run behind it (a create event that
// was lost), and with a run still open, which deleteTenant leaves alone:
// the run notices the deleted row itself (the register-tenant workflow
// unit tests cover that half). Neither state can be reached through the
// API on a healthy stack, so the rows are put in place with psql and the
// open run is parked on a task queue no worker polls.
type TnDeletePendingSuite struct {
	tests.Base

	Temporal temporalclient.Client
}

func (s *TnDeletePendingSuite) SetupSuite() {
	s.Base.SetupSuite()
	tc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, "default", s.Cfg.ServiceToken)
	s.Require().NoError(err, "dial temporal default namespace")
	s.Temporal = tc
}

func (s *TnDeletePendingSuite) TearDownSuite() {
	if s.Temporal != nil {
		s.Temporal.Close()
	}
	s.Base.TearDownSuite()
}

// rowState reads deleted_at IS NOT NULL, registration IS NULL and status
// of a tenant row, soft-deleted or not.
func (s *TnDeletePendingSuite) rowState(id string) string {
	s.T().Helper()
	out, err := tests.Psql(s.Ctx, fmt.Sprintf(
		`SELECT (deleted_at IS NOT NULL)::text || ',' || (registration IS NULL)::text || ',' || status FROM management.tenants WHERE id = '%s'`, id))
	s.Require().NoError(err)
	return out
}

// insertPendingRow writes a pending tenant row the way insertPendingTenant
// does, minus the outbox row: no create event, so no run is ever started.
func (s *TnDeletePendingSuite) insertPendingRow(id, orgRef string) {
	s.T().Helper()
	name := fixtures.NewTenant().Name
	_, err := tests.Psql(s.Ctx, fmt.Sprintf(
		`INSERT INTO management.tenants (id, created_at, created_by, name, name_key, idp_org_ref, status, registration)
		 VALUES ('%s', now(), '%s', '%s', '%s', '%s', 'pending', '{"admin_username":"admin"}')`,
		id, uuid.Max, name, types.TenantNameKey(name), orgRef))
	s.Require().NoError(err)
}

// A pending row whose create event was lost has no run; the system
// principal retires it like any other row.
func (s *TnDeletePendingSuite) TestDeleteTenant_PendingRowWithNoRun() {
	r := s.Require()
	id := uuid.NewString()
	s.insertPendingRow(id, uuid.NewString())
	r.Equal("false,false,pending", s.rowState(id))

	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, id), "deleteTenant must work for a pending row")

	r.Equal("true,true,pending", s.rowState(id), "the row is soft-deleted and its registration data cleared")
	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, id), "deleteTenant stays idempotent on the retired row")
}

// A pending row whose registration run is still open: deleteTenant retires
// the row without touching the run, and the delete event's
// DisableTenantWorkflow deactivates the org the run had already created.
// The tenant is registered for real (so its org exists), set back to
// pending, and a run is parked under its register-tenant ID on a queue no
// worker polls, so it stays RUNNING until something ends it.
func (s *TnDeletePendingSuite) TestDeleteTenant_PendingRowWithAnOpenRun() {
	r := s.Require()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	_, err = tests.Psql(s.Ctx, fmt.Sprintf(
		`UPDATE management.tenants SET status = 'pending', registration = '{"admin_username":"admin"}' WHERE id = '%s'`, rt.ID))
	r.NoError(err)

	workflowID := registerWorkflowID(rt.ID)
	run, err := s.Temporal.ExecuteWorkflow(s.Ctx, temporalclient.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                "e2e-unpolled-" + uuid.NewString(),
		WorkflowExecutionTimeout: 10 * time.Minute,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}, registerWorkflowType, map[string]any{"TenantID": rt.ID})
	r.NoError(err, "park a registration run")
	desc, err := s.Temporal.DescribeWorkflowExecution(s.Ctx, workflowID, run.GetRunID())
	r.NoError(err)
	r.Equal(enums.WORKFLOW_EXECUTION_STATUS_RUNNING, desc.GetWorkflowExecutionInfo().GetStatus())

	s.T().Cleanup(func() {
		if err := s.Temporal.TerminateWorkflow(s.Ctx, workflowID, run.GetRunID(), "tn-delete-pending suite cleanup"); err != nil {
			s.T().Logf("cleanup: terminate %s: %v", workflowID, err)
		}
	})

	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID), "deleteTenant must work while the run is open")

	desc, err = s.Temporal.DescribeWorkflowExecution(s.Ctx, workflowID, run.GetRunID())
	r.NoError(err)
	r.Equal(enums.WORKFLOW_EXECUTION_STATUS_RUNNING, desc.GetWorkflowExecutionInfo().GetStatus(),
		"deleteTenant never touches the registration run; the run notices the deleted row itself")
	r.Equal("true,true,pending", s.rowState(rt.ID))

	_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "INACTIVE", 60*time.Second)
	r.NoError(err, "the delete event's DisableTenantWorkflow deactivates the org the run had created")

	// A second delete finds the row already retired and answers as done.
	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID))
}

// A pending row whose registration run already completed is retired like
// any other, and the closed run stays as it was. The tenant is registered
// for real (its run completes) and set back to pending with psql, the
// state a run whose done flip did not land leaves behind.
func (s *TnDeletePendingSuite) TestDeleteTenant_PendingRowWhoseRunCompleted() {
	r := s.Require()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	desc, err := s.Temporal.DescribeWorkflowExecution(s.Ctx, registerWorkflowID(rt.ID), "")
	r.NoError(err)
	r.Equal(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, desc.GetWorkflowExecutionInfo().GetStatus(),
		"the registration run must be closed before the delete")

	_, err = tests.Psql(s.Ctx, fmt.Sprintf(
		`UPDATE management.tenants SET status = 'pending' WHERE id = '%s'`, rt.ID))
	r.NoError(err)

	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID), "deleteTenant must work for a pending row whose run closed")

	r.Equal("true,true,pending", s.rowState(rt.ID), "the row is soft-deleted and its registration data cleared")
	desc, err = s.Temporal.DescribeWorkflowExecution(s.Ctx, registerWorkflowID(rt.ID), "")
	r.NoError(err)
	r.Equal(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, desc.GetWorkflowExecutionInfo().GetStatus(),
		"the completed run stays completed")
}
