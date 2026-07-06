package tenantexpirycheck

import (
	"context"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"
)

// Schedule constants. Exported so main.go can reference them.
const (
	ScheduleID   = "sched-tenant-expiry-check"
	WorkflowID   = "tenant-expiry-check"
	WorkflowName = "TenantExpiryCheckWorkflow"
)

// TenantExpiryCheckWorkflow is the orchestrator. It pulls the small
// candidate set in one activity and triggers soft-delete per tenant
// in another. Cost is O(expired), not O(tenants).
//
// Failures of a single soft-delete don't block the rest of the sweep;
// they're logged and the next tick retries (the tenant remains in the
// candidate set until deleted_at is set).
func TenantExpiryCheckWorkflow(ctx workflow.Context, _ TenantExpiryCheckWorkflowInput) (TenantExpiryCheckWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)

	findRP := commonwf.DefaultRetryPolicy()
	findRP.MaximumInterval = 30 * time.Second
	findAO := workflow.ActivityOptions{
		StartToCloseTimeout:    1 * time.Minute,
		ScheduleToCloseTimeout: 2 * time.Minute,
		RetryPolicy:            findRP,
	}
	findCtx := workflow.WithActivityOptions(ctx, findAO)

	var found []ExpiredTenantRef
	if err := workflow.ExecuteActivity(findCtx, activityRefs.FindExpiredTenantsActivity, FindExpiredTenantsActivityInput{}).
		Get(findCtx, &found); err != nil {
		logger.Error("find expired tenants failed", "err", err)
		return TenantExpiryCheckWorkflowOutput{}, err
	}

	out := TenantExpiryCheckWorkflowOutput{Found: len(found)}
	if len(found) == 0 {
		logger.Info("tenant expiry check: no expired tenants")
		return out, nil
	}

	deleteRP := commonwf.DefaultRetryPolicy()
	deleteRP.MaximumInterval = 15 * time.Second
	deleteAO := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Second,
		ScheduleToCloseTimeout: 1 * time.Minute,
		RetryPolicy:            deleteRP,
	}
	deleteCtx := workflow.WithActivityOptions(ctx, deleteAO)

	for _, ref := range found {
		in := SoftDeleteExpiredTenantActivityInput{TenantID: ref.TenantID}
		if err := workflow.ExecuteActivity(deleteCtx, activityRefs.SoftDeleteExpiredTenantActivity, in).
			Get(deleteCtx, nil); err != nil {
			logger.Warn("soft-delete expired tenant failed; will retry on next sweep",
				"tenant_id", ref.TenantID, "idp_org_ref", ref.IdpOrgRef, "err", err)
			out.Failed++
			continue
		}
		out.Disabled++
	}

	logger.Info("tenant expiry check complete",
		"found", out.Found,
		"disabled", out.Disabled,
		"failed", out.Failed,
	)
	return out, nil
}

// EnsureSchedule installs the periodic schedule that drives the
// expiry-check workflow. The default overlap policy (SKIP) is correct
// here: a hung sweep surfaces as a missed interval, not as a backlog
// of concurrent sweepers.
func EnsureSchedule(ctx context.Context, temporalClient client.Client, taskQueue string, every time.Duration) error {
	return commonwf.EnsureSchedule(ctx, temporalClient, commonwf.EnsureScheduleOptions{
		ScheduleID: ScheduleID,
		WorkflowID: WorkflowID,
		TaskQueue:  taskQueue,
		Workflow:   TenantExpiryCheckWorkflow,
		Args:       []any{TenantExpiryCheckWorkflowInput{}},
		Every:      every,
	})
}
