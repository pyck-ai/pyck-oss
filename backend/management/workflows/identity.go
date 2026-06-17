package workflows

import "github.com/google/uuid"

// tenantLifecycleWorkflowIDPrefix is the shared prefix for disable and
// restore workflow IDs. Both operations use the same workflow ID per
// tenant so Temporal's WorkflowIDConflictPolicy serializes them — at
// most one lifecycle workflow may run per tenant at any given time.
const tenantLifecycleWorkflowIDPrefix = "tenant-lifecycle-"

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
