//nolint:testpackage // resolveOrganization is intentionally unexported; tests live alongside.
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	object_v2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	org_v2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	user_v2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stubUserClient is a hand-rolled fake for [userByIDClient]. It returns
// the configured response/error verbatim and records the last request
// for assertion. testify/mock would also work but a hand-roll keeps the
// auth-path test surface minimal and dependency-free.
type stubUserClient struct {
	resp   *user_v2.GetUserByIDResponse
	err    error
	called int
	lastID string
}

func (s *stubUserClient) GetUserByID(_ context.Context, in *user_v2.GetUserByIDRequest, _ ...grpc.CallOption) (*user_v2.GetUserByIDResponse, error) {
	s.called++
	s.lastID = in.GetUserId()
	return s.resp, s.err
}

// stubOrgClient is a hand-rolled fake for [listOrgsClient].
type stubOrgClient struct {
	resp    *org_v2.ListOrganizationsResponse
	err     error
	called  int
	lastReq *org_v2.ListOrganizationsRequest
}

func (s *stubOrgClient) ListOrganizations(_ context.Context, in *org_v2.ListOrganizationsRequest, _ ...grpc.CallOption) (*org_v2.ListOrganizationsResponse, error) {
	s.called++
	s.lastReq = in
	return s.resp, s.err
}

// helpers

func userResp(orgID string) *user_v2.GetUserByIDResponse {
	return &user_v2.GetUserByIDResponse{
		User: &user_v2.User{
			Details: &object_v2.Details{
				ResourceOwner: orgID,
			},
		},
	}
}

func orgResp(orgID string, state org_v2.OrganizationState) *org_v2.ListOrganizationsResponse {
	return &org_v2.ListOrganizationsResponse{
		Result: []*org_v2.Organization{
			{Id: orgID, State: state},
		},
	}
}

func emptyOrgResp() *org_v2.ListOrganizationsResponse {
	return &org_v2.ListOrganizationsResponse{Result: nil}
}

// ============================================================================
// Caller-bug guard
// ============================================================================

func TestResolveOrganization_EmptySubIsCallerBug(t *testing.T) {
	t.Parallel()

	user := &stubUserClient{} // would not be called
	org := &stubOrgClient{}

	res, err := resolveOrganization(context.Background(), user, org, "")

	assert.Nil(t, res)
	require.ErrorIs(t, err, ErrResolveOrganizationEmptySub)
	assert.Equal(t, 0, user.called, "GetUserByID must not be called for empty sub")
	assert.Equal(t, 0, org.called, "ListOrganizations must not be called for empty sub")
}

// ============================================================================
// GetUserByID branch — happy path through every error code
// ============================================================================

// Routine revocation path. NotFound from Zitadel means the user is
// genuinely gone — definitive answer about upstream state, caller
// routes through revocation. This is the only "definite-no" gRPC code
// path on the user-lookup side.
func TestResolveOrganization_GetUserByID_NotFound_RoutesToInactive(t *testing.T) {
	t.Parallel()

	user := &stubUserClient{err: status.Error(codes.NotFound, "user not found")}
	org := &stubOrgClient{}

	res, err := resolveOrganization(context.Background(), user, org, "missing-user")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Active, "NotFound → Active=false (legitimate revoke)")
	assert.Empty(t, res.OrganizationID, "no org lookup attempted")
	assert.Equal(t, 0, org.called, "ListOrganizations must not be called after NotFound")
}

// THE M5 regression test. PermissionDenied from GetUserByID is OUR
// sa-admin service account losing read scope, NOT a statement about
// the user's home org. Pre-M5 this was bundled with NotFound and
// returned (Active=false, nil), which silently revoked every healthy
// org's tokens whenever the sa-admin RBAC drifted. Post-M5 it surfaces
// as an error so authn keeps the cached decision and ops gets visible
// log/alert noise.
func TestResolveOrganization_GetUserByID_PermissionDenied_IsError_M5(t *testing.T) {
	t.Parallel()

	denied := status.Error(codes.PermissionDenied, "AUTHZ-cdgFk")
	user := &stubUserClient{err: denied}
	org := &stubOrgClient{}

	res, err := resolveOrganization(context.Background(), user, org, "any-user")

	assert.Nil(t, res, "must NOT return a result — caller would treat (false, nil) as revoke")
	require.Error(t, err)
	require.ErrorIs(t, err, denied, "must wrap the original gRPC status so callers can errors.Is on it")
	assert.Contains(t, err.Error(), "sa-admin permission denied",
		"error message must name the diagnostic ('our side') for operator triage")
	assert.Equal(t, 0, org.called, "must not call ListOrganizations after a user-lookup permission error")
}

// Transport / 5xx / decode faults from GetUserByID are infrastructure
// problems with unknown impact on upstream state. Always surface as
// error so authn doesn't conflate them with revocation.
func TestResolveOrganization_GetUserByID_TransportError_IsError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code codes.Code
	}{
		{"unavailable", codes.Unavailable},
		{"deadline_exceeded", codes.DeadlineExceeded},
		{"internal", codes.Internal},
		{"unknown", codes.Unknown},
		{"unauthenticated", codes.Unauthenticated},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rpcErr := status.Error(tc.code, "boom")
			user := &stubUserClient{err: rpcErr}
			org := &stubOrgClient{}

			res, err := resolveOrganization(context.Background(), user, org, "sub-x")

			assert.Nil(t, res)
			require.ErrorIs(t, err, rpcErr)
			assert.Equal(t, 0, org.called)
		})
	}
}

// ============================================================================
// resource_owner edge case
// ============================================================================

// Defensive branch: if a Zitadel user somehow has no resource_owner,
// log Warn and return inactive. This shouldn't happen in practice but
// the existing behaviour predates M5 and stays as-is.
func TestResolveOrganization_NoResourceOwner_LogsWarn_ReturnsInactive(t *testing.T) {
	t.Parallel()

	user := &stubUserClient{resp: userResp("" /* empty resource_owner */)}
	org := &stubOrgClient{}

	res, err := resolveOrganization(context.Background(), user, org, "user-without-org")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Active)
	assert.Empty(t, res.OrganizationID)
	assert.Equal(t, 0, org.called, "no org lookup when resource_owner is empty")
}

// ============================================================================
// ListOrganizations branch — every state + error code
// ============================================================================

func TestResolveOrganization_OrgActive(t *testing.T) {
	t.Parallel()

	const orgID = "9876"
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{resp: orgResp(orgID, org_v2.OrganizationState_ORGANIZATION_STATE_ACTIVE)}

	res, err := resolveOrganization(context.Background(), user, org, "user-active")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Active, "ACTIVE state must map to Active=true")
	assert.Equal(t, orgID, res.OrganizationID)
	assert.Equal(t, 1, user.called)
	assert.Equal(t, 1, org.called)
}

func TestResolveOrganization_OrgInactive(t *testing.T) {
	t.Parallel()

	const orgID = "9876"
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{resp: orgResp(orgID, org_v2.OrganizationState_ORGANIZATION_STATE_INACTIVE)}

	res, err := resolveOrganization(context.Background(), user, org, "user-inactive")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Active, "INACTIVE state must map to Active=false (legitimate revoke)")
	assert.Equal(t, orgID, res.OrganizationID)
}

func TestResolveOrganization_OrgRemoved(t *testing.T) {
	t.Parallel()

	const orgID = "9876"
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{resp: orgResp(orgID, org_v2.OrganizationState_ORGANIZATION_STATE_REMOVED)}

	res, err := resolveOrganization(context.Background(), user, org, "user-removed")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Active, "REMOVED state must map to Active=false")
	assert.Equal(t, orgID, res.OrganizationID)
}

func TestResolveOrganization_OrgStateUnspecified(t *testing.T) {
	t.Parallel()

	const orgID = "9876"
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{resp: orgResp(orgID, org_v2.OrganizationState_ORGANIZATION_STATE_UNSPECIFIED)}

	res, err := resolveOrganization(context.Background(), user, org, "user-weird")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Active, "UNSPECIFIED state is not ACTIVE — fails closed")
}

// Edge case: the user's resource_owner points to an org that
// ListOrganizations doesn't return (org was deleted between the
// two calls, or the filter is inconsistent). Treat as inactive.
func TestResolveOrganization_OrgNotInListResult(t *testing.T) {
	t.Parallel()

	const orgID = "ghost"
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{resp: emptyOrgResp()}

	res, err := resolveOrganization(context.Background(), user, org, "user-ghost-org")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Active)
	assert.Equal(t, orgID, res.OrganizationID, "still surface the org ID we tried to look up")
}

// Symmetric M5 protection on the second gRPC call. PermissionDenied on
// ListOrganizations is the same sa-admin RBAC drift, just caught
// after we successfully read the user. Must NOT map to inactive.
func TestResolveOrganization_ListOrganizations_PermissionDenied_IsError_M5(t *testing.T) {
	t.Parallel()

	const orgID = "1234"
	denied := status.Error(codes.PermissionDenied, "AUTHZ-org-read")
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{err: denied}

	res, err := resolveOrganization(context.Background(), user, org, "user-ok")

	assert.Nil(t, res)
	require.ErrorIs(t, err, denied)
	assert.Contains(t, err.Error(), "sa-admin permission denied")
	assert.Contains(t, err.Error(), orgID, "error must name the org so operators can diagnose")
}

// Transport faults on ListOrganizations route through the default
// branch — wrapped error, no result.
func TestResolveOrganization_ListOrganizations_TransportError_IsError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code codes.Code
	}{
		{"unavailable", codes.Unavailable},
		{"deadline_exceeded", codes.DeadlineExceeded},
		{"internal", codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rpcErr := status.Error(tc.code, "boom")
			user := &stubUserClient{resp: userResp("org-1")}
			org := &stubOrgClient{err: rpcErr}

			res, err := resolveOrganization(context.Background(), user, org, "user-y")

			assert.Nil(t, res)
			require.ErrorIs(t, err, rpcErr)
		})
	}
}

// ============================================================================
// Diagnostic: error wrapping survives multiple wraps so callers' errors.Is works
// ============================================================================

func TestResolveOrganization_ErrorsIsThroughWrap(t *testing.T) {
	t.Parallel()

	denied := status.Error(codes.PermissionDenied, "AUTHZ-cdgFk")
	user := &stubUserClient{err: denied}
	org := &stubOrgClient{}

	_, err := resolveOrganization(context.Background(), user, org, "any-user")

	// Both errors.Is (direct match) and a status-extracted Code check
	// must work — these are the two patterns callers actually use.
	require.ErrorIs(t, err, denied, "errors.Is must traverse the wrap chain")
	require.Equal(t, codes.PermissionDenied, status.Code(errors.Unwrap(err)),
		"status.Code on the unwrapped error must yield the original code")
}

// ============================================================================
// Request shape — confirm we call the right SDK with the right args
// ============================================================================

func TestResolveOrganization_PassesSubToUserClient(t *testing.T) {
	t.Parallel()

	const orgID = "org-call-shape"
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{resp: orgResp(orgID, org_v2.OrganizationState_ORGANIZATION_STATE_ACTIVE)}

	_, err := resolveOrganization(context.Background(), user, org, "expected-sub")

	require.NoError(t, err)
	assert.Equal(t, "expected-sub", user.lastID, "must forward the sub verbatim to GetUserByID")
}

func TestResolveOrganization_PassesOrgIDToListOrgs(t *testing.T) {
	t.Parallel()

	const orgID = "org-call-shape"
	user := &stubUserClient{resp: userResp(orgID)}
	org := &stubOrgClient{resp: orgResp(orgID, org_v2.OrganizationState_ORGANIZATION_STATE_ACTIVE)}

	_, err := resolveOrganization(context.Background(), user, org, "sub")

	require.NoError(t, err)
	require.NotNil(t, org.lastReq)
	require.Len(t, org.lastReq.GetQueries(), 1, "must filter to exactly the user's home org")
	idQuery := org.lastReq.GetQueries()[0].GetIdQuery()
	require.NotNil(t, idQuery, "must use IdQuery, not e.g. ListQuery")
	assert.Equal(t, orgID, idQuery.GetId(), "must filter by the user's resource_owner")
}
