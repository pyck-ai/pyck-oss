package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/pyck-ai/pyck/backend/common/auth"
	"github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/accesspolicy"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/group"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/role"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/user"
	"github.com/rs/zerolog/log"
)

// ErrAccessDenied is returned when access is denied
var ErrAccessDenied = errors.New("access denied: insufficient permissions")

// ManagementAuthorizer handles authorization for the management service using direct Ent queries
type ManagementAuthorizer struct {
	client *gen.Client
}

// NewManagementAuthorizer creates a new management service authorizer
func NewManagementAuthorizer(client *gen.Client) *ManagementAuthorizer {
	return &ManagementAuthorizer{
		client: client,
	}
}

// Enforce checks if the user has permission to perform the given action on the resource
func (a *ManagementAuthorizer) Enforce(ctx context.Context, resource, action string) (bool, error) {
	authUser := auth.ForContext(ctx)
	if authUser == nil {
		log.Debug().Msg("No user in context")
		return false, fmt.Errorf("no user in context")
	}

	// Service users need specific permissions for internal operations
	if auth.IsServiceUser(ctx) {
		// Define allowed operations for service users
		allowedOperations := map[string][]string{
			// Zitadel sync operations
			"management.user":   {"create", "update", "read"},
			"management.tenant": {"create", "update", "read"},
			// Event registry operations
			"management.event": {"read", "create", "update"},
			// Bootstrap operations
			"management.role":   {"create", "read"},
			"management.policy": {"create", "read"},
			// Webhook operations
			"management.group": {"create", "update", "read"},
		}

		// Check if the operation is allowed for service users
		allowedActions, exists := allowedOperations[resource]
		if exists {
			for _, allowedAction := range allowedActions {
				if allowedAction == action {
					log.Debug().
						Str("zitadel_id", authUser.ID.String()).
						Str("resource", resource).
						Str("action", action).
						Msg("Service user access granted for allowed operation")
					return true, nil
				}
			}
		}

		log.Warn().
			Str("zitadel_id", authUser.ID.String()).
			Str("resource", resource).
			Str("action", action).
			Msg("Service user access denied - operation not allowed")
		return false, nil
	}

	log.Debug().
		Str("zitadel_id", authUser.ID.String()).
		Str("tenant_id", authUser.TenantID.String()).
		Str("resource", resource).
		Str("action", action).
		Msg("Checking management authorization")

	// Single optimized query with eager loading
	// This replaces the previous N+1 query pattern (4-5 separate queries)
	internalUser, err := a.client.User.Query().
		Where(
			user.And(
				user.IdpID(authUser.ID.String()),
				user.TenantID(authUser.TenantID),
			),
		).
		WithRoles(func(rq *gen.RoleQuery) {
			rq.Where(role.TenantID(authUser.TenantID)).
				WithPolicies(func(pq *gen.AccessPolicyQuery) {
					pq.Where(
						accesspolicy.And(
							accesspolicy.Resource(resource),
							accesspolicy.Action(action),
							accesspolicy.TenantID(authUser.TenantID),
						),
					)
				})
		}).
		WithGroups(func(gq *gen.GroupQuery) {
			gq.Where(group.TenantID(authUser.TenantID)).
				WithRoles(func(rq *gen.RoleQuery) {
					rq.Where(role.TenantID(authUser.TenantID)).
						WithPolicies(func(pq *gen.AccessPolicyQuery) {
							pq.Where(
								accesspolicy.And(
									accesspolicy.Resource(resource),
									accesspolicy.Action(action),
									accesspolicy.TenantID(authUser.TenantID),
								),
							)
						})
				})
		}).
		Only(ctx)

	if err != nil {
		if gen.IsNotFound(err) {
			log.Warn().
				Str("idp_id", authUser.ID.String()).
				Str("tenant_id", authUser.TenantID.String()).
				Msg("User lookup failed - potential tenant mismatch")
			return false, fmt.Errorf("user not found in tenant")
		}
		log.Error().Err(err).
			Str("zitadel_id", authUser.ID.String()).
			Str("tenant_id", authUser.TenantID.String()).
			Msg("Failed to find internal user by Zitadel ID")
		return false, fmt.Errorf("user not found in internal database")
	}

	// Additional validation: ensure tenant ID matches
	if internalUser.TenantID != authUser.TenantID {
		log.Error().
			Str("expected_tenant", authUser.TenantID.String()).
			Str("actual_tenant", internalUser.TenantID.String()).
			Msg("Tenant mismatch detected - possible security issue")
		return false, fmt.Errorf("tenant validation failed")
	}

	log.Debug().
		Str("zitadel_id", authUser.ID.String()).
		Str("internal_user_id", internalUser.ID.String()).
		Str("username", internalUser.Username).
		Msg("Mapped Zitadel ID to internal user")

	// Process policies from both direct roles and group roles
	// This is now done with the data already loaded from the single query
	hasAllow := false

	// Check direct role policies
	for _, role := range internalUser.Edges.Roles {
		for _, policy := range role.Edges.Policies {
			if policy.Effect == "deny" {
				log.Debug().
					Str("user_id", authUser.ID.String()).
					Str("policy_id", policy.ID.String()).
					Str("role_id", role.ID.String()).
					Str("resource", resource).
					Str("action", action).
					Msg("Access explicitly denied by direct role policy")
				return false, nil
			}
			if policy.Effect == "allow" {
				hasAllow = true
			}
		}
	}

	// Check group role policies
	for _, group := range internalUser.Edges.Groups {
		for _, role := range group.Edges.Roles {
			for _, policy := range role.Edges.Policies {
				if policy.Effect == "deny" {
					log.Debug().
						Str("user_id", authUser.ID.String()).
						Str("policy_id", policy.ID.String()).
						Str("role_id", role.ID.String()).
						Str("group_id", group.ID.String()).
						Str("resource", resource).
						Str("action", action).
						Msg("Access explicitly denied by group role policy")
					return false, nil
				}
				if policy.Effect == "allow" {
					hasAllow = true
				}
			}
		}
	}

	if hasAllow {
		log.Debug().
			Str("user_id", authUser.ID.String()).
			Str("resource", resource).
			Str("action", action).
			Msg("Access granted by policy")
		return true, nil
	}

	log.Debug().
		Str("user_id", authUser.ID.String()).
		Str("resource", resource).
		Str("action", action).
		Msg("No allow policies found, access denied")

	return false, nil
}

// MustEnforce is a helper method that returns an error if access is denied
// This simplifies the authorization pattern in resolvers
func (a *ManagementAuthorizer) MustEnforce(ctx context.Context, resource, action string) error {
	allowed, err := a.Enforce(ctx, resource, action)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w to %s %s", ErrAccessDenied, action, resource)
	}
	return nil
}
