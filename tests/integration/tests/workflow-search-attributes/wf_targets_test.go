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
	// WfTargetsSuite drives a minimal keep-open workflow through every
	// layer of pyck's workflow stack to verify the
	// pyck_workflow_targets KeywordList search attribute round-trips
	// end to end. Same shape as WfIsAssignableSuite — register tenant,
	// register workflow with pyck, host an in-process worker, trigger
	// via NATS, set/get targets via the gateway — but exercises the
	// WorkflowTargetsGetter/Setter interface pair instead of the
	// is_assignable pair.
	//
	// Targets is more interesting than is_assignable because it's a
	// KeywordList: the SA can hold zero, one, or many entries, and
	// SetWorkflowTargets unsets the SA entirely when the slice is
	// empty (vs keeping a "false"). The phases below cover all four
	// states.
	//
	// Each targets assertion is checked twice from independent vantage
	// points so a handler that updates only one of the two
	// representations would fail the test:
	//
	//   - getTargets (gateway):
	//       gateway → pyck-workflow → Temporal Query →
	//       GetTargets handler → state.Targets struct field
	//
	//   - waitForTargetsSA (direct):
	//       Temporal SDK → DescribeWorkflowExecution →
	//       pyck_workflow_targets KeywordList search attribute,
	//       polled to span the visibility-index propagation window
	//
	// Coverage:
	//   - register tenant + machine user + grant + PAT
	//   - per-tenant Temporal namespace becomes describable
	//   - workflow registered with pyck via the gateway workflow API,
	//     including the NATS start signal so the signalrouter can map
	//     an inbound event to this workflow row
	//   - worker registers the test workflow with Temporal and starts
	//     polling
	//   - publishing a MutationEventWithReplyTopic message lands a
	//     RUNNING execution after travelling through the signalrouter
	//   - targets defaults to empty: SetupDefaults does NOT seed the
	//     SA when the targets slice is empty, so the SA stays absent
	//     from visibility (gateway returns empty list)
	//   - setWorkflowTargets([WEB]) writes both representations;
	//     gateway Get and direct SA read both report [WEB]
	//   - setWorkflowTargets([WEB,MOBILE]) round-trips multi-element
	//     lists, catching handlers that drop entries
	//   - setWorkflowTargets([]) unsets the SA (KeywordList ValueUnset
	//     semantics, not "empty list"); gateway Get returns empty,
	//     direct SA read confirms the field is absent from
	//     IndexedFields
	//   - cancelling the execution while the worker is running drives
	//     it to CANCELED
	//   - worker can be stopped and a fresh worker started against the
	//     same namespace
	//   - workflow unregistered from pyck (deleteWorkflow mutation)
	//   - tenant soft-deleted via a test cleanup registered at
	//     provisioning time, so it runs even on early failure
	//
	// Not covered: workflowExecutions(where:{targets:[WEB]}) and
	// targetsNotIn listing filters (exercised by myworkflow's tests).
	WfTargetsSuite struct {
		tests.Base
	}

	// targetsState satisfies workflowsdk.WorkflowTargetsGetter and
	// .WorkflowTargetsSetter, which is all SetupDefaults needs to
	// register both the GetTargets query handler and the SetTargets
	// update handler. Mirrors myworkflow/workflows/targets's state
	// struct so the test exercises the same code path production
	// workflows do.
	targetsState struct {
		Targets []common_workflow.WorkflowTarget
	}
)

func (s *targetsState) GetTargets(_ temporalworkflow.Context) []common_workflow.WorkflowTarget {
	return s.Targets
}

func (s *targetsState) SetTargets(_ temporalworkflow.Context, v []common_workflow.WorkflowTarget) {
	s.Targets = v
}

func (s *WfTargetsSuite) TestWfTargetsLifecycle() {
	l := newWfLifecycle(&s.Base, "WfTargets", keepOpenAwait[targetsState])

	if !l.provisionWorkerIdentity() {
		return
	}
	if !l.startLifecycle() {
		return
	}

	// Targets default to empty. Unlike is_assignable, SetupDefaults
	// does NOT seed the SA when the slice is empty — it leaves the
	// search attribute genuinely absent from visibility. Gateway Get
	// reads state.Targets (zero-value nil slice → empty list); direct
	// SA read confirms IndexedFields doesn't carry the key.
	if !s.Run("targets default to empty", func() {
		r := s.Require()
		v, err := getTargets(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.Empty(v, "fresh workflow should default to empty targets (gateway)")

		_, present, err := readTargetsSA(s.Ctx, l.worker.client, l.workflowID, l.runID)
		r.NoError(err)
		r.False(present, "pyck_workflow_targets SA should be absent before any setTargets")
	}) {
		return
	}

	// Set a single target. The update handler upserts the SA as
	// KeywordList["WEB"] and syncs state.Targets. Both read paths
	// must agree.
	if !s.Run("set targets to [WEB]", func() {
		r := s.Require()
		want := []workflowmodel.WorkflowTarget{workflowmodel.WorkflowTargetWeb}
		r.NoError(setTargets(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, want))

		v, err := getTargets(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.Equal(want, v, "gateway should report [WEB]")

		r.NoError(waitForTargetsSA(s.Ctx, l.worker.client, l.workflowID, l.runID,
			[]string{"WEB"}, 5*time.Second),
			"pyck_workflow_targets SA should be [WEB]")
	}) {
		return
	}

	// Multi-element list catches handlers that drop entries during
	// marshalling, plus any KeywordList ordering bugs in the
	// signalrouter / SDK round-trip.
	if !s.Run("set targets to [WEB, MOBILE]", func() {
		r := s.Require()
		want := []workflowmodel.WorkflowTarget{
			workflowmodel.WorkflowTargetWeb,
			workflowmodel.WorkflowTargetMobile,
		}
		r.NoError(setTargets(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, want))

		v, err := getTargets(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.ElementsMatch(want, v, "gateway should report [WEB, MOBILE]")

		r.NoError(waitForTargetsSA(s.Ctx, l.worker.client, l.workflowID, l.runID,
			[]string{"WEB", "MOBILE"}, 5*time.Second),
			"pyck_workflow_targets SA should be [WEB, MOBILE]")
	}) {
		return
	}

	// Setting targets to empty is the interesting transition: the
	// workflowsdk helper calls PyckWorkflowTargets.ValueUnset(), which
	// removes the SA from visibility entirely (vs storing "[]"). The
	// gateway should report empty, the direct SA read should report
	// the field as absent from IndexedFields.
	if !s.Run("clear targets ([])", func() {
		r := s.Require()
		// Pass an explicit non-nil empty slice — the GraphQL input
		// type is `[WorkflowTarget!]!` (non-null list of non-nullable
		// values), so a nil slice is serialised as missing-field and
		// rejected by Apollo's input validation. An empty slice is
		// serialised as `[]`, which the schema accepts and the
		// resolver forwards to the SetTargets handler. The handler's
		// SetWorkflowTargets helper sees len==0 and calls
		// PyckWorkflowTargets.ValueUnset() to remove the SA.
		r.NoError(setTargets(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID, []workflowmodel.WorkflowTarget{}))

		v, err := getTargets(s.Ctx, s.Cfg, l.pat, l.workflowID, l.runID)
		r.NoError(err)
		r.Empty(v, "gateway should report empty after clearing")

		r.NoError(waitForTargetsSAAbsent(s.Ctx, l.worker.client, l.workflowID, l.runID, 5*time.Second),
			"pyck_workflow_targets SA should be unset after clearing")
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

// getTargets calls the gateway workflowTargets query. The query
// routes through pyck-workflow → Temporal to invoke the GetTargets
// query handler the workflow registered at start.
func getTargets(ctx context.Context, cfg *config.Config, token, workflowID, runID string) ([]workflowmodel.WorkflowTarget, error) {
	c := gateway.NewWorkflowClient(cfg, token)
	resp, err := c.GetWorkflowTargets(ctx, workflowapi.GetWorkflowTargetsArgs{
		Input: workflowmodel.GetWorkflowTargetsInput{
			WorkflowID:          workflowID,
			WorkflowExecutionID: runID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("workflowTargets: %w", err)
	}
	r := resp.GetWorkflowTargets()
	if r == nil {
		return nil, fmt.Errorf("workflowTargets: nil response")
	}
	return r.Targets, nil
}

// setTargets calls the gateway setWorkflowTargets mutation, which
// sends a Temporal Update to the SetTargets handler the workflow
// registered. The update both writes state.Targets and re-upserts
// the search attribute (or unsets it, when the slice is empty).
func setTargets(ctx context.Context, cfg *config.Config, token, workflowID, runID string, value []workflowmodel.WorkflowTarget) error {
	c := gateway.NewWorkflowClient(cfg, token)
	_, err := c.SetWorkflowTargets(ctx, workflowapi.SetWorkflowTargetsArgs{
		Input: workflowmodel.SetWorkflowTargetsInput{
			WorkflowID:          workflowID,
			WorkflowExecutionID: runID,
			Targets:             value,
		},
	})
	if err != nil {
		return fmt.Errorf("setWorkflowTargets: %w", err)
	}
	return nil
}

// readTargetsSA returns one snapshot of the pyck_workflow_targets
// search attribute from DescribeWorkflowExecution. KeywordList SAs
// decode into []string; empty/cleared targets are stored as
// PyckWorkflowTargets.ValueUnset(), which removes the field from
// IndexedFields entirely (present=false), distinct from "present
// with empty list".
func readTargetsSA(ctx context.Context, c temporalclient.Client, workflowID, runID string) (values []string, present bool, err error) {
	payload, present, err := describeSearchAttributePayload(ctx, c, workflowID, runID, common_workflow.PyckWorkflowTargets.GetName())
	if err != nil || !present {
		return nil, present, err
	}
	if err := converter.GetDefaultDataConverter().FromPayload(payload, &values); err != nil {
		return nil, false, fmt.Errorf("decode pyck_workflow_targets: %w", err)
	}
	return values, true, nil
}

// waitForTargetsSA polls until the pyck_workflow_targets SA matches
// the expected list (order-insensitive). Use this for the "set to
// non-empty" transitions; visibility lags the workflow's mutable
// state by tens of milliseconds.
func waitForTargetsSA(ctx context.Context, c temporalclient.Client, workflowID, runID string, want []string, timeout time.Duration) error {
	return tests.PollUntil(ctx, timeout, 100*time.Millisecond, func() error {
		got, present, err := readTargetsSA(ctx, c, workflowID, runID)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("search attribute not present yet")
		}
		if !sameElements(got, want) {
			return fmt.Errorf("got %v, want %v", got, want)
		}
		return nil
	})
}

// waitForTargetsSAAbsent polls until the pyck_workflow_targets SA is
// removed from visibility — the post-condition of
// setWorkflowTargets([]), which calls ValueUnset() rather than writing
// an empty list. Without polling we'd race the visibility-index
// removal that follows the upsert event.
func waitForTargetsSAAbsent(ctx context.Context, c temporalclient.Client, workflowID, runID string, timeout time.Duration) error {
	return tests.PollUntil(ctx, timeout, 100*time.Millisecond, func() error {
		_, present, err := readTargetsSA(ctx, c, workflowID, runID)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("search attribute still present")
		}
		return nil
	})
}

// sameElements reports whether two string slices contain the same
// elements regardless of order. KeywordList ordering isn't part of
// the SA contract, so order-insensitive comparison is the right
// equality for assertions.
func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}
