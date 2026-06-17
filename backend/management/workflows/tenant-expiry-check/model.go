// Package tenantexpirycheck implements a periodic Temporal-scheduled
// sweeper that soft-deletes tenants whose expires_at has passed.
//
// It is the action half of the expiry pipeline:
//
//  1. registerTenant or setTenantExpiry writes the expiry directly
//     to the tenant.expires_at column. The DB is the single source
//     of truth — no Zitadel round-trip.
//  2. tenant-expiry-check (this package) selects tenants where
//     expires_at <= now AND deleted_at IS NULL and triggers the
//     existing soft-delete pipeline by setting deleted_at — the same
//     side-effect the disableTenant resolver performs. The Ent
//     MutationEventHook then writes an outbox row and emits the NATS
//     event that drives DisableTenantWorkflow + revocation-cache
//     propagation.
package tenantexpirycheck

import "github.com/google/uuid"

type (
	// TenantExpiryCheckWorkflowInput is the orchestrator input. Empty
	// today; kept as a struct so we can add per-sweep options
	// (max-batch, lookback bound, …) without breaking the schedule.
	TenantExpiryCheckWorkflowInput struct{}

	// TenantExpiryCheckWorkflowOutput reports the size of the candidate
	// set and how many soft-deletes succeeded vs. failed in the sweep.
	TenantExpiryCheckWorkflowOutput struct {
		Found    int
		Disabled int
		Failed   int
	}

	// FindExpiredTenantsActivityInput is the input to FindExpiredTenantsActivity.
	FindExpiredTenantsActivityInput struct{}

	// ExpiredTenantRef is a minimal pointer to a tenant that is past
	// its expiry and still active. Carries the org id only so the
	// soft-delete activity can log it without re-reading the tenant.
	ExpiredTenantRef struct {
		TenantID  uuid.UUID
		IdpOrgRef string
	}

	// SoftDeleteExpiredTenantActivityInput drives one soft-delete pass.
	SoftDeleteExpiredTenantActivityInput struct {
		TenantID uuid.UUID
	}
)
