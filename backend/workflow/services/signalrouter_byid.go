package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"

	"github.com/pyck-ai/pyck/backend/common/eventid"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
)

type deliveryKey struct{}

// withDelivery returns ctx carrying the JetStream delivery count of the event
// being handled: 1 for the first delivery, more for a redelivery.
func withDelivery(ctx context.Context, delivered uint64) context.Context {
	return context.WithValue(ctx, deliveryKey{}, delivered)
}

// isRedelivery reports whether the event being handled was delivered before. A
// context without a count (a direct call) is a first delivery.
func isRedelivery(ctx context.Context) bool {
	delivered, _ := ctx.Value(deliveryKey{}).(uint64)

	return delivered > 1
}

// deliverByID sends the Signal-With-Start and signal-by-ID subscriptions of
// match to the workflow ID of the event, startOpts.ID. startOpts carries the
// start half of a Signal-With-Start: the task queue, the search attributes and
// the reuse policy of the event's operation. It returns the failures, which the
// caller joins.
func (wr *SignalRouter) deliverByID(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, match routedSubscriptions, startOpts *workflow.StartWorkflowOptions) []error {
	var errs []error

	for _, signal := range match.signalWithStart {
		if err := wr.signalWithStart(ctx, client, payload, wf, signal, startOpts); err != nil {
			errs = append(errs, err)
		}
	}

	for _, signal := range match.signalByID {
		if err := wr.signalByID(ctx, client, payload, wf, signal, startOpts.ID); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// signalWithStart signals workflow startOpts.ID, starting it with payload as
// its input if it is not running. The event is always delivered as a signal,
// the one that starts the workflow included, so a new workflow sees its first
// event twice, as input and as signal.
//
// The conflict policy is always USE_EXISTING: Temporal rejects FAIL for
// Signal-With-Start, and a running execution is the case the call exists for.
// The reuse policy of the operation applies to a finished execution as far as
// Temporal applies it (see the README): a refusal is a policy drop, not a
// failure.
//
// The Temporal SDK does not say whether the call started the workflow, so the
// target is recorded as signalled, with the run it reached.
//
// The request ID makes a redelivery a no-op only while the run is still
// running. For a closed run Temporal applies the reuse policy without checking
// request IDs: a repeated call under update (allow duplicate) would start a
// second run, and under create and delete it would be recorded as dropped. So
// the run records the event that started it in its memo (workflow.MemoEventID),
// and a redelivery first describes the latest run (deliveredToClosedRun): if it
// is closed and this event started it, nothing is sent and the target is
// recorded as signalled. One limit remains: if the first delivery signalled a
// run started by a different event, and that run closed before the redelivery,
// the redelivery still follows the reuse policy, because Temporal does not
// expose which signal request IDs a closed run received.
func (wr *SignalRouter) signalWithStart(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, signal *ent.WorkflowSignal, startOpts *workflow.StartWorkflowOptions) error {
	opts := *startOpts
	opts.WorkflowIDConflictPolicy = enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING
	opts.WorkflowExecutionErrorWhenAlreadyStarted = false

	// The event is recorded in the memo of the run the call may start, so a
	// redelivery can tell whether this event started the run it finds closed.
	if eventID, ok := eventid.FromContext(ctx); ok {
		opts.Memo = map[string]any{workflow.MemoEventID: eventID.String()}

		if wr.deliveredToClosedRun(ctx, client, wf, signal, opts.ID, eventID) {
			return nil
		}
	}

	callCtx := withRequestID(ctx, wf.Name, callKindSignalWithStart, signal.TemporalSignal, opts.ID)

	run, err := client.SignalWithStartWorkflow(callCtx, wf.Name, opts.ID, signal.TemporalSignal, payload, payload, &opts)

	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &alreadyStarted) {
		log.ForContext(ctx).Info().
			Str("workflow_name", wf.Name).
			Str("workflow_id", opts.ID).
			Str("signal", signal.TemporalSignal).
			Msg("finished execution refused by its reuse policy, dropping signal-with-start")

		routingFrom(ctx).add(RoutingTarget{
			Kind: RoutingTargetDropped, Workflow: wf.Name, WorkflowID: opts.ID, Signal: signal.TemporalSignal,
			Reason: "the execution finished and its reuse policy does not allow a new run",
		})

		return nil
	}

	if err != nil {
		routingFrom(ctx).add(RoutingTarget{Kind: RoutingTargetFailed, Workflow: wf.Name, WorkflowID: opts.ID, Signal: signal.TemporalSignal, Error: err.Error()})

		return byIDError(fmt.Errorf("failed to signal-with-start workflow %q (ID: %q): %w", wf.Name, opts.ID, err))
	}

	routingFrom(ctx).add(RoutingTarget{
		Kind: RoutingTargetSignalled, Workflow: wf.Name, WorkflowID: run.GetID(), RunID: run.GetRunID(), Signal: signal.TemporalSignal,
	})

	return nil
}

// deliveredToClosedRun reports whether a redelivered event already started the
// latest run of workflowID, which has closed since. Temporal would apply the
// reuse policy to a repeated Signal-With-Start for a closed run without looking
// at its request ID (a second run under update, a drop under create and
// delete), so the router sends nothing and records the target as signalled.
//
// It asks only for a redelivery, so a first delivery costs no call. It answers
// false in every case it cannot be sure of: no such workflow, a running run, a
// run started by another event or without the memo. A failed describe is not
// an error either: it is logged and the call goes out as it would have.
func (wr *SignalRouter) deliveredToClosedRun(ctx context.Context, client *workflow.Client, wf *ent.Workflow, signal *ent.WorkflowSignal, workflowID string, eventID uuid.UUID) bool {
	if !isRedelivery(ctx) {
		return false
	}

	info, err := client.DescribeLatestRun(ctx, workflowID)
	if err != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			log.ForContext(ctx).Debug().Err(err).
				Str("workflow_name", wf.Name).
				Str("workflow_id", workflowID).
				Msg("failed to describe the latest run before a redelivered signal-with-start, sending it")
		}

		return false
	}

	if info.GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_RUNNING || info.GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED ||
		!workflow.StartedByEvent(info, eventID.String()) {
		return false
	}

	log.ForContext(ctx).Info().
		Str("workflow_name", wf.Name).
		Str("workflow_id", workflowID).
		Str("run_id", info.GetExecution().GetRunId()).
		Str("signal", signal.TemporalSignal).
		Msg("redelivered event already started the finished run, skipping signal-with-start")

	routingFrom(ctx).add(RoutingTarget{
		Kind: RoutingTargetSignalled, Workflow: wf.Name, WorkflowID: workflowID, RunID: info.GetExecution().GetRunId(), Signal: signal.TemporalSignal,
	})

	return true
}

// byIDError marks a subscription the workflow client refuses to send as
// permanent: one without a signal name (a raw API registration) fails the same
// way on every redelivery.
func byIDError(err error) error {
	if errors.Is(err, workflow.ErrInvalidSignalName) {
		return permanent(err)
	}

	return err
}

// signalByID signals the current run of workflowID and never starts anything.
// With no running execution the event is a policy drop: recorded, and
// acknowledged like any other finished outcome.
func (wr *SignalRouter) signalByID(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, signal *ent.WorkflowSignal, workflowID string) error {
	callCtx := withRequestID(ctx, wf.Name, callKindSignalByID, signal.TemporalSignal, workflowID)

	err := client.SignalWorkflowByID(callCtx, workflowID, signal.TemporalSignal, payload)

	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		log.ForContext(ctx).Info().
			Str("workflow_name", wf.Name).
			Str("workflow_id", workflowID).
			Str("signal", signal.TemporalSignal).
			Msg("no running execution to signal by ID, dropping")

		routingFrom(ctx).add(RoutingTarget{
			Kind: RoutingTargetDropped, Workflow: wf.Name, WorkflowID: workflowID, Signal: signal.TemporalSignal,
			Reason: "no running execution to signal",
		})

		return nil
	}

	if err != nil {
		routingFrom(ctx).add(RoutingTarget{Kind: RoutingTargetFailed, Workflow: wf.Name, WorkflowID: workflowID, Signal: signal.TemporalSignal, Error: err.Error()})

		return byIDError(fmt.Errorf("failed to signal workflow %q (ID: %q) by ID: %w", wf.Name, workflowID, err))
	}

	routingFrom(ctx).add(RoutingTarget{Kind: RoutingTargetSignalled, Workflow: wf.Name, WorkflowID: workflowID, Signal: signal.TemporalSignal})

	return nil
}
