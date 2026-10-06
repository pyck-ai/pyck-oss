// Package observer is the workflow the event-delivery suite's test workers
// run. It records what the signal router delivered to it, so the suite can
// compare it with what the router reported: the event ID of the start, and the
// name and event ID of every signal.
//
// It lives in its own package so the worker (package main) and the suite share
// the Observed type instead of mirroring it.
package observer

import (
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

const (
	// QueryObserved returns the Observed state of the execution.
	QueryObserved = "observed"

	// SignalA and SignalB are the intermediate signals the test workers
	// subscribe to. The workflow listens on both whichever worker process hosts
	// the execution, so the subscriptions alone decide which one is delivered.
	SignalA = "SigA"
	SignalB = "SigB"

	// SignalDone ends the execution.
	SignalDone = "done"

	// lifetime bounds an execution the suite leaves running.
	lifetime = 10 * time.Minute
)

// Signal is one signal the workflow received through workflowsdk.ReceiveEvent.
type Signal struct {
	Name string
	// EventID is the router's pyck-event-id of the delivery, empty if none.
	EventID string
	HasID   bool
}

// Observed is what the workflow saw.
type Observed struct {
	// StartEventID is the event ID of the event that started the execution.
	StartEventID string
	StartHasID   bool
	Signals      []Signal
}

// Workflow records its start event ID and every SignalA and SignalB it
// receives, until SignalDone or its lifetime. The input is the start event's
// payload, which the router forwards and the workflow ignores.
func Workflow(ctx workflow.Context, _ any) (Observed, error) {
	var obs Observed

	if id, ok := workflowsdk.EventID(ctx); ok {
		obs.StartHasID, obs.StartEventID = true, id.String()
	}

	if err := workflow.SetQueryHandler(ctx, QueryObserved, func() (Observed, error) { return obs, nil }); err != nil {
		return obs, err
	}

	done := workflow.GetSignalChannel(ctx, SignalDone)
	timer := workflow.NewTimer(ctx, lifetime)

	for finished := false; !finished; {
		sel := workflow.NewSelector(ctx)

		for _, name := range []string{SignalA, SignalB} {
			sel.AddReceive(workflow.GetSignalChannel(ctx, name), func(c workflow.ReceiveChannel, _ bool) {
				ev, err := workflowsdk.ReceiveEvent[any](ctx, c)
				if err != nil {
					return
				}

				obs.Signals = append(obs.Signals, Signal{Name: name, EventID: eventID(ev), HasID: ev.HasID()})
			})
		}

		sel.AddReceive(done, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil); finished = true })
		sel.AddFuture(timer, func(workflow.Future) { finished = true })
		sel.Select(ctx)
	}

	return obs, nil
}

func eventID(ev workflowsdk.ReceivedEvent[any]) string {
	if !ev.HasID() {
		return ""
	}

	return ev.ID.String()
}
