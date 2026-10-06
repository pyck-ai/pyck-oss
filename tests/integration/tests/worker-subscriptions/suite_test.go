//go:build integration

// Package workersubscriptions_test proves, against a live stack and with real
// workflowsdk workers, that workers of different types do not delete each
// other's workflows (#1564).
//
// Before the fix, every worker start listed the tenant's workflows and deleted
// each one missing from its own registry, so starting worker B removed the
// workflow worker A had just registered. The suite runs two worker types as
// separate OS processes (testworker, one workflow per process, because the SDK
// reads its workflows from a package-level registry) and checks that:
//
//   - starting B, restarting A and restarting B never change either workflow's
//     row ID;
//   - one matching event starts an execution of both workflows;
//   - a second replica of A that stops cleanly leaves its subscriptions in
//     place but they stop routing while another replica of A runs; once no
//     running replica is left, the stopped subscriptions route until their TTL.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/worker-subscriptions/...
//
// or: task test:integration -- -run TestWorkerSubscriptions
package workersubscriptions_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestWorkerSubscriptions runs the suite.
//
//nolint:paralleltest // starts real worker processes against the shared stack; runs alone.
func TestWorkerSubscriptions(t *testing.T) {
	suite.Run(t, new(WorkerSubscriptionsSuite))
}
