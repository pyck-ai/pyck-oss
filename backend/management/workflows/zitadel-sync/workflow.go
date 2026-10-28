package zitadel_sync

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"
)

const (
	TenantSyncTaskQueue    = "ZITADEL_SYNC_TQ"
	TenantWorkflowIDPrefix = "tenant-sync-"

	// oneWaySyncChangeID is the workflow.GetVersion change ID of executions
	// that list tenants from the DB alone. An execution without the marker
	// was started by a worker that still reconciled tenants from Zitadel and
	// is replayed through that worker's command sequence (listTenantOrgIDs).
	oneWaySyncChangeID = "one-way-sync"
)

// ZitadelSyncWorkflow lists the live, fully registered tenants and staggers
// one TenantSyncWorkflow start per tenant across the schedule window.
// Tenants themselves are DB-owned and never taken from Zitadel; only users
// are projected, by the per-tenant workflow.
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

	orgIDs, err := listTenantOrgIDs(ctx)
	if err != nil {
		logger.Error("list tenants failed", "err", err)
		return err
	}
	if len(orgIDs) == 0 {
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
	sort.Strings(orgIDs)

	n := len(orgIDs)
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
	ordered := make([]string, n)
	for i := 0; i < n; i++ {
		ordered[i] = orgIDs[(start+i)%n]
	}

	cursor := 0
	allFutures := make([]workflow.Future, 0, n)

	for cursor < n {
		now = workflow.Now(ctx).UTC()
		if !now.Before(deadline) {
			// Out of time: fire the rest immediately (no sleep) to stay within window.
			for cursor < n {
				inp := StartTenantSyncActivityInput{
					TenantID:         ordered[cursor],
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
				TenantID:         ordered[cursor],
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

// listTenantOrgIDs returns the Zitadel org IDs of the tenants to sync.
//
// The DefaultVersion branch exists for executions that were in flight when
// the Zitadel-to-DB tenant reconcile was retired: it re-issues their
// FetchZitadelTenantsActivity, FetchDbTenantsActivity,
// ReconcileTenantsActivity, FetchDbTenantsActivity sequence so replay
// matches their history. The results come from history (the retired
// activities are not registered on any worker) so a run whose retired
// activity was still pending at the rollout fails on it and the next
// schedule tick replaces it. A run whose second FetchDbTenantsActivity was
// still pending receives the new worker's []string result and fails
// decoding it into []struct{ID string}: same outcome, different error text
// in the Temporal UI. Nothing wedges. The branch can be deleted once
// `WorkflowType = 'ZitadelSyncWorkflow' AND ExecutionStatus = 'Running' AND TemporalChangeVersion IS NULL`
// returns nothing.
func listTenantOrgIDs(ctx workflow.Context) ([]string, error) {
	var orgIDs []string
	if workflow.GetVersion(ctx, oneWaySyncChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		err := workflow.ExecuteActivity(ctx, "FetchDbTenantsActivity", FetchDbTenantsInput{}).Get(ctx, &orgIDs)
		return orgIDs, err
	}

	for _, name := range []string{"FetchZitadelTenantsActivity", "FetchDbTenantsActivity", "ReconcileTenantsActivity"} {
		if err := workflow.ExecuteActivity(ctx, name).Get(ctx, nil); err != nil {
			return nil, err
		}
	}
	// The retired listing returned {ID, Name, Flavour} rows; only the org
	// ID is read.
	var tenants []struct{ ID string }
	if err := workflow.ExecuteActivity(ctx, "FetchDbTenantsActivity").Get(ctx, &tenants); err != nil {
		return nil, err
	}
	orgIDs = make([]string, len(tenants))
	for i, t := range tenants {
		orgIDs[i] = t.ID
	}
	return orgIDs, nil
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
		var appErr *temporal.ApplicationError
		if errors.As(err, &appErr) && appErr.Type() == ErrZitadelOrgNotFound {
			// Org was deleted out of band. Skip user sync rather than
			// soft-deleting every user on a transient race. The tenant row
			// is the liveness truth and stays active until an operator
			// disables it, so this branch repeats every cycle for that
			// tenant.
			logger.Warn("tenant org no longer exists; user sync skipped until an operator runs deleteTenant",
				"idp_org_ref", input.TenantID)
			return nil
		}
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

// EnsureOrchestratorSchedule installs the top-level orchestrator
// schedule. BUFFER_ONE so a single replay queues behind a running
// orchestrator — both would touch the same per-tenant children.
//
// The orchestrator is in flight for almost the whole window, so every
// rollout meets one running execution. With deployment versioning the run
// stays pinned to its old worker; without it the new worker replays the
// run, which is why ZitadelSyncWorkflow gates its listing step with
// workflow.GetVersion. An ungated change to its command sequence would
// leave that run failing its workflow task indefinitely (the worker's
// default BlockWorkflow panic policy), with the buffered tick queued
// behind it and user sync stopped, until an operator terminates the run.
func EnsureOrchestratorSchedule(ctx context.Context, temporalClient client.Client, taskQueue string, every time.Duration) error {
	return commonwf.EnsureSchedule(ctx, temporalClient, commonwf.EnsureScheduleOptions{
		ScheduleID: "sched-zitadel-orchestrator",
		WorkflowID: "zitadel-tenant-schedules",
		TaskQueue:  taskQueue,
		Workflow:   ZitadelSyncWorkflow,
		Args:       []any{ZitadelSyncWorkflowInput{Period: every}},
		Every:      every,
		Overlap:    enums.SCHEDULE_OVERLAP_POLICY_BUFFER_ONE,
	})
}
