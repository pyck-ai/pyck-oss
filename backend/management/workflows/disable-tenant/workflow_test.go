package disabletenant_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.uber.org/goleak"

	"github.com/pyck-ai/pyck/backend/management/core"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	disabletenant "github.com/pyck-ai/pyck/backend/management/workflows/disable-tenant"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

var testTenantID = uuid.MustParse("f35fe3be-efe9-53e8-88a4-8c550c63205d")

// The workflows read the worker-api activity budget from config; give the
// test process the production defaults. goleak (no ignores) fails the
// binary when a goroutine a test started outlives the tests.
func TestMain(m *testing.M) {
	core.Config.WorkerAPIRetry = workerapi.RetryConfig{
		StartToClose: 30 * time.Second, ScheduleToClose: 5 * time.Minute,
		InitialInterval: 2 * time.Second, MaximumInterval: 30 * time.Second,
	}

	goleak.VerifyTestMain(m)
}

func newTestEnv() *testsuite.TestWorkflowEnvironment {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Minute})
	return env
}

func TestDisableTenantWorkflow(t *testing.T) {
	t.Parallel()

	input := disabletenant.DisableTenantWorkflowInput{TenantID: testTenantID, IdpOrgRef: "org-1"}

	t.Run("deactivates the org, then stops the worker", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a disabletenant.Activities
		var order []string

		env.OnActivity(a.DeactivateZitadelOrgActivity, mock.Anything, disabletenant.DeactivateZitadelOrgActivityInput(input)).
			Run(func(mock.Arguments) { order = append(order, "zitadel") }).Return(nil)
		env.OnActivity(a.StopTenantWorkerActivity, mock.Anything, disabletenant.StopTenantWorkerActivityInput{TenantID: testTenantID}).
			Run(func(mock.Arguments) { order = append(order, "worker") }).
			Return(&disabletenant.StopTenantWorkerActivityOutput{}, nil)

		env.ExecuteWorkflow(disabletenant.DisableTenantWorkflow, input)
		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		var out disabletenant.DisableTenantWorkflowOutput
		require.NoError(t, env.GetWorkflowResult(&out))
		assert.True(t, out.Success)
		assert.Equal(t, testTenantID, out.TenantID)
		// Blocking access is the security half and must never wait on the
		// worker cluster.
		assert.Equal(t, []string{"zitadel", "worker"}, order)
		env.AssertExpectations(t)
	})

	t.Run("a tenant without a platform worker disables fine", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a disabletenant.Activities

		env.OnActivity(a.DeactivateZitadelOrgActivity, mock.Anything, mock.Anything).Return(nil)
		env.OnActivity(a.StopTenantWorkerActivity, mock.Anything, mock.Anything).
			Return(&disabletenant.StopTenantWorkerActivityOutput{Skipped: registertenant.SkipNotPyckGo}, nil)

		env.ExecuteWorkflow(disabletenant.DisableTenantWorkflow, input)
		require.NoError(t, env.GetWorkflowError())
	})

	t.Run("a failed org deactivation never reaches the worker", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a disabletenant.Activities

		env.OnActivity(a.DeactivateZitadelOrgActivity, mock.Anything, mock.Anything).
			Return(errors.New("zitadel down"))

		env.ExecuteWorkflow(disabletenant.DisableTenantWorkflow, input)
		require.Error(t, env.GetWorkflowError())
		env.AssertNotCalled(t, "StopTenantWorkerActivity", mock.Anything, mock.Anything)
	})

	// The workflow must not report success while the worker is in the wrong
	// state.
	t.Run("a stop that did not take fails the workflow", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a disabletenant.Activities

		env.OnActivity(a.DeactivateZitadelOrgActivity, mock.Anything, mock.Anything).Return(nil)
		env.OnActivity(a.StopTenantWorkerActivity, mock.Anything, mock.Anything).
			Return(nil, errors.New("worker-api reports 1 replicas after stop"))

		env.ExecuteWorkflow(disabletenant.DisableTenantWorkflow, input)
		require.True(t, env.IsWorkflowCompleted())
		require.ErrorContains(t, env.GetWorkflowError(), "1 replicas after stop")
	})
}
