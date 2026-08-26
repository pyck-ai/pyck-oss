// Package tenantreconcile implements a periodic sweeper that compares the
// management DB's tenant.deleted_at flag with each tenant's Zitadel org
// state and dispatches corrective disable/restore workflows when they
// disagree.
//
// The sweeper exists to close the consistency gap opened when the
// tenant-lifecycle NATS subscriber exhausts its redelivery budget (see
// maxRedeliver in events/tenants/config.go). If a disable/restore message
// is dropped because the prior workflow runs longer than the total
// backoff window, the DB's intent and Zitadel's side-effects diverge.
// This workflow runs on a schedule and heals any such drift.
package tenantreconcile

import (
	"context"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"
)

// Schedule constants. Exported so main.go can reference them if needed.
const (
	ScheduleID = "sched-tenant-reconcile"
	WorkflowID = "tenant-reconcile"
)

// TenantReconcileWorkflow is the orchestrator. It computes the drift
// set in a single activity (two narrow lookups: DB-disabled tenants +
// Zitadel-inactive orgs) and dispatches a corrective workflow per
// mismatched tenant. Cost is O(drift), not O(total tenants).
func TenantReconcileWorkflow(ctx workflow.Context, _ TenantReconcileWorkflowInput) (TenantReconcileWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)

	driftRP := commonwf.DefaultRetryPolicy()
	driftRP.MaximumInterval = 30 * time.Second
	driftAO := workflow.ActivityOptions{
		StartToCloseTimeout:    1 * time.Minute,
		ScheduleToCloseTimeout: 2 * time.Minute,
		RetryPolicy:            driftRP,
	}
	driftCtx := workflow.WithActivityOptions(ctx, driftAO)

	var drift DriftSet
	if err := workflow.ExecuteActivity(driftCtx, activityRefs.ComputeDriftActivity, ComputeDriftActivityInput{}).
		Get(driftCtx, &drift); err != nil {
		logger.Error("compute drift failed", "err", err)
		return TenantReconcileWorkflowOutput{}, err
	}

	out := TenantReconcileWorkflowOutput{
		Disabled: len(drift.ToDisable),
		Restored: len(drift.ToRestore),
	}
	if out.Disabled == 0 && out.Restored == 0 {
		logger.Info("tenant reconcile: no drift")
		return out, nil
	}

	dispatchRP := commonwf.DefaultRetryPolicy()
	dispatchRP.MaximumInterval = 15 * time.Second
	dispatchAO := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Second,
		ScheduleToCloseTimeout: 1 * time.Minute,
		RetryPolicy:            dispatchRP,
	}
	dispatchCtx := workflow.WithActivityOptions(ctx, dispatchAO)

	dispatchOne := func(ref TenantRef, op string) {
		in := DispatchLifecycleActivityInput{
			TenantID:  ref.TenantID,
			IdpOrgRef: ref.IdpOrgRef,
			Op:        op,
		}
		var result DispatchLifecycleActivityOutput
		if err := workflow.ExecuteActivity(dispatchCtx, activityRefs.DispatchLifecycleActivity, in).
			Get(dispatchCtx, &result); err != nil {
			// One failed dispatch shouldn't block the rest. The next
			// sweep will retry.
			logger.Warn("dispatch lifecycle failed; continuing",
				"tenant_id", ref.TenantID, "op", op, "err", err)
			out.Skipped++
			return
		}
		if result.Dispatched {
			out.Dispatched++
		}
	}

	for _, ref := range drift.ToDisable {
		dispatchOne(ref, OpDisable)
	}
	for _, ref := range drift.ToRestore {
		dispatchOne(ref, OpRestore)
	}

	logger.Info("tenant reconcile complete",
		"to_disable", out.Disabled,
		"to_restore", out.Restored,
		"dispatched", out.Dispatched,
		"skipped", out.Skipped,
	)
	return out, nil
}

// EnsureSchedule installs the periodic schedule that drives the
// reconcile workflow. `every` must exceed the NATS redelivery window
// (~112s) so dispatched workflows finish before the next sweep.
func EnsureSchedule(ctx context.Context, temporalClient client.Client, taskQueue string, every time.Duration) error {
	return commonwf.EnsureSchedule(ctx, temporalClient, commonwf.EnsureScheduleOptions{
		ScheduleID: ScheduleID,
		WorkflowID: WorkflowID,
		TaskQueue:  taskQueue,
		Workflow:   TenantReconcileWorkflow,
		Args:       []any{TenantReconcileWorkflowInput{}},
		Every:      every,
	})
}
