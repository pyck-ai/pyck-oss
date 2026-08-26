package resolvers

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	authz_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filter_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	proj_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	entuser "github.com/pyck-ai/pyck/backend/management/ent/gen/user"
)

// resolveTenantUser resolves a tenant's Zitadel organization id and a user's
// Zitadel id, validating that the tenant exists (not soft-deleted) and that the
// user belongs to it. systemCtx must carry the system user so the lookups
// bypass row-level tenant filters.
func (r *Resolver) resolveTenantUser(systemCtx context.Context, tenantID, userID uuid.UUID) (orgID, zitadelUserID string, err error) {
	t, err := r.client.Tenant.Query().
		Where(enttenant.ID(tenantID), enttenant.DeletedAtIsNil()).
		Only(systemCtx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", "", fmt.Errorf("%w: %s", ErrTenantNotFound, tenantID)
		}
		return "", "", fmt.Errorf("looking up tenant %s: %w", tenantID, err)
	}
	if t.IdpOrgRef == "" {
		return "", "", fmt.Errorf("%w: %s", ErrTenantNoOrgRef, tenantID)
	}

	u, err := r.client.User.Query().
		Where(entuser.ID(userID), entuser.TenantID(tenantID), entuser.DeletedAtIsNil()).
		Only(systemCtx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", "", fmt.Errorf("%w: user %s in tenant %s", ErrUserNotFoundInTenant, userID, tenantID)
		}
		return "", "", fmt.Errorf("looking up user %s: %w", userID, err)
	}

	return t.IdpOrgRef, u.IdpID, nil
}

// listUserServiceRoles returns the per-service roles the Zitadel user currently
// holds on the central Pyck project (ladder roles are filtered out).
func (r *Resolver) listUserServiceRoles(ctx context.Context, orgID, zitadelUserID string) ([]string, error) {
	authz, found, err := r.userAuthorization(ctx, orgID, zitadelUserID)
	if err != nil {
		return nil, err
	}
	if !found {
		return []string{}, nil
	}

	out := make([]string, 0)
	for _, role := range authz.GetRoles() {
		if serviceroles.IsServiceRole(role.GetKey()) {
			out = append(out, role.GetKey())
		}
	}
	sort.Strings(out)
	return out, nil
}

// assignServiceRoles adds the given per-service roles to the user's
// authorization on the central Pyck project, preserving any roles they already
// hold (additive). Returns the per-service roles held after the change.
func (r *Resolver) assignServiceRoles(ctx context.Context, orgID, zitadelUserID string, add []string) ([]string, error) {
	// A user authorization can only carry roles the tenant org's project grant
	// allows. Ensure the requested roles are on the grant first, so existing
	// tenants (provisioned before these roles existed) self-heal rather than
	// failing with Errors.Project.Role.NotFound.
	if err := r.ensureProjectGrantRoles(ctx, orgID, add); err != nil {
		return nil, err
	}

	authzClient := authz_pb.NewAuthorizationServiceClient(r.zitadelConn)

	authz, found, err := r.userAuthorization(ctx, orgID, zitadelUserID)
	if err != nil {
		return nil, err
	}

	// No existing authorization on the project: create one with exactly the
	// requested roles.
	if !found {
		if _, err := authzClient.CreateAuthorization(ctx, &authz_pb.CreateAuthorizationRequest{
			UserId:         zitadelUserID,
			ProjectId:      core.Config.ZitadelProjectId,
			OrganizationId: orgID,
			RoleKeys:       sortedUnique(add),
		}); err != nil {
			return nil, fmt.Errorf("creating authorization for user %s: %w", zitadelUserID, err)
		}
		return sortedUnique(add), nil
	}

	// Existing authorization: union current roles with the requested ones and
	// replace the role set (UpdateAuthorization is a full replace).
	set := make(map[string]struct{})
	for _, role := range authz.GetRoles() {
		set[role.GetKey()] = struct{}{}
	}
	for _, k := range add {
		set[k] = struct{}{}
	}
	fullSet := keysOf(set)

	if _, err := authzClient.UpdateAuthorization(ctx, &authz_pb.UpdateAuthorizationRequest{
		Id:       authz.GetId(),
		RoleKeys: fullSet,
	}); err != nil {
		return nil, fmt.Errorf("updating authorization %s: %w", authz.GetId(), err)
	}

	return serviceRoleSubset(fullSet), nil
}

// removeServiceRoles removes the given per-service roles from the user's
// authorization on the central Pyck project, preserving every other role they
// hold (ladder roles and any other service roles). Idempotent — removing a role
// the user does not hold is a no-op. If the removal would leave the
// authorization with no roles at all, the authorization is deleted instead
// (Zitadel rejects an empty role set). Returns the per-service roles held after
// the change.
//
// Unlike assignServiceRoles this does not touch the tenant org's project grant:
// the grant is the org-level menu shared by all its users, so removing a role
// from one user must not shrink it.
func (r *Resolver) removeServiceRoles(ctx context.Context, orgID, zitadelUserID string, remove []string) ([]string, error) {
	authzClient := authz_pb.NewAuthorizationServiceClient(r.zitadelConn)

	authz, found, err := r.userAuthorization(ctx, orgID, zitadelUserID)
	if err != nil {
		return nil, err
	}
	if !found {
		// No authorization at all: nothing to remove.
		return []string{}, nil
	}

	removeSet := make(map[string]struct{}, len(remove))
	for _, k := range remove {
		removeSet[k] = struct{}{}
	}

	current := authz.GetRoles()
	remaining := make([]string, 0, len(current))
	for _, role := range current {
		if _, drop := removeSet[role.GetKey()]; drop {
			continue
		}
		remaining = append(remaining, role.GetKey())
	}

	// No-op: the user held none of the requested roles.
	if len(remaining) == len(current) {
		return serviceRoleSubset(remaining), nil
	}

	// Removing the last role leaves an empty authorization; delete it instead.
	if len(remaining) == 0 {
		if _, err := authzClient.DeleteAuthorization(ctx, &authz_pb.DeleteAuthorizationRequest{
			Id: authz.GetId(),
		}); err != nil {
			return nil, fmt.Errorf("deleting authorization %s: %w", authz.GetId(), err)
		}
		return []string{}, nil
	}

	sort.Strings(remaining)
	if _, err := authzClient.UpdateAuthorization(ctx, &authz_pb.UpdateAuthorizationRequest{
		Id:       authz.GetId(),
		RoleKeys: remaining,
	}); err != nil {
		return nil, fmt.Errorf("updating authorization %s: %w", authz.GetId(), err)
	}

	return serviceRoleSubset(remaining), nil
}

// serviceRoleSubset returns only the per-service role keys from keys, sorted.
func serviceRoleSubset(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if serviceroles.IsServiceRole(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ensureProjectGrantRoles makes sure the tenant org's project grant for the
// central Pyck project includes the given role keys, adding any that are
// missing (a full-set replace via UpdateProjectGrant). Idempotent — a no-op
// when the grant already covers them. The grant is the org-level "menu" of
// roles its users may hold; without this, assigning a role the grant lacks
// fails with Errors.Project.Role.NotFound.
func (r *Resolver) ensureProjectGrantRoles(ctx context.Context, orgID string, roleKeys []string) error {
	projClient := proj_pb.NewProjectServiceClient(r.zitadelConn)

	resp, err := projClient.ListProjectGrants(ctx, &proj_pb.ListProjectGrantsRequest{
		Filters: []*proj_pb.ProjectGrantSearchFilter{
			{Filter: &proj_pb.ProjectGrantSearchFilter_InProjectIdsFilter{
				InProjectIdsFilter: &filter_pb.InIDsFilter{Ids: []string{core.Config.ZitadelProjectId}},
			}},
			{Filter: &proj_pb.ProjectGrantSearchFilter_GrantedOrganizationIdFilter{
				GrantedOrganizationIdFilter: &filter_pb.IDFilter{Id: orgID},
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("listing project grants for org %s: %w", orgID, err)
	}

	grants := resp.GetProjectGrants()
	if len(grants) == 0 {
		return fmt.Errorf("%w: org %s", ErrTenantNoProjectGrant, orgID)
	}

	set := make(map[string]struct{})
	for _, k := range grants[0].GetGrantedRoleKeys() {
		set[k] = struct{}{}
	}
	missing := false
	for _, k := range roleKeys {
		if _, ok := set[k]; !ok {
			set[k] = struct{}{}
			missing = true
		}
	}
	if !missing {
		return nil
	}

	if _, err := projClient.UpdateProjectGrant(ctx, &proj_pb.UpdateProjectGrantRequest{
		ProjectId:             core.Config.ZitadelProjectId,
		GrantedOrganizationId: orgID,
		RoleKeys:              keysOf(set),
	}); err != nil {
		return fmt.Errorf("updating project grant for org %s: %w", orgID, err)
	}
	return nil
}

// userAuthorization returns the user's authorization on the central Pyck
// project within the given org. found is false when the user has no
// authorization there. Scoping by org is required because a cross-tenant user
// (authorized in multiple orgs for the project) has one authorization per org;
// without it we'd reconcile an arbitrary tenant's grant.
func (r *Resolver) userAuthorization(ctx context.Context, orgID, zitadelUserID string) (authz *authz_pb.Authorization, found bool, err error) {
	authzClient := authz_pb.NewAuthorizationServiceClient(r.zitadelConn)
	resp, err := authzClient.ListAuthorizations(ctx, &authz_pb.ListAuthorizationsRequest{
		Filters: []*authz_pb.AuthorizationsSearchFilter{
			{Filter: &authz_pb.AuthorizationsSearchFilter_InUserIds{
				InUserIds: &filter_pb.InIDsFilter{Ids: []string{zitadelUserID}},
			}},
			{Filter: &authz_pb.AuthorizationsSearchFilter_ProjectId{
				ProjectId: &filter_pb.IDFilter{Id: core.Config.ZitadelProjectId},
			}},
			{Filter: &authz_pb.AuthorizationsSearchFilter_OrganizationId{
				OrganizationId: &filter_pb.IDFilter{Id: orgID},
			}},
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("listing authorizations for user %s: %w", zitadelUserID, err)
	}
	if len(resp.GetAuthorizations()) == 0 {
		return nil, false, nil
	}
	return resp.GetAuthorizations()[0], true, nil
}

func sortedUnique(in []string) []string {
	set := make(map[string]struct{}, len(in))
	for _, s := range in {
		set[s] = struct{}{}
	}
	return keysOf(set)
}

func keysOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
