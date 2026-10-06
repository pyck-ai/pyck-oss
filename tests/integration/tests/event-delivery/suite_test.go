//go:build integration

// Package eventdelivery_test proves, against a live stack and with real
// workflowsdk workers, that a mutation's events reach workflows and are
// accounted for:
//
//   - a mutation returns transactionID and eventCount; transactionRouting
//     reports one entry per event, all DONE once routing is complete;
//     workflowExecutions(where: {transactionID}) lists the started execution;
//     the workflow sees the same event ID the routing entry carries;
//   - one event is fanned out to every live subscription: two workers of one
//     workflow, each subscribing a different signal name, both get their
//     signal, and the router reports both targets;
//   - clean stop: when one of two workers stops, its subscriptions are marked
//     stopped and no longer route while the other worker runs, whose
//     subscriptions keep routing.
//
// The workers are separate OS processes (testworker, serving the observer
// workflow) because the SDK reads its workflows from a package-level registry.
// The suite needs a stack built from this branch: transactionRouting, the
// event_id payload field, the pyck-event-id header and the durable router
// consumer are all part of it.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/event-delivery/...
//
// or: task test:integration -- -run TestEventDelivery
package eventdelivery_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestEventDelivery runs the suite.
//
//nolint:paralleltest // starts real worker processes against the shared stack; runs alone.
func TestEventDelivery(t *testing.T) {
	suite.Run(t, new(DeliverySuite))
}
