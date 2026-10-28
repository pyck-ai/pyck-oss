package registertenant_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"

	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// newTestEnv relaxes the deadlock detector, which defaults to 1s of wall-clock
// per workflow task — a loaded CI runner exceeds that under -race and every
// subtest fails with TMPRL1101. This workflow is a plain sequence of activity
// calls and never blocks, so the timeout only removes the flake.
func newTestEnv() *testsuite.TestWorkflowEnvironment {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Minute})
	return env
}

// testTenantID stands in for a real tenant: the Temporal namespace IS the
// tenant id, and names derived from it have to stay valid.
var testTenantID = uuid.MustParse("f35fe3be-efe9-53e8-88a4-8c550c63205d")

const testOrgID = "org-123"

type activityMock struct {
	loadReturn              *registertenant.LoadTenantRegistrationActivityOutput
	createTenantReturn      *registertenant.CreateTenantActivityOutput
	createZitadelUserReturn *registertenant.CreateZitadelUserActivityOutput
	setOrgMetadataErr       error
	triggerTenantSyncErr    error

	// deployedWorker records what the worker deployment was asked to create.
	deployedWorker registertenant.CreateTenantWorkerDeploymentInput
	// statusCalls records every SetTenantStatusActivity input, in order.
	statusCalls []registertenant.SetTenantStatusActivityInput
	// retired records every tenant whose organization the run deactivated
	// because deleteTenant retired its row mid-run.
	retired []uuid.UUID
	// recorded holds the JSON of every activity input and output the
	// environment saw, the way history would persist them.
	recorded []string
}

func newActivityMock() *activityMock {
	return &activityMock{
		loadReturn: &registertenant.LoadTenantRegistrationActivityOutput{
			Status:         enttenant.StatusPending,
			OrganizationID: testOrgID,
			Name:           "Test Tenant",
		},
		createTenantReturn: &registertenant.CreateTenantActivityOutput{
			OrganizationID:    testOrgID,
			TenantID:          testTenantID,
			TemporalNamespace: testTenantID.String(),
		},
		createZitadelUserReturn: &registertenant.CreateZitadelUserActivityOutput{UserID: "user-123", LoginName: "testadmin"},
	}
}

func (m *activityMock) recordInput(args mock.Arguments) {
	b, err := json.Marshal(args.Get(1))
	if err == nil {
		m.recorded = append(m.recorded, string(b))
	}
}

func (m *activityMock) register(env *testsuite.TestWorkflowEnvironment) {
	var a registertenant.Activities

	env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(m.loadReturn, nil)
	env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(m.createTenantReturn, nil)
	env.OnActivity(a.CreateZitadelUserActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(m.createZitadelUserReturn, nil)
	env.OnActivity(a.SetUserAsOrganizationOwnerActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(nil)
	env.OnActivity(a.AddProjectGrantActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(nil)
	env.OnActivity(a.AddUserGrantActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(nil)
	env.OnActivity(a.AddDefaultDataTypesActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(nil)
	env.OnActivity(a.CreateTemporalNamespaceActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(nil)
	env.OnActivity(a.ProvisionTenantWorkerIdentityActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(&registertenant.ProvisionTenantWorkerIdentityOutput{UserID: "svc-user-123"}, nil)
	env.OnActivity(a.CreateTenantWorkerDeploymentActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			m.recordInput(args)
			m.deployedWorker, _ = args.Get(1).(registertenant.CreateTenantWorkerDeploymentInput)
		}).
		Return(nil)
	env.OnActivity(a.SetOrgMetadataActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(m.setOrgMetadataErr)
	env.OnActivity(a.TriggerTenantSyncActivity, mock.Anything, mock.Anything).
		Run(m.recordInput).Return(m.triggerTenantSyncErr)
	env.OnActivity(a.SetTenantStatusActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			m.recordInput(args)
			in, ok := args.Get(1).(registertenant.SetTenantStatusActivityInput)
			if ok {
				m.statusCalls = append(m.statusCalls, in)
			}
		}).
		Return(nil)
	env.OnActivity(a.DeactivateRetiredOrganizationActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			m.recordInput(args)
			if in, ok := args.Get(1).(registertenant.DeactivateRetiredOrganizationActivityInput); ok {
				m.retired = append(m.retired, in.TenantID)
			}
		}).
		Return(nil)
}

// assertMarkedError checks the run ended with the row marked error exactly
// once and never done: the contract of every failure path.
func (m *activityMock) assertMarkedError(t *testing.T) {
	t.Helper()
	require.Equal(t, []registertenant.SetTenantStatusActivityInput{
		{TenantID: testTenantID, Status: enttenant.StatusError},
	}, m.statusCalls)
}

func defaultInput() registertenant.RegisterTenantWorkflowInput {
	return registertenant.RegisterTenantWorkflowInput{TenantID: testTenantID}
}

func TestRegisterTenantWorkflow(t *testing.T) {
	t.Parallel()

	var activities registertenant.Activities

	t.Run("executes all activities in order", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		var result registertenant.RegisterTenantWorkflowOutput
		require.NoError(t, env.GetWorkflowResult(&result))
		require.Equal(t, testOrgID, result.OrganizationID)
		require.Equal(t, testTenantID, result.TenantID)
		require.Equal(t, "testadmin", result.LoginName)
		require.Equal(t, "user-123", result.UserID)
		assert.False(t, result.Skipped)
		require.Equal(t, []registertenant.SetTenantStatusActivityInput{
			{TenantID: testTenantID, Status: enttenant.StatusDone},
		}, m.statusCalls)
	})

	t.Run("passes flavour to SetOrgMetadataActivity", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		env.OnActivity(activities.SetOrgMetadataActivity, mock.Anything, mock.MatchedBy(func(input registertenant.SetOrgMetadataActivityInput) bool {
			return input.OrganizationID == testOrgID && input.Data["flavour"] == "pyck-go"
		})).Return(nil)

		m := newActivityMock()
		m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
	})

	t.Run("marks the row error on SetOrgMetadataActivity failure", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.setOrgMetadataErr = errors.New("zitadel unavailable")
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.ErrorContains(t, env.GetWorkflowError(), "zitadel unavailable")
		m.assertMarkedError(t)
	})

	// worker-api validates the deployment name as a DNS-1123 subdomain, which
	// forbids uppercase — so it cannot be built from the extension name
	// ("pyckGo"). Getting this wrong fails every pyck-go registration at the
	// last step, and only against a real server.
	t.Run("deploys the worker under a DNS-1123 name", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
		assert.Empty(t, validation.IsDNS1123Subdomain(m.deployedWorker.Name),
			"name %q", m.deployedWorker.Name)
		assert.Equal(t, "pyckGo", m.deployedWorker.Extension)
	})

	t.Run("marks the row error when the final status flip fails", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		// Specific expectations first: the mock picks the first match, and
		// the catch-all registered below would otherwise win.
		m := newActivityMock()
		env.OnActivity(activities.SetTenantStatusActivity, mock.Anything, mock.MatchedBy(func(in registertenant.SetTenantStatusActivityInput) bool {
			return in.Status == enttenant.StatusDone
		})).Return(errors.New("tenant row is not pending"))
		env.OnActivity(activities.SetTenantStatusActivity, mock.Anything, mock.MatchedBy(func(in registertenant.SetTenantStatusActivityInput) bool {
			return in.Status == enttenant.StatusError
		})).Run(func(args mock.Arguments) {
			in, ok := args.Get(1).(registertenant.SetTenantStatusActivityInput)
			require.True(t, ok)
			m.statusCalls = append(m.statusCalls, in)
		}).Return(nil)
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.ErrorContains(t, env.GetWorkflowError(), "tenant row is not pending")
		m.assertMarkedError(t)
	})

	t.Run("works without data", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		env.OnActivity(activities.SetOrgMetadataActivity, mock.Anything, mock.MatchedBy(func(input registertenant.SetOrgMetadataActivityInput) bool {
			return len(input.Data) == 0
		})).Return(nil)

		m := newActivityMock()
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
	})

	t.Run("skips worker deployment for non-pyckGo tenant", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.loadReturn.Data = map[string]any{"someKey": "someValue"} // no flavour
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		// A plain tenant brings its own worker: nothing is deployed for it.
		env.AssertNotCalled(t, "ProvisionTenantWorkerIdentityActivity", mock.Anything, mock.Anything)
		env.AssertNotCalled(t, "CreateTenantWorkerDeploymentActivity", mock.Anything, mock.Anything)
	})

	t.Run("succeeds even if TriggerTenantSyncActivity fails", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.triggerTenantSyncErr = errors.New("temporal unavailable")
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
	})
}

// A start without a tenant row to work from must fail before any Zitadel
// call and without touching any row.
func TestRegisterTenantWorkflow_RejectsInputWithoutTenantID(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	m := newActivityMock()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, registertenant.RegisterTenantWorkflowInput{})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), registertenant.ErrMissingTenantID.Error())
	env.AssertNotCalled(t, "LoadTenantRegistrationActivity", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "CreateTenantActivity", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "SetTenantStatusActivity", mock.Anything, mock.Anything)
}

// A load that fails for good (DB unreachable past the retry budget) ends
// the run with that error and, like every other step, attempts to mark
// the row; the resolver's re-read then reports whichever state landed.
func TestRegisterTenantWorkflow_LoadFailureMarksRowError(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).
		Return(nil, errors.New("db unreachable"))
	m := newActivityMock()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "db unreachable")
	m.assertMarkedError(t)
	env.AssertNotCalled(t, "CreateTenantActivity", mock.Anything, mock.Anything)
}

// When the failure mark itself cannot be written, the step's error is
// still what the run fails with (never the status flip's) so the
// resolver reports the provisioning failure and its re-read the row's
// actual state.
func TestRegisterTenantWorkflow_StepErrorSurvivesFailedErrorMark(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	env.OnActivity(a.SetUserAsOrganizationOwnerActivity, mock.Anything, mock.Anything).
		Return(errors.New("owner boom"))
	env.OnActivity(a.SetTenantStatusActivity, mock.Anything, mock.Anything).
		Return(errors.New("tenant row is not pending"))
	m := newActivityMock()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.ErrorContains(t, err, "owner boom")
	assert.NotContains(t, err.Error(), "not pending", "the mark's own failure must not replace the step's error")
	env.AssertCalled(t, "SetTenantStatusActivity", mock.Anything, registertenant.SetTenantStatusActivityInput{TenantID: testTenantID, Status: enttenant.StatusError})
}

// A row that is not pending (replayed create event, registration already
// finished or failed, row gone) is a no-op: nothing is created and the
// row's status is left as it is.
func TestRegisterTenantWorkflow_NonPendingRowIsNoOp(t *testing.T) {
	t.Parallel()

	for _, status := range []enttenant.Status{enttenant.StatusDone, enttenant.StatusError, ""} {
		t.Run("status "+string(status), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv()
			m := newActivityMock()
			m.loadReturn.Status = status
			m.register(env)

			env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			var result registertenant.RegisterTenantWorkflowOutput
			require.NoError(t, env.GetWorkflowResult(&result))
			assert.True(t, result.Skipped)
			assert.Equal(t, testTenantID, result.TenantID)
			env.AssertNotCalled(t, "CreateTenantActivity", mock.Anything, mock.Anything)
			env.AssertNotCalled(t, "SetTenantStatusActivity", mock.Anything, mock.Anything)
		})
	}
}

// callRecorder captures the order of activity executions; the workflow's
// ordering IS the contract (load first, status=done only after every
// provisioning step).
type callRecorder struct {
	order       []string
	createInput any
	userInput   any
}

func (r *callRecorder) record(name string) func(mock.Arguments) {
	return func(mock.Arguments) { r.order = append(r.order, name) }
}

// The org is created under the row's IDs (never derived here), the admin
// user activity gets nothing but the tenant ID, and the row is flipped to
// done strictly after every other step, including the sync trigger: only
// done rows take part in sync, expiry and reconcile. No input or output
// the run records carries a password-hash field; that the row load leaves
// the hash out is TestLoadTenantRegistrationActivity_ReturnsRowWithoutRegistration's
// job.
func TestRegisterTenantWorkflow_LoadsFirstAndFlipsStatusLast(t *testing.T) {
	t.Parallel()
	env := newTestEnv()

	// Specific expectations first: the mock picks the first match, and
	// the catch-alls m.register adds would otherwise win.
	var a registertenant.Activities
	rec := &callRecorder{}
	m := newActivityMock()
	env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).
		Run(rec.record("LoadTenantRegistrationActivity")).Return(m.loadReturn, nil)
	env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			rec.order = append(rec.order, "CreateTenantActivity")
			rec.createInput = args.Get(1)
		}).
		Return(m.createTenantReturn, nil)
	env.OnActivity(a.CreateZitadelUserActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			rec.order = append(rec.order, "CreateZitadelUserActivity")
			rec.userInput = args.Get(1)
		}).
		Return(m.createZitadelUserReturn, nil)
	env.OnActivity(a.TriggerTenantSyncActivity, mock.Anything, mock.Anything).
		Run(rec.record("TriggerTenantSyncActivity")).Return(nil)
	env.OnActivity(a.SetTenantStatusActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			rec.order = append(rec.order, "SetTenantStatusActivity")
			in, ok := args.Get(1).(registertenant.SetTenantStatusActivityInput)
			require.True(t, ok, "SetTenantStatusActivity input type")
			m.statusCalls = append(m.statusCalls, in)
		}).Return(nil)
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	createInput, ok := rec.createInput.(registertenant.CreateTenantActivityInput)
	require.True(t, ok, "CreateTenantActivity input type, got %T", rec.createInput)
	assert.Equal(t, "Test Tenant", createInput.Name)
	assert.Equal(t, testOrgID, createInput.OrganizationID, "org must be created under the row's ID")
	assert.Equal(t, testTenantID, createInput.TenantID)
	assert.Equal(t, registertenant.CreateZitadelUserActivityInput{TenantID: testTenantID}, rec.userInput,
		"the admin account is read from the row inside the activity")

	require.Equal(t, []string{
		"LoadTenantRegistrationActivity",
		"CreateTenantActivity",
		"CreateZitadelUserActivity",
		"TriggerTenantSyncActivity",
		"SetTenantStatusActivity",
	}, rec.order)
	require.Equal(t, []registertenant.SetTenantStatusActivityInput{
		{TenantID: testTenantID, Status: enttenant.StatusDone},
	}, m.statusCalls)
	for _, recorded := range m.recorded {
		assert.NotContains(t, recorded, "PasswordHash")
	}
}

// Every failing step ends the run with that step's error and the row
// marked error: nothing is compensated, the org created so far stays.
func TestRegisterTenantWorkflow_EachFailureMarksRowErrorWithoutCompensation(t *testing.T) {
	t.Parallel()

	var a registertenant.Activities
	steps := []struct {
		name string
		fail func(env *testsuite.TestWorkflowEnvironment, err error)
	}{
		{"CreateTenantActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).Return(nil, err)
		}},
		{"CreateZitadelUserActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.CreateZitadelUserActivity, mock.Anything, mock.Anything).Return(nil, err)
		}},
		{"SetUserAsOrganizationOwnerActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.SetUserAsOrganizationOwnerActivity, mock.Anything, mock.Anything).Return(err)
		}},
		{"AddProjectGrantActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.AddProjectGrantActivity, mock.Anything, mock.Anything).Return(err)
		}},
		{"AddUserGrantActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.AddUserGrantActivity, mock.Anything, mock.Anything).Return(err)
		}},
		{"AddDefaultDataTypesActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.AddDefaultDataTypesActivity, mock.Anything, mock.Anything).Return(err)
		}},
		{"CreateTemporalNamespaceActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.CreateTemporalNamespaceActivity, mock.Anything, mock.Anything).Return(err)
		}},
		{"ProvisionTenantWorkerIdentityActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.ProvisionTenantWorkerIdentityActivity, mock.Anything, mock.Anything).Return(nil, err)
		}},
		{"CreateTenantWorkerDeploymentActivity", func(env *testsuite.TestWorkflowEnvironment, err error) {
			env.OnActivity(a.CreateTenantWorkerDeploymentActivity, mock.Anything, mock.Anything).Return(err)
		}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv()
			boom := errors.New(step.name + " boom")
			step.fail(env, boom)
			m := newActivityMock()
			m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
			m.register(env)

			env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

			require.True(t, env.IsWorkflowCompleted())
			require.ErrorContains(t, env.GetWorkflowError(), boom.Error())
			m.assertMarkedError(t)
		})
	}
}

// A run cancelled mid-provisioning (operator cancel from the Temporal UI or
// CLI) still marks the row error: the status flip has to run on a context
// that survives the cancellation, otherwise the row stays pending with no
// run behind it, the state the error status exists to rule out.
func TestRegisterTenantWorkflow_CancelledRunStillMarksRowError(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	m := newActivityMock()
	env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { env.CancelWorkflow() }).
		Return(m.createTenantReturn, nil)
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	var canceled *temporal.CanceledError
	require.ErrorAs(t, env.GetWorkflowError(), &canceled, "the run must end cancelled, not succeed past the cancel")
	env.AssertCalled(t, "SetTenantStatusActivity", mock.Anything,
		registertenant.SetTenantStatusActivityInput{TenantID: testTenantID, Status: enttenant.StatusError})
	m.assertMarkedError(t)
	env.AssertNotCalled(t, "SetUserAsOrganizationOwnerActivity", mock.Anything, mock.Anything)
}

// The project grant is what lets the tenant's service users hold the
// per-service roles behind the service-token role gate; a grant without
// them registers a tenant whose every service-token call is refused, and
// nothing but a live call would notice.
func TestRegisterTenantWorkflow_ProjectGrantCarriesServiceRoles(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	m := newActivityMock()
	var grantInput registertenant.AddProjectGrantActivityInput
	env.OnActivity(a.AddProjectGrantActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			in, ok := args.Get(1).(registertenant.AddProjectGrantActivityInput)
			require.True(t, ok, "AddProjectGrantActivity input type, got %T", args.Get(1))
			grantInput = in
		}).
		Return(nil)
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.NotEmpty(t, serviceroles.ServiceRoleStrings(), "the per-service role set must not be empty")
	assert.Equal(t, testOrgID, grantInput.OrganizationID)
	assert.ElementsMatch(t,
		append([]string{authn.ROLE_ADMIN.String(), authn.ROLE_WRITER.String(), authn.ROLE_READER.String()}, serviceroles.ServiceRoleStrings()...),
		grantInput.Roles)
}

// =============================================================================
// Retry budget (issue #1436)
// =============================================================================

// activityErrorFor digs the failing step's ActivityError out of the run's
// error, which is what names the step in the resolver's message.
func activityErrorFor(t *testing.T, err error) *temporal.ActivityError {
	t.Helper()
	var activityErr *temporal.ActivityError
	require.ErrorAs(t, err, &activityErr, "a failed step must surface as an activity error, got %v", err)
	return activityErr
}

// The registration must survive a dependency that is briefly gone: a
// Zitadel restart, a rollout, a network partition. Before #1436 every
// activity was capped at three attempts, so an outage of more than ~93 s
// permanently failed a tenant whose registration would have completed.
func TestRegisterTenantWorkflow_TransientFailureIsRetriedUntilItSucceeds(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	m := newActivityMock()

	// Zitadel refuses the org creation for the length of a restart, then
	// accepts it. Nine refusals is over two minutes of mock time at the
	// policy's backoff, comfortably longer than a pod restart and well
	// past what the old three-attempt cap bought.
	attempts := 0
	var recoveredAt time.Duration
	start := env.Now()
	count := func(mock.Arguments) { attempts++ }
	env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).
		Run(count).Return(nil, status.Error(codes.Unavailable, "zitadel is restarting")).Times(9)
	env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			count(args)
			recoveredAt = env.Now().Sub(start)
		}).
		Return(m.createTenantReturn, nil)
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "the registration must complete once the dependency comes back")
	assert.Equal(t, 10, attempts, "the step is retried past the old three-attempt cap")
	assert.Greater(t, recoveredAt, 2*time.Minute,
		"the activity budget must span a real dependency outage, not just a blip")
	require.Equal(t, []registertenant.SetTenantStatusActivityInput{
		{TenantID: testTenantID, Status: enttenant.StatusDone},
	}, m.statusCalls, "the row must end done, never error, when the dependency recovers")
}

// A validation failure is the answer the dependency gives to this request
// and will give to every repeat of it. Retrying it holds the workflow ID
// and the row in pending for the whole activity budget while nothing can
// change, so it has to end the run on the first attempt.
//
// Every row injects a verdict that is already non-retryable, so this table
// pins the workflow half of the contract: the retry policy honours the
// verdict, the run fails with the step that produced it, and the row is
// marked error. That an activity reaches that verdict at all (the half a
// step returning a bare error would break) is pinned per activity in
// activities_test.go against the values its dependency really answers with.
func TestRegisterTenantWorkflow_ValidationFailureIsAttemptedExactlyOnce(t *testing.T) {
	t.Parallel()

	var a registertenant.Activities
	steps := []struct {
		name    string
		errType string
		fail    func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments))
	}{
		{"LoadTenantRegistrationActivity", "tenant-row-unreadable", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.LoadTenantRegistrationActivity, mock.Anything, mock.Anything).Run(count).Return(nil, err)
		}},
		{"CreateTenantActivity", "organization-name-taken", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).Run(count).Return(nil, err)
		}},
		{"CreateZitadelUserActivity", "admin-username-taken", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.CreateZitadelUserActivity, mock.Anything, mock.Anything).Run(count).Return(nil, err)
		}},
		{"SetUserAsOrganizationOwnerActivity", "organization-owner-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.SetUserAsOrganizationOwnerActivity, mock.Anything, mock.Anything).Run(count).Return(err)
		}},
		{"AddProjectGrantActivity", "project-grant-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.AddProjectGrantActivity, mock.Anything, mock.Anything).Run(count).Return(err)
		}},
		{"AddUserGrantActivity", "user-grant-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.AddUserGrantActivity, mock.Anything, mock.Anything).Run(count).Return(err)
		}},
		{"AddDefaultDataTypesActivity", "default-datatypes-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.AddDefaultDataTypesActivity, mock.Anything, mock.Anything).Run(count).Return(err)
		}},
		{"CreateTemporalNamespaceActivity", "temporal-namespace-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.CreateTemporalNamespaceActivity, mock.Anything, mock.Anything).Run(count).Return(err)
		}},
		{"ProvisionTenantWorkerIdentityActivity", "worker-identity-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.ProvisionTenantWorkerIdentityActivity, mock.Anything, mock.Anything).Run(count).Return(nil, err)
		}},
		{"CreateTenantWorkerDeploymentActivity", "worker-api-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.CreateTenantWorkerDeploymentActivity, mock.Anything, mock.Anything).Run(count).Return(err)
		}},
		{"SetOrgMetadataActivity", "org-metadata-rejected", func(env *testsuite.TestWorkflowEnvironment, err error, count func(mock.Arguments)) {
			env.OnActivity(a.SetOrgMetadataActivity, mock.Anything, mock.Anything).Run(count).Return(err)
		}},
	}

	// Every step of a pyck-go registration is here except the final flip
	// to done: its refusal is the row's own state, pinned on the activity
	// in activities_test.go. A step added without a classified refusal
	// path would go unnoticed.
	require.Len(t, steps, 11)

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv()
			attempts := 0
			refusal := temporal.NewNonRetryableApplicationError(
				step.name+" refused", step.errType, errors.New(step.name+" refused"))
			step.fail(env, refusal, func(mock.Arguments) { attempts++ })
			m := newActivityMock()
			// The worker steps only run for a pyck-go tenant.
			m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
			m.register(env)

			env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

			require.True(t, env.IsWorkflowCompleted())
			err := env.GetWorkflowError()
			require.Error(t, err)

			assert.Equal(t, 1, attempts, "a refused request must not be retried")

			assert.Equal(t, step.name, activityErrorFor(t, err).ActivityType().GetName(),
				"the run's error must name the failing step")
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, err, &appErr)
			assert.Equal(t, step.errType, appErr.Type(),
				"the error type is what the resolver's message and the e2e suites read")

			m.assertMarkedError(t)
		})
	}
}

// A dependency that never comes back must still end the run, and end it
// with the row in a terminal state: the schedule-to-close budget is the
// only bound on a retry loop with no attempt cap, and its exhaustion has
// to run the same failure path a refusal does.
func TestRegisterTenantWorkflow_ExhaustedBudgetMarksRowError(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	// The trigger sets this bound as WorkflowExecutionTimeout, which the
	// test suite has no setter for; the run timeout stands in for it
	// because this workflow never continues as new, so the two coincide.
	// A run that exceeds the bound closes TIMED_OUT WITHOUT the status
	// flip, leaving the row pending with no run behind it, the state the
	// error status rules out.
	env.SetWorkflowRunTimeout(registertenant.ExecutionTimeout)

	var a registertenant.Activities
	attempts := 0
	env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { attempts++ }).
		Return(nil, status.Error(codes.Unavailable, "zitadel is gone"))
	m := newActivityMock()
	m.register(env)

	start := env.Now()
	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted(), "the run must terminate, not retry forever")
	err := env.GetWorkflowError()
	require.Error(t, err)

	assert.Equal(t, "CreateTenantActivity", activityErrorFor(t, err).ActivityType().GetName(),
		"the run must fail with the step that ran out of budget, not with a workflow timeout")
	assert.Greater(t, attempts, 3, "the step must retry past the old three-attempt cap while the outage lasts")
	m.assertMarkedError(t)

	elapsed := env.Now().Sub(start)
	assert.LessOrEqual(t, elapsed, registertenant.ActivityScheduleToClose,
		"the step must give up on its budget, which is the only bound on a policy with no attempt cap")
	assert.Less(t, elapsed, registertenant.ExecutionTimeout,
		"a step spending its whole budget must not push the run past the execution bound, "+
			"where it would close TIMED_OUT and leave the row pending")
	// The old shared policy capped every step at three attempts, ~93 s in
	// total; a budget that does not buy minutes of retrying has not
	// changed the outage a registration survives.
	assert.Greater(t, elapsed, 2*time.Minute,
		"the outage a registration survives must be minutes, not the old three-attempt cap")
}

// The sync trigger is the one step whose failure is only logged, so it
// must not be able to add the full activity budget to every registration
// whose Temporal call hiccups.
func TestRegisterTenantWorkflow_SyncTriggerFailureIsBoundedAndStillCompletes(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	attempts := 0
	env.OnActivity(a.TriggerTenantSyncActivity, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { attempts++ }).
		Return(status.Error(codes.Unavailable, "temporal is gone"))
	m := newActivityMock()
	m.register(env)

	start := env.Now()
	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a fully provisioned tenant must not be failed by the sync trigger")
	require.Equal(t, []registertenant.SetTenantStatusActivityInput{
		{TenantID: testTenantID, Status: enttenant.StatusDone},
	}, m.statusCalls)

	assert.Greater(t, attempts, 1, "the trigger is still retried inside its own budget")

	elapsed := env.Now().Sub(start)
	assert.LessOrEqual(t, elapsed, registertenant.SyncScheduleToClose,
		"the best-effort step must give up on its own short budget")
	assert.Less(t, elapsed, registertenant.ActivityScheduleToClose,
		"a step whose failure is only logged must not cost a provisioning step's budget")
}

// Every attempt of the worker-identity step mints a personal access token
// that nothing revokes, so its retries are bounded by a shorter budget
// than the rest of the run: an unreachable Zitadel must cost a handful of
// live credentials, not one per attempt for five minutes.
func TestRegisterTenantWorkflow_WorkerIdentityRetriesOnItsOwnShortBudget(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	attempts := 0
	env.OnActivity(a.ProvisionTenantWorkerIdentityActivity, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { attempts++ }).
		Return(nil, status.Error(codes.Unavailable, "zitadel is gone"))
	m := newActivityMock()
	// The worker steps only run for a pyck-go tenant.
	m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
	m.register(env)

	start := env.Now()
	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	assert.Equal(t, "ProvisionTenantWorkerIdentityActivity", activityErrorFor(t, env.GetWorkflowError()).ActivityType().GetName())
	m.assertMarkedError(t)

	assert.Greater(t, attempts, 1, "the step is still retried inside its own budget")

	elapsed := env.Now().Sub(start)
	assert.LessOrEqual(t, elapsed, registertenant.WorkerIdentityScheduleToClose,
		"the step must give up on the budget that bounds how many tokens it mints")
	assert.Less(t, elapsed, registertenant.ActivityScheduleToClose,
		"a step that leaves a live credential per attempt must not retry for a provisioning budget")
}

// scheduledStep is one activity the run scheduled and the budget it was
// scheduled with.
type scheduledStep struct {
	name   string
	budget time.Duration
}

// recordSchedules collects the ScheduleToCloseTimeout of every activity a
// run schedules. That budget is the bound the whole design rests on: the
// retry policy has no attempt cap, so a step scheduled without one retries
// until the run's execution bound and closes TIMED_OUT with the row left
// pending. Elapsed time cannot show its absence: the test environment caps
// a retrying activity at ten attempts ("in unit test we don't want to retry
// forever"), so a step with no budget still stops.
func recordSchedules(env *testsuite.TestWorkflowEnvironment) *[]scheduledStep {
	var steps []scheduledStep
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		steps = append(steps, scheduledStep{name: info.ActivityType.Name, budget: info.ScheduleToCloseTimeout})
	})
	return &steps
}

// wantBudget is the budget a step must be scheduled with: the provisioning
// one unless minting credentials or best-effort work asks for a shorter.
func wantBudget(step string) time.Duration {
	switch step {
	case "TriggerTenantSyncActivity":
		return registertenant.SyncScheduleToClose
	case "ProvisionTenantWorkerIdentityActivity":
		return registertenant.WorkerIdentityScheduleToClose
	default:
		return registertenant.ActivityScheduleToClose
	}
}

// assertBudgets checks every scheduled step ran on the budget its design
// assigns it. A step scheduled without one is the failure mode #1436
// exists to close.
func assertBudgets(t *testing.T, steps []scheduledStep) {
	t.Helper()
	for _, step := range steps {
		assert.Equal(t, wantBudget(step.name), step.budget,
			"%s must be scheduled with the budget that bounds its retries; without one it retries "+
				"until the run's execution bound and closes TIMED_OUT with the row pending", step.name)
	}
}

// Every step of a run carries its budget, and the longest run carries them
// all: the schedule-to-close bound is what turns a dependency outage into
// attempts and still ends the run.
func TestRegisterTenantWorkflow_EveryStepRunsOnItsOwnBudget(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	steps := recordSchedules(env)
	m := newActivityMock()
	// A pyck-go tenant is the longest run: it schedules every step.
	m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Len(t, *steps, registertenant.MaxActivitiesPerRun-2,
		"a pyck-go run schedules every activity but the extra error mark and the organization's "+
			"retirement maxActivitiesPerRun budgets for")
	assertBudgets(t, *steps)
}

// The status flip is the one activity that guarantees the row reaches a
// terminal state, so it runs on the provisioning budget whichever step
// failed: the worker identity narrows its own context to a minute to
// bound the tokens it mints, and the mark must not inherit that.
func TestRegisterTenantWorkflow_ErrorMarkKeepsTheProvisioningBudget(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	env.OnActivity(a.ProvisionTenantWorkerIdentityActivity, mock.Anything, mock.Anything).
		Return(nil, status.Error(codes.Unavailable, "zitadel is gone"))
	steps := recordSchedules(env)
	m := newActivityMock()
	// The worker identity, the narrowest budget in the run, only runs for
	// a pyck-go tenant.
	m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	m.assertMarkedError(t)

	var marks int
	for _, step := range *steps {
		if step.name != "SetTenantStatusActivity" {
			continue
		}
		marks++
		assert.Equal(t, registertenant.ActivityScheduleToClose, step.budget,
			"the error mark must not inherit the failing step's budget: a short one leaves the row "+
				"pending when the database is briefly unreachable")
	}
	require.Equal(t, 1, marks, "the run must mark the row exactly once")
	assertBudgets(t, *steps)
}

// WorkflowExecutionTimeout is recorded on the execution when the trigger
// starts it and never changes afterwards, while activityScheduleToClose
// is read from whichever binary serves the workflow task. A run started
// before a deploy that raises the constant must therefore not schedule
// more activity budget than its own recorded bound affords: a run that
// crosses that bound closes TIMED_OUT, which never executes the status
// flip, and leaves the row pending with no run behind it.
func TestRegisterTenantWorkflow_BudgetsFitTheRunsOwnRecordedTimeout(t *testing.T) {
	t.Parallel()
	// The bound a run started by a build with a two-minute step budget
	// carries; the code under test wants five.
	const recorded = 34 * time.Minute

	env := newTestEnv()
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{WorkflowExecutionTimeout: recorded})
	steps := recordSchedules(env)
	m := newActivityMock()
	// A pyck-go tenant is the longest run: it schedules every step.
	m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	affordable := recorded / (registertenant.MaxActivitiesPerRun + 1)
	require.NotEmpty(t, *steps)
	var scheduled time.Duration
	for _, step := range *steps {
		assert.Equal(t, min(wantBudget(step.name), affordable), step.budget,
			"%s must be scheduled with what this run's own execution bound affords, not with the "+
				"constant the running binary happens to carry", step.name)
		scheduled += step.budget
	}
	// The error mark a failure adds on top, and the retirement a mark that
	// finds the row deleted adds after it, have to fit as well, or a run
	// that spends every budget times out before recording its outcome.
	assert.Less(t, scheduled+2*affordable, recorded,
		"every step at its full budget plus the error mark and the retirement must finish inside the recorded bound")
}

// The retry policy is what turns a dependency outage into attempts
// instead of a failed tenant; an attempt cap would restore exactly the
// behaviour #1436 removes.
func TestRegistrationRetryPolicy_IsBoundedByBudgetNotByAttempts(t *testing.T) {
	t.Parallel()

	policy := registertenant.RegistrationRetryPolicy()

	assert.Zero(t, policy.MaximumAttempts,
		"an attempt cap turns a Zitadel restart into a permanently failed tenant; the budget is the bound")
	assert.Positive(t, policy.InitialInterval)
	assert.Greater(t, policy.BackoffCoefficient, 1.0, "the backoff must widen, or an outage becomes a hot loop")
	assert.Positive(t, policy.MaximumInterval)
	assert.Less(t, policy.MaximumInterval, registertenant.ActivityScheduleToClose,
		"an interval at or above the budget spends it on a single wait")
	assert.Empty(t, policy.NonRetryableErrorTypes,
		"classification decides what is permanent, not a list of type names kept in sync by hand")
}

// A run that exceeds WorkflowExecutionTimeout closes TIMED_OUT without
// executing the status flip, leaving the row pending. The bound therefore
// has to cover every activity the run can schedule at its full retry
// budget, including the error mark, which is what maxActivitiesPerRun
// counts. Adding a step without growing it silently re-opens that hole.
func TestExecutionTimeout_CoversEveryActivityAtItsFullBudget(t *testing.T) {
	t.Parallel()

	// The longest run: a pyck-go registration provisions every step, and
	// a failure adds the error mark on top of the steps already run.
	env := newTestEnv()
	m := newActivityMock()
	// A pyck-go tenant is the longest run: it provisions the worker too.
	m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
	scheduled := 0
	env.SetOnActivityStartedListener(func(*activity.Info, context.Context, converter.EncodedValues) { scheduled++ })
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Positive(t, scheduled)
	// The worst run is every provisioning step at its full budget plus the
	// error mark a failure adds on top of them, plus the retirement of the
	// organization when that mark finds the row deleted.
	worst := scheduled + 2
	assert.LessOrEqual(t, worst, registertenant.MaxActivitiesPerRun,
		"maxActivitiesPerRun must count every activity a run schedules, the error mark and the "+
			"retirement included; grow it (and ExecutionTimeout with it) when a step is added")

	assert.Greater(t, registertenant.ExecutionTimeout,
		time.Duration(worst)*registertenant.ActivityScheduleToClose,
		"every step spending its full budget must still finish inside the execution bound")
	assert.Greater(t, registertenant.ActivityScheduleToClose, registertenant.ActivityStartToClose,
		"a budget at or below one attempt leaves no room for a retry")
	assert.Greater(t, registertenant.SyncScheduleToClose, registertenant.ActivityStartToClose,
		"the best-effort step must still have room for a retry")
	assert.Less(t, registertenant.SyncScheduleToClose, registertenant.ActivityScheduleToClose,
		"the best-effort step must keep the shorter budget")
	assert.Greater(t, registertenant.WorkerIdentityScheduleToClose, registertenant.ActivityStartToClose,
		"a budget at or below one attempt leaves the worker identity no retry at all")
	assert.Less(t, registertenant.WorkerIdentityScheduleToClose, registertenant.ActivityScheduleToClose,
		"the step that mints a credential per attempt must keep the shorter budget")
}

// The admin's user grant is what its token then carries as project roles:
// a grant without admin registers a tenant whose admin passes no
// admin-gated resolver (userServiceRoles, assignRoles, deleteTenant's
// role gate, …), and only a live call would notice. admin alone: every
// consumer of the ladder is cumulative, so writer and reader are implied.
func TestRegisterTenantWorkflow_AdminUserGrantCarriesAdminRole(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	m := newActivityMock()

	var userGrants []registertenant.AddUserGrantActivityInput
	env.OnActivity(a.AddUserGrantActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			in, ok := args.Get(1).(registertenant.AddUserGrantActivityInput)
			require.True(t, ok, "AddUserGrantActivity input type, got %T", args.Get(1))
			userGrants = append(userGrants, in)
		}).
		Return(nil)
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Len(t, userGrants, 1, "a plain tenant grants exactly one user: the admin")
	adminGrant := userGrants[0]
	assert.Equal(t, "user-123", adminGrant.UserID, "the grant must attach to the created admin user")
	assert.Equal(t, testOrgID, adminGrant.OrganizationID)
	assert.Equal(t, []string{authn.ROLE_ADMIN.String()}, adminGrant.Roles,
		"the admin must be granted admin and nothing else: the ladder is cumulative on every consumer")
}
