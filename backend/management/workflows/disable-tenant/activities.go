package disabletenant

import (
	"context"
	"fmt"

	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"go.temporal.io/sdk/activity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Activities groups the side-effect activities for the disable workflow.
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

// DeactivateZitadelOrgActivity deactivates the tenant's Zitadel
// organization so new logins and refresh-token grants are blocked.
// Idempotent: returns nil if the org is already deactivated.
func (a *Activities) DeactivateZitadelOrgActivity(ctx context.Context, input DeactivateZitadelOrgActivityInput) error {
	logger := activity.GetLogger(ctx)
	logger.Info("DeactivateZitadelOrgActivity: deactivating Zitadel org",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)

	orgClient := org_pb.NewOrganizationServiceClient(a.zitadelConn)
	_, err := orgClient.DeactivateOrganization(ctx, &org_pb.DeactivateOrganizationRequest{
		OrganizationId: input.IdpOrgRef,
	})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			logger.Info("DeactivateZitadelOrgActivity: org already deactivated",
				"tenant_id", input.TenantID,
				"idp_org_ref", input.IdpOrgRef,
			)
			return nil
		}
		return fmt.Errorf("deactivate zitadel org %s: %w", input.IdpOrgRef, err)
	}

	logger.Info("DeactivateZitadelOrgActivity: org deactivated",
		"tenant_id", input.TenantID,
		"idp_org_ref", input.IdpOrgRef,
	)
	return nil
}
