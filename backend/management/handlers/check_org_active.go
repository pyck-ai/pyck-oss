package handlers

import (
	"context"
	"errors"
	"fmt"

	zitadelsdk "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	org_v2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	user_v2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

// ErrResolveOrganizationEmptySub is returned when [ResolveOrganization] is
// called without a Zitadel user ID — a caller bug, not a token-revocation
// signal. Typed so unit tests can assert it instead of string-matching.
var ErrResolveOrganizationEmptySub = errors.New("ResolveOrganization: empty sub")

// ErrResolveOrganizationNoResourceOwner is returned when GetUserByID
// succeeded but Details.ResourceOwner is empty. The chained nil-safe
// getters coalesce any intermediate nil, so surfacing as error (not
// Active=false) prevents an SDK regression or anonymized sub from
// silently revoking every healthy tenant.
var ErrResolveOrganizationNoResourceOwner = errors.New("ResolveOrganization: user has no resource_owner")

// userByIDClient is the slice of [user_v2.UserServiceClient] this package
// needs. Narrow interface so tests can inject a fake without stubbing
// the full Zitadel v2 user surface.
type userByIDClient interface {
	GetUserByID(ctx context.Context, in *user_v2.GetUserByIDRequest, opts ...grpc.CallOption) (*user_v2.GetUserByIDResponse, error)
}

// listOrgsClient is the slice of [org_v2.OrganizationServiceClient] this
// package needs. Same narrowing rationale as userByIDClient.
type listOrgsClient interface {
	ListOrganizations(ctx context.Context, in *org_v2.ListOrganizationsRequest, opts ...grpc.CallOption) (*org_v2.ListOrganizationsResponse, error)
}

// ResolveOrganization resolves whether the given Zitadel user's home org
// is ORG_STATE_ACTIVE via two v2 SDK calls over the supplied system-auth
// gRPC connection:
//
//  1. user/v2.UserService.GetUserByID(sub) → details.ResourceOwner (orgID)
//  2. org/v2.OrganizationService.ListOrganizations(IdQuery=orgID) → org.State
//
// The connection is the same *zitadelsdk.Connection management uses for
// tenant-lifecycle workflows — its baked-in JWT-profile auth (sa-admin
// service account) is exactly what the v2 surfaces need.
//
// Return shape — boolean-only on the normal path, error for both caller
// bugs and "our side" gRPC faults:
//
//   - (Active=true,  nil) — user exists, owning org is ACTIVE.
//   - (Active=false, nil) — user not found / no resource_owner /
//     no matching org / org INACTIVE / org REMOVED. Each is a
//     definitive answer about upstream state.
//   - (nil, err)          — caller bug (empty sub), our own RBAC
//     denial (sa-admin permission scope), transport / 5xx / decode
//     fault. The caller treats this as an unknown state — not a
//     revocation — and stays with any cached decision (see
//     authn.rejectOrgActive).
//
// Particular care is taken to NOT map `codes.PermissionDenied` to
// `(Active=false, nil)`. PermissionDenied here is OUR sa-admin
// service account losing read scope on a Zitadel user — it's an
// infrastructure misconfiguration on our side that says nothing
// about the user's home-org state. Mapping it to "inactive" would
// silently revoke every token of every healthy org as soon as the
// sa-admin role drifts (e.g. a Zitadel role rename or scope edit).
// We surface it as an error so the authn cache-hit path logs at
// Error and ops sees a configuration alert instead of a
// fleet-wide silent revocation.
func ResolveOrganization(ctx context.Context, conn *zitadelsdk.Connection, sub string) (*zitadel.OrganizationResult, error) {
	return resolveOrganization(
		ctx,
		user_v2.NewUserServiceClient(conn),
		org_v2.NewOrganizationServiceClient(conn),
		sub,
	)
}

// resolveOrganization is the package-private testable core. Same
// semantics as the exported [ResolveOrganization] but takes the two
// gRPC clients directly so unit tests can drive each gRPC code path
// without standing up a real Zitadel connection.
func resolveOrganization(ctx context.Context, userClient userByIDClient, orgClient listOrgsClient, sub string) (*zitadel.OrganizationResult, error) {
	if sub == "" {
		return nil, ErrResolveOrganizationEmptySub
	}

	userResp, err := userClient.GetUserByID(ctx, &user_v2.GetUserByIDRequest{UserId: sub})
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound:
			// Upstream state: the user is genuinely gone. Definitive
			// answer; caller routes through revocation.
			return &zitadel.OrganizationResult{Active: false}, nil
		case codes.PermissionDenied:
			// OUR side: sa-admin service account lacks read scope on
			// this user. Says nothing about upstream state. Surface
			// as error so the caller (authn) logs at Error and stays
			// with any cached decision until ops fixes RBAC.
			return nil, fmt.Errorf("getUserByID(%s): sa-admin permission denied: %w", sub, err)
		default:
			return nil, fmt.Errorf("getUserByID(%s): %w", sub, err)
		}
	}
	orgID := userResp.GetUser().GetDetails().GetResourceOwner()
	if orgID == "" {
		// Every Zitadel user belongs to an org; surface as error so
		// authn keeps any cached decision rather than treating the
		// nil-coalesced empty string as a definitive revocation.
		return nil, fmt.Errorf("%w: sub=%s", ErrResolveOrganizationNoResourceOwner, sub)
	}

	orgResp, err := orgClient.ListOrganizations(ctx, &org_v2.ListOrganizationsRequest{
		Queries: []*org_v2.SearchQuery{{
			Query: &org_v2.SearchQuery_IdQuery{
				IdQuery: &org_v2.OrganizationIDQuery{Id: orgID},
			},
		}},
	})
	if err != nil {
		// Same triage as the GetUserByID path: PermissionDenied here is
		// OUR sa-admin losing org.read scope. Surface as error rather
		// than mapping to "inactive".
		switch status.Code(err) {
		case codes.PermissionDenied:
			return nil, fmt.Errorf("listOrganizations(%s): sa-admin permission denied: %w", orgID, err)
		default:
			return nil, fmt.Errorf("listOrganizations(%s): %w", orgID, err)
		}
	}
	if len(orgResp.GetResult()) == 0 {
		return &zitadel.OrganizationResult{
			Active:         false,
			OrganizationID: orgID,
		}, nil
	}

	return &zitadel.OrganizationResult{
		Active:         orgResp.GetResult()[0].GetState() == org_v2.OrganizationState_ORGANIZATION_STATE_ACTIVE,
		OrganizationID: orgID,
	}, nil
}
