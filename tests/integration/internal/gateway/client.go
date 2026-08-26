// Package gateway wraps the management GraphQL client for use from
// integration suites. All calls use the shared bearer-token interceptor
// — pass either the system service token or a freshly minted PAT.
package gateway

import (
	"context"
	nethttp "net/http"

	"github.com/gqlgo/gqlgenc/clientv2"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	workflowapi "github.com/pyck-ai/pyck/backend/workflow/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
)

// NewClient builds a typed management API client wired to the gateway
// URL with a fixed bearer token. Exported so test packages can construct
// their own request-shaped helpers without duplicating the auth setup.
func NewClient(cfg *config.Config, token string) managementapi.Client {
	return managementapi.NewClient(
		nethttp.DefaultClient,
		cfg.GatewayURL,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		bearerInterceptor(token),
	)
}

// NewClientForTenant is NewClient plus an X-Pyck-Tenant-Id header on
// every request, scoping the call to that tenant. Required by the
// lifecycle mutations (deleteTenant, restoreTenant, setTenantExpiry)
// which derive their target from req.MutationTenantID after the M1
// schema change — the input no longer carries an id.
func NewClientForTenant(cfg *config.Config, token, tenantID string) managementapi.Client {
	return managementapi.NewClient(
		nethttp.DefaultClient,
		cfg.GatewayURL,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		bearerInterceptor(token),
		tenantIDInterceptor(tenantID),
	)
}

// NewInventoryClient is the inventory-API equivalent of NewClient — a
// probe into a non-management subgraph. Management authenticates with an
// in-process org validator while every other service goes through the
// HTTP OrganizationValidator, so a suite asserting on the auth path needs
// a vantage point on each.
func NewInventoryClient(cfg *config.Config, token string) inventoryapi.Client {
	return inventoryapi.NewClient(
		nethttp.DefaultClient,
		cfg.GatewayURL,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		bearerInterceptor(token),
	)
}

// NewInventoryClientForTenant is NewInventoryClient plus an X-Pyck-Tenant-Id
// header, scoping every call to that tenant. Inventory mutations derive their
// operative tenant from req.MutationTenantID (the header), so a suite that
// drives writes as a specific tenant — e.g. a cross-tenant isolation probe —
// must set it explicitly.
//
//nolint:ireturn // returns the generated inventory API client interface, mirroring NewInventoryClient
func NewInventoryClientForTenant(cfg *config.Config, token, tenantID string) inventoryapi.Client {
	return inventoryapi.NewClient(
		nethttp.DefaultClient,
		cfg.GatewayURL,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		bearerInterceptor(token),
		tenantIDInterceptor(tenantID),
	)
}

// NewPickingClientForTenant is the picking-API client with an
// X-Pyck-Tenant-Id header. Picking reads resolve their datatype per tenant, so
// the header is not optional for a suite that queries by a datatype slug.
//
//nolint:ireturn // returns the generated picking API client interface, mirroring NewInventoryClientForTenant
func NewPickingClientForTenant(cfg *config.Config, token, tenantID string) pickingapi.Client {
	return pickingapi.NewClient(
		nethttp.DefaultClient,
		cfg.GatewayURL,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		bearerInterceptor(token),
		tenantIDInterceptor(tenantID),
	)
}

// NewWorkflowClient is the workflow-API equivalent of NewClient — used
// by tests that register / unregister workflows with the pyck workflow
// service (the same call workflowsdk's worker makes during Start).
func NewWorkflowClient(cfg *config.Config, token string) workflowapi.Client {
	return workflowapi.NewClient(
		nethttp.DefaultClient,
		cfg.GatewayURL,
		&clientv2.Options{ParseDataAlongWithErrors: true},
		bearerInterceptor(token),
	)
}

// bearerInterceptor returns a gqlgenc request interceptor that puts a
// fixed Bearer token in the Authorization header.
func bearerInterceptor(token string) clientv2.RequestInterceptor {
	return func(ctx context.Context, r *nethttp.Request, gqlInfo *clientv2.GQLRequestInfo, res any, next clientv2.RequestInterceptorFunc) error {
		r.Header.Set("Authorization", "Bearer "+token)
		return next(ctx, r, gqlInfo, res)
	}
}

// tenantIDInterceptor sets the X-Pyck-Tenant-Id header. Pyck's tenant
// middleware reads this and exposes it to resolvers as
// req.MutationTenantID(), which the lifecycle mutations use as the
// target tenant.
func tenantIDInterceptor(tenantID string) clientv2.RequestInterceptor {
	return func(ctx context.Context, r *nethttp.Request, gqlInfo *clientv2.GQLRequestInfo, res any, next clientv2.RequestInterceptorFunc) error {
		r.Header.Set("X-Pyck-Tenant-Id", tenantID)
		return next(ctx, r, gqlInfo, res)
	}
}
