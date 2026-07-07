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
	"errors"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
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

	driftAO := workflow.ActivityOptions{
		StartToCloseTimeout:    1 * time.Minute,
		ScheduleToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
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

	dispatchAO := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Second,
		ScheduleToCloseTimeout: 1 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    15 * time.Second,
			MaximumAttempts:    3,
		},
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

// EnsureSchedule creates or updates the Temporal schedule that drives the
// reconcile workflow. Mirrors the pattern in
// workflows/zitadel-sync/workflow.go:EnsureOrchestratorSchedule.
//
// The caller must pass an `every` duration that exceeds the NATS
// redelivery window in events/tenants/config.go (sum of
// redeliverBackoff ≈ 112s). 5 minutes is the sensible default.
func EnsureSchedule(ctx context.Context, temporalClient client.Client, taskQueue string, every time.Duration) error {
	sc := temporalClient.ScheduleClient()

	spec := client.ScheduleSpec{
		Intervals: []client.ScheduleIntervalSpec{{Every: every}},
	}

	action := &client.ScheduleWorkflowAction{
		ID:        WorkflowID,
		TaskQueue: taskQueue,
		Workflow:  TenantReconcileWorkflow,
		Args:      []any{TenantReconcileWorkflowInput{}},
	}

	h := sc.GetHandle(ctx, ScheduleID)
	if _, err := h.Describe(ctx); err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			_, createErr := sc.Create(ctx, client.ScheduleOptions{
				ID:            ScheduleID,
				Spec:          spec,
				Action:        action,
				CatchupWindow: every,
			})
			return createErr
		}
		return err
	}

	return h.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(inU client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			s := inU.Description.Schedule
			s.Spec = &spec
			s.Action = action
			if s.Policy == nil {
				s.Policy = &client.SchedulePolicies{}
			}
			s.Policy.CatchupWindow = every
			return &client.ScheduleUpdate{Schedule: &s}, nil
		},
	})
}
