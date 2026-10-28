package registertenant_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"

	"github.com/pyck-ai/pyck/backend/management/workflows"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// completedRunHistory is the history of a successful registration of a
// plain (non-pyck-go) tenant, captured from a live run with
// `temporal workflow show --output json`. Its eleven activities are the
// command sequence every registration of such a tenant produces.
const completedRunHistory = "testdata/register_tenant_completed_history.json"

// A registration run outlives a deploy: the worker that picks one up
// replays its history against the code it is running, and a command
// sequence that no longer matches wedges the execution: management sets
// no WorkflowPanicPolicy, so the SDK default BlockWorkflow neither fails
// the run nor lets it progress, and its row stays pending until an
// operator terminates it.
//
// What this catches is a step added, removed or reordered, an activity
// renamed, or a timer or side effect introduced. It does not catch a
// changed timeout or retry policy: the replayer matches a scheduled
// activity on its ID and type, so those replay against any history: the
// reason the budgets in workflow.go could be raised without a
// workflow.GetVersion gate.
func TestRegisterTenantWorkflow_ReplaysACompletedRun(t *testing.T) {
	t.Parallel()

	replayer := worker.NewWorkflowReplayer()
	// The type name the trigger starts, and the name the history carries;
	// replaying under the Go function name would not find the workflow.
	replayer.RegisterWorkflowWithOptions(registertenant.RegisterTenantWorkflow,
		workflow.RegisterOptions{Name: workflows.RegisterTenantWorkflow})

	err := replayer.ReplayWorkflowHistoryFromJSONFile(logadapter.TemporalSDKLogAdapter(*log.ForContext(t.Context())), completedRunHistory)
	require.NoError(t, err, "the workflow must replay a run started by the previous build")
}
