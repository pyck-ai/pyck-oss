package disabletenant

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

// Reasons recorded in StopTenantWorkerActivityOutput.Skipped besides
// registertenant.SkipNotPyckGo (all three mean nothing needs stopping), and
// the error type of a Zitadel refusal.
const (
	// SkipNoDeployment: the deployment does not exist, so nothing runs.
	SkipNoDeployment = "no-deployment"
	// SkipDeleting: the deployment is being deleted; the delete already
	// stops its workers.
	SkipDeleting = "deployment-deleting"
	// SkipTenantRestored: the tenant is no longer disabled, a restore
	// overtook this workflow; its resume owns the worker now.
	SkipTenantRestored = "tenant-restored"

	// appErrTypeOrgDeactivateRejected is the application-error type under
	// which a deactivation Zitadel refuses on its own terms (InvalidArgument,
	// PermissionDenied) fails the workflow: non-retryable, so the run ends on
	// its first attempt with Zitadel's reason instead of repeating the same
	// answer for the whole retry budget.
	appErrTypeOrgDeactivateRejected = "org-deactivate-rejected"
)

// ErrWorkerNotStopped is returned when worker-api reports the deployment
// above zero replicas after the stop.
var ErrWorkerNotStopped = errors.New("worker deployment not stopped")

// Activities groups the side-effect activities for the disable workflow.
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

// DeactivateZitadelOrgActivity deactivates the tenant's Zitadel
// organization so new logins and refresh-token grants are blocked.
//
// Idempotent: an org that is already INACTIVE (FailedPrecondition) or gone
// (NotFound, e.g. removed by the org-cleanup job or a dev reset) already
// satisfies the goal and is success; the tenant row is soft-deleted by the
// resolver before this runs. Those two are settled before classification,
// as commonworkflow.Classify requires; every other answer is classified there:
// a refusal fails with appErrTypeOrgDeactivateRejected, a fault is left to
// the retry policy.
func (a *Activities) DeactivateZitadelOrgActivity(ctx context.Context, input DeactivateZitadelOrgActivityInput) (err error) {
	defer func() { err = commonworkflow.Classify(ctx, appErrTypeOrgDeactivateRejected, err) }()

	logger := activity.GetLogger(ctx)
	logger.Info("DeactivateZitadelOrgActivity: deactivating Zitadel org",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)

	orgClient := org_pb.NewOrganizationServiceClient(a.zitadelConn)
	_, err = orgClient.DeactivateOrganization(ctx, &org_pb.DeactivateOrganizationRequest{
		OrganizationId: input.IdpOrgRef,
	})
	switch status.Code(err) {
	case codes.OK:
		logger.Info("DeactivateZitadelOrgActivity: org deactivated",
			"tenant_id", input.TenantID,
			"idp_org_ref", input.IdpOrgRef,
		)
		return nil
	case codes.FailedPrecondition:
		logger.Info("DeactivateZitadelOrgActivity: org already deactivated",
			"tenant_id", input.TenantID,
			"idp_org_ref", input.IdpOrgRef,
		)
		return nil
	case codes.NotFound:
		// Info, not Warn: an expected no-op during org cleanup / dev reset,
		// logged the way the tenant-reconcile path logs the same condition.
		logger.Info("DeactivateZitadelOrgActivity: org no longer exists, treating disable as complete",
			"tenant_id", input.TenantID,
			"idp_org_ref", input.IdpOrgRef,
		)
		return nil
	default:
		return fmt.Errorf("deactivate zitadel org %s: %w", input.IdpOrgRef, err)
	}
}

// StopTenantWorkerActivity scales a pyck-go tenant's worker to zero through
// worker-api. The deployment, its connection, secrets and service user
// survive, so restore resumes the same worker. worker-api verifies the
// scale before answering 200; anything else fails the activity.
func (a *Activities) StopTenantWorkerActivity(ctx context.Context, input StopTenantWorkerActivityInput) (*StopTenantWorkerActivityOutput, error) {
	logger := activity.GetLogger(ctx)

	if !a.workerLifecycle {
		logger.Info("StopTenantWorkerActivity: tenant worker lifecycle is switched off, skipping",
			"tenant_id", input.TenantID,
		)
		return &StopTenantWorkerActivityOutput{Skipped: registertenant.SkipLifecycleDisabled}, nil
	}

	tw, ok, err := registertenant.LookupTenantWorker(ctx, a.db, a.nsGetter, input.TenantID)
	if errors.Is(err, registertenant.ErrTenantNotFound) {
		return nil, temporal.NewNonRetryableApplicationError(
			"stop worker", registertenant.AppErrorTypeTenantNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		logger.Info("StopTenantWorkerActivity: tenant has no platform worker, nothing to stop",
			"tenant_id", input.TenantID,
		)
		return &StopTenantWorkerActivityOutput{Skipped: registertenant.SkipNotPyckGo}, nil
	}
	if !tw.Disabled {
		logger.Warn("StopTenantWorkerActivity: tenant is no longer disabled, leaving the worker to the restore",
			"tenant_id", input.TenantID,
			"deployment", tw.Deployment,
		)
		return &StopTenantWorkerActivityOutput{Skipped: SkipTenantRestored}, nil
	}

	res, err := a.workerAPI.StopDeployment(ctx, tw.Deployment)
	if err != nil {
		switch {
		case errors.Is(err, workerapi.ErrNotFound):
			logger.Warn("StopTenantWorkerActivity: pyck-go tenant has no worker deployment; nothing runs, continuing",
				"tenant_id", input.TenantID,
				"deployment", tw.Deployment,
				"url", a.workerAPI.DeploymentURL(tw.Deployment),
			)
			return &StopTenantWorkerActivityOutput{Skipped: SkipNoDeployment}, nil
		case errors.Is(err, workerapi.ErrTerminating):
			logger.Warn("StopTenantWorkerActivity: worker deployment is being deleted; its workers stop with it",
				"tenant_id", input.TenantID,
				"deployment", tw.Deployment,
			)
			return &StopTenantWorkerActivityOutput{Skipped: SkipDeleting}, nil
		case errors.Is(err, workerapi.ErrNotConfigured):
			return nil, temporal.NewNonRetryableApplicationError(
				"stop worker deployment "+tw.Deployment, workerapi.AppErrorTypeNotConfigured, err)
		case workerapi.IsPermanent(err):
			return nil, temporal.NewNonRetryableApplicationError(
				"stop worker deployment "+tw.Deployment, workerapi.AppErrorTypeRejected, err)
		default:
			return nil, fmt.Errorf("stop worker deployment %s: %w", tw.Deployment, err)
		}
	}

	// Defence in depth: worker-api's k8s layer reads the patched object
	// back before the handler answers 200 with a literal 0.
	if res.Replicas != 0 {
		return nil, fmt.Errorf("%w: %s reports %d replicas after stop", ErrWorkerNotStopped, tw.Deployment, res.Replicas)
	}

	logger.Info("StopTenantWorkerActivity: worker deployment stopped",
		"tenant_id", input.TenantID,
		"deployment", tw.Deployment,
	)
	return &StopTenantWorkerActivityOutput{}, nil
}
