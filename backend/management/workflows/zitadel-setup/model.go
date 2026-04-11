package zitadelsetup

const (
	zitadelAdminFirstName = "PYCK"
	zitadelAdminLastName  = "Administrator"
	zitadelProjectName    = "PYCK"
	zitadelApiAppName     = "PYCK-API"

	zitadelServiceUserUsername = "service-user"
	zitadelServiceUserName     = "Service User"
)

type ZitadelServiceInfoInput struct {
	Issuer         string
	SdkClientAPI   string
	JwtProfilePath string
	TlsInsecure    bool
}

type ZitadelAdminProfile struct {
	Username string
	Email    string
	Password string
}

type ZitadelAppInput struct {
	Name               string
	AppType            string
	AuthMethodType     string
	K8sSecret          string
	LoginRedirectUrls  []string
	LogoutRedirectUrls []string
	IsDevMode          bool
	GenerateKey        bool
}

type ZitadelK8sConfigInput struct {
	CreateSecrets     bool
	InCluster         bool
	Namespace         string
	ConfigPath        string
	SecretsName       string
	TenantSecretsName string
	AppsSecretsName   string
}

type ZitadelOrganizationInput struct {
	Name string
}

type ZitadelActionTargets struct {
	Name                    string
	WebHookUrl              string
	InterruptWebHookOnError bool
	WithResponse            bool
	TriggerOnActions        []string
}

type ZitadelSetupWorkflowInput struct {
	ServiceInfo        ZitadelServiceInfoInput
	AdminProfile       ZitadelAdminProfile
	Apps               []ZitadelAppInput
	TenantOrganization ZitadelOrganizationInput
	ActionTargets      []ZitadelActionTargets
	K8sConfig          ZitadelK8sConfigInput
}

type ServiceInfoOutput struct {
	Audience     string
	ServiceToken string
}

type OrganizationOutput struct {
	ID string
}

type ProjectOutput struct {
	ID string
}

type AdminWebAppOutput struct {
	ID       string
	ClientID string
}

type TenantSetupOutput struct {
	OrganizationID     string
	TenantID           string
	ApiToken           string
	ServiceWorkerToken string
}

type ZitadelSetupWorkflowOutput struct {
	ServiceInfo  ServiceInfoOutput
	AdminProfile ZitadelAdminProfile
	Organization OrganizationOutput
	Project      ProjectOutput
	CreatedApps  []app
	TenantSetup  TenantSetupOutput
	K8sSecrets   createK8sSecretsOutput
}

type zitadelClientInput struct {
	Issuer         string
	SdkClientAPI   string
	JwtProfilePath string
	OrganizationID string
	TlsInsecure    bool
}

type createUserInput struct {
	ZitadelClientInput zitadelClientInput
	Username           string
	FirstName          string
	LastName           string
	Email              string
	IsEmailVerified    bool
	Password           string
	ChangePassword     bool
}

type setUserAdminInput struct {
	ZitadelClientInput zitadelClientInput
	UserID             string
}

type addProjectInput struct {
	ZitadelClientInput zitadelClientInput
	ProjectName        string
}

type addProjectRolesInput struct {
	ZitadelClientInput zitadelClientInput
	ProjectID          string
}

type addAppToProjectInput struct {
	ZitadelClientInput zitadelClientInput
	ProjectID          string
	App                ZitadelAppInput
}

type addJsonAppKeyInput struct {
	ZitadelClientInput zitadelClientInput
	ProjectID          string
	AppID              string
}

type addServiceUserInput struct {
	ZitadelClientInput zitadelClientInput
	Username           string
	Name               string
}

type addServiceUserTokenInput struct {
	ZitadelClientInput zitadelClientInput
	ServiceUserID      string
}

type addServiceGrantForProjectInput struct {
	ZitadelClientInput zitadelClientInput
	ProjectID          string
	ServiceUserID      string
	Roles              []string
}

type addUserGrantInput struct {
	ZitadelClientInput zitadelClientInput
	ProjectID          string
	UserID             string
	GrantID            string
	Roles              []string
}

type addOrganizationInput struct {
	ZitadelClientInput zitadelClientInput
	OrganizationName   string
}

type addProjectGrantInput struct {
	ZitadelClientInput zitadelClientInput
	ProjectID          string
	OrganizationID     string
	Roles              []string
}

type createK8sSecretsInput struct {
	Namespace          string
	ConfigPath         string
	IsInCluster        bool
	ZitadelSetupOutput *ZitadelSetupWorkflowOutput
}

type createK8sSecretsOutput struct {
	AppSecretsDataMap map[string]map[string][]byte
}

type jsonAppKey struct {
	KeyID    string
	JsonBody string
}

type serviceToken struct {
	ID    string
	Token string
}

type organization struct {
	ID string
}

type user struct {
	ID        string
	LoginName string
}

type project struct {
	ID string
}

type grant struct {
	ID string
}

type app struct {
	ID           string
	ClientID     string
	ClientSecret string
	Name         string
	K8sSecret    string
	KeyFileJSON  string
}

type addTargetInput struct {
	ZitadelClientInput      zitadelClientInput
	Name                    string
	WebHookUrl              string
	InterruptWebHookOnError bool
}

type addTargetOutput struct {
	TargetID string
}

type addActionsOnTargetInput struct {
	ZitadelClientInput zitadelClientInput
	TargetID           string
	WithResponse       bool
	TriggerOnActions   []string
}
