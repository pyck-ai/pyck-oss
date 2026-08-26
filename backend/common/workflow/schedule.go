package workflow

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// DefaultRetryPolicy returns the standard activity retry policy
// (1s initial, 2× backoff, 10s cap, 3 attempts). Callers needing a
// different cap modify the returned struct.
func DefaultRetryPolicy() *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    1 * time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    10 * time.Second,
		MaximumAttempts:    3,
	}
}

// DefaultActivityOptions returns ActivityOptions with the standard
// retry policy and the supplied StartToCloseTimeout.
func DefaultActivityOptions(startToClose time.Duration) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: startToClose,
		RetryPolicy:         DefaultRetryPolicy(),
	}
}

// EnsureScheduleOptions configures the schedule asserted by EnsureSchedule.
// Workflow is the workflow function (or its registered name); Args is the
// argument slice passed at each tick. Overlap defaults to
// SCHEDULE_OVERLAP_POLICY_SKIP — correct for sweepers, where a hung run
// must surface as a missed interval rather than as a backlog.
type EnsureScheduleOptions struct {
	ScheduleID string
	WorkflowID string
	TaskQueue  string
	Workflow   any
	Args       []any
	Every      time.Duration
	Overlap    enums.ScheduleOverlapPolicy
}

// EnsureSchedule creates the schedule when missing and otherwise
// updates its Spec, Action, and policy to match opts. CatchupWindow
// equals Every so one missed interval is replayed but longer outages
// are dropped. Idempotent; safe to call on every boot.
func EnsureSchedule(ctx context.Context, tc client.Client, opts EnsureScheduleOptions) error {
	sc := tc.ScheduleClient()

	spec := client.ScheduleSpec{
		Intervals: []client.ScheduleIntervalSpec{{Every: opts.Every}},
	}

	action := &client.ScheduleWorkflowAction{
		ID:        opts.WorkflowID,
		TaskQueue: opts.TaskQueue,
		Workflow:  opts.Workflow,
		Args:      opts.Args,
	}

	h := sc.GetHandle(ctx, opts.ScheduleID)
	if _, err := h.Describe(ctx); err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			_, createErr := sc.Create(ctx, client.ScheduleOptions{
				ID:            opts.ScheduleID,
				Spec:          spec,
				Action:        action,
				Overlap:       opts.Overlap,
				CatchupWindow: opts.Every,
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
			s.Policy.Overlap = opts.Overlap
			s.Policy.CatchupWindow = opts.Every
			return &client.ScheduleUpdate{Schedule: &s}, nil
		},
	})
}
