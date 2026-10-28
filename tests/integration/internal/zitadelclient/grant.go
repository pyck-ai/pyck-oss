package zitadelclient

import (
	"context"
	"fmt"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	authz_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filter_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
)

// grantProjectionTimeout bounds how long EnsureProjectGrant waits for
// Zitadel's eventstore→projection lag to catch up after creating an
// authorization. Local stacks usually project within tens of
// milliseconds; we cap at 5s to keep runaway introspection caches from
// being papered over.
const grantProjectionTimeout = 5 * time.Second

// EnsureProjectGrant authorizes the user on the Pyck project at the given
// org's scope. Critical for our flow: granting at the *new sub-org's*
// scope (rather than the central Zitadel org) is what makes the resulting
// PAT a real tenant member — its introspected user.Roles will be keyed by
// this org id, which matches what's in management.tenants.
//
// Idempotent per organization: skips only if an authorization already
// exists for this user on this project *in this org*, regardless of
// role-key drift. Scoping the check by org is essential for cross-tenant
// members — a user granted a role in one sub-org must still get a distinct
// authorization created in another, which a user+project-only check would
// wrongly treat as already satisfied.
//
// After creating a new authorization, polls ListAuthorizations until the
// grant is visible on the read side of Zitadel. This closes a flaky race
// where:
//
//  1. CreateAuthorization returns as soon as the event is in the
//     eventstore.
//  2. The test immediately uses the PAT against pyck-management.
//  3. pyck-management calls Zitadel IntrospectToken, which reads from
//     the eventually-consistent projection.
//  4. If the projection hasn't caught up, the introspected User has
//     empty roles. pyck-management caches that empty-roles result for
//     the full PAT cache TTL, locking the PAT into "no role" state.
//
// Polling List on the same projection that introspect reads from is the
// barrier that prevents step 4 from ever firing with stale data.
func EnsureProjectGrant(ctx context.Context, conn *zitadel.Connection, orgID, projectID, userID string, roleKeys []string) error {
	c := authz_pb.NewAuthorizationServiceClient(conn)

	list, err := c.ListAuthorizations(ctx, &authz_pb.ListAuthorizationsRequest{
		Filters: grantFilters(userID, projectID, orgID),
	})
	if err != nil {
		return fmt.Errorf("list authorizations: %w", err)
	}
	if len(list.GetAuthorizations()) > 0 {
		return nil
	}

	if _, err := c.CreateAuthorization(ctx, &authz_pb.CreateAuthorizationRequest{
		UserId:         userID,
		ProjectId:      projectID,
		OrganizationId: orgID,
		RoleKeys:       roleKeys,
	}); err != nil {
		return fmt.Errorf("create authorization: %w", err)
	}

	if err := waitForGrantProjected(ctx, c, userID, projectID, orgID, grantProjectionTimeout); err != nil {
		return fmt.Errorf("wait for grant projection: %w", err)
	}

	return nil
}

// GrantedRoleKeys returns the role keys of the authorization the user with
// the given username holds on the project within the org. Unlike
// EnsureProjectGrant, it looks the grant up by username, for inspecting a
// grant some other flow (tenant registration, say) created, whose Zitadel
// user id the caller never learns. Zitadel's authorization read side is
// eventually consistent, so callers poll: "no authorization" is an error,
// not an empty slice.
func GrantedRoleKeys(ctx context.Context, conn *zitadel.Connection, orgID, projectID, username string) ([]string, error) {
	_, authz, err := grantedAuthorization(ctx, conn, orgID, projectID, username)
	if err != nil {
		return nil, err
	}
	return authz.Roles, nil
}

// grantFilters builds the authorization search filter set that identifies a
// single user's grant on a project within one organization. Scoping by org
// (not just user+project) is what lets EnsureProjectGrant and
// waitForGrantProjected reason about cross-tenant members, whose same
// user+project pair has a separate authorization per sub-org.
func grantFilters(userID, projectID, orgID string) []*authz_pb.AuthorizationsSearchFilter {
	return []*authz_pb.AuthorizationsSearchFilter{
		{Filter: &authz_pb.AuthorizationsSearchFilter_InUserIds{
			InUserIds: &filter_pb.InIDsFilter{Ids: []string{userID}},
		}},
		{Filter: &authz_pb.AuthorizationsSearchFilter_ProjectId{
			ProjectId: &filter_pb.IDFilter{Id: projectID},
		}},
		{Filter: &authz_pb.AuthorizationsSearchFilter_OrganizationId{
			OrganizationId: &filter_pb.IDFilter{Id: orgID},
		}},
	}
}

// waitForGrantProjected polls Zitadel's authorization read side until
// the user's grant on the project becomes visible, or the timeout
// expires. We poll the same read path IntrospectToken consults, so a
// successful poll guarantees the next PAT introspection sees the role.
func waitForGrantProjected(ctx context.Context, c authz_pb.AuthorizationServiceClient, userID, projectID, orgID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		list, err := c.ListAuthorizations(ctx, &authz_pb.ListAuthorizationsRequest{
			Filters: grantFilters(userID, projectID, orgID),
		})
		if err == nil && len(list.GetAuthorizations()) > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("timeout: last error %w", err)
			}
			return fmt.Errorf("timeout: grant for user=%s project=%s not visible after %s", userID, projectID, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// GrantedUserID returns the Zitadel user id behind the authorization the
// username holds on the project within the org. It is the counterpart of
// GrantedRoleKeys for callers that must address the user itself: the grant
// APIs take a user id, and a registration-created admin's id is never
// reported to the harness. Like GrantedRoleKeys it fails until the
// authorization is readable, which is the barrier callers poll on before
// replacing that authorization.
func GrantedUserID(ctx context.Context, conn *zitadel.Connection, orgID, projectID, username string) (string, error) {
	userID, _, err := grantedAuthorization(ctx, conn, orgID, projectID, username)
	if err != nil {
		return "", err
	}
	return userID, nil
}

// grantedAuthorization resolves the org's user by username and reads the
// authorization it holds on the project, reporting both: GrantedUserID
// keeps the id, GrantedRoleKeys the roles.
//
// The user id is resolved first so the authorization read can filter on it.
// Listing an org's authorizations unfiltered answers one page, and a tenant
// org gains an authorization per machine user on every provisioning run, so
// scanning that page for a login name stops finding the human admin as soon
// as the org outgrows it, reported as "no authorization", which reads like
// a missing grant.
func grantedAuthorization(ctx context.Context, conn *zitadel.Connection, orgID, projectID, username string) (string, *sdk.Grant, error) {
	userID, err := UserIDByUsername(ctx, conn, orgID, username)
	if err != nil {
		return "", nil, err
	}
	authz, err := sdk.NewGrantClient(conn).GetUserAuthorization(ctx, projectID, orgID, userID)
	if err != nil {
		return "", nil, err
	}
	return userID, authz, nil
}
