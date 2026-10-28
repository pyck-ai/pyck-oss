package restoretenant

import (
	"context"
	"errors"
	"fmt"

	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// Temporal application-error types the restore activities raise as
// non-retryable: retrying cannot change any of these.
const (
	// AppErrorTypeZitadelOrgNotFound: the tenant's Zitadel org is gone; only
	// the SSOT-aligning reconcile/sync can resolve the divergence.
	AppErrorTypeZitadelOrgNotFound = "ZitadelOrgNotFound"
	// AppErrorTypeWorkerDeploymentMissing: a pyck-go tenant's worker
	// deployment no longer exists. Restore never re-provisions, so it fails
	// instead of claiming a running worker.
	AppErrorTypeWorkerDeploymentMissing = "WorkerDeploymentMissing"
	// AppErrorTypeWorkerDeploymentDeleting: the deployment is being deleted
	// and cannot be scaled back.
	AppErrorTypeWorkerDeploymentDeleting = "WorkerDeploymentDeleting"

	// SkipTenantDisabled is recorded in ResumeTenantWorkerActivityOutput.Skipped
	// when the tenant is disabled again: a disable overtook this workflow and
	// its stop owns the worker now.
	SkipTenantDisabled = "tenant-disabled"
)

// ErrWorkerNotResumed is returned when worker-api reports the deployment at
// zero replicas after the resume.
var ErrWorkerNotResumed = errors.New("worker deployment not resumed")

// Activities groups the side-effect activities for the restore workflow.
type Activities struct {
	db              *ent.Client
	zitadelConn     *zitadelsdk.Connection
	workerAPI       *workerapi.Client
	nsGetter        commonworkflow.NamespaceGetter
	workerLifecycle bool
}

// NewActivities returns a new Activities instance.
func NewActivities(db *ent.Client, zitadelConn *zitadelsdk.Connection, workerAPI *workerapi.Client, nsGetter commonworkflow.NamespaceGetter, workerLifecycle bool) *Activities {
	return &Activities{
		db:              db,
		zitadelConn:     zitadelConn,
		workerAPI:       workerAPI,
		nsGetter:        nsGetter,
		workerLifecycle: workerLifecycle,
	}
}

// ActivateZitadelOrgActivity reactivates a previously disabled tenant's
// Zitadel organization so logins and refresh-token grants start working again.
// Idempotent: returns nil if the org is already active.
func (a *Activities) ActivateZitadelOrgActivity(ctx context.Context, input ActivateZitadelOrgActivityInput) error {
	logger := activity.GetLogger(ctx)
	logger.Info("ActivateZitadelOrgActivity: activating Zitadel org",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)

	orgClient := org_pb.NewOrganizationServiceClient(a.zitadelConn)
	_, err := orgClient.ActivateOrganization(ctx, &org_pb.ActivateOrganizationRequest{
		OrganizationId: input.IdpOrgRef,
	})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			logger.Info("ActivateZitadelOrgActivity: org already active",
				"tenant_id", input.TenantID,
				"idp_org_ref", input.IdpOrgRef,
			)
			return nil
		}
		if status.Code(err) == codes.NotFound {
			// The org no longer exists in Zitadel, so there is nothing to reactivate.
			logger.Error("ActivateZitadelOrgActivity: org no longer exists, cannot restore",
				"tenant_id", input.TenantID,
				"idp_org_ref", input.IdpOrgRef,
			)
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("activate zitadel org %s: org no longer exists", input.IdpOrgRef),
				AppErrorTypeZitadelOrgNotFound,
				err,
			)
		}
		return fmt.Errorf("activate zitadel org %s: %w", input.IdpOrgRef, err)
	}

	logger.Info("ActivateZitadelOrgActivity: org activated",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)
	return nil
}

// ResumeTenantWorkerActivity scales a pyck-go tenant's worker back to its
// deployed size through worker-api: the same deployment, connection,
// secrets and service user as before the disable. worker-api verifies the
// scale before answering 200; anything else fails the activity. A
// deployment with no recorded size comes back at worker-api's default with
// a warning; a missing or deleting deployment fails non-retryably.
func (a *Activities) ResumeTenantWorkerActivity(ctx context.Context, input ResumeTenantWorkerActivityInput) (*ResumeTenantWorkerActivityOutput, error) {
	logger := activity.GetLogger(ctx)

	if !a.workerLifecycle {
		logger.Info("ResumeTenantWorkerActivity: tenant worker lifecycle is switched off, skipping",
			"tenant_id", input.TenantID,
		)
		return &ResumeTenantWorkerActivityOutput{Skipped: registertenant.SkipLifecycleDisabled}, nil
	}

	tw, ok, err := registertenant.LookupTenantWorker(ctx, a.db, a.nsGetter, input.TenantID)
	if errors.Is(err, registertenant.ErrTenantNotFound) {
		return nil, temporal.NewNonRetryableApplicationError(
			"resume worker", registertenant.AppErrorTypeTenantNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		logger.Info("ResumeTenantWorkerActivity: tenant has no platform worker, nothing to resume",
			"tenant_id", input.TenantID,
		)
		return &ResumeTenantWorkerActivityOutput{Skipped: registertenant.SkipNotPyckGo}, nil
	}
	if tw.Disabled {
		logger.Warn("ResumeTenantWorkerActivity: tenant is disabled again, leaving the worker to the disable",
			"tenant_id", input.TenantID,
			"deployment", tw.Deployment,
		)
		return &ResumeTenantWorkerActivityOutput{Skipped: SkipTenantDisabled}, nil
	}

	res, err := a.workerAPI.ResumeDeployment(ctx, tw.Deployment)
	if err != nil {
		switch {
		case errors.Is(err, workerapi.ErrNotFound):
			logger.Error("ResumeTenantWorkerActivity: pyck-go tenant has no worker deployment; restore cannot bring it back, it must be re-created",
				"tenant_id", input.TenantID,
				"deployment", tw.Deployment,
				"url", a.workerAPI.DeploymentURL(tw.Deployment),
			)
			return nil, temporal.NewNonRetryableApplicationError(
				"resume worker deployment "+tw.Deployment+": deployment does not exist",
				AppErrorTypeWorkerDeploymentMissing, err)
		case errors.Is(err, workerapi.ErrTerminating):
			logger.Error("ResumeTenantWorkerActivity: worker deployment is being deleted; restore cannot bring it back",
				"tenant_id", input.TenantID,
				"deployment", tw.Deployment,
			)
			return nil, temporal.NewNonRetryableApplicationError(
				"resume worker deployment "+tw.Deployment+": deployment is being deleted",
				AppErrorTypeWorkerDeploymentDeleting, err)
		case errors.Is(err, workerapi.ErrNotConfigured):
			return nil, temporal.NewNonRetryableApplicationError(
				"resume worker deployment "+tw.Deployment, workerapi.AppErrorTypeNotConfigured, err)
		case workerapi.IsPermanent(err):
			return nil, temporal.NewNonRetryableApplicationError(
				"resume worker deployment "+tw.Deployment, workerapi.AppErrorTypeRejected, err)
		default:
			return nil, fmt.Errorf("resume worker deployment %s: %w", tw.Deployment, err)
		}
	}

	// Defence in depth: worker-api's k8s layer reads the patched object
	// back before the handler answers 200 with the count it scaled to.
	if res.Replicas <= 0 {
		return nil, fmt.Errorf("%w: %s reports %d replicas after resume", ErrWorkerNotResumed, tw.Deployment, res.Replicas)
	}

	if res.ReplicasDefaulted {
		logger.Warn("ResumeTenantWorkerActivity: deployment has no recorded size, worker resumed at worker-api's default",
			"tenant_id", input.TenantID,
			"deployment", tw.Deployment,
			"replicas", res.Replicas,
		)
	} else {
		logger.Info("ResumeTenantWorkerActivity: worker deployment resumed",
			"tenant_id", input.TenantID,
			"deployment", tw.Deployment,
			"replicas", res.Replicas,
		)
	}

	return &ResumeTenantWorkerActivityOutput{
		Replicas:          res.Replicas,
		ReplicasDefaulted: res.ReplicasDefaulted,
	}, nil
}
