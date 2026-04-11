package registertenant

import "github.com/google/uuid"

type RegisterTenantWorkflowInput struct {
	Name           string
	AdminUsername  string
	AdminEmail     string
	AdminFirstName string
	AdminLastName  string
	AdminPassword  string
	Data           map[string]any
	// K8s worker deployment config
	WorkerImage    string            // e.g. "ghcr.io/pyck-ai/pyck-go/worker:latest"
	WorkerReplicas int32             // number of worker replicas (defaults to 2 if zero)
	WorkerEnvVars  map[string]string // env vars stripped of PYCK_FLAVOUR_GO_ prefix
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

type SetOrgMetadataActivityInput struct {
	OrganizationID string
	Data           map[string]any
}

type CreateTenantInDbActivityInput struct {
	OrganizationID string
	Name           string
	Data           map[string]any
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

type createK8sTenantSecretInput struct {
	Namespace   string
	SecretName  string
	SecretKey   string
	Token       string
	IsInCluster bool
	ConfigPath  string
}

type upsertK8sWorkersNamespaceInput struct {
	Namespace   string
	IsInCluster bool
	ConfigPath  string
}

type createK8sTemporalConnectionInput struct {
	Namespace   string
	Name        string
	HostPort    string
	IsInCluster bool
	ConfigPath  string
}

type createK8sWorkerDeploymentInput struct {
	Namespace           string
	Name                string
	ConnectionName      string
	TemporalNamespace   string
	Image               string
	TenantID            string
	EnvVars             map[string]string // plain env vars (from PYCK_FLAVOUR_GO_ prefix)
	Replicas            int32
	ImagePullSecretName string
	APIKeySecretName    string
	APIKeySecretKey     string
	IsInCluster         bool
	ConfigPath          string
}
