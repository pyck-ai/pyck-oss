package registertenant_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// startedActivities records the type of every activity the environment
// starts, in order, retries included.
type startedActivities struct {
	mu    sync.Mutex
	names []string
}

func (s *startedActivities) listen(env *testsuite.TestWorkflowEnvironment) {
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.names = append(s.names, info.ActivityType.Name)
	})
}

func (s *startedActivities) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...)
}

// workerSteps are the activities that leave a running pyck-go worker and
// its long-lived credential behind.
var workerSteps = []string{
	"ProvisionTenantWorkerIdentityActivity",
	"CreateTenantWorkerDeploymentActivity",
}

// The pyck-go worker starts as soon as its deployment exists and carries a
// 20-year credential. Every step that can still fail permanently must run
// before it, so a permanent failure never leaves a crash-looping worker
// behind an error row: the organization metadata and the sync trigger come
// before the worker steps, and only the done flip follows the deployment.
func TestRegisterTenantWorkflow_PyckGoDeploysTheWorkerLast(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	rec := &startedActivities{}
	rec.listen(env)

	m := newActivityMock()
	m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{
		"LoadTenantRegistrationActivity",
		"CreateTenantActivity",
		"CreateZitadelUserActivity",
		"SetUserAsOrganizationOwnerActivity",
		"AddProjectGrantActivity",
		"AddUserGrantActivity",
		"AddDefaultDataTypesActivity",
		"CreateTemporalNamespaceActivity",
		"SetOrgMetadataActivity",
		"TriggerTenantSyncActivity",
		// The re-read before the worker, then the worker itself.
		"LoadTenantRegistrationActivity",
		"ProvisionTenantWorkerIdentityActivity",
		"AddUserGrantActivity",
		"CreateTenantWorkerDeploymentActivity",
		"SetTenantStatusActivity",
	}, rec.list())
	assert.Equal(t, []registertenant.SetTenantStatusActivityInput{
		{TenantID: testTenantID, Status: enttenant.StatusDone},
	}, m.statusCalls)
}

// A permanent failure of the metadata write ends the run with the row
// marked error before any worker exists: no identity is minted and no
// deployment is created, so nothing outside Zitadel is left behind.
func TestRegisterTenantWorkflow_MetadataFailureDeploysNoWorker(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	rec := &startedActivities{}
	rec.listen(env)

	m := newActivityMock()
	m.loadReturn.Data = map[string]any{"flavour": "pyck-go"}
	m.setOrgMetadataErr = temporal.NewNonRetryableApplicationError("metadata refused", "org-metadata-rejected", nil)
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, env.GetWorkflowError(), &appErr)
	assert.Equal(t, "org-metadata-rejected", appErr.Type())

	started := rec.list()
	assert.Contains(t, started, "SetOrgMetadataActivity")
	for _, step := range workerSteps {
		assert.NotContains(t, started, step, "a failed metadata write must not leave a worker behind")
	}
	assert.Equal(t, registertenant.CreateTenantWorkerDeploymentInput{}, m.deployedWorker)
	m.assertMarkedError(t)
}
