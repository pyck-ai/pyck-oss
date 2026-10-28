package workflows

import "github.com/google/uuid"

const (
	// tenantLifecycleWorkflowIDPrefix is the shared prefix for disable and
	// restore workflow IDs. Both operations use the same workflow ID per
	// tenant so Temporal's WorkflowIDConflictPolicy serializes them: at
	// most one lifecycle workflow may run per tenant at any given time.
	tenantLifecycleWorkflowIDPrefix = "tenant-lifecycle-"

	// registerTenantWorkflowIDPrefix keys RegisterTenantWorkflow executions
	// by tenant. The tenant ID is chosen by the resolver before the row is
	// committed, so the ID is known to the NATS trigger (start), the
	// resolver (wait for the result) and an operator (retry) without any
	// lookup.
	registerTenantWorkflowIDPrefix = "register-tenant-"
)

// TenantLifecycleWorkflowID returns the shared workflow ID for the
// tenant's disable/restore lifecycle. Both the NATS trigger
// (events/tenants/trigger.go) and the reconcile sweeper
// (workflows/tenant-reconcile) use this so that a concurrent
// disable+restore cannot race — the second dispatch gets
// WorkflowExecutionAlreadyStarted from Temporal and is deferred or
// rejected.
func TenantLifecycleWorkflowID(tenantID uuid.UUID) string {
	return tenantLifecycleWorkflowIDPrefix + tenantID.String()
}

// RegisterTenantWorkflowID returns the workflow ID of the registration
// run for the tenant. The tenant-lifecycle trigger starts the workflow
// under this ID on the pending row's create event, and the resolver
// waits for the result under it.
func RegisterTenantWorkflowID(tenantID uuid.UUID) string {
	return registerTenantWorkflowIDPrefix + tenantID.String()
}
