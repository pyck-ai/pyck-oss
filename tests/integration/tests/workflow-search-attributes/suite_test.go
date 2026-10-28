//go:build integration

// Package workflow_test exercises in-process Temporal workers hosting
// minimal "keep-open" workflows against a freshly-provisioned tenant.
// Each suite constructs its own worker through generic helpers
// (wfIdentity, wfWorker, publishWfTrigger, registerWithPyck, ...),
// then drives a workflow through the full client trigger path:
// NATS publish → pyck-workflow signalrouter → Temporal → worker.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/workflow-search-attributes/...
package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestWfAssignment runs the WfAssignmentSuite, which covers the whole
// workflow assignment API (is_assignable, assignee, their list filters and
// their refusals) through the full client trigger path.
func TestWfAssignment(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(WfAssignmentSuite))
}

// TestWfTargets runs the WfTargetsSuite which exercises the
// pyck_workflow_targets KeywordList search attribute through the full
// client trigger path. Same shape as TestWfAssignment, exercising
// the WorkflowTargetsGetter/Setter interface pair.
func TestWfTargets(t *testing.T) {
	suite.Run(t, new(WfTargetsSuite))
}
