package resolvers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"
	authz_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filter_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"

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
//
// Both writes are the read-union-replace of sdk.ExtendProjectGrantRoles and
// sdk.ExtendUserAuthorizationRoles, which document the lost-update window
// Zitadel's full-replace updates leave open.
func (r *Resolver) assignServiceRoles(ctx context.Context, orgID, zitadelUserID string, add []string) ([]string, error) {
	grants := sdk.NewGrantClient(r.zitadelConn)
	projectID := core.Config.ZitadelProjectId

	// A user authorization can only carry roles the tenant org's project grant
	// allows. Ensure the requested roles are on the grant first, so existing
	// tenants (provisioned before these roles existed) self-heal rather than
	// failing with Errors.Project.Role.NotFound.
	if _, _, err := sdk.ExtendProjectGrantRoles(ctx, grants, projectID, orgID, add); err != nil {
		if errors.Is(err, sdk.ErrProjectGrantNotFound) {
			return nil, fmt.Errorf("%w: org %s", ErrTenantNoProjectGrant, orgID)
		}
		return nil, fmt.Errorf("extending project grant for org %s: %w", orgID, err)
	}

	authz, _, err := sdk.ExtendUserAuthorizationRoles(ctx, grants, projectID, orgID, zitadelUserID, add)
	if err == nil {
		return serviceRoleSubset(authz.Roles), nil
	}
	if !errors.Is(err, sdk.ErrUserGrantNotFound) {
		return nil, fmt.Errorf("extending authorization for user %s: %w", zitadelUserID, err)
	}

	// No existing authorization on the project: create one with exactly the
	// requested roles.
	roles := sortedUnique(add)
	authzClient := authz_pb.NewAuthorizationServiceClient(r.zitadelConn)
	if _, err := authzClient.CreateAuthorization(ctx, &authz_pb.CreateAuthorizationRequest{
		UserId:         zitadelUserID,
		ProjectId:      projectID,
		OrganizationId: orgID,
		RoleKeys:       roles,
	}); err != nil {
		return nil, fmt.Errorf("creating authorization for user %s: %w", zitadelUserID, err)
	}
	return roles, nil
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

// sortedUnique returns the distinct values of in, sorted.
func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}
