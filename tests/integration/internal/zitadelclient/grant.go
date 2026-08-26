package zitadelclient

import (
	"context"
	"fmt"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	authz_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filter_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
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
