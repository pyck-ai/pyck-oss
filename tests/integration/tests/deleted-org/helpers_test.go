//go:build integration

package deletedorg

import (
	"context"
	"fmt"
	"time"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"

	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// waitUntilOrgGone polls until the org no longer appears as ACTIVE/INACTIVE
// — either absent ("") or projected REMOVED. Both count as "deleted" for the
// drift logic (absent from the active/inactive sets).
func waitUntilOrgGone(ctx context.Context, conn *zitadel.Connection, orgID string, timeout time.Duration) (time.Duration, error) {
	return tests.PollUntilElapsed(ctx, timeout, 200*time.Millisecond, func() error {
		state, err := zitadelclient.OrgState(ctx, conn, orgID)
		if err != nil {
			return fmt.Errorf("org state: %w", err)
		}
		if state != "" && state != "REMOVED" {
			return fmt.Errorf("org still %q, wanted gone", state)
		}
		return nil
	})
}

// countRuns returns how many workflow executions match the given workflow ID
// and type, paging through all visibility results.
func countRuns(ctx context.Context, tc temporalclient.Client, workflowID, workflowType string) (int, error) {
	query := fmt.Sprintf(`WorkflowId = %q AND WorkflowType = %q`, workflowID, workflowType)
	var total int
	var next []byte
	for {
		resp, err := tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Query:         query,
			NextPageToken: next,
		})
		if err != nil {
			return 0, fmt.Errorf("list workflows: %w", err)
		}
		total += len(resp.GetExecutions())
		next = resp.GetNextPageToken()
		if len(next) == 0 {
			return total, nil
		}
	}
}

// waitForRunStatus polls until at least one execution of the given workflow
// ID + type that started at or after since reports the wanted status, or
// the timeout fires, and returns that execution's run ID. A zero since
// matches every run. It deliberately queries visibility (ListWorkflow)
// rather than DescribeWorkflowExecution: the run is dispatched
// asynchronously (NATS trigger, schedule tick), so it may not exist yet
// when the wait starts, and Describe on a missing execution errors instead
// of reporting "not yet". The start-time cut-off compares the test host's
// clock with Temporal's, which share a machine in local and CI runs.
func waitForRunStatus(ctx context.Context, tc temporalclient.Client, workflowID, workflowType string, want enums.WorkflowExecutionStatus, since time.Time, timeout time.Duration) (string, time.Duration, error) {
	query := fmt.Sprintf(`WorkflowId = %q AND WorkflowType = %q`, workflowID, workflowType)
	var runID string
	took, err := tests.PollUntilElapsed(ctx, timeout, 500*time.Millisecond, func() error {
		resp, err := tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{Query: query})
		if err != nil {
			return fmt.Errorf("list workflows: %w", err)
		}
		var lastSeen string
		for _, e := range resp.GetExecutions() {
			if e.GetStartTime().AsTime().Before(since) {
				continue
			}
			if e.GetStatus() == want {
				runID = e.GetExecution().GetRunId()
				return nil
			}
			lastSeen = e.GetStatus().String()
		}
		return fmt.Errorf("no %s run started after %s in status %s yet (lastSeen=%q)", workflowType, since.Format(time.RFC3339), want.String(), lastSeen)
	})
	return runID, took, err
}

// scheduledActivityTypes returns the activity type of every
// ActivityTaskScheduled event in the run's history, in order. It is how a
// test tells which branch a completed run took: the activities it scheduled
// are the only durable trace of its control flow.
func scheduledActivityTypes(ctx context.Context, tc temporalclient.Client, workflowID, runID string) ([]string, error) {
	iter := tc.GetWorkflowHistory(ctx, workflowID, runID, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var types []string
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("read history of %s/%s: %w", workflowID, runID, err)
		}
		if ev.GetEventType() != enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED {
			continue
		}
		types = append(types, ev.GetActivityTaskScheduledEventAttributes().GetActivityType().GetName())
	}
	return types, nil
}
