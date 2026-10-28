package gateway

import (
	"context"
	"errors"
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
// the freshly-created Zitadel sub-org, Status is the registration state
// the row was in when the mutation returned ("done" once provisioning
// finished), Data is the tenant.data map as the mutation returned it
// (nil when the input carried none).
type RegisteredTenant struct {
	ID        string
	IdpOrgRef string
	Name      string
	Status    string
	Data      map[string]any
}

// ErrTenantNotFound reports that no live tenant carries the requested name.
var ErrTenantNotFound = errors.New("no live tenant with that name")

// FindTenantByName returns the live tenant carrying exactly the given
// name, or ErrTenantNotFound when none does. Names are unique across
// every row, soft-deleted ones included, compared by their Zitadel-style
// case fold (the unique index on name_key enforces it), so at most one can
// match. The query
// sees live rows only: a retired tenant reads as not found, yet still holds
// its name, so registering that name again is refused until the stack is
// wiped.
//
// Callers use it to make provisioning idempotent: a fixture tenant that
// outlives one run is reused rather than re-registered, which would fail on
// the name.
func FindTenantByName(ctx context.Context, cfg *config.Config, name string) (*RegisteredTenant, error) {
	c := NewClient(cfg, cfg.ServiceToken)
	first := 1
	resp, err := c.GetTenants(ctx, managementapi.GetTenantsArgs{
		First: &first,
		Where: &managementapi.TenantWhereInput{Name: &name},
	})
	if err != nil {
		return nil, fmt.Errorf("getTenants by name %q: %w", name, err)
	}
	tenants := resp.GetTenants()
	if tenants == nil {
		return nil, fmt.Errorf("%w: %q", ErrTenantNotFound, name)
	}
	for _, edge := range tenants.GetEdges() {
		if edge == nil || edge.GetNode() == nil {
			continue
		}
		node := edge.GetNode()
		found := &RegisteredTenant{
			ID:        node.GetID(),
			IdpOrgRef: node.GetIdpOrgRef(),
			Name:      node.GetName(),
		}
		if status := node.GetStatus(); status != nil {
			found.Status = string(*status)
		}
		return found, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrTenantNotFound, name)
}

// RegisterTenant calls the gateway's registerTenant mutation with the
// system-role service token. The resolver gates on authentication only,
// so the token's role does not matter; it stays the system one because
// the suites read back what the registration wrote.
func RegisterTenant(ctx context.Context, cfg *config.Config, t *fixtures.Tenant) (*RegisteredTenant, error) {
	return RegisterTenantWithExpiry(ctx, cfg, t, nil)
}

// RegisterTenantWithExpiry is RegisterTenant plus an optional expiry
// passed through to RegisterTenantInput.expiresAt. The value is
// written directly to tenant.expires_at by the registerTenant
// resolver's pending-row insert; tenant-expiry-check soft-deletes
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
			Data:           t.Data,
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
		Status:    got.GetStatus().String(),
		Data:      got.GetData(),
	}, nil
}

// SetTenantExpiry calls the management.setTenantExpiry mutation.
// Passing nil for expiresAt clears the existing expiry. The target
// tenant is named by input.tenantId; the resolver requires a system
// caller (ServiceToken).
func SetTenantExpiry(ctx context.Context, cfg *config.Config, tenantID string, expiresAt *time.Time) error {
	return SetTenantExpiryAs(ctx, cfg, cfg.ServiceToken, tenantID, expiresAt)
}

// SetTenantExpiryAs is SetTenantExpiry as an arbitrary bearer token. The
// expiry schedules the operator's disable, so the only token expected to
// succeed is a system one; the parameter exists so a suite can assert that
// a tenant's own admin is refused.
func SetTenantExpiryAs(ctx context.Context, cfg *config.Config, token, tenantID string, expiresAt *time.Time) error {
	return SetTenantExpiryFrom(ctx, cfg, token, tenantID, tenantID, expiresAt)
}

// SetTenantExpiryFrom is SetTenantExpiryAs with the request's own tenant
// header (headerTenantID) decoupled from the mutation's target
// (targetTenantID). The two used to be the same value by construction
// (the resolver read its target from the header) so a suite can only
// assert that the target is the argument by setting them apart.
func SetTenantExpiryFrom(ctx context.Context, cfg *config.Config, token, headerTenantID, targetTenantID string, expiresAt *time.Time) error {
	id, err := uuid.Parse(targetTenantID)
	if err != nil {
		return fmt.Errorf("setTenantExpiry: parse tenant id: %w", err)
	}
	c := NewClientForTenant(cfg, token, headerTenantID)
	resp, err := c.SetTenantExpiry(ctx, managementapi.SetTenantExpiryArgs{
		Input: managementmodel.SetTenantExpiryInput{TenantID: id, ExpiresAt: expiresAt},
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
// the soft-delete half of the tenant lifecycle. Target tenant is named
// by input.tenantId; the resolver requires ROLE_ADMIN on it; a done
// tenant's own admin passes, as does ServiceToken.
func DeleteTenant(ctx context.Context, cfg *config.Config, tenantID string) error {
	return DeleteTenantAs(ctx, cfg, cfg.ServiceToken, tenantID)
}

// DeleteTenantAs is DeleteTenant as an arbitrary bearer token. A tenant
// may disable itself, so the parameter exists so a suite can assert that
// a tenant's own admin is admitted, the one lifecycle mutation where it
// is.
func DeleteTenantAs(ctx context.Context, cfg *config.Config, token, tenantID string) error {
	return DeleteTenantFrom(ctx, cfg, token, tenantID, tenantID)
}

// DeleteTenantFrom is DeleteTenantAs with the request's own tenant header
// (headerTenantID) decoupled from the mutation's target (targetTenantID),
// so a suite can assert that the role gate answers about the named tenant
// rather than about the caller's header.
func DeleteTenantFrom(ctx context.Context, cfg *config.Config, token, headerTenantID, targetTenantID string) error {
	id, err := uuid.Parse(targetTenantID)
	if err != nil {
		return fmt.Errorf("deleteTenant: parse tenant id: %w", err)
	}
	c := NewClientForTenant(cfg, token, headerTenantID)
	resp, err := c.DeleteTenant(ctx, managementapi.DeleteTenantArgs{
		Input: managementmodel.DeleteTenantInput{TenantID: id},
	})
	if err != nil {
		return fmt.Errorf("deleteTenant: %w", err)
	}
	if d := resp.GetDeleteTenant(); d == nil || !d.GetSuccess() {
		return fmt.Errorf("deleteTenant: success=false")
	}
	return nil
}

// RestoreTenant calls the management.restoreTenant mutation. Idempotent
// on already-active tenants. Target tenant is named by input.tenantId;
// the resolver requires a system caller (ServiceToken).
func RestoreTenant(ctx context.Context, cfg *config.Config, tenantID string) error {
	return RestoreTenantWithExpiry(ctx, cfg, tenantID, nil)
}

// RestoreTenantWithExpiry is RestoreTenant plus an optional expiresAt
// that overrides the tenant's prior expires_at on the same UpdateOneID.
// Pass nil to leave the existing expiry untouched.
func RestoreTenantWithExpiry(ctx context.Context, cfg *config.Config, tenantID string, expiresAt *time.Time) error {
	return RestoreTenantAs(ctx, cfg, cfg.ServiceToken, tenantID, expiresAt)
}

// RestoreTenantAs is RestoreTenantWithExpiry as an arbitrary bearer token.
// Restore is an operator action, so the only token expected to succeed is a
// system one; the parameter exists so a suite can assert that a tenant's own
// admin is refused.
func RestoreTenantAs(ctx context.Context, cfg *config.Config, token, tenantID string, expiresAt *time.Time) error {
	return RestoreTenantFrom(ctx, cfg, token, tenantID, tenantID, expiresAt)
}

// RestoreTenantFrom is RestoreTenantAs with the request's own tenant
// header (headerTenantID) decoupled from the mutation's target
// (targetTenantID). Restoring a disabled tenant is the case that needs it:
// naming a disabled tenant in the header is denied by the tenant
// middleware, so an operator restoring one has to send a header of its
// own while the target rides in the input.
func RestoreTenantFrom(ctx context.Context, cfg *config.Config, token, headerTenantID, targetTenantID string, expiresAt *time.Time) error {
	id, err := uuid.Parse(targetTenantID)
	if err != nil {
		return fmt.Errorf("restoreTenant: parse tenant id: %w", err)
	}
	c := NewClientForTenant(cfg, token, headerTenantID)
	resp, err := c.RestoreTenant(ctx, managementapi.RestoreTenantArgs{
		Input: managementmodel.RestoreTenantInput{TenantID: id, ExpiresAt: expiresAt},
	})
	if err != nil {
		return fmt.Errorf("restoreTenant: %w", err)
	}
	if r := resp.GetRestoreTenant(); r == nil || !r.GetSuccess() {
		return fmt.Errorf("restoreTenant: success=false")
	}
	return nil
}
