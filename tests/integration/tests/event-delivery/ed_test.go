//go:build integration

package eventdelivery_test

import (
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/tests/integration/tests"
	"github.com/pyck-ai/pyck/tests/integration/tests/event-delivery/observer"
)

// TestMutationIsRoutedAndAccountedFor is case 1: the whole path of one create
// mutation, read back through every surface a client has.
//
//  1. The mutation returns transactionID and eventCount.
//  2. transactionRouting has eventCount entries, every one DONE, and exactly
//     one of them started the workflow.
//  3. workflowExecutions(where: {transactionID}) and Temporal itself both list
//     that execution, under the workflow ID and run ID the routing entry names.
//  4. The workflow saw the event ID the routing entry carries.
func (s *DeliverySuite) TestMutationIsRoutedAndAccountedFor() {
	r := s.Require()
	s.provision()

	kind := s.newKind("Case1")
	w := s.startWorker(kind, "W1", true)
	s.waitReady(w)
	s.waitWorkflow(kind.workflow)

	created := s.createItem("c1")
	r.Positive(created.eventCount, "creating an item publishes at least one event")

	entries := s.waitRouted(created)
	started := startedTargets(entries)
	r.Len(started, 1, "one start subscription, so exactly one workflow is started: %+v", entries)

	target, entry := started[0].target, started[0].entry
	r.Equal(kind.workflow, target.Workflow)
	r.NotEmpty(target.WorkflowID)
	r.NotEmpty(target.RunID)
	s.executions = append(s.executions, target.WorkflowID)

	// The gateway lookup by transaction ID finds the same execution ...
	s.waitListedByTransaction(created.transactionID, target.WorkflowID)
	// ... and so does Temporal, read directly.
	s.waitRunning(kind)

	// The workflow's own view of the event that started it.
	obs := s.waitObserved(target.WorkflowID, target.RunID, func(o observer.Observed) bool { return o.StartHasID })
	r.Equal(entry.EventID, obs.StartEventID, "the workflow sees the event ID the router recorded")
}

// TestOneEventFansOutToEverySubscription is case 2: two workers of one
// workflow, one subscribing SigA and the other SigB. One update event is
// delivered as both signals, reported as two targets, and each signal reaches
// the workflow with the event's ID.
//
// The brief's "two task queues" is not possible: a workflow name is unique per
// tenant and its task queue is immutable, so both workers serve the same queue
// and differ only in their subscriptions.
func (s *DeliverySuite) TestOneEventFansOutToEverySubscription() {
	r := s.Require()
	s.provision()

	kind := s.newKind("Case2")
	// One at a time: two first registrations of one workflow name would race
	// on creating its row.
	w1 := s.startWorker(kind, "W1", true, observer.SignalA)
	s.waitReady(w1)
	workflowID := s.waitWorkflow(kind.workflow)

	w2 := s.startWorker(kind, "W2", false, observer.SignalB)
	s.waitReady(w2)
	s.waitSignalRows(workflowID, func(rows []signalRow) error {
		if len(rows) != 3 {
			return errRows("want W1's start and SigA plus W2's SigB", rows)
		}
		return nil
	})

	// A running execution to signal.
	created := s.createItem("c2")
	startEntries := s.waitRouted(created)
	started := startedTargets(startEntries)
	r.Len(started, 1)
	exec := started[0].target
	s.executions = append(s.executions, exec.WorkflowID)

	// The router signals what visibility lists as running; an update routed
	// before the execution is listed would find no target and be settled as
	// dropped, never retried. Wait for the listing the router itself reads.
	s.waitRunning(kind)

	updated := s.updateItem(created.itemID, "c2-updated")
	// The fan-out below counts targets across the transaction's entries; that
	// only means "per event" if the update publishes exactly one.
	r.Equal(1, updated.eventCount, "an item update publishes one outbox event")
	entries := s.waitRouted(updated)

	signalled := signalledTargets(entries)
	r.Len(signalled, 2, "both subscriptions deliver: %+v", entries)
	got := map[string]routingTarget{}
	for _, st := range signalled {
		got[st.target.Signal] = st.target
	}
	r.Contains(got, observer.SignalA)
	r.Contains(got, observer.SignalB)
	for name, target := range got {
		r.Equal(exec.WorkflowID, target.WorkflowID, "%s goes to the running execution", name)
		r.Equal(exec.RunID, target.RunID, "%s goes to the running run", name)
	}

	updateEventID := signalled[0].entry.EventID

	// The workflow's own view: both signals, each carrying the update's event ID.
	obs := s.waitObserved(exec.WorkflowID, exec.RunID, func(o observer.Observed) bool { return len(o.Signals) >= 2 })
	r.Len(obs.Signals, 2, "each signal exactly once: %+v", obs.Signals)
	seen := map[string]observer.Signal{}
	for _, sig := range obs.Signals {
		seen[sig.Name] = sig
	}
	r.Contains(seen, observer.SignalA)
	r.Contains(seen, observer.SignalB)
	for name, sig := range seen {
		r.True(sig.HasID, "%s carries an event ID", name)
		r.Equal(updateEventID, sig.EventID, "%s carries the update event's ID", name)
	}

	// The start and the signals are different events with different IDs.
	r.NotEqual(started[0].entry.EventID, updateEventID)
	r.Equal(started[0].entry.EventID, obs.StartEventID)
}

// TestStoppedWorkerKeepsTheOthersSubscriptions is case 3, the clean-stop
// behaviour: with two workers of one workflow, stopping one marks its
// subscriptions stopped (they stay until their TTL), and the router prefers the
// running worker's: the stopped worker's signal is no longer delivered, the live
// worker's is. Worker IDs and stopped_at are not exposed by the API, so this is
// proven by routing, not by row counts.
//
// Only the graceful path is covered. A crashed worker's subscriptions are never
// marked and lapse through their TTL (PYCK_WORKFLOW_SUBSCRIPTION_TTL, 1h by
// default), which is too slow to observe here; backend/workflow's janitor and
// router tests cover it.
func (s *DeliverySuite) TestStoppedWorkerKeepsTheOthersSubscriptions() {
	r := s.Require()
	s.provision()

	kind := s.newKind("Case3")
	// Both register the start, so the survivor can still start the workflow.
	// One at a time: two first registrations of one workflow name would race
	// on creating its row.
	w1 := s.startWorker(kind, "W1", true, observer.SignalA)
	s.waitReady(w1)
	workflowID := s.waitWorkflow(kind.workflow)

	w2 := s.startWorker(kind, "W2", true, observer.SignalB)
	s.waitReady(w2)

	// Each worker owns a start and one signal subscription.
	s.waitSignalRows(workflowID, func(rows []signalRow) error {
		if len(rows) != 4 {
			return errRows("want 4 subscription rows (start + signal per worker)", rows)
		}
		return nil
	})

	r.NoError(s.stopWorker(w1), "worker W1 exits gracefully")

	// W1's rows are marked stopped, not deleted: all four stay until their TTL.
	// The router ignores the stopped ones while W2 runs (asserted below by what
	// routes).
	s.waitSignalRows(workflowID, func(rows []signalRow) error {
		if len(rows) != 4 {
			return errRows("want all 4 rows to stay after W1 unregistered", rows)
		}
		return nil
	})
	s.assertWorkflowKept(kind.workflow, workflowID)

	// The survivor still starts the workflow ...
	created := s.createItem("c3")
	started := startedTargets(s.waitRouted(created))
	r.Len(started, 1, "W2's start subscription still routes")
	exec := started[0].target
	s.executions = append(s.executions, exec.WorkflowID)
	s.waitRunning(kind)

	// ... and delivers its own signal, and only that one.
	updated := s.updateItem(created.itemID, "c3-updated")
	r.Equal(1, updated.eventCount, "an item update publishes one outbox event")
	entries := s.waitRouted(updated)
	signalled := signalledTargets(entries)
	r.Len(signalled, 1, "only W2's signal routes while W2 runs: %+v", entries)
	r.Equal(observer.SignalB, signalled[0].target.Signal)

	obs := s.waitObserved(exec.WorkflowID, exec.RunID, func(o observer.Observed) bool { return len(o.Signals) >= 1 })
	r.Len(obs.Signals, 1)
	r.Equal(observer.SignalB, obs.Signals[0].Name)
	r.Equal(signalled[0].entry.EventID, obs.Signals[0].EventID)

	// Nothing else arrives afterwards: W1's stopped subscription is not used late.
	r.NoError(tests.PollStable(s.Ctx, 3*time.Second, pollInterval, func() error {
		o, err := s.observed(exec.WorkflowID, exec.RunID)
		if err != nil {
			return err
		}
		if len(o.Signals) != 1 {
			return fmt.Errorf("want exactly 1 signal, got %+v", o.Signals)
		}
		return nil
	}))
}
