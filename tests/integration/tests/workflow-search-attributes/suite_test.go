//go:build integration

// Package workflow exercises in-process Temporal workers hosting
// minimal "keep-open" workflows against a freshly-provisioned tenant.
// Each suite constructs its own worker through generic helpers
// (wfIdentity, wfWorker, publishWfTrigger, registerWithPyck, ...),
// then drives a workflow through the full client trigger path:
// NATS publish → pyck-workflow signalrouter → Temporal → worker.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/workflow-search-attributes/...
package workflow

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestWfIsAssignable runs the WfIsAssignableSuite which exercises the
// is_assignable search attribute through the full client trigger path.
func TestWfIsAssignable(t *testing.T) {
	suite.Run(t, new(WfIsAssignableSuite))
}

// TestWfTargets runs the WfTargetsSuite which exercises the
// pyck_workflow_targets KeywordList search attribute through the full
// client trigger path. Same shape as TestWfIsAssignable, exercising
// the WorkflowTargetsGetter/Setter interface pair.
func TestWfTargets(t *testing.T) {
	suite.Run(t, new(WfTargetsSuite))
}
