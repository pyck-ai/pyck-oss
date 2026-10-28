package disabletenant

import (
	"time"

	"go.temporal.io/sdk/workflow"

	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
)

var activities Activities

// DisableTenantWorkflow orchestrates the side effects of disabling a
// tenant: deactivating the Zitadel organization, then scaling the tenant's
// platform worker to zero. (Future: publishing the revocation notification.)
//
// The tenant row has already been marked soft-deleted (deleted_at set)
// by the GraphQL resolver before this workflow runs. The workflow does
// NOT write to the DB.
//
// Zitadel goes first: blocking access must never wait on the worker cluster.
func DisableTenantWorkflow(ctx workflow.Context, input DisableTenantWorkflowInput) (*DisableTenantWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("DisableTenantWorkflow: started",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)

	zitadelCtx := workflow.WithActivityOptions(ctx, commonwf.DefaultActivityOptions(30*time.Second))

	logger.Info("DisableTenantWorkflow: invoking DeactivateZitadelOrgActivity",
		"tenant_id", input.TenantID,
	)

	err := workflow.ExecuteActivity(zitadelCtx,
		activities.DeactivateZitadelOrgActivity,
		DeactivateZitadelOrgActivityInput(input),
	).Get(zitadelCtx, nil)
	if err != nil {
		logger.Error("DisableTenantWorkflow: DeactivateZitadelOrgActivity failed",
			"tenant_id", input.TenantID,
			"err", err,
		)
		return nil, err
	}

	logger.Info("DisableTenantWorkflow: invoking StopTenantWorkerActivity",
		"tenant_id", input.TenantID,
	)

	// Once the org is deactivated the reconcile sweeper no longer
	// re-dispatches this workflow, so these retries are all the stop gets.
	workerCtx := workflow.WithActivityOptions(ctx, workerapi.ActivityOptions(core.Config.WorkerAPIRetry))

	var stop StopTenantWorkerActivityOutput
	err = workflow.ExecuteActivity(workerCtx,
		activities.StopTenantWorkerActivity,
		StopTenantWorkerActivityInput{TenantID: input.TenantID},
	).Get(workerCtx, &stop)
	if err != nil {
		logger.Error("DisableTenantWorkflow: StopTenantWorkerActivity failed",
			"tenant_id", input.TenantID,
			"err", err,
		)
		return nil, err
	}

	logger.Info("DisableTenantWorkflow: completed successfully",
		"tenant_id", input.TenantID,
		"worker_skipped", stop.Skipped,
	)
	return &DisableTenantWorkflowOutput{TenantID: input.TenantID, Success: true}, nil
}
