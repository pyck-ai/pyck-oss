package tenantexpirycheck

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
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

	findAO := workflow.ActivityOptions{
		StartToCloseTimeout:    1 * time.Minute,
		ScheduleToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
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

	deleteAO := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Second,
		ScheduleToCloseTimeout: 1 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    15 * time.Second,
			MaximumAttempts:    3,
		},
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

// EnsureSchedule creates or updates the Temporal schedule that drives
// the expiry-check workflow. Mirrors the pattern in
// tenant-reconcile/workflow.go:EnsureSchedule.
//
// OverlapPolicy is left at the Temporal default
// (SCHEDULE_OVERLAP_POLICY_SKIP): if a previous tick is still
// running when the next fires, the new tick is dropped instead of
// queued. Right semantics for a sweeper — a hung sweep should
// surface as a missed interval rather than as a backlog.
func EnsureSchedule(ctx context.Context, temporalClient client.Client, taskQueue string, every time.Duration) error {
	sc := temporalClient.ScheduleClient()

	spec := client.ScheduleSpec{
		Intervals: []client.ScheduleIntervalSpec{{Every: every}},
	}

	action := &client.ScheduleWorkflowAction{
		ID:        WorkflowID,
		TaskQueue: taskQueue,
		Workflow:  TenantExpiryCheckWorkflow,
		Args:      []any{TenantExpiryCheckWorkflowInput{}},
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
