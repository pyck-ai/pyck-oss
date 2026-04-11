package workflowsdk

import (
	"fmt"

	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
	"go.temporal.io/sdk/workflow"
)

type WorkflowAssigneeUpdater struct {
	WorkflowUpdate[common_workflow.WorkflowAssigneeUpdaterInput, WorkflowAssigneeUpdaterContext]
}

type WorkflowAssigneeUpdaterContext *struct{} // No context needed for assignee update

func (u *WorkflowAssigneeUpdater) Type() *common_workflow.WorkflowUpdateType {
	typ := u.DefaultType(u)
	typ.ID = common_workflow.WorkflowQueryTypeSetAssignee.String()

	return typ
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
