package disabletenant

import "github.com/google/uuid"

type (
	// DisableTenantWorkflowInput is passed to the disable workflow by the
	// tenant-lifecycle NATS subscriber after it observes a tenant whose
	// deleted_at went from unset to set.
	DisableTenantWorkflowInput struct {
		TenantID  uuid.UUID
		IdpOrgRef string
	}

	// DisableTenantWorkflowOutput is the workflow's result envelope.
	DisableTenantWorkflowOutput struct {
		TenantID uuid.UUID
		Success  bool
	}

	// DeactivateZitadelOrgActivityInput is the input for the activity that
	// deactivates the Zitadel org when a tenant is disabled.
	DeactivateZitadelOrgActivityInput struct {
		TenantID  uuid.UUID
		IdpOrgRef string
	}
)
