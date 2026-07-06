package restoretenant

import (
	"context"
	"fmt"

	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrZitadelOrgNotFound is the application-error type returned when a
// tenant's Zitadel org cannot be found during restore. It is registered
// as non-retryable so Temporal fails the workflow cleanly instead of
// retrying an activation that can never succeed — the org is gone and
// only the SSOT-aligning reconcile/sync can resolve the divergence.
const ErrZitadelOrgNotFound = "ZitadelOrgNotFound"

// Activities groups the side-effect activities for the restore workflow.
type Activities struct {
	zitadelConn *zitadelsdk.Connection
}

// NewActivities returns a new Activities instance with the Zitadel gRPC
// connection injected for org lifecycle operations.
func NewActivities(zitadelConn *zitadelsdk.Connection) *Activities {
	return &Activities{
		zitadelConn: zitadelConn,
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
				ErrZitadelOrgNotFound,
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
