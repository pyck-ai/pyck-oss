package core

import (
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
)

// SelfTenantSchemas returns the Ent schemas whose entity ID doubles as the
// tenant ID of the events they emit: Tenant has no tenant_id column, so the
// tenant row itself is the tenant. Every MutationEventHook wiring for the
// management schema (the service and its tests) passes this as
// HookConfig.SelfTenantSchemas / EventSystemConfig.SelfTenantSchemas.
func SelfTenantSchemas() []string {
	return []string{ent.TypeTenant}
}
