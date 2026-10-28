package registertenant_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// The worker steps are the only ones that leave something outside Zitadel
// behind: a deployment on the worker cluster and a live credential in the
// tenant secret store. A deleteTenant issued while the run is provisioning
// used to be noticed only at the final status flip, after the worker
// existed, with no row left that names it. The run re-reads the row
// immediately before those steps and stops; a row it finds retired gets
// its organization deactivated on the way out.

// pyckGoData is the tenant data that routes a registration into the
// platform-managed worker branch, which is the branch the re-check guards.
func pyckGoData() map[string]any { return map[string]any{"flavour": "pyck-go"} }

// loadReturnsThenDefault programs the two mid-run reads: the run's opening
// read sees a pending row, the re-check before the worker sees whatever
// happened in between.
func loadReturnsThenDefault(
	env *testsuite.TestWorkflowEnvironment,
	second *registertenant.LoadTenantRegistrationActivityOutput,
) {
	var a registertenant.Activities
	first := &registertenant.LoadTenantRegistrationActivityOutput{
		Status:         enttenant.StatusPending,
		OrganizationID: testOrgID,
		Name:           "Test Tenant",
		Data:           pyckGoData(),
	}
	env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).Return(first, nil).Once()
	env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).Return(second, nil).Once()
}

// A tenant retired while its registration was importing the admin user
// must not get a worker afterwards: the run stops at the re-check, reports
// Skipped, and never touches the row's status: the delete owns it now.
func TestRegisterTenantWorkflow_DeleteAfterUserImportStopsBeforeTheWorkerIdentity(t *testing.T) {
	t.Parallel()

	// A soft-deleted row reads as an empty status; a run that already
	// failed reads as error. Both mean "not pending" and both must stop the
	// run at the same point.
	for name, status := range map[string]enttenant.Status{
		"row retired mid-run":      "",
		"row already marked error": enttenant.StatusError,
		"row already marked done":  enttenant.StatusDone,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv()

			loadReturnsThenDefault(env, &registertenant.LoadTenantRegistrationActivityOutput{
				Status:         status,
				OrganizationID: testOrgID,
				Name:           "Test Tenant",
				Data:           pyckGoData(),
			})
			m := newActivityMock()
			m.loadReturn.Data = pyckGoData()
			m.register(env)

			env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError(), "the stop is a clean no-op, not a failure")

			var result registertenant.RegisterTenantWorkflowOutput
			require.NoError(t, env.GetWorkflowResult(&result))
			assert.True(t, result.Skipped, "a run that stopped at the re-check reports Skipped")
			assert.Equal(t, testTenantID, result.TenantID)

			// The run got as far as the org and the admin: it is the worker
			// half that must not happen.
			env.AssertCalled(t, "CreateTenantActivity", mock.Anything, mock.Anything)
			env.AssertCalled(t, "CreateZitadelUserActivity", mock.Anything, mock.Anything)
			env.AssertNotCalled(t, "ProvisionTenantWorkerIdentityActivity", mock.Anything, mock.Anything)
			env.AssertNotCalled(t, "CreateTenantWorkerDeploymentActivity", mock.Anything, mock.Anything)
			env.AssertNotCalled(t, "TriggerTenantSyncActivity", mock.Anything, mock.Anything)
			assert.Empty(t, m.statusCalls,
				"the row belongs to whoever changed it: the run must not flip it to done or error")
			if status == "" {
				assert.Equal(t, []uuid.UUID{testTenantID}, m.retired,
					"the org this run created belongs to a retired row: the run deactivates it")
			} else {
				assert.Empty(t, m.retired, "a live row's org is never deactivated")
			}
		})
	}
}

// The complement: a row still pending at the re-check provisions the worker
// as before. Without this, a re-check that stopped every run would look
// just as green as a correct one.
func TestRegisterTenantWorkflow_StillPendingAtTheRecheckProvisionsTheWorker(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	loadReturnsThenDefault(env, &registertenant.LoadTenantRegistrationActivityOutput{
		Status:         enttenant.StatusPending,
		OrganizationID: testOrgID,
		Name:           "Test Tenant",
		Data:           pyckGoData(),
	})
	m := newActivityMock()
	m.loadReturn.Data = pyckGoData()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result registertenant.RegisterTenantWorkflowOutput
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.False(t, result.Skipped)
	env.AssertCalled(t, "ProvisionTenantWorkerIdentityActivity", mock.Anything, mock.Anything)
	env.AssertCalled(t, "CreateTenantWorkerDeploymentActivity", mock.Anything, mock.Anything)
	require.Equal(t, []registertenant.SetTenantStatusActivityInput{
		{TenantID: testTenantID, Status: enttenant.StatusDone},
	}, m.statusCalls)
}

// The re-check sits inside the pyck-go branch on purpose: a tenant that
// brings its own worker has nothing to guard, and keeping the extra read
// out of its path leaves its recorded activity sequence (the one the
// replay test pins) unchanged.
func TestRegisterTenantWorkflow_PlainTenantReadsTheRowExactlyOnce(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	m := newActivityMock() // no flavour: not a pyck-go tenant
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNumberOfCalls(t, "LoadTenantRegistrationActivity", 1)
	env.AssertNotCalled(t, "ProvisionTenantWorkerIdentityActivity", mock.Anything, mock.Anything)
}

// A pyck-go run reads the row twice: once to start and once before the
// worker. The count is the contract: it is what makes the branch's
// activity sequence, and the replay fixture derived from it, deterministic.
func TestRegisterTenantWorkflow_PyckGoTenantReadsTheRowTwice(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	m := newActivityMock()
	m.loadReturn.Data = pyckGoData()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNumberOfCalls(t, "LoadTenantRegistrationActivity", 2)
}

// The re-check is a step like any other: a read that cannot be answered
// fails the run and marks the row error, rather than being treated as
// "still pending" and provisioning a worker on an assumption.
func TestRegisterTenantWorkflow_RecheckFailureMarksTheRowError(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	var a registertenant.Activities
	first := &registertenant.LoadTenantRegistrationActivityOutput{
		Status:         enttenant.StatusPending,
		OrganizationID: testOrgID,
		Name:           "Test Tenant",
		Data:           pyckGoData(),
	}
	env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).Return(first, nil).Once()
	// Non-retryable so the step ends on its first attempt: a retryable
	// error would be re-attempted and fall through to the catch-all mock.
	env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).
		Return(nil, temporal.NewNonRetryableApplicationError("row unreadable", "registration-load-rejected", nil)).Once()

	m := newActivityMock()
	m.loadReturn.Data = pyckGoData()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "row unreadable")
	env.AssertNotCalled(t, "ProvisionTenantWorkerIdentityActivity", mock.Anything, mock.Anything)
	m.assertMarkedError(t)
}
