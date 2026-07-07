package disabletenant

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var activities Activities

// DisableTenantWorkflow orchestrates the side effects of disabling a
// tenant: deactivating the Zitadel organization, (future) stopping K8s
// worker deployments, (future) publishing the revocation notification.
//
// The tenant row has already been marked soft-deleted (deleted_at set)
// by the GraphQL resolver before this workflow runs. The workflow does
// NOT touch the DB — its only job is the Zitadel side-effect.
func DisableTenantWorkflow(ctx workflow.Context, input DisableTenantWorkflowInput) (*DisableTenantWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("DisableTenantWorkflow: started",
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

	logger.Info("DisableTenantWorkflow: invoking DeactivateZitadelOrgActivity",
		"tenant_id", input.TenantID,
	)

	err := workflow.ExecuteActivity(ctx,
		activities.DeactivateZitadelOrgActivity,
		DeactivateZitadelOrgActivityInput(input),
	).Get(ctx, nil)
	if err != nil {
		logger.Error("DisableTenantWorkflow: DeactivateZitadelOrgActivity failed",
			"tenant_id", input.TenantID,
			"err", err,
		)
		return nil, err
	}

	logger.Info("DisableTenantWorkflow: completed successfully",
		"tenant_id", input.TenantID,
	)
	return &DisableTenantWorkflowOutput{TenantID: input.TenantID, Success: true}, nil
}
