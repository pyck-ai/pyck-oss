//go:build integration

package workflow

import (
	"context"
	"fmt"
	"time"

	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	temporalworkflow "go.temporal.io/sdk/workflow"

	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
	workflowapi "github.com/pyck-ai/pyck/backend/workflow/api"
	workflowmodel "github.com/pyck-ai/pyck/backend/workflow/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

type (
	// WfIsAssignableSuite drives a minimal keep-open workflow through
	// every layer of pyck's workflow stack to verify the
	// pyck_workflow_is_assignable search attribute round-trips end to
	// end. The workflow is hosted by an in-process Temporal worker
	// inside a freshly-provisioned tenant namespace, and triggered the
	// same way production code triggers workflows:
	//
	//	NATS publish → pyck-workflow signalrouter → SignalWithStartWorkflow
	//	→ pyck-temporal → in-process worker → workflow runs.
	//
	// The trigger shape mirrors myworkflow/cmd/trigger, but the
	// service/schema names are randomised per test so concurrent runs
	// (or future tests built on the same generic helpers) can't see
	// each other's NATS events. The test workflow body is the smallest
	// viable workflowsdk citizen: an isAssignableState struct +
	// SetupDefaults call, exercising the same handler-wiring code path
	// production workflows go through.
	//
	// Each is_assignable assertion is checked twice from independent
	// vantage points so a handler that updates only one of the two
	// representations would fail the test:
	//
	//   - getIsAssignable (gateway):
	//       gateway → pyck-workflow → Temporal Query →
	//       GetIsAssignable handler → state.IsAssignable struct field
	//
	//   - waitForIsAssignableSA (direct):
	//       Temporal SDK → DescribeWorkflowExecution →
	//       pyck_workflow_is_assignable typed search attribute,
	//       polled to span the visibility-index propagation window
	//
	// Coverage:
	//   - register tenant + machine user + grant + PAT
	//   - per-tenant Temporal namespace becomes describable
	//   - workflow registered with pyck via the gateway workflow API,
	//     including the NATS start signal so the signalrouter can map an
	//     inbound event to this workflow row (mirror of what workflowsdk's
	//     worker does on Start)
	//   - worker registers the test workflow with Temporal and starts
	//     polling
	//   - publishing a fire-and-forget mutation event lands a RUNNING
	//     execution after travelling through the signalrouter, resolved
	//     via the pyck_transaction_id handle
	//   - is_assignable defaults to true: SetupDefaults' first-run
	//     upsert wires both the in-process state field (gateway-visible)
	//     and the durable typed search attribute (Describe-visible)
	//   - setWorkflowIsAssignable(false) flips both representations;
	//     gateway Get and direct SA read both report false
	//   - toggling back to true round-trips cleanly on both paths,
	//     catching SetIsAssignable handlers that ignore the input value
	//   - cancelling the execution while the worker is running drives it
	//     to CANCELED (worker has to be live to process the cancel task)
	//   - worker can be stopped and a fresh worker started against the
	//     same namespace (register/unregister cycle on the Temporal side)
	//   - workflow unregistered from pyck (deleteWorkflow mutation)
	//   - tenant soft-deleted via a test cleanup registered at
	//     provisioning time, so it runs even on early failure
	//
	// Not covered: workflowExecutions(where:{isAssignable:true}) listing
	// filter (exercised by myworkflow's test.sh and would duplicate
	// coverage here).
	WfIsAssignableSuite struct {
		tests.Base
	}

	// isAssignableState satisfies workflowsdk.WorkflowIsAssignableGetter
	// and .WorkflowIsAssignableSetter, which is all SetupDefaults needs to
	// wire up the pyck_workflow_is_assignable search attribute on first
	// run and register both the GetIsAssignable query handler and the
	// SetIsAssignable update handler. Mirrors myworkflow/keepopen's state
	// struct so the test exercises the same code path production
	// workflows do.
	isAssignableState struct {
		IsAssignable bool
	}
)

func (s *isAssignableState) GetIsAssignable(_ temporalworkflow.Context) bool {
	return s.IsAssignable
}

func (s *isAssignableState) SetIsAssignable(_ temporalworkflow.Context, v bool) {
	s.IsAssignable = v
}

func (s *WfIsAssignableSuite) TestWfIsAssignableLifecycle() {
	l := newWfLifecycle(&s.Base, "WfIsAssignable", keepOpenAwait[isAssignableState])

	if !l.provisionWorkerIdentity() {
		return
	}
	if !l.startLifecycle() {
		return
	}

	// is_assignable defaults to true at workflow start (the workflow
	// upserts the search attribute on first run). The gateway query
	// routes through Temporal's GetIsAssignable query handler, so a
	// failure here means either the handler wasn't registered or the
	// gateway → Temporal path is broken.
	//
	// The SA read via DescribeWorkflowExecution is an independent
	// witness: gateway Get returns state.IsAssignable (the struct
	// field), the SA read returns the durable typed search attribute
	// — they're written by separate calls inside the update handler,
	// so we want both to confirm the workflow is actually assignable.
	if !s.Run("isAssignable defaults to true", func() {
		r := s.Require()
		v, err := getIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.True(v, "fresh workflow should default to is_assignable=true (gateway)")

		r.NoError(waitForIsAssignableSA(s.Ctx, l.worker.client, l.workflowID, l.runID, true, 5*time.Second),
			"pyck_workflow_is_assignable SA should be true on first run")
	}) {
		return
	}

	// setWorkflowIsAssignable(false) sends a Temporal Update to the
	// SetIsAssignable handler. The handler flips in-workflow state
	// and re-upserts the search attribute — both observable via the
	// next read.
	if !s.Run("toggle isAssignable to false", func() {
		r := s.Require()
		r.NoError(setIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, false))

		v, err := getIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.False(v, "is_assignable should read false after toggle (gateway)")

		r.NoError(waitForIsAssignableSA(s.Ctx, l.worker.client, l.workflowID, l.runID, false, 5*time.Second),
			"pyck_workflow_is_assignable SA should be false after toggle")
	}) {
		return
	}

	// Toggle back to true so the round-trip is symmetric. Catches
	// SetIsAssignable handlers that ignore the input value (always
	// write true / false regardless).
	if !s.Run("toggle isAssignable back to true", func() {
		r := s.Require()
		r.NoError(setIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, true))

		v, err := getIsAssignable(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.True(v, "is_assignable should read true after toggle back (gateway)")

		r.NoError(waitForIsAssignableSA(s.Ctx, l.worker.client, l.workflowID, l.runID, true, 5*time.Second),
			"pyck_workflow_is_assignable SA should be true after toggle back")
	}) {
		return
	}

	if !l.assertStaysRunningWithoutWorker() {
		return
	}
	if !l.reRegisterAndCancel() {
		return
	}
	l.unregisterFromPyck()
}

// getIsAssignable calls the gateway workflowIsAssignable query for the
// given execution. The query routes through pyck-workflow → Temporal
// to invoke the GetIsAssignable query handler the workflow registered
// at start.
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
		return false, fmt.Errorf("workflowIsAssignable: nil response")
	}
	return r.IsAssignable, nil
}

// setIsAssignable calls the gateway setWorkflowIsAssignable mutation,
// which sends a Temporal Update to the SetIsAssignable handler the
// workflow registered. The update both flips the in-workflow state
// and re-upserts the search attribute, so workflowExecutions filters
// see the new value on the next poll.
func setIsAssignable(ctx context.Context, cfg *config.Config, token, workflowID, runID string, value bool) error {
	c := gateway.NewWorkflowClient(cfg, token)
	_, err := c.SetWorkflowIsAssignable(ctx, workflowapi.SetWorkflowIsAssignableArgs{
		Input: workflowmodel.SetWorkflowIsAssignableInput{
			WorkflowID:          workflowID,
			WorkflowExecutionID: runID,
			IsAssignable:        value,
		},
	})
	if err != nil {
		return fmt.Errorf("setWorkflowIsAssignable: %w", err)
	}
	return nil
}

// readIsAssignableSA returns one snapshot of the
// pyck_workflow_is_assignable search attribute, read directly from
// Temporal via DescribeWorkflowExecution — bypassing the gateway and
// the workflow's query handler, so it independently verifies that an
// Update RPC actually upserted the search attribute, not just mutated
// the workflow's in-process state struct (which is what the gateway
// Get reads). present=false means the SA is genuinely absent
// (workflow hasn't upserted it yet). Callers that need to assert
// against a freshly-upserted value should use waitForIsAssignableSA
// so they're not racing the visibility index.
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

// waitForIsAssignableSA polls until the pyck_workflow_is_assignable
// search attribute has propagated to the visibility index AND matches
// the expected value. The visibility index is an async projection of
// mutable state: a value that was just written (and is already visible
// via the workflow's query handler) lags in DescribeWorkflowExecution
// by tens of milliseconds (occasionally longer), so a single-shot read
// after a gateway-side mutation can race.
func waitForIsAssignableSA(ctx context.Context, c temporalclient.Client, workflowID, runID string, want bool, timeout time.Duration) error {
	return tests.PollUntil(ctx, timeout, 100*time.Millisecond, func() error {
		v, present, err := readIsAssignableSA(ctx, c, workflowID, runID)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("search attribute not present yet")
		}
		if v != want {
			return fmt.Errorf("got %v, want %v", v, want)
		}
		return nil
	})
}
