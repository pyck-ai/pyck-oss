package utils

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/request"
	commontenant "github.com/pyck-ai/pyck/backend/common/tenant"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/role"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/user"
	"github.com/rs/zerolog/log"
)

const WarehouseManagerRoleName = "warehouse-manager"

// EnsureTenantOnboarded ensures that all necessary entities (Tenant, Roles, Policies)
// exist for the current user and their tenant, and automatically assigns the first
// user the warehouse-manager role
func EnsureTenantOnboarded(ctx context.Context, tx *ent.Tx) error {
	req := request.ForContext(ctx)
	user := req.User()

	if !user.IsAuthenticated() {
		return fmt.Errorf("unauthorized: user not authenticated")
	}

	// 1. Ensure tenant exists for tenant
	err := ensureTenantExists(ctx, tx, user.TenantID)
	if err != nil {
		return fmt.Errorf("failed to ensure tenant exists: %w", err)
	}

	// 2. Ensure warehouse-manager role exists for tenant
	warehouseManagerRole, err := ensureWarehouseManagerRole(ctx, tx, user.TenantID, user.ID)
	if err != nil {
		return fmt.Errorf("failed to ensure warehouse-manager role: %w", err)
	}

	// 3. Assign user as warehouse-manager if they are the first user in the tenant
	err = assignFirstUserAsWarehouseManager(ctx, tx, user.TenantID, user.ID, warehouseManagerRole.ID)
	if err != nil {
		return fmt.Errorf("failed to assign warehouse-manager role: %w", err)
	}

	return nil
}

// ensureTenantExists creates a Tenant for the tenant if it doesn't exist
func ensureTenantExists(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID) error {
	exists, err := tx.Tenant.Query().
		Where(enttenant.ID(tenantID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to check tenant existence: %w", err)
	}

	if !exists {
		_, err = tx.Tenant.Create().
			SetName("Warehouse " + tenantID.String()[:8]). // Fallback name
			SetID(tenantID).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create tenant: %w", err)
		}
		log.Info().Str("tenant_id", tenantID.String()).Msg("Created new tenant for tenant")
	}

	return nil
}

// ensureWarehouseManagerRole creates the warehouse-manager role with all necessary policies
func ensureWarehouseManagerRole(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, createdBy uuid.UUID) (*ent.Role, error) {
	// Check if role already exists
	existingRole, err := tx.Role.Query().
		Where(role.And(role.Name(WarehouseManagerRoleName), role.TenantID(tenantID))).
		First(ctx)
	if err == nil {
		return existingRole, nil // Role already exists
	}

	// Create warehouse-manager role
	warehouseManagerRole, err := tx.Role.Create().
		SetName(WarehouseManagerRoleName).
		SetDescription("Warehouse Manager - can manage all management functions in this warehouse").
		SetTenantID(tenantID).
		SetCreatedBy(createdBy).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create warehouse-manager role: %w", err)
	}

	// Create management policies for warehouse-manager
	managementPolicies := []struct {
		resource string
		action   string
	}{
		{"management.role", "read"},
		{"management.role", "write"},
		{"management.role", "delete"},
		{"management.group", "read"},
		{"management.group", "write"},
		{"management.group", "delete"},
		{"management.policy", "read"},
		{"management.policy", "write"},
		{"management.policy", "delete"},
		{"management.user", "read"},
		{"management.user", "write"},
		{"management.tenant", "read"},
	}

	for _, policy := range managementPolicies {
		_, err = tx.AccessPolicy.Create().
			SetResource(policy.resource).
			SetAction(policy.action).
			SetEffect("allow").
			SetTenantID(tenantID).
			SetCreatedBy(createdBy).
			SetRole(warehouseManagerRole).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create policy %s:%s: %w", policy.resource, policy.action, err)
		}
	}

	log.Info().
		Str("tenant_id", tenantID.String()).
		Str("role_id", warehouseManagerRole.ID.String()).
		Int("policies_created", len(managementPolicies)).
		Msg("Created warehouse-manager role with policies")

	return warehouseManagerRole, nil
}

// assignFirstUserAsWarehouseManager assigns the warehouse-manager role to the first user in the tenant
func assignFirstUserAsWarehouseManager(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error {
	// Check if user already has the warehouse-manager role
	hasRole, err := tx.User.Query().
		Where(user.And(user.ID(userID), user.HasRolesWith(role.ID(roleID)))).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to check user role assignment: %w", err)
	}

	if hasRole {
		return nil // User already has the role
	}

	// Check if there are already other warehouse-managers in the tenant
	warehouseManagerCount, err := tx.User.Query().
		Where(user.And(
			user.TenantID(tenantID),
			user.HasRolesWith(role.And(role.Name(WarehouseManagerRoleName), role.TenantID(tenantID))),
		)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count existing warehouse managers: %w", err)
	}

	// Only assign the role automatically if there are no warehouse-managers yet
	if warehouseManagerCount == 0 {
		err = tx.User.UpdateOneID(userID).
			AddRoleIDs(roleID).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to assign warehouse-manager role to user: %w", err)
		}

		log.Info().
			Str("tenant_id", tenantID.String()).
			Str("user_id", userID.String()).
			Str("role_id", roleID.String()).
			Msg("Assigned warehouse-manager role to first user in tenant")
	}

	return nil
}

// EnsureAllTenantsOnboarded runs tenant onboarding for all existing tenants/tenants
func EnsureAllTenantsOnboarded(ctx context.Context, client *ent.Client) error {
	// Get all tenants (tenants)
	tenants, err := client.Tenant.Query().AllPages(ctx, mixin.Limit)
	if err != nil {
		return fmt.Errorf("failed to fetch tenants: %w", err)
	}

	for _, tenant := range tenants {
		// Get first user for each tenant to trigger onboarding
		firstUser, err := client.User.Query().
			Where(user.TenantID(tenant.ID)).
			First(ctx)
		if err != nil {
			log.Warn().
				Str("tenant_id", tenant.ID.String()).
				Err(err).
				Msg("No users found for tenant, skipping onboarding")
			continue
		}

		// Create auth context for the first user
		userCtx := commontenant.Context(authn.Context(ctx, &authn.User{
			ID:       firstUser.ID,
			TenantID: firstUser.TenantID,
			Roles: map[uuid.UUID]authn.Role{
				firstUser.TenantID: authn.ROLE_ADMIN,
			},
		}), firstUser.TenantID)

		// Run onboarding in transaction
		tx, err := client.Tx(userCtx)
		if err != nil {
			log.Error().
				Str("tenant_id", tenant.ID.String()).
				Err(err).
				Msg("Failed to start transaction")
			continue
		}

		err = EnsureTenantOnboarded(userCtx, tx)
		if err != nil {
			_ = tx.Rollback()
		} else {
			_ = tx.Commit()
		}
		if err != nil {
			log.Error().
				Str("tenant_id", tenant.ID.String()).
				Err(err).
				Msg("Failed to ensure tenant onboarding")
			// Continue with other tenants even if one fails
		}
	}

	return nil
}
