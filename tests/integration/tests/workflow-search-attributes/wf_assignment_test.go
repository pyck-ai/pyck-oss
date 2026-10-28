//go:build integration

package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/authn"
	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	workflowapi "github.com/pyck-ai/pyck/backend/workflow/api"
	workflowmodel "github.com/pyck-ai/pyck/backend/workflow/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// saTimeout bounds every wait for a search attribute or a
	// visibility-backed list filter to catch up with an update. Both are
	// async projections of the workflow's mutable state; they normally lag
	// by milliseconds.
	saTimeout = 10 * time.Second

	// The refusals this suite expects, as the gateway reports them. Matching
	// the message keeps a call that fails for another reason from passing.
	errWriterRoleRequired    = "writer role required"
	errAssigneeNotTenantUser = "assignee is not a user of this tenant"
	errInvalidWorkflowID     = "invalid workflow id"
	errInvalidExecutionID    = "invalid workflow execution id"
	// The assignee resolvers leave id validation to the workflow client.
	errClientInvalidWorkflowID = "invalid WorkflowID"
	errClientInvalidRunID      = "invalid WorkflowRunID"
	// Temporal's own answers.
	errTemporalInvalidRunID = "Invalid RunId"
	errExecutionNotFound    = "workflow execution not found"
	errExecutionCompleted   = "workflow execution already completed"
)

type (
	// WfAssignmentSuite drives one keep-open workflow through every layer of
	// pyck's workflow stack and covers the whole assignment API on it:
	// setWorkflowIsAssignable / workflowIsAssignable,
	// setWorkflowAssignee / workflowAssignee, the isAssignable and assignee
	// filters of workflowExecutions, and the assignableWorkflowExecutions
	// wrapper. The workflow is hosted by an in-process Temporal worker inside
	// a freshly-provisioned tenant namespace and triggered the way production
	// code triggers workflows:
	//
	//	NATS publish → pyck-workflow signalrouter → SignalWithStartWorkflow
	//	→ pyck-temporal → in-process worker → workflow runs.
	//
	// Every write is checked from two vantage points, so a handler that
	// updates only one representation fails:
	//
	//   - the gateway getter (workflowIsAssignable / workflowAssignee), and
	//     the gateway list filters, which read Temporal's visibility index;
	//   - the typed search attribute read directly from Temporal with
	//     DescribeWorkflowExecution.
	//
	// Coverage, in order:
	//   - defaults: assignable, no assignee; both list filters agree
	//   - isAssignable false → true round trip, on the getter, the search
	//     attribute, workflowExecutions(isAssignable) and
	//     assignableWorkflowExecutions
	//   - assign the caller, reassign to another user of the tenant, and
	//     unassign, on the getter, the search attribute and
	//     workflowExecutions(assignee / assigneeIsNil)
	//   - a reader of the tenant can read but not change either value
	//   - an assignee who is no user of the tenant is refused: an unknown
	//     id, and a real user of another tenant
	//   - a writer of another tenant can neither read nor change this
	//     execution, and does not see it in its own lists
	//   - empty and oversized ids are refused and change nothing
	//   - the execution survives a worker outage; a cancelled execution can
	//     no longer be changed
	//
	// Every refusal is matched on its message, so a call that fails for
	// another reason (an expired token, an outage, an earlier check) does
	// not pass as the refusal under test.
	WfAssignmentSuite struct {
		tests.Base
	}

	// assignmentState satisfies the workflowsdk getter/setter interfaces for
	// both is_assignable and the assignee, which is all SetupDefaults needs to
	// upsert the defaults and register the query and update handlers the
	// gateway calls.
	assignmentState struct {
		IsAssignable bool
		Assignee     common_workflow.WorkflowAssignee
	}
)

func (s *assignmentState) GetIsAssignable(_ temporalworkflow.Context) bool {
	return s.IsAssignable
}

func (s *assignmentState) SetIsAssignable(_ temporalworkflow.Context, v bool) {
	s.IsAssignable = v
}

func (s *assignmentState) GetAssignee(_ temporalworkflow.Context) common_workflow.WorkflowAssignee {
	return s.Assignee
}

func (s *assignmentState) SetAssignee(_ temporalworkflow.Context, a common_workflow.WorkflowAssignee) {
	s.Assignee = a
}

// assignment is the state the test expects the execution to be in; every
// stage that changes it updates the expectation, and the refusal stages
// assert it is unchanged.
type assignment struct {
	isAssignable bool
	assignee     string // "" = unassigned
}

func (s *WfAssignmentSuite) TestWfAssignmentLifecycle() {
	l := newWfLifecycle(&s.Base, "WfAssignment", keepOpenAwait[assignmentState])

	if !l.provisionWorkerIdentity() {
		return
	}
	if !l.startLifecycle() {
		return
	}

	want := assignment{isAssignable: true}

	// SetupDefaults upserts is_assignable=true on the first run and leaves the
	// assignee unset. The list filters must agree with the getters.
	if !s.Run("defaults: assignable and unassigned", func() {
		s.requireAssignment(l, want)
	}) {
		return
	}

	// setWorkflowIsAssignable sends a Temporal Update to the SetIsAssignable
	// handler, which re-upserts the search attribute. The execution must drop
	// out of isAssignable=true and out of assignableWorkflowExecutions.
	if !s.Run("isAssignable false: getter, search attribute and filters follow", func() {
		r := s.Require()
		got, err := setIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, false)
		r.NoError(err)
		r.False(got, "setWorkflowIsAssignable must echo the value it wrote")
		want.isAssignable = false
		s.requireAssignment(l, want)
	}) {
		return
	}

	// Back to true, so a handler that ignores its input (always writes the
	// same value) cannot pass.
	if !s.Run("isAssignable true again: getter, search attribute and filters follow", func() {
		r := s.Require()
		got, err := setIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, true)
		r.NoError(err)
		r.True(got)
		want.isAssignable = true
		s.requireAssignment(l, want)
	}) {
		return
	}

	// setWorkflowAssignee accepts only a live user of the tenant. The caller
	// may always assign themself; anyone else needs a users row, which the
	// Zitadel sync writes some time after the user is created.
	self := l.userID

	if !s.Run("assign the caller: getter, search attribute and assignee filter follow", func() {
		r := s.Require()
		got, err := setAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, self)
		r.NoError(err)
		r.Equal(self, got, "setWorkflowAssignee must echo the assignee it wrote")
		want.assignee = self
		s.requireAssignment(l, want)
	}) {
		return
	}

	var colleague string
	if !s.Run("a second user of the tenant is synced", func() {
		r := s.Require()
		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, l.registered, []string{"reader"})
		r.NoError(err)
		colleague = pyckUserID(s.Cfg, p.UserID)
		r.NoError(waitForTenantUser(s.Ctx, s.Cfg, l.registered.ID, colleague))
	}) {
		return
	}

	if !s.Run("reassign to another user of the tenant", func() {
		r := s.Require()
		got, err := setAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, colleague)
		r.NoError(err)
		r.Equal(colleague, got)
		want.assignee = colleague
		s.requireAssignment(l, want)
		r.NoError(waitForCount(s.Ctx, l, &workflowmodel.WorkflowExecutionsWhereInput{Assignee: &self}, 0),
			"the previous assignee must no longer match")
	}) {
		return
	}

	// assigneeID omitted clears the assignee: the handler unsets the search
	// attribute instead of writing an empty string.
	if !s.Run("unassign: assignee cleared everywhere", func() {
		r := s.Require()
		got, err := setAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, "")
		r.NoError(err)
		r.Empty(got, "clearing must answer with no assignee")
		want.assignee = ""
		s.requireAssignment(l, want)
	}) {
		return
	}

	// Leave an assignee in place so the refusal stages below can prove that
	// a refused write changed nothing on either attribute.
	if !s.Run("assign the caller again before the refusal checks", func() {
		r := s.Require()
		_, err := setAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, self)
		r.NoError(err)
		want.assignee = self
		s.requireAssignment(l, want)
	}) {
		return
	}

	if !s.Run("a reader can read the assignment but not change it", func() {
		r := s.Require()
		reader, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, l.registered, []string{"reader"})
		r.NoError(err)

		v, err := getIsAssignable(s.Ctx, s.Cfg, reader.PAT, l.workflowID, l.runID)
		r.NoError(err)
		r.Equal(want.isAssignable, v)
		a, err := getAssignee(s.Ctx, s.Cfg, reader.PAT, l.workflowID, l.runID)
		r.NoError(err)
		r.Equal(want.assignee, a)

		_, err = setIsAssignable(s.Ctx, s.Cfg, reader.PAT, l.workflowID, l.runID, !want.isAssignable)
		r.ErrorContains(err, errWriterRoleRequired)
		_, err = setAssignee(s.Ctx, s.Cfg, reader.PAT, l.workflowID, l.runID, colleague)
		r.ErrorContains(err, errWriterRoleRequired)
		_, err = setAssignee(s.Ctx, s.Cfg, reader.PAT, l.workflowID, l.runID, "")
		r.ErrorContains(err, errWriterRoleRequired, "a reader must not clear the assignee either")

		s.requireAssignment(l, want)
	}) {
		return
	}

	// A second tenant whose writer is synced into its users, so the stages
	// below have a real user who belongs to another tenant.
	var foreign *tests.ProvisionedTenant
	var foreignUser string
	if !s.Run("another tenant with a synced writer", func() {
		r := s.Require()
		other, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		r.NoError(err)
		s.DeferTenantCleanup(other.ID)
		foreign, err = tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, other, []string{"writer"})
		r.NoError(err)
		foreignUser = pyckUserID(s.Cfg, foreign.UserID)
		r.NoError(waitForTenantUser(s.Ctx, s.Cfg, other.ID, foreignUser))
		_, err = waitForNamespace(s.Ctx, s.Cfg.TemporalAddress, other.ID, foreign.PAT, 30*time.Second)
		r.NoError(err)
	}) {
		return
	}

	if !s.Run("an assignee outside the tenant is refused and changes nothing", func() {
		r := s.Require()
		_, err := setAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, uuid.NewString())
		r.ErrorContains(err, errAssigneeNotTenantUser, "an id that belongs to no one")
		_, err = setAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, foreignUser)
		r.ErrorContains(err, errAssigneeNotTenantUser, "a real user of another tenant")
		s.requireAssignment(l, want)
	}) {
		return
	}

	// The tenant comes from the caller's token, so another tenant's writer is
	// routed to its own namespace, where this execution does not exist. The
	// writer assigns themself, which needs no users lookup, so the refusal
	// comes from the missing execution.
	if !s.Run("another tenant can neither read nor change the execution", func() {
		r := s.Require()
		_, err := getIsAssignable(s.Ctx, s.Cfg, foreign.PAT, l.workflowID, l.runID)
		r.ErrorContains(err, errExecutionNotFound, "another tenant read this execution's is_assignable")
		_, err = getAssignee(s.Ctx, s.Cfg, foreign.PAT, l.workflowID, l.runID)
		r.ErrorContains(err, errExecutionNotFound, "another tenant read this execution's assignee")
		_, err = setIsAssignable(s.Ctx, s.Cfg, foreign.PAT, l.workflowID, l.runID, !want.isAssignable)
		r.ErrorContains(err, errExecutionNotFound, "another tenant changed this execution's is_assignable")
		_, err = setAssignee(s.Ctx, s.Cfg, foreign.PAT, l.workflowID, l.runID, foreignUser)
		r.ErrorContains(err, errExecutionNotFound, "another tenant changed this execution's assignee")

		wfID := l.workflowID
		n, err := countExecutions(s.Ctx, s.Cfg, foreign.PAT, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID})
		r.NoError(err)
		r.Zero(n, "another tenant lists this execution")
		n, err = countAssignableExecutions(s.Ctx, s.Cfg, foreign.PAT, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID})
		r.NoError(err)
		r.Zero(n, "another tenant lists this execution as assignable")

		s.requireAssignment(l, want)
	}) {
		return
	}

	// The isAssignable resolvers validate both ids themselves; the assignee
	// resolvers leave it to the workflow client, which answers with its own
	// messages and leaves the run id's length to Temporal.
	if !s.Run("unusable execution ids are refused and change nothing", func() {
		oversized := strings.Repeat("x", 2000)
		ids := []struct {
			name, workflowID, runID      string
			isAssignableErr, assigneeErr string
		}{
			{"empty workflow id", "", l.runID, errInvalidWorkflowID, errClientInvalidWorkflowID},
			{"empty run id", l.workflowID, "", errInvalidExecutionID, errClientInvalidRunID},
			{"oversized workflow id", oversized, l.runID, errInvalidWorkflowID, errClientInvalidWorkflowID},
			{"oversized run id", l.workflowID, oversized, errInvalidExecutionID, errTemporalInvalidRunID},
		}
		for _, id := range ids {
			s.Run(id.name, func() {
				r := s.Require()
				_, err := getIsAssignable(s.Ctx, s.Cfg, l.pat, id.workflowID, id.runID)
				r.ErrorContains(err, id.isAssignableErr, "workflowIsAssignable")
				_, err = getAssignee(s.Ctx, s.Cfg, l.pat, id.workflowID, id.runID)
				r.ErrorContains(err, id.assigneeErr, "workflowAssignee")
				_, err = setIsAssignable(s.Ctx, s.Cfg, l.pat, id.workflowID, id.runID, !want.isAssignable)
				r.ErrorContains(err, id.isAssignableErr, "setWorkflowIsAssignable")
				// Self passes the assignee check, so the refusal is the id's.
				_, err = setAssignee(s.Ctx, s.Cfg, l.pat, id.workflowID, id.runID, self)
				r.ErrorContains(err, id.assigneeErr, "setWorkflowAssignee")
			})
		}
		s.requireAssignment(l, want)
	}) {
		return
	}

	if !l.assertStaysRunningWithoutWorker() {
		return
	}
	if !l.reRegisterAndCancel() {
		return
	}

	// Updates need a running execution: once it is cancelled, both setters
	// must fail instead of reporting a change that never happened. The
	// assignee is the caller, so the refusal is the closed run's.
	if !s.Run("a finished execution can no longer be changed", func() {
		r := s.Require()
		_, err := setIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, !want.isAssignable)
		r.ErrorContains(err, errExecutionCompleted, "setWorkflowIsAssignable on a cancelled execution")
		_, err = setAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, self)
		r.ErrorContains(err, errExecutionCompleted, "setWorkflowAssignee on a cancelled execution")

		v, err := getIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err, "the getter still reads a closed execution")
		r.Equal(want.isAssignable, v)
		a, err := getAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.Equal(want.assignee, a)
	}) {
		return
	}

	l.unregisterFromPyck()
}

// requireAssignment asserts that every read path reports want: the gateway
// getters, the search attributes read directly from Temporal, and the
// gateway list filters. The search attribute and the filters are polled
// because the visibility index lags the update.
func (s *WfAssignmentSuite) requireAssignment(l *wfLifecycle, want assignment) {
	r := s.Require()

	v, err := getIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
	r.NoError(err)
	r.Equal(want.isAssignable, v, "workflowIsAssignable (gateway)")
	a, err := getAssignee(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
	r.NoError(err)
	r.Equal(want.assignee, a, "workflowAssignee (gateway)")

	r.NoError(waitForIsAssignableSA(s.Ctx, l.worker.client, l.workflowID, l.runID, want.isAssignable, saTimeout),
		"pyck_workflow_is_assignable search attribute")
	r.NoError(waitForAssigneeSA(s.Ctx, l.worker.client, l.workflowID, l.runID, want.assignee, saTimeout),
		"pyck_workflow_assignee search attribute")

	wfID := l.workflowID
	yes, no := true, false
	inAssignable, inNotAssignable := 0, 1
	if want.isAssignable {
		inAssignable, inNotAssignable = 1, 0
	}
	r.NoError(waitForCount(s.Ctx, l, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID, IsAssignable: &yes}, inAssignable),
		"workflowExecutions(isAssignable: true)")
	r.NoError(waitForCount(s.Ctx, l, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID, IsAssignable: &no}, inNotAssignable),
		"workflowExecutions(isAssignable: false)")
	r.NoError(waitForExecutionCount(s.Ctx, inAssignable, func() (int, error) {
		return countAssignableExecutions(s.Ctx, s.Cfg, l.pat, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID})
	}), "assignableWorkflowExecutions")

	unassigned, assigned := 1, 0
	if want.assignee != "" {
		unassigned, assigned = 0, 1
		assignee := want.assignee
		r.NoError(waitForCount(s.Ctx, l, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID, Assignee: &assignee}, 1),
			"workflowExecutions(assignee)")
	}
	r.NoError(waitForCount(s.Ctx, l, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID, AssigneeIsNil: &yes}, unassigned),
		"workflowExecutions(assigneeIsNil)")
	r.NoError(waitForCount(s.Ctx, l, &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &wfID, AssigneeNotNil: &yes}, assigned),
		"workflowExecutions(assigneeNotNil)")
}

// getIsAssignable calls the gateway workflowIsAssignable query for the given
// execution.
func getIsAssignable(ctx context.Context, cfg *config.Config, token, workflowID, runID string) (bool, error) {
	c := gateway.NewWorkflowClient(cfg, token)
	resp, err := c.GetWorkflowIsAssignable(ctx, workflowapi.GetWorkflowIsAssignableArgs{
		Input: workflowmodel.GetWorkflowIsAssignableInput{
			WorkflowID:          workflowID,
			WorkflowExecutionID: runID,
		},
	})
	if err != nil {
		return false, fmt.Errorf("workflowIsAssignable: %w", err)
	}
	r := resp.GetWorkflowIsAssignable()
	if r == nil {
		return false, errors.New("workflowIsAssignable: nil response")
	}
	return r.IsAssignable, nil
}

// setIsAssignable calls the gateway setWorkflowIsAssignable mutation, which
// sends a Temporal Update to the workflow's SetIsAssignable handler, and
// returns the value the mutation reports.
func setIsAssignable(ctx context.Context, cfg *config.Config, token, workflowID, runID string, value bool) (bool, error) {
	c := gateway.NewWorkflowClient(cfg, token)
	resp, err := c.SetWorkflowIsAssignable(ctx, workflowapi.SetWorkflowIsAssignableArgs{
		Input: workflowmodel.SetWorkflowIsAssignableInput{
			WorkflowID:          workflowID,
			WorkflowExecutionID: runID,
			IsAssignable:        value,
		},
	})
	if err != nil {
		return false, fmt.Errorf("setWorkflowIsAssignable: %w", err)
	}
	r := resp.GetSetWorkflowIsAssignable()
	if r == nil {
		return false, errors.New("setWorkflowIsAssignable: nil response")
	}
	return r.IsAssignable, nil
}

// getAssignee calls the gateway workflowAssignee query; "" means unassigned.
func getAssignee(ctx context.Context, cfg *config.Config, token, workflowID, runID string) (string, error) {
	c := gateway.NewWorkflowClient(cfg, token)
	resp, err := c.GetWorkflowAssignee(ctx, workflowapi.GetWorkflowAssigneeArgs{
		Input: workflowmodel.GetWorkflowAssigneeInput{
			WorkflowID:          workflowID,
			WorkflowExecutionID: runID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("workflowAssignee: %w", err)
	}
	r := resp.GetWorkflowAssignee()
	if r == nil {
		return "", errors.New("workflowAssignee: nil response")
	}
	return r.Assignee, nil
}

// setAssignee calls the gateway setWorkflowAssignee mutation. An empty
// assignee omits assigneeID, which clears the assignment. It returns the
// assignee the mutation reports ("" when cleared).
func setAssignee(ctx context.Context, cfg *config.Config, token, workflowID, runID, assignee string) (string, error) {
	input := workflowmodel.SetWorkflowAssigneeInput{
		WorkflowID:          workflowID,
		WorkflowExecutionID: runID,
	}
	if assignee != "" {
		id, err := uuid.Parse(assignee)
		if err != nil {
			return "", fmt.Errorf("parse assignee %q: %w", assignee, err)
		}
		input.AssigneeID = &id
	}

	c := gateway.NewWorkflowClient(cfg, token)
	resp, err := c.SetWorkflowAssignee(ctx, workflowapi.SetWorkflowAssigneeArgs{Input: input})
	if err != nil {
		return "", fmt.Errorf("setWorkflowAssignee: %w", err)
	}
	r := resp.GetSetWorkflowAssignee()
	if r == nil {
		return "", errors.New("setWorkflowAssignee: nil response")
	}
	if r.Assignee == nil {
		return "", nil
	}
	return *r.Assignee, nil
}

// pyckUserID returns the pyck user id of a Zitadel user. The Zitadel sync
// writes the users row under ComputeUUID(audience, user id), and authn derives
// the caller's id as ComputeUUID(issuer, sub). The test config defaults the
// audience to the issuer, so both agree.
func pyckUserID(cfg *config.Config, zitadelUserID string) string {
	return authn.ComputeUUID(cfg.ZitadelAudience, zitadelUserID).String()
}

// waitForTenantUser polls management's users query until userID has a live
// row in tenantID. The Zitadel sync writes that row some time after the user
// is created, and setWorkflowAssignee refuses anyone without one. The service
// token skips management's tenant filter, so the query names the tenant.
func waitForTenantUser(ctx context.Context, cfg *config.Config, tenantID, userID string) error {
	c := gateway.NewClient(cfg, cfg.ServiceToken)
	first := 1
	return tests.PollUntil(ctx, 60*time.Second, 500*time.Millisecond, func() error {
		resp, err := c.GetUsers(ctx, managementapi.GetUsersArgs{
			First: &first,
			Where: &managementapi.UserWhereInput{ID: &userID, TenantID: &tenantID},
		})
		if err != nil {
			return fmt.Errorf("users: %w", err)
		}
		for _, edge := range resp.GetUsers().GetEdges() {
			if node := edge.GetNode(); node != nil && node.DeletedAt == nil {
				return nil
			}
		}
		return fmt.Errorf("user %s not synced into tenant %s yet", userID, tenantID)
	})
}

// waitForCount polls workflowExecutions(where) as the lifecycle's own tenant
// until it reports want executions.
func waitForCount(ctx context.Context, l *wfLifecycle, where *workflowmodel.WorkflowExecutionsWhereInput, want int) error {
	return waitForExecutionCount(ctx, want, func() (int, error) {
		return countExecutions(ctx, l.base.Cfg, l.pat, where)
	})
}

// readIsAssignableSA returns one snapshot of the pyck_workflow_is_assignable
// search attribute, read directly from Temporal via
// DescribeWorkflowExecution, bypassing the gateway. present=false means the
// attribute is absent.
func readIsAssignableSA(ctx context.Context, c temporalclient.Client, workflowID, runID string) (value bool, present bool, err error) {
	payload, present, err := describeSearchAttributePayload(ctx, c, workflowID, runID, common_workflow.PyckWorkflowIsAssignable.GetName())
	if err != nil || !present {
		return false, present, err
	}
	if err := converter.GetDefaultDataConverter().FromPayload(payload, &value); err != nil {
		return false, false, fmt.Errorf("decode pyck_workflow_is_assignable: %w", err)
	}
	return value, true, nil
}

// waitForIsAssignableSA polls until pyck_workflow_is_assignable is present
// and equals want.
func waitForIsAssignableSA(ctx context.Context, c temporalclient.Client, workflowID, runID string, want bool, timeout time.Duration) error {
	return tests.PollUntil(ctx, timeout, 100*time.Millisecond, func() error {
		v, present, err := readIsAssignableSA(ctx, c, workflowID, runID)
		if err != nil {
			return err
		}
		if !present {
			return errors.New("search attribute not present yet")
		}
		if v != want {
			return fmt.Errorf("got %v, want %v", v, want)
		}
		return nil
	})
}

// waitForAssigneeSA polls until pyck_workflow_assignee equals want, read
// directly from Temporal. want "" means the attribute must be absent: the
// SDK unsets it rather than writing an empty string.
func waitForAssigneeSA(ctx context.Context, c temporalclient.Client, workflowID, runID, want string, timeout time.Duration) error {
	return tests.PollUntil(ctx, timeout, 100*time.Millisecond, func() error {
		payload, present, err := describeSearchAttributePayload(ctx, c, workflowID, runID, common_workflow.PyckWorkflowAssignee.GetName())
		if err != nil {
			return err
		}
		if want == "" {
			if present {
				return errors.New("pyck_workflow_assignee still present")
			}
			return nil
		}
		if !present {
			return errors.New("pyck_workflow_assignee not present yet")
		}
		var got string
		if err := converter.GetDefaultDataConverter().FromPayload(payload, &got); err != nil {
			return fmt.Errorf("decode pyck_workflow_assignee: %w", err)
		}
		if got != want {
			return fmt.Errorf("got %q, want %q", got, want)
		}
		return nil
	})
}
