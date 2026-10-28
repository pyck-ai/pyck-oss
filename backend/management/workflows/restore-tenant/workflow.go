package restoretenant

import (
	"time"

	"go.temporal.io/sdk/workflow"

	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
)

var activities Activities

// RestoreTenantWorkflow orchestrates the side effects of restoring a
// previously disabled tenant: reactivating the Zitadel organization, then
// scaling the tenant's platform worker back to its deployed size. Nothing
// is re-provisioned.
//
// The tenant row has already had deleted_at cleared by the GraphQL
// resolver before this workflow runs. The workflow does NOT write to the DB.
//
// Zitadel goes first: the worker authenticates with a service user from the
// tenant's org, so the org must be active before the worker polls again.
func RestoreTenantWorkflow(ctx workflow.Context, input RestoreTenantWorkflowInput) (*RestoreTenantWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("RestoreTenantWorkflow: started",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)

	zitadelCtx := workflow.WithActivityOptions(ctx, commonwf.DefaultActivityOptions(30*time.Second))

	logger.Info("RestoreTenantWorkflow: invoking ActivateZitadelOrgActivity",
		"tenant_id", input.TenantID,
	)

	err := workflow.ExecuteActivity(zitadelCtx,
		activities.ActivateZitadelOrgActivity,
		ActivateZitadelOrgActivityInput(input),
	).Get(zitadelCtx, nil)
	if err != nil {
		logger.Error("RestoreTenantWorkflow: ActivateZitadelOrgActivity failed",
			"tenant_id", input.TenantID,
			"err", err,
		)
		return nil, err
	}

	logger.Info("RestoreTenantWorkflow: invoking ResumeTenantWorkerActivity",
		"tenant_id", input.TenantID,
	)

	workerCtx := workflow.WithActivityOptions(ctx, workerapi.ActivityOptions(core.Config.WorkerAPIRetry))

	var resume ResumeTenantWorkerActivityOutput
	err = workflow.ExecuteActivity(workerCtx,
		activities.ResumeTenantWorkerActivity,
		ResumeTenantWorkerActivityInput{TenantID: input.TenantID},
	).Get(workerCtx, &resume)
	if err != nil {
		logger.Error("RestoreTenantWorkflow: ResumeTenantWorkerActivity failed",
			"tenant_id", input.TenantID,
			"err", err,
		)
		return nil, err
	}

	logger.Info("RestoreTenantWorkflow: completed successfully",
		"tenant_id", input.TenantID,
		"worker_skipped", resume.Skipped,
		"worker_replicas", resume.Replicas,
		"worker_replicas_defaulted", resume.ReplicasDefaulted,
	)
	return &RestoreTenantWorkflowOutput{TenantID: input.TenantID, Success: true}, nil
}
