package registertenant

import (
	"time"

	"github.com/google/uuid"
)

type RegisterTenantWorkflowInput struct {
	Name           string
	AdminUsername  string
	AdminEmail     string
	AdminFirstName string
	AdminLastName  string
	AdminPassword  string
	Data           map[string]any
	// Optional expiry. When non-nil, written directly to the
	// tenant.expires_at column by CreateTenantInDbActivity so the
	// periodic tenant-expiry-check workflow soft-deletes the tenant
	// once the timestamp is reached. Not propagated to Zitadel
	// metadata — DB is the source of truth for this field.
	ExpiresAt *time.Time
}

type RegisterTenantWorkflowOutput struct {
	OrganizationID string
	TenantID       uuid.UUID
	LoginName      string
	UserID         string
	UserRoles      []string
}

type createZitadelUserActivityInput struct {
	OrganizationID string
	Username       string
	Email          string
	FirstName      string
	LastName       string
	Password       string
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
	Name string
}

type CreateTenantActivityOutput struct {
	OrganizationID    string
	TenantID          uuid.UUID
	TemporalNamespace string
}

type DeleteTenantActivityInput struct {
	OrganizationID string
}

type Grant struct {
	ID string
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
	GrantID        string
	Roles          []string
}

type AddDefaultDataTypesActivityInput struct {
	TenantID  uuid.UUID
	UserID    string
	UserRoles []string
}

type createTemporalNamespaceInput struct {
	TemporalUrl string
	Namespace   string
}

// SetOrgMetadataActivityInput drives the round-trip that sets the
// caller-supplied `Data` keys on the Zitadel organization metadata.
// Tenant expiry is NOT carried here — it's a DB-only field written
// by CreateTenantInDbActivity (registration) and the
// setTenantExpiry resolver (post-registration updates).
type SetOrgMetadataActivityInput struct {
	OrganizationID string
	Data           map[string]any
}

type CreateTenantInDbActivityInput struct {
	OrganizationID string
	Name           string
	Data           map[string]any
	// Optional initial expiry. Written directly to the
	// tenant.expires_at column when non-nil. The DB column is the
	// source of truth that tenant-expiry-check reads.
	ExpiresAt *time.Time
}

type DeleteTenantFromDbActivityInput struct {
	OrganizationID string
}

type TriggerTenantSyncActivityInput struct {
	OrganizationID string
}

type createTenantServiceUserInput struct {
	OrganizationID string
}

type CreateTenantServiceUserOutput struct {
	UserID string
	Token  string
}

type storeTenantWorkerSecretInput struct {
	TenantID uuid.UUID
	Key      string
	Value    string
}

type CreateTenantWorkerDeploymentInput struct {
	Name              string
	TenantID          uuid.UUID
	TemporalNamespace string
	Extension         string
}
