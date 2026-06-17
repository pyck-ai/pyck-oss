package zitadel

import "github.com/pyck-ai/pyck/backend/bootstrap/internal/exporters"

type (
	// Configuration holds Zitadel bootstrap settings loaded from environment variables.
	Configuration struct {
		// Issuer is the OIDC issuer URL (e.g. http://localhost:8080)
		Issuer string `env:"PYCK_ZITADEL_ISSUER"`

		// OAuthURL is the Zitadel URL for OAuth2 token exchange (e.g. http://localhost:8080)
		OAuthURL string `env:"PYCK_ZITADEL_OAUTH_URL"`

		// GrpcAddr is the gRPC dial address (e.g. localhost:8080)
		GrpcAddr string `env:"PYCK_ZITADEL_GRPC_ADDR"`

		// Insecure determines if the connection to Zitadel should use HTTP instead of HTTPS.
		GrpcInsecure bool `env:"PYCK_ZITADEL_GRPC_INSECURE"`

		// Path to the key files
		KeyPath string `env:"PYCK_BOOTSTRAP_ZITADEL_KEY_PATH"`

		// Path to the .env file to update with exported credentials
		EnvPath string `env:"PYCK_BOOTSTRAP_ZITADEL_ENV_PATH"`

		// Kubernetes namespace for secret storage
		K8sNamespace string `env:"PYCK_BOOTSTRAP_ZITADEL_K8S_NAMESPACE"`

		// Kubernetes secret name for storing credentials (default: pyck-secrets)
		K8sSecretName string `env:"PYCK_BOOTSTRAP_ZITADEL_K8S_SECRET_NAME"`

		// Kubernetes in-cluster flag (default: true)
		K8sInCluster bool `env:"PYCK_BOOTSTRAP_ZITADEL_K8S_IN_CLUSTER"`

		// Path to Kubernetes config file (default: $HOME/.kube/config)
		K8sConfigPath string `env:"PYCK_BOOTSTRAP_ZITADEL_K8S_CONFIG_PATH"`

		// RegenerateClientSecrets opts in to auto-regenerating OIDC app client
		// secrets when the configured export destination is empty. Off by
		// default — Zitadel only returns client_secret on app creation, so an
		// existing app whose secret was never captured (e.g. after a chart
		// upgrade) needs an explicit go-ahead to mint a new one, since doing
		// so invalidates the previous secret.
		RegenerateClientSecrets bool `env:"PYCK_BOOTSTRAP_ZITADEL_REGENERATE_CLIENT_SECRETS"`

		// PreTokenWebhookBaseURL is the base URL Zitadel uses to reach pyck-
		// management's Actions v2 webhook that emits the pyck_tenant_id
		// claim onto OIDC tokens. Bootstrap appends
		// /webhook/zitadel/actions/pre-token. Defaults to
		// http://management:8082 — that's the **compose service name +
		// HTTP port** for pyck-management (the `pyck-management` you see in
		// `docker ps` is container_name, used for display; inter-service
		// DNS uses the service name). Production environments override to
		// their in-cluster Service DNS.
		PreTokenWebhookBaseURL string `env:"PYCK_BOOTSTRAP_PRE_TOKEN_WEBHOOK_BASE_URL" envDefault:"http://management:8082"`
	}

	// Zitadel is the configuration for seeding Zitadel.
	Zitadel struct {
		// Organizations is a list of organizations
		Organizations []Organization `yaml:"organizations"`

		// PreTokenAction configures the instance-scoped Actions v2 Target +
		// Executions that emit pyck_tenant_id as a plain top-level OIDC
		// claim. The HMAC signing_key Zitadel mints at Target creation is
		// routed through the listed exporters — local compose uses
		// file/env/process-env; production K8s uses k8s Secrets.
		PreTokenAction *PreTokenAction `yaml:"pre_token_action,omitempty"`

		// LoginEventAction configures the Actions v2 Target + Event Executions
		// that POST to pyck-management on human login, so it can publish a NATS
		// login event. The Target's server-minted HMAC key is routed through
		// the listed exporters.
		LoginEventAction *LoginEventAction `yaml:"login_event_action,omitempty"`
	}

	// PreTokenAction declares how the Actions v2 pre-token webhook's HMAC
	// signing key is provisioned and where it gets routed. The key is
	// server-minted by Zitadel on CreateTarget and only returned once —
	// pyck never persists it locally; it goes straight from Zitadel into
	// the configured Exports sinks.
	//
	// KeyCreation guards control idempotency: before any Zitadel call,
	// bootstrap checks whether the key is already present in the listed
	// destinations. If yes, the entire Target+Execution provisioning is
	// skipped on this run. If no, the existing Target (if any) is deleted
	// and a fresh one is created — the new signing key flows out through
	// Exports.
	//
	// The `field` on each Exports entry must be "signing_key" — that's the
	// only value the producer puts in the fields map.
	PreTokenAction struct {
		KeyCreation []*exporters.Export `yaml:"key-creation,omitempty"`
		Exports     []*exporters.Export `yaml:"exports"`
	}

	// LoginEventAction declares how the login-event webhook's HMAC signing key
	// is provisioned and routed. Same contract as PreTokenAction: the key is
	// server-minted on Target creation, returned once, and flows into the
	// configured Exports. KeyCreation guards drive self-healing — if the key is
	// absent at its destination (lost export), bootstrap rotates the Target to
	// mint and re-export a fresh one. The `field` on each Exports entry must be
	// "signing_key".
	LoginEventAction struct {
		KeyCreation []*exporters.Export `yaml:"key-creation,omitempty"`
		Exports     []*exporters.Export `yaml:"exports"`
	}

	// Organization represents an organization in Zitadel.
	Organization struct {
		// Name is the unique name of the organization.
		Name string `yaml:"name"`

		// HumanUsers are real users (employees, admins) associated with this organization.
		HumanUsers []HumanUser `yaml:"human_users"`

		// Projects are the OIDC/Zitadel projects to create within this organization.
		Projects []Project `yaml:"projects"`

		// ProjectGrants allow this organization to access projects owned by other organizations.
		ProjectGrants []ProjectGrant `yaml:"project_grants"`

		// MachineUsers are service accounts (non-human users) for API access.
		MachineUsers []MachineUser `yaml:"machine_users"`

		// Exports defines where to export organization attributes (id, name).
		Exports []*exporters.Export `yaml:"exports"`
	}

	// HumanUser represents a human user to be created.
	HumanUser struct {
		// Username is the unique username for login. If empty, Email is used.
		Username string `yaml:"username"`

		// Email is the contact email address of the user.
		Email string `yaml:"email"`

		// FirstName of the user.
		FirstName string `yaml:"first_name"`

		// LastName of the user.
		LastName string `yaml:"last_name"`

		// DisplayName is usually the full name.
		DisplayName string `yaml:"display_name"`

		// Password represents the initial password for the user.
		Password string `yaml:"password"`

		// IsEmailVerified determines if the email is automatically verified (default: true).
		IsEmailVerified bool `yaml:"is_email_verified"`

		// ChangePassword requires the user to change their password on first login (default: false).
		ChangePassword bool `yaml:"change_password"`

		// Role defines IAM roles for the user (e.g., IAM_OWNER, IAM_ADMIN).
		Role []string `yaml:"role"`

		// UserGrants defines which projects and roles this user has access to.
		UserGrants []UserGrant `yaml:"user_grants"`
	}

	// Project represents a project within an organization.
	Project struct {
		// Name is the display name of the project.
		Name string `yaml:"name"`

		// Apps are OIDC applications (API, Web, Native) within this project.
		Apps []App `yaml:"apps"`

		// Roles are the specific permissions available within this project.
		Roles []Role `yaml:"roles"`

		// Exports defines where to export project attributes (id, name).
		Exports []*exporters.Export `yaml:"exports"`
	}

	// AppType defines the type of Zitadel application.
	AppType string
)

const (
	// AppTypeAPI is the default application type for service-to-service communication.
	AppTypeAPI AppType = "api"

	// AppTypeOIDC is an OIDC application for interactive user login flows.
	AppTypeOIDC AppType = "oidc"
)

type (

	// App represents an application within a project.
	App struct {
		// Name is the name of the application.
		Name string `yaml:"name"`

		// Type is the application type: "api" (default) or "oidc".
		Type AppType `yaml:"type"`

		// OIDCConfig holds OIDC-specific configuration. Required when Type is "oidc".
		OIDCConfig *OIDCAppConfig `yaml:"oidc_config,omitempty"`

		// KeyCreation defines guards to check before generating new credentials.
		// If any guard target already exists, key generation is skipped.
		KeyCreation []*exporters.Export `yaml:"key-creation,omitempty"`

		// Exports defines where to export the generated credentials (file, env, k8s).
		Exports []*exporters.Export `yaml:"exports"`
	}

	// OIDCAppConfig holds OIDC application settings for Web, Native, or UserAgent apps.
	OIDCAppConfig struct {
		// RedirectURIs are the allowed callback URIs for the OAuth2/OIDC flows,
		// where the authorization code or tokens will be sent to.
		RedirectURIs []string `yaml:"redirect_uris"`

		// PostLogoutRedirectURIs are the allowed URIs to redirect to after logout.
		PostLogoutRedirectURIs []string `yaml:"post_logout_redirect_uris,omitempty"`

		// ResponseTypes define what is returned: "code", "id_token", "id_token_token".
		ResponseTypes []string `yaml:"response_types"`

		// GrantTypes define the allowed flows: "authorization_code", "implicit", "refresh_token".
		GrantTypes []string `yaml:"grant_types"`

		// ApplicationType is the OIDC client type: "web", "native", "user_agent".
		ApplicationType string `yaml:"application_type"`

		// AuthMethodType is the authentication method: "basic", "post", "none", "private_key_jwt".
		AuthMethodType string `yaml:"auth_method_type"`

		// AccessTokenType is the token format: "bearer" (opaque, default) or "jwt".
		AccessTokenType string `yaml:"access_token_type,omitempty"`

		// AccessTokenRoleAssertion adds user roles to the access token.
		AccessTokenRoleAssertion bool `yaml:"access_token_role_assertion,omitempty"`

		// IDTokenRoleAssertion adds user roles to the ID token.
		IDTokenRoleAssertion bool `yaml:"id_token_role_assertion,omitempty"`

		// IDTokenUserinfoAssertion adds profile/email/phone/address claims to the ID token.
		IDTokenUserinfoAssertion bool `yaml:"id_token_userinfo_assertion,omitempty"`

		// DevMode enables non-compliant settings like HTTP redirect URIs.
		DevMode bool `yaml:"dev_mode,omitempty"`
	}

	// Role represents a role definition within a project.
	Role struct {
		// Key is the unique identifier for the role (e.g., "reader", "admin").
		Key string `yaml:"key"`

		// DisplayName is the human-readable name of the role.
		DisplayName string `yaml:"display_name"`
	}

	// ProjectGrant allows an organization to use a project owned by another organization.
	ProjectGrant struct {
		// OrganizationName is the name of the organization that owns the project.
		OrganizationName string `yaml:"organization_name"`

		// ProjectName is the name of the project to access.
		ProjectName string `yaml:"project_name"`

		// RoleKeys are the roles granted to the organization for this project.
		RoleKeys []string `yaml:"role_keys"`
	}

	// MachineUser represents a service account.
	MachineUser struct {
		// Username for the machine user.
		Username string `yaml:"username"`

		// Name is the display name of the machine user.
		Name string `yaml:"name"`

		// AccessTokenType defines the token type: "pat" (Personal Access Token) or "jwt" (JSON Web Token - default).
		AccessTokenType string `yaml:"access_token_type"`

		// Role defines IAM roles for the machine user (e.g., IAM_LOGIN_CLIENT).
		Role []string `yaml:"role"`

		// UserGrants defines which projects and roles this machine user has access to.
		UserGrants []UserGrant `yaml:"user_grants"`

		// KeyCreation defines guards to check before generating new credentials.
		// If any guard target already exists, key generation is skipped.
		KeyCreation []*exporters.Export `yaml:"key-creation,omitempty"`

		// Exports defines where to export the generated credentials (file, env, k8s).
		Exports []*exporters.Export `yaml:"exports"`
	}

	// UserGrant assigns specific roles on a project to a user.
	UserGrant struct {
		// OrganizationName is the name of the org owning the project. Optional for same-org grants.
		OrganizationName string `yaml:"organization_name"`

		// ProjectName is the name of the project.
		ProjectName string `yaml:"project_name"`

		// RoleKey is the permission role to assign (e.g., "reader").
		RoleKey string `yaml:"role_key"`
	}
)
