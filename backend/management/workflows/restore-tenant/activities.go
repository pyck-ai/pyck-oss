package restoretenant

import (
	"context"
	"fmt"

	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"go.temporal.io/sdk/activity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
		return fmt.Errorf("activate zitadel org %s: %w", input.IdpOrgRef, err)
	}

	logger.Info("ActivateZitadelOrgActivity: org activated",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)
	return nil
}
