package registertenant_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// deleteTenant never touches a registration run: a run whose row is
// deleted under it notices on its own and deactivates the organization the
// row names, because the delete event's DisableTenantWorkflow may have run
// before this run created it.

// rowRetired is what SetTenantStatusActivity answers for a row deleteTenant
// soft-deleted.
func rowRetired() error {
	return temporal.NewNonRetryableApplicationError(registertenant.ErrTenantRetired.Error(), "tenant-row-missing", nil)
}

// A step that fails on the deleted row (the admin import reads it) leads to
// the error mark, which the deleted row refuses: the run deactivates the
// org and still fails with the step's own error.
func TestRegisterTenantWorkflow_ErrorMarkOnARetiredRowRetiresTheOrg(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	env.OnActivity(a.CreateZitadelUserActivity, mock.Anything, mock.Anything).
		Return(nil, temporal.NewNonRetryableApplicationError("load tenant row: not found", "tenant-row-missing", nil))
	env.OnActivity(a.SetTenantStatusActivity, mock.Anything, mock.Anything).Return(rowRetired())
	m := newActivityMock()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "load tenant row", "the step's error is what the run fails with")
	env.AssertCalled(t, "SetTenantStatusActivity", mock.Anything,
		registertenant.SetTenantStatusActivityInput{TenantID: testTenantID, Status: enttenant.StatusError})
	assert.Equal(t, []uuid.UUID{testTenantID}, m.retired)
	env.AssertNotCalled(t, "SetUserAsOrganizationOwnerActivity", mock.Anything, mock.Anything)
}

// A delete that lands after the last provisioning step is noticed by the
// done flip; the error mark confirms it and the run retires the org. This
// is the run's longest path, which maxActivitiesPerRun counts.
func TestRegisterTenantWorkflow_DoneFlipOnARetiredRowRetiresTheOrg(t *testing.T) {
	t.Parallel()
	env := newTestEnv()
	var a registertenant.Activities
	var flips []enttenant.Status
	env.OnActivity(a.SetTenantStatusActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			if in, ok := args.Get(1).(registertenant.SetTenantStatusActivityInput); ok {
				flips = append(flips, in.Status)
			}
		}).
		Return(rowRetired())
	m := newActivityMock()
	m.loadReturn.Data = pyckGoData()
	m.register(env)

	env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

	require.True(t, env.IsWorkflowCompleted())
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, env.GetWorkflowError(), &appErr)
	assert.Equal(t, "tenant-row-missing", appErr.Type())
	assert.Equal(t, []enttenant.Status{enttenant.StatusDone, enttenant.StatusError}, flips)
	assert.Equal(t, []uuid.UUID{testTenantID}, m.retired)
}

// Only a row the mark finds deleted is retired: a mark refused for any
// other reason (the row closed with another status, the database gone)
// leaves the organization alone.
func TestRegisterTenantWorkflow_ErrorMarkRefusedOtherwiseLeavesTheOrg(t *testing.T) {
	t.Parallel()
	for name, markErr := range map[string]error{
		"not pending": temporal.NewNonRetryableApplicationError("tenant row is not pending", "tenant-not-pending", nil),
		"unreachable": temporal.NewNonRetryableApplicationError("db unreachable", "tenant-row-unwritable", nil),
		"untyped":     errors.New("boom"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv()
			var a registertenant.Activities
			env.OnActivity(a.SetUserAsOrganizationOwnerActivity, mock.Anything, mock.Anything).
				Return(temporal.NewNonRetryableApplicationError("owner boom", "organization-owner-rejected", nil))
			env.OnActivity(a.SetTenantStatusActivity, mock.Anything, mock.Anything).Return(markErr)
			m := newActivityMock()
			m.register(env)

			env.ExecuteWorkflow(registertenant.RegisterTenantWorkflow, defaultInput())

			require.True(t, env.IsWorkflowCompleted())
			require.ErrorContains(t, env.GetWorkflowError(), "owner boom")
			assert.Empty(t, m.retired)
		})
	}
}
