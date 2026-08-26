package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementmodel "github.com/pyck-ai/pyck/backend/management/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
)

// RegisteredTenant is the subset of the registerTenant response we care
// about downstream — ID is the row in management.tenants, IdpOrgRef is
// the freshly-created Zitadel sub-org.
type RegisteredTenant struct {
	ID        string
	IdpOrgRef string
	Name      string
}

// RegisterTenant calls the gateway's registerTenant mutation with the
// system-role service token. The resolver guard requires system role.
func RegisterTenant(ctx context.Context, cfg *config.Config, t *fixtures.Tenant) (*RegisteredTenant, error) {
	return RegisterTenantWithExpiry(ctx, cfg, t, nil)
}

// RegisterTenantWithExpiry is RegisterTenant plus an optional expiry
// passed through to RegisterTenantInput.expiresAt. The value is
// written directly to tenant.expires_at by the register-tenant
// workflow's CreateTenantInDbActivity; tenant-expiry-check soft-deletes
// once it's in the past.
func RegisterTenantWithExpiry(ctx context.Context, cfg *config.Config, t *fixtures.Tenant, expiresAt *time.Time) (*RegisteredTenant, error) {
	c := NewClient(cfg, cfg.ServiceToken)
	resp, err := c.RegisterTenant(ctx, managementapi.RegisterTenantArgs{
		Input: managementmodel.RegisterTenantInput{
			Name:           t.Name,
			AdminUsername:  t.AdminUsername,
			AdminEmail:     t.AdminEmail,
			AdminFirstName: t.AdminFirstName,
			AdminLastName:  t.AdminLastName,
			AdminPassword:  t.AdminPassword,
			ExpiresAt:      expiresAt,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("registerTenant: %w", err)
	}
	rt := resp.GetRegisterTenant()
	if rt == nil || !rt.GetSuccess() || rt.GetTenant() == nil {
		return nil, fmt.Errorf("registerTenant: success=false or tenant nil")
	}
	got := rt.GetTenant()
	return &RegisteredTenant{
		ID:        got.GetID(),
		IdpOrgRef: got.GetIdpOrgRef(),
		Name:      got.GetName(),
	}, nil
}

// SetTenantExpiry calls the management.setTenantExpiry mutation.
// Passing nil for expiresAt clears the existing expiry. After M1, the
// target tenant is taken from the X-Pyck-Tenant-Id header instead of
// the input; the resolver also enforces ROLE_ADMIN on that tenant
// (ServiceToken carries ROLE_SYSTEM which satisfies the gate).
func SetTenantExpiry(ctx context.Context, cfg *config.Config, tenantID string, expiresAt *time.Time) error {
	if _, err := uuid.Parse(tenantID); err != nil {
		return fmt.Errorf("setTenantExpiry: parse tenant id: %w", err)
	}
	c := NewClientForTenant(cfg, cfg.ServiceToken, tenantID)
	resp, err := c.SetTenantExpiry(ctx, managementapi.SetTenantExpiryArgs{
		Input: managementmodel.SetTenantExpiryInput{ExpiresAt: expiresAt},
	})
	if err != nil {
		return fmt.Errorf("setTenantExpiry: %w", err)
	}
	if r := resp.GetSetTenantExpiry(); r == nil || !r.GetSuccess() {
		return fmt.Errorf("setTenantExpiry: success=false")
	}
	return nil
}

// DeleteTenant calls the management.deleteTenant mutation. Triggers
// the soft-delete half of the tenant lifecycle. Target tenant is
// taken from the X-Pyck-Tenant-Id header; the resolver enforces
// ROLE_ADMIN on it (ServiceToken's ROLE_SYSTEM satisfies the gate).
func DeleteTenant(ctx context.Context, cfg *config.Config, tenantID string) error {
	if _, err := uuid.Parse(tenantID); err != nil {
		return fmt.Errorf("deleteTenant: parse tenant id: %w", err)
	}
	c := NewClientForTenant(cfg, cfg.ServiceToken, tenantID)
	resp, err := c.DeleteTenant(ctx)
	if err != nil {
		return fmt.Errorf("deleteTenant: %w", err)
	}
	if d := resp.GetDeleteTenant(); d == nil || !d.GetSuccess() {
		return fmt.Errorf("deleteTenant: success=false")
	}
	return nil
}

// RestoreTenant calls the management.restoreTenant mutation. Idempotent
// on already-active tenants. Target tenant is taken from the
// X-Pyck-Tenant-Id header; the resolver enforces ROLE_ADMIN on it
// (ServiceToken's ROLE_SYSTEM satisfies the gate).
func RestoreTenant(ctx context.Context, cfg *config.Config, tenantID string) error {
	return RestoreTenantWithExpiry(ctx, cfg, tenantID, nil)
}

// RestoreTenantWithExpiry is RestoreTenant plus an optional expiresAt
// that overrides the tenant's prior expires_at on the same UpdateOneID.
// Pass nil to leave the existing expiry untouched.
func RestoreTenantWithExpiry(ctx context.Context, cfg *config.Config, tenantID string, expiresAt *time.Time) error {
	if _, err := uuid.Parse(tenantID); err != nil {
		return fmt.Errorf("restoreTenant: parse tenant id: %w", err)
	}
	c := NewClientForTenant(cfg, cfg.ServiceToken, tenantID)
	resp, err := c.RestoreTenant(ctx, managementapi.RestoreTenantArgs{
		Input: managementmodel.RestoreTenantInput{ExpiresAt: expiresAt},
	})
	if err != nil {
		return fmt.Errorf("restoreTenant: %w", err)
	}
	if r := resp.GetRestoreTenant(); r == nil || !r.GetSuccess() {
		return fmt.Errorf("restoreTenant: success=false")
	}
	return nil
}
