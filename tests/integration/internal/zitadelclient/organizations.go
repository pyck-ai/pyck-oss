package zitadelclient

import (
	"context"
	"fmt"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DeactivateOrg flips a Zitadel organization to ORG_STATE_INACTIVE
// directly via the v2 OrganizationService — sidestepping pyck's
// management.deleteTenant mutation so a test can isolate "did the
// org-active check on the auth path catch this?" from "did pyck's
// soft-delete propagate?". Idempotent: returns nil if the org is
// already inactive.
func DeactivateOrg(ctx context.Context, conn *zitadel.Connection, orgID string) error {
	c := org_pb.NewOrganizationServiceClient(conn)
	_, err := c.DeactivateOrganization(ctx, &org_pb.DeactivateOrganizationRequest{OrganizationId: orgID})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return nil
		}
		return fmt.Errorf("deactivate org %s: %w", orgID, err)
	}
	return nil
}

// ActivateOrg flips a Zitadel organization back to ORG_STATE_ACTIVE.
// Idempotent: returns nil if the org is already active.
func ActivateOrg(ctx context.Context, conn *zitadel.Connection, orgID string) error {
	c := org_pb.NewOrganizationServiceClient(conn)
	_, err := c.ActivateOrganization(ctx, &org_pb.ActivateOrganizationRequest{OrganizationId: orgID})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return nil
		}
		return fmt.Errorf("activate org %s: %w", orgID, err)
	}
	return nil
}

// OrgState returns the lifecycle state of the Zitadel org as a normalised
// string ("ACTIVE", "INACTIVE", "REMOVED"), or "" if the org is not found.
// It reads the same eventually-consistent ListOrganizations projection the
// org-active validator consults, so it doubles as a barrier: once OrgState
// reports a state, the validator's next probe sees at least that state.
func OrgState(ctx context.Context, conn *zitadel.Connection, orgID string) (string, error) {
	c := org_pb.NewOrganizationServiceClient(conn)
	resp, err := c.ListOrganizations(ctx, &org_pb.ListOrganizationsRequest{
		Queries: []*org_pb.SearchQuery{{
			Query: &org_pb.SearchQuery_IdQuery{
				IdQuery: &org_pb.OrganizationIDQuery{Id: orgID},
			},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("listOrganizations: %w", err)
	}
	if len(resp.GetResult()) == 0 {
		return "", nil
	}
	switch resp.GetResult()[0].GetState() {
	case org_pb.OrganizationState_ORGANIZATION_STATE_ACTIVE:
		return "ACTIVE", nil
	case org_pb.OrganizationState_ORGANIZATION_STATE_INACTIVE:
		return "INACTIVE", nil
	case org_pb.OrganizationState_ORGANIZATION_STATE_REMOVED:
		return "REMOVED", nil
	default:
		return "UNSPECIFIED", nil
	}
}

// WaitForOrgState polls Zitadel until the org reaches want, or timeout.
func WaitForOrgState(ctx context.Context, conn *zitadel.Connection, orgID, want string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	var last string
	for {
		state, err := OrgState(ctx, conn, orgID)
		if err == nil && state == want {
			return time.Since(start), nil
		}
		last = state
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout: org state still %q after %s, wanted %q", last, timeout, want)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// DeleteOrg removes a Zitadel organization outright via the v2
// OrganizationService — simulating the cleanup job / dev-reset that
// removes an org out of band while pyck still holds a tenant row for it.
// Used to exercise the "org no longer exists" paths in the disable,
// restore, and reconcile workflows. Idempotent: returns nil if the org is
// already gone.
func DeleteOrg(ctx context.Context, conn *zitadel.Connection, orgID string) error {
	c := org_pb.NewOrganizationServiceClient(conn)
	_, err := c.DeleteOrganization(ctx, &org_pb.DeleteOrganizationRequest{OrganizationId: orgID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("delete org %s: %w", orgID, err)
	}
	return nil
}
