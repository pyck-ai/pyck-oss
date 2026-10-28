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

	// ResumeTenantWorkerActivityInput is the input for the activity that
	// scales the tenant's worker deployment back up.
	ResumeTenantWorkerActivityInput struct {
		TenantID uuid.UUID
	}

	// ResumeTenantWorkerActivityOutput records what the resume did, for the
	// workflow history.
	ResumeTenantWorkerActivityOutput struct {
		// Skipped is empty when the worker was resumed, else
		// SkipTenantDisabled or one of registertenant's Skip* reasons.
		Skipped string
		// Replicas is the count worker-api reports the worker runs at now.
		Replicas int64
		// ReplicasDefaulted is true when the deployment recorded no deployed
		// size and worker-api used its default.
		ReplicasDefaulted bool
	}
)
