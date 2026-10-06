//go:build !skiphooks

package schema

import (
	"context"
	"errors"
	"fmt"

	entgo "entgo.io/ent"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	enthook "github.com/pyck-ai/pyck/backend/workflow/ent/gen/hook"
	entworkflow "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflow"
)

// ErrWorkflowNotInTenant refuses a signal whose workflow belongs to another tenant.
var ErrWorkflowNotInTenant = errors.New("workflow not found in tenant")

var errMissingMutationTenant = errors.New("workflow signal mutation has no tenant")

// Hooks checks workflow_id against the signal's tenant: the single-column FK
// only proves the workflow exists. Runs after TenantMixin's hook, which stamps
// tenant_id.
func (WorkflowSignal) Hooks() []entgo.Hook {
	return []entgo.Hook{
		enthook.On(func(next entgo.Mutator) entgo.Mutator {
			return enthook.WorkflowSignalFunc(func(ctx context.Context, m *ent.WorkflowSignalMutation) (ent.Value, error) {
				if err := checkWorkflowInTenant(ctx, m); err != nil {
					return nil, err
				}
				return next.Mutate(ctx, m)
			})
		}, entgo.OpCreate|entgo.OpUpdate|entgo.OpUpdateOne),
	}
}

func checkWorkflowInTenant(ctx context.Context, m *ent.WorkflowSignalMutation) error {
	workflowID, ok := m.WorkflowID()
	if !ok {
		return nil
	}

	tenantID, ok := m.TenantID()
	if !ok {
		return errMissingMutationTenant
	}

	// Explicit tenant predicate: the system user bypasses the privacy filter.
	exists, err := m.Client().Workflow.Query().
		Where(entworkflow.ID(workflowID), entworkflow.TenantID(tenantID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("check workflow %q tenant: %w", workflowID, err)
	}
	if !exists {
		return fmt.Errorf("%w %q", ErrWorkflowNotInTenant, workflowID)
	}

	return nil
}
