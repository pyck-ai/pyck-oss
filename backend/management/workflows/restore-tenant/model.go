package restoretenant

import "github.com/google/uuid"

type (
	// RestoreTenantWorkflowInput is passed to the restore workflow by the
	// tenant-lifecycle NATS subscriber after it observes a tenant whose
	// deleted_at went from set to cleared.
	RestoreTenantWorkflowInput struct {
		TenantID  uuid.UUID
		IdpOrgRef string
	}

	// RestoreTenantWorkflowOutput is the workflow's result envelope.
	RestoreTenantWorkflowOutput struct {
		TenantID uuid.UUID
		Success  bool
	}

	// ActivateZitadelOrgActivityInput is the input for the activity that
	// reactivates the Zitadel org after a tenant restore.
	ActivateZitadelOrgActivityInput struct {
		TenantID  uuid.UUID
		IdpOrgRef string
	}
)
