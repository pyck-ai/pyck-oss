//go:build integration

// Package workflowfilterrule_test checks, against a live stack, how the
// workflow service handles the FEEL filter rule a tenant writer attaches to a
// workflow signal with registerWorkflow:
//
//   - registerWorkflow refuses a rule that does not parse, nests too deeply or
//     is too long, and stores nothing;
//   - a rule already stored beyond those limits (rows written before the check
//     existed), or one that makes the FEEL evaluator panic, fails its
//     evaluation instead of crashing the shared workflow service, which one
//     process runs for every tenant.
//
// The crash tests watch the workflow container and one plants its rule with
// SQL, so the suite needs docker access to the local stack.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/workflow-filter-rule/...
package workflowfilterrule_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestWorkflowFilterRule runs the suite.
//
//nolint:paralleltest // watches the shared workflow container; must run alone.
func TestWorkflowFilterRule(t *testing.T) {
	suite.Run(t, new(FilterRuleSuite))
}
