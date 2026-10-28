package registertenant

import (
	"github.com/google/uuid"

	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

// RegisterTenantWorkflowInput names the status=pending tenant row the
// workflow provisions. Everything else (the pre-chosen Zitadel org ID,
// name, data and the admin account) is read from that row by the first
// activity, so the input stays the same whoever starts the run: the
// tenant-lifecycle trigger on the row's create event, or an operator
// re-running a failed registration.
type RegisterTenantWorkflowInput struct {
	TenantID uuid.UUID
}

// RegisterTenantWorkflowOutput is the result of a registration run.
// Skipped is true when the row was not pending at the start (already
// done, marked error, or gone) and nothing was provisioned.
type RegisterTenantWorkflowOutput struct {
	OrganizationID string
	TenantID       uuid.UUID
	LoginName      string
	UserID         string
	UserRoles      []string
	Skipped        bool
}

// LoadTenantRegistrationActivityInput names the tenant row to read.
type LoadTenantRegistrationActivityInput struct {
	TenantID uuid.UUID
}

// LoadTenantRegistrationActivityOutput is the non-secret part of the
// tenant row the workflow drives provisioning from. Status is empty when
// the row does not exist (or is soft-deleted); the admin account and its
// password hash are deliberately absent, CreateZitadelUserActivity reads
// them from the row itself so they never enter workflow history.
type LoadTenantRegistrationActivityOutput struct {
	Status         enttenant.Status
	OrganizationID string
	Name           string
	Data           map[string]any
}

type createZitadelUserActivityInput struct {
	TenantID uuid.UUID
}

type CreateZitadelUserActivityOutput struct {
	UserID    string
	LoginName string
}

type setUserAsOrganizationAdmin struct {
	OrganizationID string
	UserID         string
}

type createTenantActivityInput struct {
	Name           string
	OrganizationID string
	TenantID       uuid.UUID
}

type CreateTenantActivityOutput struct {
	OrganizationID    string
	TenantID          uuid.UUID
	TemporalNamespace string
}

type addProjectGrantsInput struct {
	ProjectID      string
	OrganizationID string
	Roles          []string
}

type addUserGrantInput struct {
	OrganizationID string
	ProjectID      string
	UserID         string
	Roles          []string
}

type AddDefaultDataTypesActivityInput struct {
	TenantID uuid.UUID
	UserID   string
}

type createTemporalNamespaceInput struct {
	TemporalUrl string
	Namespace   string
}

// SetOrgMetadataActivityInput drives the round-trip that sets the
// caller-supplied `Data` keys on the Zitadel organization metadata.
// Tenant expiry is NOT carried here — it's a DB-only field written
// by the registerTenant resolver (registration) and the
// setTenantExpiry resolver (post-registration updates).
type SetOrgMetadataActivityInput struct {
	OrganizationID string
	Data           map[string]any
}

// SetTenantStatusActivityInput names the pending tenant row to close and
// the terminal status to close it with: done once provisioning finished,
// error when a step failed for good.
type SetTenantStatusActivityInput struct {
	TenantID uuid.UUID
	Status   enttenant.Status
}

// DeactivateRetiredOrganizationActivityInput names the retired tenant row
// whose organization the run deactivates.
type DeactivateRetiredOrganizationActivityInput struct {
	TenantID uuid.UUID
}

type TriggerTenantSyncActivityInput struct {
	OrganizationID string
}

// provisionTenantWorkerIdentityInput names the organization the worker's
// Zitadel service user is created in and the tenant secret its credential
// is pushed to. The credential itself is never an input or an output:
// ProvisionTenantWorkerIdentityActivity mints and stores it inside one
// call, so it never enters workflow history.
type provisionTenantWorkerIdentityInput struct {
	OrganizationID string
	TenantID       uuid.UUID
	// Key is the tenant-secret name worker-api projects into the worker's
	// Connection.
	Key string
}

// ProvisionTenantWorkerIdentityOutput carries only the worker's Zitadel
// user ID, which the user-grant step that follows acts on.
type ProvisionTenantWorkerIdentityOutput struct {
	UserID string
}

type CreateTenantWorkerDeploymentInput struct {
	Name              string
	TenantID          uuid.UUID
	TemporalNamespace string
	Extension         string
}
