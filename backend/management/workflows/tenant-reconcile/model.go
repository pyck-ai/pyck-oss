package tenantreconcile

import "github.com/google/uuid"

// Operation kinds used by DispatchLifecycleActivity to select which
// corrective workflow to dispatch. String-typed so the activity input
// stays JSON-friendly for Temporal serialization.
const (
	OpDisable = "disable"
	OpRestore = "restore"
)

type (
	// TenantReconcileWorkflowInput is the orchestrator input. Currently empty;
	// kept as a struct for forward compatibility (e.g. filter by tenant ID or
	// batch size in the future).
	TenantReconcileWorkflowInput struct{}

	// TenantReconcileWorkflowOutput reports the drift sizes and how many
	// corrective dispatches were issued.
	TenantReconcileWorkflowOutput struct {
		Disabled   int
		Restored   int
		Dispatched int
		Skipped    int
	}

	// ComputeDriftActivityInput is the input to ComputeDriftActivity; empty.
	ComputeDriftActivityInput struct{}

	// TenantRef is a minimal pointer to a tenant that needs correction.
	TenantRef struct {
		TenantID  uuid.UUID
		IdpOrgRef string
	}

	// DriftSet enumerates tenants whose DB and Zitadel states disagree.
	//
	// ToDisable: DB has deleted_at set, Zitadel org is still ACTIVE →
	//            need to dispatch DisableTenantWorkflow.
	// ToRestore: Zitadel org is INACTIVE, DB has deleted_at cleared →
	//            need to dispatch RestoreTenantWorkflow.
	//
	// Both slices are expected to be small (usually empty) in steady state.
	DriftSet struct {
		ToDisable []TenantRef
		ToRestore []TenantRef
	}

	// DispatchLifecycleActivityInput drives a single corrective dispatch.
	DispatchLifecycleActivityInput struct {
		TenantID  uuid.UUID
		IdpOrgRef string
		Op        string // OpDisable | OpRestore
	}

	// DispatchLifecycleActivityOutput reports whether a corrective
	// workflow was started or deferred because one is already running.
	DispatchLifecycleActivityOutput struct {
		Dispatched bool
		Deferred   bool
	}
)
