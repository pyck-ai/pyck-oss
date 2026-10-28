package restoretenant_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"github.com/pyck-ai/pyck/backend/management/core"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
	restoretenant "github.com/pyck-ai/pyck/backend/management/workflows/restore-tenant"
)

var testTenantID = uuid.MustParse("f35fe3be-efe9-53e8-88a4-8c550c63205d")

// The workflows read the worker-api activity budget from config; give the
// test process the production defaults.
func TestMain(m *testing.M) {
	core.Config.WorkerAPIRetry = workerapi.RetryConfig{
		StartToClose: 30 * time.Second, ScheduleToClose: 5 * time.Minute,
		InitialInterval: 2 * time.Second, MaximumInterval: 30 * time.Second,
	}

	os.Exit(m.Run())
}

func newTestEnv() *testsuite.TestWorkflowEnvironment {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Minute})
	return env
}

func TestRestoreTenantWorkflow(t *testing.T) {
	t.Parallel()

	input := restoretenant.RestoreTenantWorkflowInput{TenantID: testTenantID, IdpOrgRef: "org-1"}

	t.Run("activates the org, then resumes the worker", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a restoretenant.Activities
		var order []string

		env.OnActivity(a.ActivateZitadelOrgActivity, mock.Anything, restoretenant.ActivateZitadelOrgActivityInput(input)).
			Run(func(mock.Arguments) { order = append(order, "zitadel") }).Return(nil)
		env.OnActivity(a.ResumeTenantWorkerActivity, mock.Anything, restoretenant.ResumeTenantWorkerActivityInput{TenantID: testTenantID}).
			Run(func(mock.Arguments) { order = append(order, "worker") }).
			Return(&restoretenant.ResumeTenantWorkerActivityOutput{Replicas: 1}, nil)

		env.ExecuteWorkflow(restoretenant.RestoreTenantWorkflow, input)
		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		var out restoretenant.RestoreTenantWorkflowOutput
		require.NoError(t, env.GetWorkflowResult(&out))
		assert.True(t, out.Success)
		// The worker authenticates against the tenant's org: it must be
		// active before the worker polls again.
		assert.Equal(t, []string{"zitadel", "worker"}, order)
		env.AssertExpectations(t)
	})

	t.Run("a tenant without a platform worker restores fine", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a restoretenant.Activities

		env.OnActivity(a.ActivateZitadelOrgActivity, mock.Anything, mock.Anything).Return(nil)
		env.OnActivity(a.ResumeTenantWorkerActivity, mock.Anything, mock.Anything).
			Return(&restoretenant.ResumeTenantWorkerActivityOutput{Skipped: registertenant.SkipNotPyckGo}, nil)

		env.ExecuteWorkflow(restoretenant.RestoreTenantWorkflow, input)
		require.NoError(t, env.GetWorkflowError())
	})

	t.Run("a failed org activation never reaches the worker", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a restoretenant.Activities

		env.OnActivity(a.ActivateZitadelOrgActivity, mock.Anything, mock.Anything).
			Return(errors.New("zitadel down"))

		env.ExecuteWorkflow(restoretenant.RestoreTenantWorkflow, input)
		require.Error(t, env.GetWorkflowError())
		env.AssertNotCalled(t, "ResumeTenantWorkerActivity", mock.Anything, mock.Anything)
	})

	t.Run("a resume that did not take fails the workflow", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv()
		var a restoretenant.Activities

		env.OnActivity(a.ActivateZitadelOrgActivity, mock.Anything, mock.Anything).Return(nil)
		env.OnActivity(a.ResumeTenantWorkerActivity, mock.Anything, mock.Anything).
			Return(nil, errors.New("worker-api reports 0 replicas after resume"))

		env.ExecuteWorkflow(restoretenant.RestoreTenantWorkflow, input)
		require.ErrorContains(t, env.GetWorkflowError(), "0 replicas after resume")
	})
}
