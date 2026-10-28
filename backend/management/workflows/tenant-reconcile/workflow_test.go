package tenantreconcile_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	tenantreconcile "github.com/pyck-ai/pyck/backend/management/workflows/tenant-reconcile"
)

func newTestEnv() *testsuite.TestWorkflowEnvironment {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Minute})
	return env
}

// Every sweep repairs the tenant event streams, with or without drift.
func TestTenantReconcileWorkflow_RepairsEventStreams(t *testing.T) {
	t.Parallel()

	drifts := map[string]tenantreconcile.DriftSet{
		"no drift": {},
		"drift":    {ToDisable: []tenantreconcile.TenantRef{{TenantID: uuid.New(), IdpOrgRef: "org"}}},
	}

	for name, drift := range drifts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv()

			var a *tenantreconcile.Activities
			env.OnActivity(a.ComputeDriftActivity, mock.Anything, mock.Anything).Return(drift, nil)
			env.OnActivity(a.DispatchLifecycleActivity, mock.Anything, mock.Anything).
				Return(tenantreconcile.DispatchLifecycleActivityOutput{Dispatched: true}, nil)
			env.OnActivity(a.RepairEventStreamsActivity, mock.Anything, mock.Anything).Return(nil)

			env.ExecuteWorkflow(tenantreconcile.TenantReconcileWorkflow, tenantreconcile.TenantReconcileWorkflowInput{})

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			env.AssertNumberOfCalls(t, "RepairEventStreamsActivity", 1)
		})
	}
}

// Stream repair needs no Zitadel, so a failed drift computation still runs it.
func TestTenantReconcileWorkflow_RepairsEventStreamsWhenDriftFails(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a *tenantreconcile.Activities
	env.OnActivity(a.ComputeDriftActivity, mock.Anything, mock.Anything).
		Return(tenantreconcile.DriftSet{}, temporal.NewNonRetryableApplicationError("zitadel down", "test", errors.New("zitadel down")))
	env.OnActivity(a.RepairEventStreamsActivity, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(tenantreconcile.TenantReconcileWorkflow, tenantreconcile.TenantReconcileWorkflowInput{})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "zitadel down")
	env.AssertNumberOfCalls(t, "RepairEventStreamsActivity", 1)
}

// A failed stream repair is left to the next sweep; it must not fail this
// one, whose lifecycle corrections are done.
func TestTenantReconcileWorkflow_StreamRepairFailureDoesNotFail(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a *tenantreconcile.Activities
	env.OnActivity(a.ComputeDriftActivity, mock.Anything, mock.Anything).Return(tenantreconcile.DriftSet{}, nil)
	env.OnActivity(a.RepairEventStreamsActivity, mock.Anything, mock.Anything).
		Return(temporal.NewNonRetryableApplicationError("nats down", "test", errors.New("nats down")))

	env.ExecuteWorkflow(tenantreconcile.TenantReconcileWorkflow, tenantreconcile.TenantReconcileWorkflowInput{})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var out tenantreconcile.TenantReconcileWorkflowOutput
	require.NoError(t, env.GetWorkflowResult(&out))
	assert.Zero(t, out.Disabled)
}
