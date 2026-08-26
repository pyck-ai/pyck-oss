package registertenant_test

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
	"k8s.io/apimachinery/pkg/util/validation"

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

type activityMock struct {
	createTenantReturn      *registertenant.CreateTenantActivityOutput
	createZitadelUserReturn *registertenant.CreateZitadelUserActivityOutput
	grantReturn             *registertenant.Grant
	setOrgMetadataErr       error
	triggerTenantSyncErr    error

	// deployedWorker records what the worker deployment was asked to create.
	deployedWorker registertenant.CreateTenantWorkerDeploymentInput
}

func newActivityMock() *activityMock {
	return &activityMock{
		createTenantReturn: &registertenant.CreateTenantActivityOutput{
			OrganizationID:    "org-123",
			TenantID:          testTenantID,
			TemporalNamespace: testTenantID.String(),
		},
		createZitadelUserReturn: &registertenant.CreateZitadelUserActivityOutput{UserID: "user-123", LoginName: "testadmin"},
		grantReturn:             &registertenant.Grant{ID: "grant-123"},
	}
}

func (m *activityMock) register(env *testsuite.TestWorkflowEnvironment) {
	var a registertenant.Activities

	env.OnActivity(a.CreateTenantActivity, mock.Anything, mock.Anything).
		Return(m.createTenantReturn, nil)
	env.OnActivity(a.CreateTenantInDbActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.CreateZitadelUserActivity, mock.Anything, mock.Anything).
		Return(m.createZitadelUserReturn, nil)
	env.OnActivity(a.SetUserAsOrganizationOwnerActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.AddProjectGrantActivity, mock.Anything, mock.Anything).
		Return(m.grantReturn, nil)
	env.OnActivity(a.AddUserGrantActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.AddDefaultDataTypesActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.CreateTemporalNamespaceActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.CreateTenantServiceUserActivity, mock.Anything, mock.Anything).
		Return(&registertenant.CreateTenantServiceUserOutput{UserID: "svc-user-123", Token: "test-token"}, nil)
	env.OnActivity(a.StoreTenantWorkerSecretActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.CreateTenantWorkerDeploymentActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			m.deployedWorker, _ = args.Get(1).(registertenant.CreateTenantWorkerDeploymentInput)
		}).
		Return(nil)
	env.OnActivity(a.SetOrgMetadataActivity, mock.Anything, mock.Anything).
		Return(m.setOrgMetadataErr)
	env.OnActivity(a.TriggerTenantSyncActivity, mock.Anything, mock.Anything).
		Return(m.triggerTenantSyncErr)
	env.OnActivity(a.DeleteTenantFromDbActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.DeleteTenantActivity, mock.Anything, mock.Anything).
		Return(nil)
}

func defaultInput(opts ...func(*registertenant.RegisterTenantWorkflowInput)) registertenant.RegisterTenantWorkflowInput {
	input := registertenant.RegisterTenantWorkflowInput{
		Name:           "Test Tenant",
		AdminUsername:  "testadmin",
		AdminEmail:     "test@example.com",
		AdminFirstName: "Test",
		AdminLastName:  "Admin",
		AdminPassword:  "password123",
	}
	for _, opt := range opts {
		opt(&input)
	}
	return input
}

func TestRegisterTenantWorkflow(t *testing.T) {
	t.Parallel()

	var activities registertenant.Activities

	t.Run("executes all activities in order", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.register(env)

		input := defaultInput(func(in *registertenant.RegisterTenantWorkflowInput) {
			in.Data = map[string]any{"flavour": "pyck-go"}
		})

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		var result registertenant.RegisterTenantWorkflowOutput
		require.NoError(t, env.GetWorkflowResult(&result))
		require.Equal(t, "org-123", result.OrganizationID)
		require.Equal(t, "testadmin", result.LoginName)
		require.Equal(t, "user-123", result.UserID)
	})

	t.Run("passes flavour to SetOrgMetadataActivity", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		env.OnActivity(activities.SetOrgMetadataActivity, mock.Anything, mock.MatchedBy(func(input registertenant.SetOrgMetadataActivityInput) bool {
			return input.OrganizationID == "org-123" && input.Data["flavour"] == "pyck-go"
		})).Return(nil)

		m := newActivityMock()
		m.register(env)

		input := defaultInput(func(in *registertenant.RegisterTenantWorkflowInput) {
			in.Data = map[string]any{"flavour": "pyck-go"}
		})

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
	})

	t.Run("rollbacks on SetOrgMetadataActivity failure", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		env.OnActivity(activities.SetOrgMetadataActivity, mock.Anything, mock.Anything).
			Return(errors.New("zitadel unavailable"))

		env.OnActivity(activities.DeleteTenantFromDbActivity, mock.Anything, mock.MatchedBy(func(input registertenant.DeleteTenantFromDbActivityInput) bool {
			return input.OrganizationID == "org-123"
		})).Return(nil)

		env.OnActivity(activities.DeleteTenantActivity, mock.Anything, mock.MatchedBy(func(input registertenant.DeleteTenantActivityInput) bool {
			return input.OrganizationID == "org-123"
		})).Return(nil)

		m := newActivityMock()
		m.register(env)

		input := defaultInput(func(in *registertenant.RegisterTenantWorkflowInput) {
			in.Data = map[string]any{"flavour": "pyck-go"}
		})

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		require.ErrorContains(t, env.GetWorkflowError(), "zitadel unavailable")
	})

	// worker-api validates the deployment name as a DNS-1123 subdomain, which
	// forbids uppercase — so it cannot be built from the extension name
	// ("pyckGo"). Getting this wrong fails every pyck-go registration at the
	// last step, and only against a real server.
	t.Run("deploys the worker under a DNS-1123 name", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow,
			defaultInput(func(in *registertenant.RegisterTenantWorkflowInput) {
				in.Data = map[string]any{"flavour": "pyck-go"}
			}))

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
		assert.Empty(t, validation.IsDNS1123Subdomain(m.deployedWorker.Name),
			"name %q", m.deployedWorker.Name)
		assert.Equal(t, "pyckGo", m.deployedWorker.Extension)
	})

	t.Run("rollbacks on CreateTenantInDbActivity failure including db cleanup", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		env.OnActivity(activities.CreateTenantActivity, mock.Anything, mock.Anything).
			Return(&registertenant.CreateTenantActivityOutput{OrganizationID: "org-123"}, nil)

		env.OnActivity(activities.CreateTenantInDbActivity, mock.Anything, mock.Anything).
			Return(errors.New("constraint violation"))

		env.OnActivity(activities.DeleteTenantFromDbActivity, mock.Anything, mock.MatchedBy(func(input registertenant.DeleteTenantFromDbActivityInput) bool {
			return input.OrganizationID == "org-123"
		})).Return(nil)

		env.OnActivity(activities.DeleteTenantActivity, mock.Anything, mock.MatchedBy(func(input registertenant.DeleteTenantActivityInput) bool {
			return input.OrganizationID == "org-123"
		})).Return(nil)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.ErrorContains(t, env.GetWorkflowError(), "constraint violation")
	})

	t.Run("works without data", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		env.OnActivity(activities.SetOrgMetadataActivity, mock.Anything, mock.MatchedBy(func(input registertenant.SetOrgMetadataActivityInput) bool {
			return len(input.Data) == 0
		})).Return(nil)

		m := newActivityMock()
		m.register(env)

		input := defaultInput(func(in *registertenant.RegisterTenantWorkflowInput) {
			in.Data = nil
		})

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
	})

	t.Run("skips worker deployment for non-pyckGo tenant", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv()

		m := newActivityMock()
		m.register(env)

		input := defaultInput(func(in *registertenant.RegisterTenantWorkflowInput) {
			in.Data = map[string]any{"someKey": "someValue"} // no isPyckGo or flavour
		})

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		// A plain tenant brings its own worker: nothing is deployed for it.
		env.AssertNotCalled(t, "CreateTenantServiceUserActivity", mock.Anything, mock.Anything)
		env.AssertNotCalled(t, "StoreTenantWorkerSecretActivity", mock.Anything, mock.Anything)
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
