package workflowsdk

import (
	"fmt"

	"go.temporal.io/sdk/workflow"

	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

type WorkflowTargetsUpdater struct {
	WorkflowUpdate[common_workflow.WorkflowTargetsUpdaterInput, WorkflowTargetsUpdaterContext]
}

type WorkflowTargetsUpdaterContext *struct{} // No context needed for targets update

func (u *WorkflowTargetsUpdater) Type() *common_workflow.WorkflowUpdateType {
	typ := u.DefaultType(u)
	typ.ID = common_workflow.WorkflowQueryTypeSetTargets.String()

	return typ
}

func (u *WorkflowTargetsUpdater) Await(ctx workflow.Context, input *common_workflow.UserDataInput, ref WorkflowTargetsUpdaterContext) error {
	return u.DefaultAwait(ctx, u, input, ref)
}

func (u *WorkflowTargetsUpdater) Update(ctx workflow.Context, input *common_workflow.UserDataInput, ref WorkflowTargetsUpdaterContext, value common_workflow.WorkflowTargetsUpdaterInput) (any, error) {
	if err := SetWorkflowTargets(ctx, value.Targets); err != nil {
		return nil, fmt.Errorf("set workflow targets: %w", err)
	}

	return u.DefaultUpdate(ctx, u, input, value)
}

func (u *WorkflowTargetsUpdater) Validate(ctx workflow.Context, ref WorkflowTargetsUpdaterContext, value common_workflow.WorkflowTargetsUpdaterInput) error {
	return u.DefaultValidate(ctx, u, ref, value)
}
