package registertenant_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

type activityMock struct {
	createTenantReturn      *registertenant.CreateTenantActivityOutput
	createZitadelUserReturn *registertenant.CreateZitadelUserActivityOutput
	grantReturn             *registertenant.Grant
	setOrgMetadataErr       error
	triggerTenantSyncErr    error
}

func newActivityMock() *activityMock {
	return &activityMock{
		createTenantReturn:      &registertenant.CreateTenantActivityOutput{OrganizationID: "org-123"},
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
	env.OnActivity(a.UpsertK8sWorkersNamespaceActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.CreateTenantServiceUserActivity, mock.Anything, mock.Anything).
		Return(&registertenant.CreateTenantServiceUserOutput{UserID: "svc-user-123", Token: "test-token"}, nil)
	env.OnActivity(a.CreateK8sTenantSecretActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.CreateK8sTemporalConnectionActivity, mock.Anything, mock.Anything).
		Return(nil)
	env.OnActivity(a.CreateK8sWorkerDeploymentActivity, mock.Anything, mock.Anything).
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
		WorkerImage:    "ghcr.io/pyck-ai/pyck-go/worker:test",
		WorkerEnvVars: map[string]string{
			"TEMPORAL_ADDRESS": "temporal.test:7236",
			"PYCK_GATEWAY_URL": "https://test.pyck.cloud/graphql",
		},
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
		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

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
		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

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
		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

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
		require.Error(t, env.GetWorkflowError())
	})

	t.Run("rollbacks on CreateTenantInDbActivity failure including db cleanup", func(t *testing.T) {
		t.Parallel()
		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

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
		require.Error(t, env.GetWorkflowError())
	})

	t.Run("works without data", func(t *testing.T) {
		t.Parallel()
		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

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
		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

		m := newActivityMock()
		m.register(env)

		input := defaultInput(func(in *registertenant.RegisterTenantWorkflowInput) {
			in.Data = map[string]any{"someKey": "someValue"} // no isPyckGo or flavour
		})

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		// Worker deployment activities should NOT have been called
		env.AssertNotCalled(t, "UpsertK8sWorkersNamespaceActivity", mock.Anything, mock.Anything)
		env.AssertNotCalled(t, "CreateTenantServiceUserActivity", mock.Anything, mock.Anything)
		env.AssertNotCalled(t, "CreateK8sTenantSecretActivity", mock.Anything, mock.Anything)
		env.AssertNotCalled(t, "CreateK8sTemporalConnectionActivity", mock.Anything, mock.Anything)
		env.AssertNotCalled(t, "CreateK8sWorkerDeploymentActivity", mock.Anything, mock.Anything)
	})

	t.Run("succeeds even if TriggerTenantSyncActivity fails", func(t *testing.T) {
		t.Parallel()
		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

		m := newActivityMock()
		m.triggerTenantSyncErr = errors.New("temporal unavailable")
		m.register(env)

		env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
	})
}
