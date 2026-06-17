package restoretenant

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var activities Activities

// RestoreTenantWorkflow orchestrates the side effects of restoring a
// previously disabled tenant: reactivating the Zitadel organization,
// (future) scaling K8s workers back up.
//
// The tenant row has already had deleted_at cleared by the GraphQL
// resolver before this workflow runs. The workflow does NOT touch the
// DB — its only job is the Zitadel side-effect.
func RestoreTenantWorkflow(ctx workflow.Context, input RestoreTenantWorkflowInput) (*RestoreTenantWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("RestoreTenantWorkflow: started",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    3,
		},
	})

	logger.Info("RestoreTenantWorkflow: invoking ActivateZitadelOrgActivity",
		"tenant_id", input.TenantID,
	)

	err := workflow.ExecuteActivity(ctx,
		activities.ActivateZitadelOrgActivity,
		ActivateZitadelOrgActivityInput(input),
	).Get(ctx, nil)
	if err != nil {
		logger.Error("RestoreTenantWorkflow: ActivateZitadelOrgActivity failed",
			"tenant_id", input.TenantID,
			"err", err,
		)
		return nil, err
	}

	logger.Info("RestoreTenantWorkflow: completed successfully",
		"tenant_id", input.TenantID,
	)
	return &RestoreTenantWorkflowOutput{TenantID: input.TenantID, Success: true}, nil
}
