package resolvers_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

var (
	userReader = &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000a001"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER},
	}
	userWriter = &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000a002"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_WRITER},
	}
	userWriterInB = &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000a003"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{tenantA: authn.ROLE_READER, tenantB: authn.ROLE_WRITER},
	}

	ensureTemporalNamespace = resolver.ParseTemplate(`mutation {
		ensureTemporalNamespace
	}`)

	setWorkflowTargets = resolver.ParseTemplate(`mutation {
		setWorkflowTargets(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}",
			targets: []
		}) {
			targets
		}
	}`)

	setWorkflowIsAssignable = resolver.ParseTemplate(`mutation {
		setWorkflowIsAssignable(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}",
			isAssignable: false
		}) {
			isAssignable
		}
	}`)

	submitUserDataInput = resolver.ParseTemplate(`mutation {
		submitUserDataInput(input: {
			name: "input",
			data: {},
			workflowID: "{{.WorkflowID}}",
			workflowRunID: "{{.WorkflowRunID}}"
		}) {
			result
		}
	}`)
)

func TestTemporalMutations_RequireWriter(t *testing.T) {
	t.Parallel()

	vars := map[string]any{
		"WorkflowID":    "test-workflow",
		"WorkflowRunID": "test-run",
		"AssigneeID":    uuid.New(),
	}

	mutations := map[string]resolver.Template{
		"ensureTemporalNamespace": ensureTemporalNamespace,
		"setWorkflowAssignee":     setAssignee,
		"setWorkflowTargets":      setWorkflowTargets,
		"setWorkflowIsAssignable": setWorkflowIsAssignable,
		"submitUserDataInput":     submitUserDataInput,
		"cancelWorkflow":          cancelWorkflow,
	}

	refused := []struct {
		name    string
		user    *authn.User
		tenants []uuid.UUID
		wantErr string
	}{
		{"reader", userReader, []uuid.UUID{tenantA}, "writer role required"},
		{"writer in another tenant", userWriterInB, []uuid.UUID{tenantA}, "writer role required"},
		{"two tenants", userAB, []uuid.UUID{tenantA, tenantB}, "a single tenant must be selected"},
	}

	allowed := []struct {
		name string
		user *authn.User
	}{
		{"writer", userWriter},
		{"admin", userA},
	}

	for name, tpl := range mutations {
		for _, tc := range refused {
			t.Run(name+" refuses "+tc.name, func(t *testing.T) {
				t.Parallel()
				te := setupWithMockWorkflow(t)
				defer te.Close(t)

				execErr(te, te.ctxWithTenants(tc.user, tc.tenants...), tpl, vars, tc.wantErr)
			})
		}

		for _, tc := range allowed {
			t.Run(name+" allows "+tc.name, func(t *testing.T) {
				t.Parallel()
				te := setupWithMockWorkflow(t)
				defer te.Close(t)

				execOK[map[string]any](te, te.ctx(tc.user), tpl, vars)
			})
		}
	}

	t.Run("reader cannot change the assignee", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)

		assignee := uuid.New()
		execOK[setAssigneeData](te, te.ctx(userWriter), setAssignee, map[string]any{
			"WorkflowID":    "test-workflow",
			"WorkflowRunID": "test-run",
			"AssigneeID":    assignee,
		})

		execErr(te, te.ctx(userReader), setAssignee, map[string]any{
			"WorkflowID":    "test-workflow",
			"WorkflowRunID": "test-run",
			"AssigneeID":    uuid.New(),
		}, "writer role required")

		desc, err := te.MockTemporalClient.DescribeWorkflowExecution(t.Context(), "test-workflow", "test-run")
		require.NoError(t, err)
		stored := desc.GetWorkflowExecutionInfo().GetSearchAttributes().GetIndexedFields()["pyck_workflow_assignee"]
		assert.JSONEq(t, `"`+assignee.String()+`"`, string(stored.GetData()))
	})

	t.Run("reader cannot cancel a workflow", func(t *testing.T) {
		t.Parallel()
		te := setupWithMockWorkflow(t)
		defer te.Close(t)

		var cancelled atomic.Bool
		te.MockTemporalClient.CancelWorkflowFunc = func(context.Context, string, string) error {
			cancelled.Store(true)
			return nil
		}

		execErr(te, te.ctx(userReader), cancelWorkflow, vars, "writer role required")
		assert.False(t, cancelled.Load())

		execOK[cancelWorkflowData](te, te.ctx(userWriter), cancelWorkflow, vars)
		assert.True(t, cancelled.Load())
	})
}

// unregisterWorker and registerWorkflow are the mutations workers call. They
// must refuse a request without a single tenant with an error (MutationTenantID
// panics, and its log includes the caller's token), and unregisterWorker needs
// the WRITER role: a reader could otherwise drop a tenant's subscriptions.
func TestWorkerMutations_TenantAndRoleGuards(t *testing.T) {
	t.Parallel()

	registerVars := map[string]any{
		"Name":       "wf_authz",
		"TaskQueue":  "test-queue",
		"DataTypeID": itemDataTypeID,
		"DataName":   "testWorkflow",
		"DataWeight": 0,
		"WorkerID":   "worker-a",
	}
	unregisterVars := map[string]any{"WorkerID": "worker-a"}

	// The tenant middleware defaults a missing header to "all of the user's
	// tenants", so a request ends up with no tenant only for a user without one.
	userNoTenant := &authn.User{
		ID:       uuid.MustParse("0198a6b2-0000-7000-8000-00000000a004"),
		TenantID: tenantA,
		Roles:    map[uuid.UUID]authn.Role{},
	}

	cases := []struct {
		name    string
		tpl     resolver.Template
		vars    map[string]any
		user    *authn.User
		tenants []uuid.UUID
		wantErr string
	}{
		{"unregisterWorker refuses a reader", unregisterWorker, unregisterVars, userReader, []uuid.UUID{tenantA}, "writer role required"},
		{"unregisterWorker refuses a writer in another tenant", unregisterWorker, unregisterVars, userWriterInB, []uuid.UUID{tenantA}, "writer role required"},
		{"unregisterWorker refuses two tenants", unregisterWorker, unregisterVars, userAB, []uuid.UUID{tenantA, tenantB}, "a single tenant must be selected"},
		{"unregisterWorker refuses no tenant", unregisterWorker, unregisterVars, userNoTenant, nil, "a single tenant must be selected"},
		{"registerWorkflow refuses two tenants", registerWorkflow, registerVars, userAB, []uuid.UUID{tenantA, tenantB}, "a single tenant must be selected"},
		{"registerWorkflow refuses no tenant", registerWorkflow, registerVars, userNoTenant, nil, "a single tenant must be selected"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)

			assert.NotPanics(t, func() {
				execErr(te, te.ctxWithTenants(tc.user, tc.tenants...), tc.tpl, tc.vars, tc.wantErr)
			})
		})
	}

	t.Run("unregisterWorker allows a writer", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		execOK[unregisterWorkerData](te, te.ctx(userWriter), unregisterWorker, unregisterVars)
	})
}
