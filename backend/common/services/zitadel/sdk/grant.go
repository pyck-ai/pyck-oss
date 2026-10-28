package sdk

import (
	"context"
	"errors"
	"fmt"
	"slices"

	authzv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filterv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	projectv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	"google.golang.org/grpc"
)

// ErrUserGrantNotFound is returned by GetUserAuthorization when the user
// holds no authorization on the project within the org. The lookup reads
// the eventually consistent authorization projection while a create
// answers AlreadyExists from the eventstore, so after an AlreadyExists
// this can also mean "not projected yet"; treat it as retryable, not as
// a settled absence.
var ErrUserGrantNotFound = errors.New("user grant not found")

// ErrProjectGrantNotFound is returned by GetProjectGrantRoles when the
// project carries no grant for the org. The lookup reads the eventually
// consistent project-grant projection while a create answers AlreadyExists
// from the eventstore, so after an AlreadyExists this can also mean "not
// projected yet"; treat it as retryable, not as a settled absence.
var ErrProjectGrantNotFound = errors.New("project grant not found")

// ProjectGrantAPI is the pair of calls needed to widen an organization's
// role menu on a project. GrantClient implements it; it exists so tests
// can drive ExtendProjectGrantRoles without a Zitadel.
type ProjectGrantAPI interface {
	GetProjectGrantRoles(ctx context.Context, projectID, organizationID string) ([]string, error)
	UpdateProjectGrant(ctx context.Context, projectID, organizationID string, roleKeys []string) error
}

// UserAuthorizationAPI is the pair of calls needed to widen a user's role
// set on a project within an organization. See ProjectGrantAPI.
type UserAuthorizationAPI interface {
	GetUserAuthorization(ctx context.Context, projectID, organizationID, userID string) (*Grant, error)
	UpdateUserAuthorization(ctx context.Context, authorizationID string, roleKeys []string) error
}

// GrantClient reads and replaces project grants and user authorizations
// through the Zitadel v2 project and authorization services, so bootstrap,
// the registration activities and the integration tooling reach the grant
// APIs through one implementation.
type GrantClient struct {
	projects projectv2_pb.ProjectServiceClient
	authz    authzv2_pb.AuthorizationServiceClient
}

// NewGrantClient serves the grant APIs over an already-dialled Zitadel
// connection. The caller keeps ownership of the connection; GrantClient
// never closes it.
func NewGrantClient(conn grpc.ClientConnInterface) *GrantClient {
	return NewGrantClientOver(projectv2_pb.NewProjectServiceClient(conn), authzv2_pb.NewAuthorizationServiceClient(conn))
}

// NewGrantClientOver serves the grant APIs from v2 service clients the
// caller already holds, for callers whose test seam is those generated
// interfaces (the registration activities), so their fakes see the
// requests this client sends.
func NewGrantClientOver(projects projectv2_pb.ProjectServiceClient, authz authzv2_pb.AuthorizationServiceClient) *GrantClient {
	return &GrantClient{projects: projects, authz: authz}
}

// GetProjectGrantRoles returns the role menu of the org's grant on the
// project. Returns ErrProjectGrantNotFound when the projection holds none;
// see the sentinel for why that is not a settled absence after an
// AlreadyExists. The granted-organization filter answers the one row, so
// no paging is needed.
func (c *GrantClient) GetProjectGrantRoles(ctx context.Context, projectID, organizationID string) ([]string, error) {
	resp, err := c.projects.ListProjectGrants(ctx, &projectv2_pb.ListProjectGrantsRequest{
		Filters: []*projectv2_pb.ProjectGrantSearchFilter{
			{Filter: &projectv2_pb.ProjectGrantSearchFilter_InProjectIdsFilter{
				InProjectIdsFilter: &filterv2_pb.InIDsFilter{Ids: []string{projectID}},
			}},
			{Filter: &projectv2_pb.ProjectGrantSearchFilter_GrantedOrganizationIdFilter{
				GrantedOrganizationIdFilter: &filterv2_pb.IDFilter{Id: organizationID},
			}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list project grants: %w", err)
	}
	grants := resp.GetProjectGrants()
	if len(grants) == 0 {
		return nil, fmt.Errorf("%w: project %q in org %q", ErrProjectGrantNotFound, projectID, organizationID)
	}
	return grants[0].GetGrantedRoleKeys(), nil
}

// UpdateProjectGrant replaces the role menu of the org's grant on the
// project with roleKeys. Zitadel removes every role not re-sent (and with
// it the user grants carrying it) so callers extending a menu must send
// the union of the existing and the added roles, never only the addition;
// ExtendProjectGrantRoles does that.
func (c *GrantClient) UpdateProjectGrant(ctx context.Context, projectID, organizationID string, roleKeys []string) error {
	_, err := c.projects.UpdateProjectGrant(ctx, &projectv2_pb.UpdateProjectGrantRequest{
		ProjectId:             projectID,
		GrantedOrganizationId: organizationID,
		RoleKeys:              roleKeys,
	})
	return err
}

// GetUserAuthorization returns the user's authorization (user grant) on the
// project within the org: its id and role keys. Returns
// ErrUserGrantNotFound when the projection holds none; see the sentinel for
// why that is not a settled absence after an AlreadyExists.
func (c *GrantClient) GetUserAuthorization(ctx context.Context, projectID, organizationID, userID string) (*Grant, error) {
	resp, err := c.authz.ListAuthorizations(ctx, &authzv2_pb.ListAuthorizationsRequest{
		Filters: []*authzv2_pb.AuthorizationsSearchFilter{
			{Filter: &authzv2_pb.AuthorizationsSearchFilter_InUserIds{
				InUserIds: &filterv2_pb.InIDsFilter{Ids: []string{userID}},
			}},
			{Filter: &authzv2_pb.AuthorizationsSearchFilter_ProjectId{
				ProjectId: &filterv2_pb.IDFilter{Id: projectID},
			}},
			{Filter: &authzv2_pb.AuthorizationsSearchFilter_OrganizationId{
				OrganizationId: &filterv2_pb.IDFilter{Id: organizationID},
			}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list authorizations: %w", err)
	}
	authorizations := resp.GetAuthorizations()
	if len(authorizations) == 0 {
		return nil, fmt.Errorf("%w: user %q on project %q in org %q", ErrUserGrantNotFound, userID, projectID, organizationID)
	}

	authz := authorizations[0]
	roles := make([]string, 0, len(authz.GetRoles()))
	for _, role := range authz.GetRoles() {
		roles = append(roles, role.GetKey())
	}
	return &Grant{ID: authz.GetId(), Roles: roles}, nil
}

// UpdateUserAuthorization replaces the role keys of the authorization
// (full replace, like UpdateProjectGrant: any role not re-sent is revoked,
// so callers extending a grant must send the union).
func (c *GrantClient) UpdateUserAuthorization(ctx context.Context, authorizationID string, roleKeys []string) error {
	_, err := c.authz.UpdateAuthorization(ctx, &authzv2_pb.UpdateAuthorizationRequest{
		Id:       authorizationID,
		RoleKeys: roleKeys,
	})
	return err
}

// ExtendProjectGrantRoles widens an organization's role menu on a project to
// include roleKeys, and returns the menu in force afterwards. A menu that
// already covers roleKeys is left untouched.
//
// Zitadel's update replaces the whole menu and drops every role not re-sent
// (cascading into the user grants carrying it), so the union of the read and
// the requested roles is what gets written.
//
// The read and the write are not atomic and neither Zitadel API offers a
// compare-and-set, so a second writer that replaces the menu between them
// silently wins: both report success and nothing re-adds the lost role,
// and if the role that got dropped from the menu was one a user held,
// Zitadel revokes it from that user as well. Nothing in pyck mitigates
// that: the menu is org-wide and no writer takes a lock on it. The other
// menu writer, the management service-role resolvers (which any tenant
// admin may call), goes through this function too, so concurrent
// assignRoles calls race each other and the registration in the same
// window; a lost addition only surfaces when the role is next needed.
//
// The returned roles are what this call intended, not a re-read: Zitadel's
// grant projection lags the eventstore. updated reports whether a menu was
// actually replaced, so a caller that must observe its own write knows when
// a re-read is worth doing (and when the lag is worth tolerating).
func ExtendProjectGrantRoles(ctx context.Context, api ProjectGrantAPI, projectID, organizationID string, roleKeys []string) (roles []string, updated bool, err error) {
	current, err := api.GetProjectGrantRoles(ctx, projectID, organizationID)
	if err != nil {
		return nil, false, err
	}
	union := unionRoles(current, roleKeys)
	if len(union) == len(current) {
		return current, false, nil
	}
	if err := api.UpdateProjectGrant(ctx, projectID, organizationID, union); err != nil {
		return nil, false, fmt.Errorf("extend project grant role menu of org %q: %w", organizationID, err)
	}
	return union, true, nil
}

// ExtendUserAuthorizationRoles widens a user's authorization on a project
// within an organization to include roleKeys, and returns the authorization
// with Roles set to the set in force afterwards. A set that already covers
// roleKeys is left untouched. updated reports whether a replace happened.
// Returns ErrUserGrantNotFound when the user holds no authorization to
// extend: the caller decides whether to create one or to retry the lagging
// projection.
//
// Same full-replace semantics and the same unmitigated lost update as
// ExtendProjectGrantRoles, with one axis more: a user's authorization also
// has a shrinking writer (removeServiceRoles, which replaces the set with
// the remainder or deletes it outright), so a concurrent *removal* is lost
// just as easily as a concurrent addition, and re-running this call does
// not repair it: it re-grants the role the removal meant to take away.
// Two admins of one tenant editing the same user at once can hit this
// window; nothing serializes them.
//
// The roles the user may hold are capped by the organization's
// project-grant menu, so callers extend that menu first.
func ExtendUserAuthorizationRoles(ctx context.Context, api UserAuthorizationAPI, projectID, organizationID, userID string, roleKeys []string) (authz *Grant, updated bool, err error) {
	authz, err = api.GetUserAuthorization(ctx, projectID, organizationID, userID)
	if err != nil {
		return nil, false, err
	}
	union := unionRoles(authz.Roles, roleKeys)
	if len(union) == len(authz.Roles) {
		return authz, false, nil
	}
	if err := api.UpdateUserAuthorization(ctx, authz.ID, union); err != nil {
		return nil, false, fmt.Errorf("extend user grant roles of user %q in org %q: %w", userID, organizationID, err)
	}
	return &Grant{ID: authz.ID, Roles: union}, true, nil
}

// unionRoles returns existing plus every requested role not already in it,
// preserving order so the caller can compare lengths to tell a no-op from a
// widening.
func unionRoles(existing, requested []string) []string {
	union := slices.Clone(existing)
	for _, role := range requested {
		if !slices.Contains(union, role) {
			union = append(union, role)
		}
	}
	return union
}
