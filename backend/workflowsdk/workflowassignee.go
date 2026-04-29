package workflowsdk

import (
	"fmt"

	"go.temporal.io/sdk/workflow"

	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

type WorkflowAssigneeUpdater struct {
	WorkflowUpdate[common_workflow.WorkflowAssigneeUpdaterInput, WorkflowAssigneeUpdaterContext]
}

type WorkflowAssigneeUpdaterContext *struct{} // No context needed for assignee update

// Type returns the update handler identity. The ID is deliberately the
// default (`"WorkflowAssigneeUpdater"`, derived from the struct name) so
// it does NOT collide with `SetAssignee` — the persistent update handler
// that SetupDefaults registers for workflows implementing
// WorkflowAssigneeSetter. Both entrypoints can coexist on the same
// workflow: Updater awaits a value interactively, SetupDefaults exposes
// a resolver-driven setter for the whole workflow lifetime.
func (u *WorkflowAssigneeUpdater) Type() *common_workflow.WorkflowUpdateType {
	return u.DefaultType(u)
}

func (u *WorkflowAssigneeUpdater) Await(ctx workflow.Context, input *common_workflow.UserDataInput, ref WorkflowAssigneeUpdaterContext) error {
	return u.DefaultAwait(ctx, u, input, ref)
}

func (u *WorkflowAssigneeUpdater) Update(ctx workflow.Context, input *common_workflow.UserDataInput, ref WorkflowAssigneeUpdaterContext, value common_workflow.WorkflowAssigneeUpdaterInput) (any, error) {
	if err := SetWorkflowAssignee(ctx, value.Assignee); err != nil {
		return nil, fmt.Errorf("set workflow assignee: %w", err)
	}

	return u.DefaultUpdate(ctx, u, input, value)
}

func (u *WorkflowAssigneeUpdater) Validate(ctx workflow.Context, ref WorkflowAssigneeUpdaterContext, value common_workflow.WorkflowAssigneeUpdaterInput) error {
	return u.DefaultValidate(ctx, u, ref, value)
}
