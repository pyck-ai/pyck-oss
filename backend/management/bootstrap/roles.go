package bootstrap

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/role"
	"github.com/rs/zerolog/log"
)

// BootstrapRole represents a role to be created during bootstrap
type BootstrapRole struct {
	Name        string
	Description string
	Policies    []Policy
}

// Policy represents an access policy
type Policy struct {
	Resource string
	Action   string
	Effect   string
}

// SystemRoles defines the default system roles that should exist
var SystemRoles = []BootstrapRole{
	{
		Name:        "System-Admin",
		Description: "Full system administration access across all services",
		Policies: []Policy{
			// Management service full access
			{Resource: "management.role", Action: "read", Effect: "allow"},
			{Resource: "management.role", Action: "create", Effect: "allow"},
			{Resource: "management.role", Action: "update", Effect: "allow"},
			{Resource: "management.role", Action: "delete", Effect: "allow"},
			{Resource: "management.group", Action: "read", Effect: "allow"},
			{Resource: "management.group", Action: "create", Effect: "allow"},
			{Resource: "management.group", Action: "update", Effect: "allow"},
			{Resource: "management.group", Action: "delete", Effect: "allow"},
			{Resource: "management.policy", Action: "read", Effect: "allow"},
			{Resource: "management.policy", Action: "create", Effect: "allow"},
			{Resource: "management.policy", Action: "update", Effect: "allow"},
			{Resource: "management.policy", Action: "delete", Effect: "allow"},
			{Resource: "management.user", Action: "read", Effect: "allow"},
			{Resource: "management.user", Action: "create", Effect: "allow"},
			{Resource: "management.user", Action: "update", Effect: "allow"},
			{Resource: "management.user", Action: "delete", Effect: "allow"},
			{Resource: "management.tenant", Action: "read", Effect: "allow"},
			{Resource: "management.tenant", Action: "create", Effect: "allow"},
			{Resource: "management.tenant", Action: "update", Effect: "allow"},
			{Resource: "management.tenant", Action: "delete", Effect: "allow"},
			{Resource: "management.datatype", Action: "read", Effect: "allow"},
			{Resource: "management.datatype", Action: "create", Effect: "allow"},
			{Resource: "management.datatype", Action: "update", Effect: "allow"},
			{Resource: "management.datatype", Action: "delete", Effect: "allow"},
			{Resource: "management.event", Action: "read", Effect: "allow"},
			{Resource: "management.event", Action: "send", Effect: "allow"},

			// Other services full access
			{Resource: "inventory.*", Action: "*", Effect: "allow"},
			{Resource: "workflow.*", Action: "*", Effect: "allow"},
			{Resource: "file.*", Action: "*", Effect: "allow"},
			{Resource: "picking.*", Action: "*", Effect: "allow"},
			{Resource: "receiving.*", Action: "*", Effect: "allow"},
		},
	},
	{
		Name:        "Management-Editor",
		Description: "Can manage roles, groups, and policies in the management service",
		Policies: []Policy{
			{Resource: "management.role", Action: "read", Effect: "allow"},
			{Resource: "management.role", Action: "create", Effect: "allow"},
			{Resource: "management.role", Action: "update", Effect: "allow"},
			{Resource: "management.group", Action: "read", Effect: "allow"},
			{Resource: "management.group", Action: "create", Effect: "allow"},
			{Resource: "management.group", Action: "update", Effect: "allow"},
			{Resource: "management.policy", Action: "read", Effect: "allow"},
			{Resource: "management.policy", Action: "create", Effect: "allow"},
			{Resource: "management.policy", Action: "update", Effect: "allow"},
			{Resource: "management.user", Action: "read", Effect: "allow"},
			{Resource: "management.tenant", Action: "read", Effect: "allow"},
			{Resource: "management.datatype", Action: "read", Effect: "allow"},
			{Resource: "management.datatype", Action: "create", Effect: "allow"},
			{Resource: "management.datatype", Action: "update", Effect: "allow"},
			{Resource: "management.event", Action: "read", Effect: "allow"},
			{Resource: "management.event", Action: "send", Effect: "allow"},
		},
	},
	{
		Name:        "Management-Viewer",
		Description: "Read-only access to management service",
		Policies: []Policy{
			{Resource: "management.role", Action: "read", Effect: "allow"},
			{Resource: "management.group", Action: "read", Effect: "allow"},
			{Resource: "management.policy", Action: "read", Effect: "allow"},
			{Resource: "management.user", Action: "read", Effect: "allow"},
			{Resource: "management.tenant", Action: "read", Effect: "allow"},
		},
	},
}

// EnsureSystemRoles creates system roles if they don't exist for a tenant
func EnsureSystemRoles(ctx context.Context, client *ent.Client, tenantID uuid.UUID, createdBy uuid.UUID) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, systemRole := range SystemRoles {
		// Check if role already exists
		exists, err := tx.Role.Query().
			Where(
				role.Name(systemRole.Name),
				role.TenantID(tenantID),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to check role existence: %w", err)
		}

		if exists {
			log.Debug().
				Str("role", systemRole.Name).
				Str("tenant_id", tenantID.String()).
				Msg("System role already exists, skipping")
			continue
		}

		// Create role
		createdRole, err := tx.Role.Create().
			SetName(systemRole.Name).
			SetDescription(systemRole.Description).
			SetTenantID(tenantID).
			SetCreatedBy(createdBy).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create role %s: %w", systemRole.Name, err)
		}

		// Create policies for the role
		for _, policy := range systemRole.Policies {
			_, err = tx.AccessPolicy.Create().
				SetResource(policy.Resource).
				SetAction(policy.Action).
				SetEffect(policy.Effect).
				SetTenantID(tenantID).
				SetCreatedBy(createdBy).
				SetRole(createdRole).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to create policy for %s: %w", systemRole.Name, err)
			}
		}

		log.Info().
			Str("role", systemRole.Name).
			Str("tenant_id", tenantID.String()).
			Int("policies", len(systemRole.Policies)).
			Msg("Created system role with policies")
	}

	return tx.Commit()
}

// RunBootstrap initializes all system roles for all tenants
func RunBootstrap(ctx context.Context, client *ent.Client) error {
	// Get all unique tenant IDs from tenants
	tenants, err := client.Tenant.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch tenants: %w", err)
	}

	log.Info().Int("tenants_count", len(tenants)).Msg("Starting bootstrap for all tenants")

	for _, tenant := range tenants {
		// Use system user (uuid.Max) as creator for bootstrap
		err := EnsureSystemRoles(ctx, client, tenant.ID, uuid.Max)
		if err != nil {
			log.Error().
				Str("tenant_id", tenant.ID.String()).
				Err(err).
				Msg("Failed to bootstrap system roles for tenant")
			// Continue with other tenants
			continue
		}
	}

	log.Info().Msg("Bootstrap completed successfully")
	return nil
}

// TODO: This bootstrap service should be called:
// 1. On management service startup (with a flag to enable/disable)
// 2. When a new tenant is onboarded
// 3. Via a management API endpoint (for manual execution)
//
// Example usage in main.go:
// if config.BootstrapEnabled {
//     err := bootstrap.RunBootstrap(ctx, entClient)
//     if err != nil {
//         log.Error().Err(err).Msg("Bootstrap failed")
//     }
// }
