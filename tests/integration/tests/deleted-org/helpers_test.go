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

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
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

// waitUntilTenantAbsent polls the tenants list until the given tenant is no
// longer returned (soft-deleted), or the timeout fires.
func waitUntilTenantAbsent(ctx context.Context, cfg *config.Config, tenantID string, timeout time.Duration) (time.Duration, error) {
	return tests.PollUntilElapsed(ctx, timeout, 500*time.Millisecond, func() error {
		present, err := tests.TenantInList(ctx, cfg, cfg.ServiceToken, tenantID)
		if err != nil {
			return fmt.Errorf("tenant list: %w", err)
		}
		if present {
			return fmt.Errorf("tenant %s still listed", tenantID)
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
// ID + type reports the wanted status, or the timeout fires. It deliberately
// queries visibility (ListWorkflow) rather than DescribeWorkflowExecution:
// the run is dispatched asynchronously (NATS trigger), so it may not exist
// yet when the wait starts, and Describe on a missing execution errors
// instead of reporting "not yet".
func waitForRunStatus(ctx context.Context, tc temporalclient.Client, workflowID, workflowType string, want enums.WorkflowExecutionStatus, timeout time.Duration) (time.Duration, error) {
	query := fmt.Sprintf(`WorkflowId = %q AND WorkflowType = %q`, workflowID, workflowType)
	return tests.PollUntilElapsed(ctx, timeout, 500*time.Millisecond, func() error {
		resp, err := tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{Query: query})
		if err != nil {
			return fmt.Errorf("list workflows: %w", err)
		}
		var lastSeen string
		for _, e := range resp.GetExecutions() {
			if e.GetStatus() == want {
				return nil
			}
			lastSeen = e.GetStatus().String()
		}
		return fmt.Errorf("no %s run in status %s yet (lastSeen=%q)", workflowType, want.String(), lastSeen)
	})
}
