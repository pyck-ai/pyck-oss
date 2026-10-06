package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/server/common/dynamicconfig"

	"github.com/pyck-ai/pyck/backend/common/workflow"
)

// Lives here, not in common, so the Temporal server dependency stays out of
// every service's module graph.
func TestMaxWorkflowIDLength_MatchesTemporalDefault(t *testing.T) {
	t.Parallel()

	want := dynamicconfig.MaxIDLengthLimit.Get(dynamicconfig.NewNoopCollection())()
	require.Equal(t, want, workflow.MaxWorkflowIDLength)
}
