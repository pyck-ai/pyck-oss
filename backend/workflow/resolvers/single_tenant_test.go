package resolvers_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/converter"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
)

// =============================================================================
// SINGLE-TENANT GUARD ON NAMESPACE-SCOPED QUERIES
// =============================================================================

var (
	getWorkflowTargets = resolver.ParseTemplate(`query {
		workflowTargets(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}"
		}) {
			targets
		}
	}`)

	getWorkflowIsAssignable = resolver.ParseTemplate(`query {
		workflowIsAssignable(input: {
			workflowId: "{{.WorkflowID}}",
			workflowExecutionId: "{{.WorkflowRunID}}"
		}) {
			isAssignable
		}
	}`)

	getCurrentUserDataInput = resolver.ParseTemplate(`query {
		currentUserDataInput(input: {
			workflowID: "{{.WorkflowID}}",
			workflowExecutionID: "{{.WorkflowRunID}}"
		}) {
			activityIndex
		}
	}`)
)

var errQueryUnavailable = errors.New("query unavailable")

var userAB = &authn.User{
	ID:       uuid.MustParse("5c2c6a2e-6f6d-4a5e-9b0e-3f1c1a4d7b01"),
	TenantID: resolver.TenantA,
	Roles: map[uuid.UUID]authn.Role{
		resolver.TenantA: authn.ROLE_ADMIN,
		resolver.TenantB: authn.ROLE_ADMIN,
	},
}

// ctxWithTenants is te.ctx with an explicit, possibly empty, tenant selection.
func (te *testEnv) ctxWithTenants(user *authn.User, tenantIDs ...uuid.UUID) context.Context {
	te.t.Helper()
	return request.Context(te.t.Context(), user, tenantIDs...)
}

func TestNamespaceScopedQueries_RequireSingleTenant(t *testing.T) {
	t.Parallel()

	args := map[string]any{
		"WorkflowID":    "wf-single-tenant",
		"WorkflowRunID": "run-single-tenant",
		"RunID":         "run-single-tenant",
		"First":         10,
	}

	// guardOnly queries need dependencies this environment does not wire, so
	// only their rejection is checked here; remoteui.resolvers_test.go
	// covers their single-tenant path.
	queries := []struct {
		name      string
		tpl       resolver.Template
		guardOnly bool
	}{
		{"workflowTargets", getWorkflowTargets, false},
		{"workflowActions", getWorkflowActions, false},
		{"workflowIsAssignable", getWorkflowIsAssignable, false},
		{"workflowAssignee", workflowAssignee, false},
		{"currentUserDataInput", getCurrentUserDataInput, false},
		{"remoteUI", remoteUIQuery, true},
		{"workerDeploymentUIBundles", workerDeploymentUIBundlesQuery, true},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			t.Parallel()

			t.Run("zero tenants returns clean error", func(t *testing.T) {
				t.Parallel()
				te := setupWithMockWorkflow(t)
				defer te.Close(t)

				execErr(te, te.ctxWithTenants(userNoRole), q.tpl, args, request.ErrSingleTenantRequired.Error())
			})

			t.Run("multiple tenants returns clean error", func(t *testing.T) {
				t.Parallel()
				te := setupWithMockWorkflow(t)
				defer te.Close(t)

				execErr(te, te.ctxWithTenants(userAB, tenantA, tenantB), q.tpl, args, request.ErrSingleTenantRequired.Error())
			})

			if q.guardOnly {
				return
			}

			t.Run("single tenant passes the guard", func(t *testing.T) {
				t.Parallel()
				te := setupWithMockWorkflow(t)
				defer te.Close(t)
				te.MockTemporalClient.QueryWorkflowFunc = func(context.Context, string, string, string, ...any) (converter.EncodedValue, error) {
					return nil, errQueryUnavailable
				}

				// Any downstream failure is fine; only the tenant guard must not trip.
				result := resolver.Exec[json.RawMessage, *ent.Client](te.TestEnvironment, te.ctxWithTenants(userAB, tenantA), q.tpl, args)
				for _, gqlErr := range result.Errors {
					assert.NotContains(t, gqlErr.Message, request.ErrSingleTenantRequired.Error())
				}
			})
		})
	}
}
