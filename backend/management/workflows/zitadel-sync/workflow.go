package zitadel_sync

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TenantSyncTaskQueue    = "ZITADEL_SYNC_TQ"
	TenantWorkflowIDPrefix = "tenant-sync-"
)

// ZitadelSyncWorkflow orchestrates tenant reconciliation and staggers per-tenant sync starts within the window.
func ZitadelSyncWorkflow(ctx workflow.Context, input ZitadelSyncWorkflowInput) error {
	logger := workflow.GetLogger(ctx)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout:    5 * time.Minute,
		ScheduleToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var zitadelTenants []Tenant
	if err := workflow.ExecuteActivity(ctx, "FetchZitadelTenantsActivity", FetchZitadelTenantsActivityInput{}).
		Get(ctx, &zitadelTenants); err != nil {
		logger.Error("fetch Zitadel tenants failed", "err", err)
		return err
	}

	var dbTenantsBefore []Tenant
	if err := workflow.ExecuteActivity(ctx, "FetchDbTenantsActivity", FetchDbTenantsInput{}).
		Get(ctx, &dbTenantsBefore); err != nil {
		logger.Error("fetch DB tenants (before) failed", "err", err)
		return err
	}

	if err := workflow.ExecuteActivity(ctx, "ReconcileTenantsActivity", ReconcileTenantsActivityInput{
		ZitadelTenants: zitadelTenants,
		DbTenants:      dbTenantsBefore,
	}).Get(ctx, nil); err != nil {
		logger.Error("reconcile tenants failed", "err", err)
		return err
	}

	var dbTenantsAfter []Tenant
	if err := workflow.ExecuteActivity(ctx, "FetchDbTenantsActivity", FetchDbTenantsInput{}).
		Get(ctx, &dbTenantsAfter); err != nil {
		logger.Error("fetch DB tenants (after) failed", "err", err)
		return err
	}
	if len(dbTenantsAfter) == 0 {
		logger.Info("no tenants to sync — orchestrator exit")
		return nil
	}

	startAO := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Second,
		ScheduleToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	}
	startCtx := workflow.WithActivityOptions(ctx, startAO)

	// --- Even spreading inside the window with 1s timers only ---
	// Temporal timers have ~1s resolution; avoid sub-second sleeps by emitting multiple tenants per second.
	// Canonical order for fairness and stability.
	sort.Slice(dbTenantsAfter, func(i, j int) bool { return dbTenantsAfter[i].ID < dbTenantsAfter[j].ID })

	n := len(dbTenantsAfter)
	now := workflow.Now(ctx).UTC()
	roundBase := now.Truncate(input.Period)

	guard := 5 * time.Second
	if input.Period/200 > guard {
		guard = input.Period / 200
	}
	deadline := roundBase.Add(input.Period - guard)

	// Rotate start index each window for fairness.
	periodSecs := int64(input.Period / time.Second)
	if periodSecs < 1 {
		periodSecs = 1
	}
	roundOrdinal := roundBase.Unix() / periodSecs
	start := int(roundOrdinal % int64(n))

	// Rotated list.
	ordered := make([]Tenant, n)
	for i := 0; i < n; i++ {
		ordered[i] = dbTenantsAfter[(start+i)%n]
	}

	cursor := 0
	allFutures := make([]workflow.Future, 0, n)

	for cursor < n {
		now = workflow.Now(ctx).UTC()
		if !now.Before(deadline) {
			// Out of time: fire the rest immediately (no sleep) to stay within window.
			for cursor < n {
				inp := StartTenantSyncActivityInput{
					TenantID:         ordered[cursor].ID,
					TaskQueue:        TenantSyncTaskQueue,
					WorkflowIDPrefix: TenantWorkflowIDPrefix,
				}
				f := workflow.ExecuteActivity(startCtx, "StartTenantSyncActivity", inp)
				allFutures = append(allFutures, f)
				cursor++
			}
			break
		}

		// Seconds left (ceil)
		leftDur := deadline.Sub(now)
		leftSecs := int64((leftDur + time.Second - time.Nanosecond) / time.Second)
		if leftSecs < 1 {
			leftSecs = 1
		}

		// Tenants left and how many to emit in this tick (ceil division).
		leftTenants := n - cursor
		emit := leftTenants / int(leftSecs)
		if leftTenants%int(leftSecs) != 0 {
			emit++
		}
		if emit < 1 {
			emit = 1
		}

		// Fire-and-don't-wait inside the tick
		for i := 0; i < emit && cursor < n; i++ {
			inp := StartTenantSyncActivityInput{
				TenantID:         ordered[cursor].ID,
				TaskQueue:        TenantSyncTaskQueue,
				WorkflowIDPrefix: TenantWorkflowIDPrefix,
			}
			f := workflow.ExecuteActivity(startCtx, "StartTenantSyncActivity", inp)
			allFutures = append(allFutures, f)
			cursor++
		}

		// Align sleep to next exact second boundary to avoid cumulative drift.
		nextTick := now.Truncate(time.Second).Add(1 * time.Second)
		if nextTick.After(deadline) {
			continue
		}

		if d := nextTick.Sub(workflow.Now(ctx).UTC()); d > 0 {
			if err := workflow.Sleep(ctx, d); err != nil {
				logger.Error("sleep interrupted", "err", err)
				return err
			}
		}
	}

	// Wait fast for all starts to complete
	for _, f := range allFutures {
		if err := f.Get(startCtx, nil); err != nil {
			logger.Error("start activity failed", "err", err)
		}
	}

	logger.Info("orchestrator completed (started off all tenant syncs within window)",
		"tenants", n, "period", input.Period.String())

	return nil
}

// TenantSyncWorkflow synchronizes users for a single tenant.
func TenantSyncWorkflow(ctx workflow.Context, input TenantSyncWorkflowInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("tenant user sync started", "tenant_id", input.TenantID)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout:    60 * time.Second,
		ScheduleToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    5,
		},
		TaskQueue: TenantSyncTaskQueue,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var zitadelUsers []User
	if err := workflow.ExecuteActivity(ctx, "FetchZitadelUsersActivity", FetchZitadelUsersActivityInput{TenantID: input.TenantID}).
		Get(ctx, &zitadelUsers); err != nil {
		logger.Error("fetch Zitadel users failed", "err", err)
		return err
	}

	var dbUsers []User
	if err := workflow.ExecuteActivity(ctx, "FetchDbUsersActivity", FetchDbUsersActivityInput{TenantID: input.TenantID}).
		Get(ctx, &dbUsers); err != nil {
		logger.Error("fetch DB users failed", "err", err)
		return err
	}

	if err := workflow.ExecuteActivity(ctx, "ReconcileUsersActivity", ReconcileUsersActivityInput{
		ZitadelUsers: zitadelUsers,
		DbUsers:      dbUsers,
		TenantID:     input.TenantID,
	}).Get(ctx, nil); err != nil {
		logger.Error("reconcile users failed", "err", err)
		return err
	}

	logger.Info("tenant user sync completed", "tenant_id", input.TenantID)
	return nil
}

// EnsureOrchestratorSchedule creates/updates the top-level orchestrator Schedule.
func EnsureOrchestratorSchedule(ctx context.Context, temporalClient client.Client, taskQueue string, every time.Duration) error {
	sc := temporalClient.ScheduleClient()

	spec := client.ScheduleSpec{
		Intervals: []client.ScheduleIntervalSpec{{Every: every}},
	}

	const schedID = "sched-zitadel-orchestrator"
	const wfID = "zitadel-tenant-schedules"

	action := &client.ScheduleWorkflowAction{
		ID:        wfID,
		TaskQueue: taskQueue,
		Workflow:  ZitadelSyncWorkflow,
		Args:      []any{ZitadelSyncWorkflowInput{Period: every}},
	}

	overlap := enums.SCHEDULE_OVERLAP_POLICY_BUFFER_ONE

	h := sc.GetHandle(ctx, schedID)
	if _, err := h.Describe(ctx); err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			_, err = sc.Create(ctx, client.ScheduleOptions{
				ID:            schedID,
				Spec:          spec,
				Action:        action,
				Overlap:       overlap,
				CatchupWindow: every,
			})
			return err
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
			s.Policy.Overlap = overlap
			s.Policy.CatchupWindow = every
			return &client.ScheduleUpdate{Schedule: &s}, nil
		},
	})
}
