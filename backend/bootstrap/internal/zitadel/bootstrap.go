package zitadel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyck-ai/pyck/backend/bootstrap/internal/exporters"
	"github.com/pyck-ai/pyck/backend/common/log"
	zitadelcommon "github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	app_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
	auth_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/auth"
	authz_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filter_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	perm_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	object_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	proj_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	user_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultKeyPath  = "/data/keys"
	defaultIssuer   = "http://localhost:8080"
	defaultOAuthURL = "http://zitadel-proxy:8080"
	defaultGrpcAddr = "zitadel-proxy:8080"
	zitadelKeyFile  = "zitadel-admin-sa.json"
)

var (
	scopes = []string{"openid", "urn:zitadel:iam:org:project:id:zitadel:aud"}
)

type (
	Seeder struct {
		issuer                  string
		oauthURL                string
		grpcAddr                string
		adminKeyFile            string
		keyPath                 string
		projectIDs              map[string]orgProject
		projectGrants           map[string]bool // Key: "urn:zitadel:iam:org:{orgName}:project:{projectName}"
		exporters               *exporters.ExporterRegistry
		grpcInsecure            bool // http connection for Zitadel
		regenerateClientSecrets bool // opt-in: auto-mint OIDC client_secret when destination is empty
	}

	// internal struct to hold organization and project IDs
	orgProject struct {
		orgID     string
		projectID string
	}
)

// New creates a new Seeder instance with the provided configuration.
func New(ctx context.Context, c Configuration) (*Seeder, error) {
	keyPath := c.KeyPath
	if keyPath == "" {
		keyPath = defaultKeyPath
	}

	// the key path must be mounted or accessible
	if _, err := os.Stat(keyPath); err != nil {
		return nil, err
	}

	// Use provided env file path or default to key path
	envPath := c.EnvPath
	if envPath == "" {
		envPath = defaultKeyPath
	}

	// Use provided issuer/API or defaults
	issuerURL := c.Issuer
	if issuerURL == "" {
		issuerURL = defaultIssuer
	}
	oauthURL := c.OAuthURL
	if oauthURL == "" {
		oauthURL = defaultOAuthURL
	}
	grpcAddr := c.GrpcAddr
	if grpcAddr == "" {
		grpcAddr = defaultGrpcAddr
	}

	// Create exporters
	exporterMap := map[exporters.ExportType]exporters.Exporter{
		exporters.ExportTypeFile:       exporters.NewFileExporter(keyPath),
		exporters.ExportTypeEnv:        exporters.NewEnvExporter(envPath),
		exporters.ExportTypeK8s:        exporters.NewK8sExporter(ctx, c.K8sNamespace, c.K8sSecretName, c.K8sInCluster, c.K8sConfigPath),
		exporters.ExportTypeProcessEnv: exporters.NewProcessEnvExporter(),
	}

	return &Seeder{
		issuer:                  issuerURL,
		oauthURL:                oauthURL,
		grpcAddr:                grpcAddr,
		keyPath:                 keyPath,
		adminKeyFile:            filepath.Join(keyPath, zitadelKeyFile),
		projectIDs:              make(map[string]orgProject),
		projectGrants:           make(map[string]bool),
		exporters:               exporters.NewExporterRegistry(exporterMap),
		grpcInsecure:            c.GrpcInsecure,
		regenerateClientSecrets: c.RegenerateClientSecrets,
	}, nil
}

// newConnection creates a gRPC connection to Zitadel using insecure transport
// and a custom token source that bypasses OIDC discovery.
func (s *Seeder) newConnection(ctx context.Context, extraOpts ...zitadel.Option) (*zitadel.Connection, error) {
	opts := []zitadel.Option{
		zitadel.WithTokenSource(zitadelcommon.NewJWTProfileTokenSource(s.oauthURL, s.issuer, s.adminKeyFile)),
	}

	// when using http, we need to explicitly set the connection to insecure,
	// otherwise the gRPC client will attempt to use TLS
	if s.grpcInsecure {
		opts = append(opts, zitadel.WithInsecure())
	}

	opts = append(opts, extraOpts...)
	return zitadel.NewConnection(ctx, s.issuer, s.grpcAddr, scopes, opts...)
}

// Bootstrap kicks off the bootstrapping process
func (s *Seeder) Bootstrap(ctx context.Context, zitadelConfig Zitadel) error {
	logger := log.ForContext(ctx)

	// Single instance-level connection (no org scope) — all v2 gRPC services
	// multiplex over the same HTTP/2 connection
	conn, err := s.newConnection(ctx)
	if err != nil {
		return fmt.Errorf("failed to create zitadel connection: %w", err)
	}
	defer conn.Close()

	// Fetch all existing organizations into a map for efficient lookups
	orgIDsByName, err := s.getOrganizations(ctx, conn)
	if err != nil {
		return fmt.Errorf("failed to fetch existing organizations: %w", err)
	}

	// seed organizations, users, projects, apps, roles, and machine users
	for _, orgSeed := range zitadelConfig.Organizations {

		// create or return organization
		orgID, err := s.ensureOrganization(ctx, conn, orgSeed.Name, orgIDsByName)
		if err != nil {
			return fmt.Errorf("failed to ensure organization %s: %w", orgSeed.Name, err)
		}

		// Export organization attributes if configured
		if len(orgSeed.Exports) > 0 {
			orgFields := map[string]string{"id": orgID, "name": orgSeed.Name}
			if err := s.exportFields(ctx, orgFields, orgSeed.Exports, fmt.Sprintf("organization %q", orgSeed.Name)); err != nil {
				return err
			}
		}

		if err := s.seedOrganization(ctx, conn, orgID, orgSeed); err != nil {
			return fmt.Errorf("failed to seed organization %s: %w", orgSeed.Name, err)
		}

		logger.Debug().Str("organization", orgSeed.Name).Msg("Seeded organization")
	}

	return nil
}

// seedOrganization seeds a single organization with its users, projects, apps, roles,
// and machine users. It creates an org-scoped connection for seeding to ensure proper
// scoping of operations.
func (s *Seeder) seedOrganization(ctx context.Context,
	adminConn *zitadel.Connection,
	orgID string,
	orgSeed Organization) error {

	// create organization-scoped connection for org-level operations
	orgConn, err := s.newConnection(ctx, zitadel.WithOrgID(orgID))
	if err != nil {
		return fmt.Errorf("failed to create org-scoped connection for %s: %w", orgSeed.Name, err)
	}

	defer orgConn.Close()

	// Projects (must be seeded before users that reference them via user_grants)
	if err := s.seedProjects(ctx, orgConn, orgSeed.Name, orgID, orgSeed.Projects); err != nil {
		return err
	}

	// Project Grants (grant access to projects from other orgs)
	if err := s.seedProjectGrants(ctx, orgConn, orgID, orgSeed.Name, orgSeed.ProjectGrants); err != nil {
		return err
	}

	// Human Users
	if err := s.seedHumanUsers(ctx, adminConn, orgConn, orgID, orgSeed.Name, orgSeed.HumanUsers); err != nil {
		return err
	}

	// Machine Users
	if err := s.seedMachineUsers(ctx, adminConn, orgConn, orgID, orgSeed.Name, orgSeed.MachineUsers); err != nil {
		return err
	}

	return nil
}

// seedHumanUsers seeds human users for an organization and optionally assigns IAM member roles.
// It iterates through the provided user seeds, creates or verifies each user exists,
// and assigns any specified IAM roles (e.g., IAM_OWNER, IAM_ADMIN).
func (s *Seeder) seedHumanUsers(ctx context.Context, adminConn, orgConn *zitadel.Connection, orgID string, orgName string, users []HumanUser) error {
	logger := log.ForContext(ctx)

	for _, userSeed := range users {
		userID, err := s.ensureHumanUser(ctx, orgConn, orgID, userSeed)
		if err != nil {
			return fmt.Errorf("failed to ensure user %s: %w", userSeed.Email, err)
		}

		// Add IAM member roles if specified
		if len(userSeed.Role) > 0 {
			if err := s.addIAMMember(ctx, adminConn, userID, userSeed.Role); err != nil {
				return fmt.Errorf("failed to add IAM member roles for user %s: %w", userSeed.Email, err)
			}
		}

		// Ensure user grants for project roles
		for _, grant := range userSeed.UserGrants {
			targetOrgName := grant.OrganizationName
			if targetOrgName == "" {
				targetOrgName = orgName
			}

			projectInfo, ok := s.resolveProjectURN(targetOrgName, grant.ProjectName)
			if !ok {
				logger.Warn().Str("project", grant.ProjectName).Str("organization", targetOrgName).Str("user", userSeed.Email).Msg("Project not found, skipping user grant")
				continue
			}

			// Same-org grants use the admin connection directly.
			// Cross-org grants require project grant verification first.
			grantConn := adminConn
			if targetOrgName != orgName {
				if _, ok := s.resolveProjectGrant(targetOrgName, grant.ProjectName); !ok {
					logger.Warn().Str("organization", targetOrgName).Str("project", grant.ProjectName).Msg("Project grant not found, skipping user grant")
					continue
				}
				grantConn = orgConn
			}

			if err := s.ensureUserGrant(ctx, grantConn, projectInfo.projectID, orgID, userID, []string{grant.RoleKey}); err != nil {
				return fmt.Errorf("failed to grant role %s to user %s: %w", grant.RoleKey, userSeed.Email, err)
			}
		}
	}
	return nil
}

// seedProjects seeds projects for an organization, including their apps and roles.
// It creates or verifies each project exists, caches the project URN for cross-org references,
// seeds associated apps with optional JWT key generation, and ensures all project roles are created.
func (s *Seeder) seedProjects(ctx context.Context, conn *zitadel.Connection, orgName, orgID string, projects []Project) error {
	for _, projSeed := range projects {
		projectID, err := s.ensureProject(ctx, conn, orgID, projSeed.Name)
		if err != nil {
			return fmt.Errorf("ensure project %s: %w", projSeed.Name, err)
		}

		s.cacheProjectURN(orgName, orgID, projSeed.Name, projectID)

		if err := s.handleProjectExports(ctx, projectID, projSeed); err != nil {
			return err
		}

		if err := s.seedApps(ctx, conn, projectID, projSeed.Apps); err != nil {
			return err
		}

		if err := s.seedRoles(ctx, conn, projectID, projSeed.Roles); err != nil {
			return err
		}
	}
	return nil
}

// handleProjectExports exports project attributes (id, name) using the configured exporters.
func (s *Seeder) handleProjectExports(ctx context.Context, projectID string, projSeed Project) error {
	if len(projSeed.Exports) == 0 {
		return nil
	}

	fields := map[string]string{"id": projectID, "name": projSeed.Name}
	return s.exportFields(ctx, fields, projSeed.Exports, fmt.Sprintf("project %q", projSeed.Name))
}

// seedApps seeds applications for a project, dispatching to API or OIDC creation based on app type.
func (s *Seeder) seedApps(ctx context.Context, conn *zitadel.Connection, projectID string, apps []App) error {
	for _, appSeed := range apps {
		switch appSeed.Type {
		case AppTypeOIDC:
			if err := s.seedOIDCApp(ctx, conn, projectID, appSeed); err != nil {
				return err
			}
		default:
			if err := s.seedAPIApp(ctx, conn, projectID, appSeed); err != nil {
				return err
			}
		}
	}
	return nil
}

// seedAPIApp creates an API application and exports its JWT key.
// If key-creation guards are set and any target already exists, key generation is skipped.
func (s *Seeder) seedAPIApp(ctx context.Context, conn *zitadel.Connection, projectID string, appSeed App) error {
	appID, err := s.ensureAPIApp(ctx, conn, projectID, appSeed.Name)
	if err != nil {
		return fmt.Errorf("ensure API app %s: %w", appSeed.Name, err)
	}

	if len(appSeed.Exports) == 0 {
		return nil
	}

	// Check if credentials already exist
	if len(appSeed.KeyCreation) > 0 {
		exists, err := s.exporters.CredentialsExist(ctx, appSeed.KeyCreation)
		if err != nil {
			return fmt.Errorf("check key existence for %s: %w", appSeed.Name, err)
		}
		if exists {
			log.ForContext(ctx).Info().Str("app", appSeed.Name).Msg("Skipping key generation, credentials already exist")
			return nil
		}
	}

	keyJSON, err := s.generateAppKey(ctx, conn, projectID, appID)
	if err != nil {
		return fmt.Errorf("generate app key for %s: %w", appSeed.Name, err)
	}

	for _, export := range appSeed.Exports {
		if err := s.exporters.Export(ctx, keyJSON, *export); err != nil {
			return fmt.Errorf("export app key for %s: %w", appSeed.Name, err)
		}
	}
	return nil
}

// seedOIDCApp creates an OIDC application and exports its client ID (and secret if generated).
func (s *Seeder) seedOIDCApp(ctx context.Context, conn *zitadel.Connection, projectID string, appSeed App) error {
	if appSeed.OIDCConfig == nil {
		return fmt.Errorf("OIDC app %q requires oidc_config", appSeed.Name)
	}

	appID, clientID, clientSecret, err := s.ensureOIDCApp(ctx, conn, projectID, appSeed)
	if err != nil {
		return fmt.Errorf("ensure OIDC app %s: %w", appSeed.Name, err)
	}

	if len(appSeed.Exports) == 0 {
		return nil
	}

	// Zitadel only returns client_secret on app creation. For pre-existing
	// apps (e.g. after a chart/version upgrade where the secret was never
	// captured) the destination ends up empty and dependent workloads can't
	// authenticate. Opt-in (PYCK_BOOTSTRAP_ZITADEL_REGENERATE_CLIENT_SECRETS):
	// detect that case and mint a new secret via the Zitadel API. Off by
	// default because regeneration invalidates the previous secret.
	if s.regenerateClientSecrets && clientSecret == "" {
		var secretExports []*exporters.Export
		for _, exp := range appSeed.Exports {
			if exp.Field == "client_secret" {
				secretExports = append(secretExports, exp)
			}
		}
		if len(secretExports) > 0 {
			exists, err := s.exporters.CredentialsExist(ctx, secretExports)
			if err != nil {
				return fmt.Errorf("checking client_secret presence for OIDC app %q: %w", appSeed.Name, err)
			}
			if !exists {
				appClient := app_pb.NewApplicationServiceClient(conn)
				resp, err := appClient.GenerateClientSecret(ctx, &app_pb.GenerateClientSecretRequest{
					ApplicationId: appID,
					ProjectId:     projectID,
				})
				if err != nil {
					return fmt.Errorf("regenerating client_secret for OIDC app %q: %w", appSeed.Name, err)
				}
				clientSecret = resp.GetClientSecret()
				log.ForContext(ctx).Info().
					Str("app", appSeed.Name).
					Str("application_id", appID).
					Msg("Regenerated client_secret (destination was empty)")
			}
		}
	}

	// For OIDC apps, export the client ID, secret (if generated), and name.
	fields := map[string]string{"client_id": clientID, "name": appSeed.Name}
	if clientSecret != "" {
		fields["client_secret"] = clientSecret
	}
	return s.exportFields(ctx, fields, appSeed.Exports, fmt.Sprintf("OIDC app %q", appSeed.Name))
}

// seedRoles ensures all configured roles exist for a project.
func (s *Seeder) seedRoles(ctx context.Context, conn *zitadel.Connection, projectID string, roles []Role) error {
	for _, roleSeed := range roles {
		if err := s.ensureProjectRole(ctx, conn, projectID, roleSeed.Key, roleSeed.DisplayName); err != nil {
			return fmt.Errorf("ensure project role %s: %w", roleSeed.Key, err)
		}
	}
	return nil
}

// seedProjectGrants creates grants for projects owned by other organizations.
// Project grants must be created by the organization that owns the project, so this method
// creates an owner-scoped connection when necessary for cross-org grants.
func (s *Seeder) seedProjectGrants(ctx context.Context, orgConn *zitadel.Connection, orgID string, orgName string, grants []ProjectGrant) error {
	logger := log.ForContext(ctx)

	for _, projectGrantSeed := range grants {
		// Find the project ID from the default org
		orgProjectID, ok := s.resolveProjectURN(projectGrantSeed.OrganizationName, projectGrantSeed.ProjectName)
		if !ok {
			logger.Warn().Str("project", projectGrantSeed.ProjectName).Str("organization", orgName).Msg("Project not found in default org, skipping project grant")
			continue
		}

		// Project grants must be created by the organization that owns the project
		// So we need to use the project owner's (Zitadel org's) scoped connection
		grantConn := orgConn
		var ownerConn *zitadel.Connection

		if orgProjectID.orgID != orgID {
			// Cross-org grant: create a connection scoped to the project owner org
			var err error
			ownerConn, err = s.newConnection(ctx, zitadel.WithOrgID(orgProjectID.orgID))
			if err != nil {
				return fmt.Errorf("failed to create project owner org-scoped connection: %w", err)
			}

			grantConn = ownerConn
		}

		err := s.ensureProjectGrant(ctx,
			grantConn,
			orgProjectID.projectID, //owner
			orgID,                  // target
			projectGrantSeed.RoleKeys,
		)

		if ownerConn != nil {
			ownerConn.Close()
		}

		if err != nil {
			return fmt.Errorf("failed to grant project %s to organization %s: %w", projectGrantSeed.ProjectName, orgName, err)
		}

		// keep it for future references
		s.cacheProjectGrant(projectGrantSeed.OrganizationName, projectGrantSeed.ProjectName)
	}
	return nil
}

// seedMachineUsers seeds machine users for an organization with their user grants and credentials.
// It creates or verifies each machine user exists, assigns user grants (including cross-org grants
// with project grant lookup), generates credentials (PAT or JWT), saves them to files, and
// optionally tests JWT access for validation.
// The username is created by combining the orgID with the base username from the seed data.
func (s *Seeder) seedMachineUsers(ctx context.Context, adminConn, orgConn *zitadel.Connection, orgID string, orgName string, machineUsers []MachineUser) error {
	logger := log.ForContext(ctx)

	for _, machSeed := range machineUsers {
		// Create unique username by combining orgID with base username (like setup.go does)
		// Format: "orgID-base-username" e.g., "123456789-service-worker-user"
		username := fmt.Sprintf("%s-%s", orgID, machSeed.Username)

		// ensure the existence of the user
		machineUserID, err := s.ensureMachineUser(ctx, orgConn, orgID, username, machSeed.Name)
		if err != nil {
			return fmt.Errorf("failed to ensure machine user %s (base: %s): %w", username, machSeed.Username, err)
		}

		// Add IAM member roles if specified (e.g., IAM_LOGIN_CLIENT)
		if len(machSeed.Role) > 0 {
			if err := s.addIAMMember(ctx, adminConn, machineUserID, machSeed.Role); err != nil {
				return fmt.Errorf("failed to add IAM member roles for machine user %s (base: %s): %w", username, machSeed.Username, err)
			}
		}

		// ensure user grants
		for _, grants := range machSeed.UserGrants {
			// Use the organization name from the grant if specified, otherwise use current org
			targetOrgName := grants.OrganizationName
			if targetOrgName == "" {
				targetOrgName = orgName
			}

			// Resolve the project from the cache
			projectInfo, ok := s.resolveProjectURN(targetOrgName, grants.ProjectName)
			if !ok {
				logger.Warn().Str("project", grants.ProjectName).Str("organization", targetOrgName).Str("username", username).Str("base", machSeed.Username).Msg("Project not found in current run, skipping grant")
				continue
			}

			// For cross-org access, verify the project grant exists
			if targetOrgName != orgName {
				if _, ok := s.resolveProjectGrant(targetOrgName, grants.ProjectName); !ok {
					logger.Warn().Str("organization", targetOrgName).Str("project", grants.ProjectName).Msg("Project grant not found, skipping user grant")
					continue
				}
			}

			// Always use the current org's connection (where the user exists)
			if err := s.ensureUserGrant(ctx, orgConn, projectInfo.projectID, orgID, machineUserID, []string{grants.RoleKey}); err != nil {
				return fmt.Errorf("failed to grant role %s to machine user %s (base: %s): %w", grants.RoleKey, username, machSeed.Username, err)
			}
		}

		// Check if credentials already exist before generating new ones
		if len(machSeed.KeyCreation) > 0 {
			exists, err := s.exporters.CredentialsExist(ctx, machSeed.KeyCreation)
			if err != nil {
				return fmt.Errorf("check key existence for machine user %s (base: %s): %w", username, machSeed.Username, err)
			}
			if exists {
				logger.Info().Str("username", username).Str("base", machSeed.Username).Msg("Skipping credential generation, credentials already exist")
				continue
			}
		}

		// Generate credentials based on auth type
		var credentials string
		var credType string
		switch machSeed.AccessTokenType {
		case "pat":
			credType = "PAT Token"
			credentials, err = s.generatePATToken(ctx, orgConn, machineUserID)
		default:
			// Generate JWT key (default)
			credType = "JWT Key"
			credentials, err = s.generateMachineKey(ctx, orgConn, machineUserID)
		}
		if err != nil {
			return fmt.Errorf("failed to generate %s: %w", credType, err)
		}

		// Export credentials if Exports is configured
		if len(machSeed.Exports) > 0 {
			for _, export := range machSeed.Exports {
				if err := s.exporters.Export(ctx, credentials, *export); err != nil {
					return fmt.Errorf("failed to export credentials for machine user %q (base: %q): %w", username, machSeed.Username, err)
				}
			}
		} else {
			logger.Warn().Str("username", username).Str("base", machSeed.Username).Msg("No export configuration provided for machine user, credentials not saved")
		}

		// Test access with generated credentials (only JWT, since PAT tokens are opaque and can't be easily tested without making an actual API call that requires the token)
		if machSeed.AccessTokenType != "pat" {
			if err := s.confirmJWTAccess(ctx, credentials); err != nil {
				logger.Warn().Err(err).Str("username", username).Str("base", machSeed.Username).Msg("Failed to confirm JWT access for machine user")
				// Log warning but don't fail the entire seeding process
			}
		}
	}
	return nil
}

func (s *Seeder) getOrganizations(ctx context.Context, conn *zitadel.Connection) (map[string]string, error) {
	logger := log.ForContext(ctx)

	orgClient := org_pb.NewOrganizationServiceClient(conn)
	listResp, err := orgClient.ListOrganizations(ctx, &org_pb.ListOrganizationsRequest{})
	if err != nil {
		return nil, err
	}

	orgIDsByName := make(map[string]string)
	for _, org := range listResp.GetResult() {
		orgKey := strings.ToLower(org.Name)
		orgIDsByName[orgKey] = org.Id
		logger.Debug().Str("organization", org.Name).Str("id", org.Id).Msg("Found organization")
	}
	return orgIDsByName, nil
}

func (s *Seeder) ensureOrganization(ctx context.Context, conn *zitadel.Connection, name string, orgIDsByName map[string]string) (string, error) {
	logger := log.ForContext(ctx)

	// Check if organization already exists in the map
	orgKey := strings.ToLower(name)
	if orgID, exists := orgIDsByName[orgKey]; exists {
		logger.Debug().Str("organization", name).Str("id", orgID).Msg("Organization already exists")
		return orgID, nil
	}

	// Create if not exists via v2 Organization API
	orgClient := org_pb.NewOrganizationServiceClient(conn)
	createResp, err := orgClient.AddOrganization(ctx, &org_pb.AddOrganizationRequest{
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("creating organization %q: %w", name, err)
	}
	logger.Debug().Str("organization", name).Str("id", createResp.OrganizationId).Msg("Created organization")
	return createResp.OrganizationId, nil
}

func (s *Seeder) ensureHumanUser(ctx context.Context, conn *zitadel.Connection, orgID string, userSeed HumanUser) (string, error) {
	logger := log.ForContext(ctx)

	// Username is the idempotency anchor — it's the uniqueness key Zitadel
	// enforces within an org. Email is a mutable attribute and cannot be
	// used to identify an existing user reliably.
	username := userSeed.Username
	if username == "" {
		username = userSeed.Email
	}

	// Check if exists using v2 User API
	userClient := user_pb.NewUserServiceClient(conn)
	listResp, err := userClient.ListUsers(ctx, &user_pb.ListUsersRequest{
		Queries: []*user_pb.SearchQuery{
			{
				Query: &user_pb.SearchQuery_UserNameQuery{
					UserNameQuery: &user_pb.UserNameQuery{
						UserName: username,
						Method:   object_pb.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE,
					},
				},
			},
			{
				Query: &user_pb.SearchQuery_OrganizationIdQuery{
					OrganizationIdQuery: &user_pb.OrganizationIdQuery{
						OrganizationId: orgID,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("listing users for %q: %w", username, err)
	}

	if len(listResp.GetResult()) > 0 {
		user := listResp.GetResult()[0]
		logger.Info().Str("username", username).Str("user_id", user.GetUserId()).Msg("User already exists")
		return user.GetUserId(), nil
	}

	// Create the user
	createResp, err := userClient.CreateUser(ctx, &user_pb.CreateUserRequest{
		OrganizationId: orgID,
		Username:       &username,
		UserType: &user_pb.CreateUserRequest_Human_{
			Human: &user_pb.CreateUserRequest_Human{
				Profile: &user_pb.SetHumanProfile{
					GivenName:  userSeed.FirstName,
					FamilyName: userSeed.LastName,
					DisplayName: func() *string {
						s := userSeed.DisplayName
						return &s
					}(),
				},
				Email: &user_pb.SetHumanEmail{
					Email: userSeed.Email,
					Verification: &user_pb.SetHumanEmail_IsVerified{
						IsVerified: userSeed.IsEmailVerified,
					},
				},
				PasswordType: &user_pb.CreateUserRequest_Human_Password{
					Password: &user_pb.Password{
						Password:       userSeed.Password,
						ChangeRequired: userSeed.ChangePassword,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating user %q: %w", userSeed.Email, err)
	}

	userID := createResp.GetId()

	logger.Debug().Str("email", userSeed.Email).Msg("Created user")
	return userID, nil
}

func (s *Seeder) ensureProject(ctx context.Context, conn *zitadel.Connection, orgID string, name string) (string, error) {
	logger := log.ForContext(ctx)

	// Search for project using v2 Project API
	projClient := proj_pb.NewProjectServiceClient(conn)
	listResp, err := projClient.ListProjects(ctx, &proj_pb.ListProjectsRequest{
		Filters: []*proj_pb.ProjectSearchFilter{
			{
				Filter: &proj_pb.ProjectSearchFilter_ProjectNameFilter{
					ProjectNameFilter: &proj_pb.ProjectNameFilter{
						ProjectName: name,
						Method:      filter_pb.TextFilterMethod_TEXT_FILTER_METHOD_EQUALS_IGNORE_CASE,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("listing projects: %w", err)
	}
	if len(listResp.GetProjects()) > 0 {
		logger.Debug().Str("project", name).Msg("Project already exists")
		return listResp.GetProjects()[0].ProjectId, nil
	}

	// Create project via v2 Project API
	cResp, err := projClient.CreateProject(ctx, &proj_pb.CreateProjectRequest{
		OrganizationId: orgID,
		Name:           name,
	})
	if err != nil {
		return "", fmt.Errorf("creating project %q: %w", name, err)
	}
	logger.Debug().Str("project", name).Str("id", cResp.ProjectId).Msg("Created project")
	return cResp.ProjectId, nil
}

func (s *Seeder) ensureAPIApp(ctx context.Context, conn *zitadel.Connection, projectID string, name string) (string, error) {
	logger := log.ForContext(ctx)

	// List apps in project using v2 Application API
	appClient := app_pb.NewApplicationServiceClient(conn)
	listResp, err := appClient.ListApplications(ctx, &app_pb.ListApplicationsRequest{
		Filters: []*app_pb.ApplicationSearchFilter{
			{
				Filter: &app_pb.ApplicationSearchFilter_ProjectIdFilter{
					ProjectIdFilter: &app_pb.ProjectIDFilter{ProjectId: projectID},
				},
			},
			{
				Filter: &app_pb.ApplicationSearchFilter_NameFilter{
					NameFilter: &app_pb.ApplicationNameFilter{
						Name:   name,
						Method: filter_pb.TextFilterMethod_TEXT_FILTER_METHOD_EQUALS_IGNORE_CASE,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("listing apps for project %q: %w", projectID, err)
	}
	for _, app := range listResp.GetApplications() {
		if strings.EqualFold(app.Name, name) {
			logger.Debug().Str("app", name).Msg("App already exists")
			return app.ApplicationId, nil
		}
	}

	// Create API App with Private Key JWT Auth via v2 Application API
	cResp, err := appClient.CreateApplication(ctx, &app_pb.CreateApplicationRequest{
		ProjectId: projectID,
		Name:      name,
		ApplicationType: &app_pb.CreateApplicationRequest_ApiConfiguration{
			ApiConfiguration: &app_pb.CreateAPIApplicationRequest{
				AuthMethodType: app_pb.APIAuthMethodType_API_AUTH_METHOD_TYPE_PRIVATE_KEY_JWT,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating API app %q: %w", name, err)
	}
	logger.Debug().Str("app", name).Str("id", cResp.ApplicationId).Msg("Created API app")
	return cResp.ApplicationId, nil
}

func (s *Seeder) ensureOIDCApp(ctx context.Context, conn *zitadel.Connection, projectID string, appSeed App) (appID, clientID, clientSecret string, err error) {
	logger := log.ForContext(ctx)

	appClient := app_pb.NewApplicationServiceClient(conn)
	listResp, err := appClient.ListApplications(ctx, &app_pb.ListApplicationsRequest{
		Filters: []*app_pb.ApplicationSearchFilter{
			{
				Filter: &app_pb.ApplicationSearchFilter_ProjectIdFilter{
					ProjectIdFilter: &app_pb.ProjectIDFilter{ProjectId: projectID},
				},
			},
			{
				Filter: &app_pb.ApplicationSearchFilter_NameFilter{
					NameFilter: &app_pb.ApplicationNameFilter{
						Name:   appSeed.Name,
						Method: filter_pb.TextFilterMethod_TEXT_FILTER_METHOD_EQUALS_IGNORE_CASE,
					},
				},
			},
		},
	})
	if err != nil {
		return "", "", "", fmt.Errorf("listing apps for project %q: %w", projectID, err)
	}
	cfg := appSeed.OIDCConfig

	responseTypes := make([]app_pb.OIDCResponseType, 0, len(cfg.ResponseTypes))
	for _, rt := range cfg.ResponseTypes {
		responseTypes = append(responseTypes, mapOIDCResponseType(rt))
	}

	grantTypes := make([]app_pb.OIDCGrantType, 0, len(cfg.GrantTypes))
	for _, gt := range cfg.GrantTypes {
		grantTypes = append(grantTypes, mapOIDCGrantType(gt))
	}

	for _, app := range listResp.GetApplications() {
		if strings.EqualFold(app.Name, appSeed.Name) {
			clientID := ""
			if oidcCfg := app.GetOidcConfiguration(); oidcCfg != nil {
				clientID = oidcCfg.GetClientId()
			}
			// App exists — reconcile its OIDC config so YAML-driven changes
			// (redirect URIs, dev_mode, etc.) take effect on subsequent runs.
			// Zitadel does not return client_secret on update, same as on
			// list; if it needs to be re-issued, that's a separate flow.
			appType := mapOIDCApplicationType(cfg.ApplicationType)
			authMethod := mapOIDCAuthMethodType(cfg.AuthMethodType)
			tokenType := mapOIDCTokenType(cfg.AccessTokenType)
			devMode := cfg.DevMode
			accessRoleAssert := cfg.AccessTokenRoleAssertion
			idTokenRoleAssert := cfg.IDTokenRoleAssertion
			idTokenUserinfoAssert := cfg.IDTokenUserinfoAssertion
			if _, err := appClient.UpdateApplication(ctx, &app_pb.UpdateApplicationRequest{
				ApplicationId: app.ApplicationId,
				ProjectId:     projectID,
				ApplicationType: &app_pb.UpdateApplicationRequest_OidcConfiguration{
					OidcConfiguration: &app_pb.UpdateOIDCApplicationConfigurationRequest{
						RedirectUris:             cfg.RedirectURIs,
						PostLogoutRedirectUris:   cfg.PostLogoutRedirectURIs,
						ResponseTypes:            responseTypes,
						GrantTypes:               grantTypes,
						ApplicationType:          &appType,
						AuthMethodType:           &authMethod,
						AccessTokenType:          &tokenType,
						AccessTokenRoleAssertion: &accessRoleAssert,
						IdTokenRoleAssertion:     &idTokenRoleAssert,
						IdTokenUserinfoAssertion: &idTokenUserinfoAssert,
						DevelopmentMode:          &devMode,
					},
				},
			}); err != nil && !isZitadelNoChangesError(err) {
				// Zitadel returns FailedPrecondition "No changes" instead of
				// OK when the desired state already matches — for an upsert
				// that's the success case, not a failure.
				return "", "", "", fmt.Errorf("updating OIDC app %q: %w", appSeed.Name, err)
			}
			logger.Debug().Str("app", appSeed.Name).Str("client_id", clientID).Msg("Reconciled existing OIDC app")
			return app.ApplicationId, clientID, "", nil
		}
	}

	cResp, err := appClient.CreateApplication(ctx, &app_pb.CreateApplicationRequest{
		ProjectId: projectID,
		Name:      appSeed.Name,
		ApplicationType: &app_pb.CreateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &app_pb.CreateOIDCApplicationRequest{
				RedirectUris:             cfg.RedirectURIs,
				PostLogoutRedirectUris:   cfg.PostLogoutRedirectURIs,
				ResponseTypes:            responseTypes,
				GrantTypes:               grantTypes,
				ApplicationType:          mapOIDCApplicationType(cfg.ApplicationType),
				AuthMethodType:           mapOIDCAuthMethodType(cfg.AuthMethodType),
				AccessTokenType:          mapOIDCTokenType(cfg.AccessTokenType),
				AccessTokenRoleAssertion: cfg.AccessTokenRoleAssertion,
				IdTokenRoleAssertion:     cfg.IDTokenRoleAssertion,
				IdTokenUserinfoAssertion: cfg.IDTokenUserinfoAssertion,
				DevelopmentMode:          cfg.DevMode,
			},
		},
	})
	if err != nil {
		return "", "", "", fmt.Errorf("creating OIDC app %q: %w", appSeed.Name, err)
	}

	oidcResp := cResp.GetOidcConfiguration()
	clientID = oidcResp.GetClientId()
	clientSecret = oidcResp.GetClientSecret()

	logger.Debug().
		Str("app", appSeed.Name).
		Str("id", cResp.ApplicationId).
		Str("client_id", clientID).
		Bool("has_secret", clientSecret != "").
		Msg("Created OIDC app")

	return cResp.ApplicationId, clientID, clientSecret, nil
}

// isZitadelNoChangesError reports whether err is Zitadel's "nothing to update"
// signal. Zitadel returns FailedPrecondition with code COMMAND-1m88i ("No
// changes") when an Update request matches the current state. For an upsert
// flow that's the success case, not a failure.
func isZitadelNoChangesError(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	return st.Code() == codes.FailedPrecondition && strings.Contains(st.Message(), "No changes")
}

func mapOIDCResponseType(s string) app_pb.OIDCResponseType {
	switch s {
	case "code":
		return app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE
	case "id_token":
		return app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_ID_TOKEN
	case "id_token_token":
		return app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_ID_TOKEN_TOKEN
	default:
		return app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE
	}
}

func mapOIDCGrantType(s string) app_pb.OIDCGrantType {
	switch s {
	case "authorization_code":
		return app_pb.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE
	case "implicit":
		return app_pb.OIDCGrantType_OIDC_GRANT_TYPE_IMPLICIT
	case "refresh_token":
		return app_pb.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN
	case "token_exchange":
		return app_pb.OIDCGrantType_OIDC_GRANT_TYPE_TOKEN_EXCHANGE
	default:
		return app_pb.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE
	}
}

func mapOIDCApplicationType(s string) app_pb.OIDCApplicationType {
	switch s {
	case "web":
		return app_pb.OIDCApplicationType_OIDC_APP_TYPE_WEB
	case "native":
		return app_pb.OIDCApplicationType_OIDC_APP_TYPE_NATIVE
	case "user_agent":
		return app_pb.OIDCApplicationType_OIDC_APP_TYPE_USER_AGENT
	default:
		return app_pb.OIDCApplicationType_OIDC_APP_TYPE_WEB
	}
}

func mapOIDCAuthMethodType(s string) app_pb.OIDCAuthMethodType {
	switch s {
	case "basic":
		return app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC
	case "post":
		return app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_POST
	case "none":
		return app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE
	case "private_key_jwt":
		return app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_PRIVATE_KEY_JWT
	default:
		return app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE
	}
}

func mapOIDCTokenType(s string) app_pb.OIDCTokenType {
	switch s {
	case "jwt":
		return app_pb.OIDCTokenType_OIDC_TOKEN_TYPE_JWT
	default:
		return app_pb.OIDCTokenType_OIDC_TOKEN_TYPE_BEARER
	}
}

func (s *Seeder) ensureMachineUser(ctx context.Context, conn *zitadel.Connection, orgID string, username string, name string) (string, error) {
	logger := log.ForContext(ctx)

	// Check if exists using v2 User API
	userClient := user_pb.NewUserServiceClient(conn)
	listResp, err := userClient.ListUsers(ctx, &user_pb.ListUsersRequest{
		Queries: []*user_pb.SearchQuery{
			{
				Query: &user_pb.SearchQuery_UserNameQuery{
					UserNameQuery: &user_pb.UserNameQuery{
						UserName: username,
						Method:   object_pb.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("listing machine users for %q: %w", username, err)
	}
	if len(listResp.GetResult()) > 0 {
		logger.Debug().Str("username", username).Msg("Machine user already exists")
		return listResp.GetResult()[0].GetUserId(), nil
	}

	// Create machine user via v2 API. New machine users default to Bearer
	// token type. The actual auth mechanism (PAT or JWT key) is configured
	// separately via generatePATToken or generateMachineKey.
	createResp, err := userClient.CreateUser(ctx, &user_pb.CreateUserRequest{
		OrganizationId: orgID,
		Username:       &username,
		UserType: &user_pb.CreateUserRequest_Machine_{
			Machine: &user_pb.CreateUserRequest_Machine{
				Name: name,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating machine user %q: %w", username, err)
	}
	logger.Debug().Str("username", username).Msg("Created machine user")
	return createResp.GetId(), nil
}

func (s *Seeder) generateMachineKey(ctx context.Context, conn *zitadel.Connection, userID string) (string, error) {
	// Generate a key pair via v2 User API
	expiration := timestamppb.New(time.Now().AddDate(1, 0, 0)) // 1 Year validity

	userClient := user_pb.NewUserServiceClient(conn)
	resp, err := userClient.AddKey(ctx, &user_pb.AddKeyRequest{
		UserId:         userID,
		ExpirationDate: expiration,
	})
	if err != nil {
		return "", err
	}
	log.ForContext(ctx).Debug().Str("user-id", userID).Msg("Generated machine key")
	return string(resp.KeyContent), nil
}

func (s *Seeder) generatePATToken(ctx context.Context, conn *zitadel.Connection, userID string) (string, error) {
	// Generate a Personal Access Token via v2 User API
	expiration := timestamppb.New(time.Now().AddDate(1, 0, 0)) // 1 Year validity

	userClient := user_pb.NewUserServiceClient(conn)
	resp, err := userClient.AddPersonalAccessToken(ctx, &user_pb.AddPersonalAccessTokenRequest{
		UserId:         userID,
		ExpirationDate: expiration,
	})
	if err != nil {
		return "", err
	}
	log.ForContext(ctx).Debug().Str("user-id", userID).Msg("Generated PAT token")
	return resp.Token, nil
}

func (s *Seeder) generateAppKey(ctx context.Context, conn *zitadel.Connection, projectID string, appID string) (string, error) {
	// Generate a new app key via v2 Application API
	expiration := timestamppb.New(time.Now().AddDate(1, 0, 0))

	appClient := app_pb.NewApplicationServiceClient(conn)
	resp, err := appClient.CreateApplicationKey(ctx, &app_pb.CreateApplicationKeyRequest{
		ProjectId:      projectID,
		ApplicationId:  appID,
		ExpirationDate: expiration,
	})
	if err != nil {
		return "", err
	}
	log.ForContext(ctx).Debug().Str("app-id", appID).Str("project-id", projectID).Msg("Generated app key")
	return string(resp.KeyDetails), nil
}

func (s *Seeder) ensureProjectRole(ctx context.Context, conn *zitadel.Connection, projectID string, roleKey string, displayName string) error {
	logger := log.ForContext(ctx)

	// List project roles using v2 Project API
	projClient := proj_pb.NewProjectServiceClient(conn)
	listResp, err := projClient.ListProjectRoles(ctx, &proj_pb.ListProjectRolesRequest{
		ProjectId: projectID,
		Filters: []*proj_pb.ProjectRoleSearchFilter{
			{
				Filter: &proj_pb.ProjectRoleSearchFilter_RoleKeyFilter{
					RoleKeyFilter: &proj_pb.ProjectRoleKeyFilter{
						Key:    roleKey,
						Method: filter_pb.TextFilterMethod_TEXT_FILTER_METHOD_EQUALS_IGNORE_CASE,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("listing project roles for %q: %w", roleKey, err)
	}

	for _, role := range listResp.GetProjectRoles() {
		if strings.EqualFold(role.Key, roleKey) {
			logger.Debug().Str("role", roleKey).Msg("Project role already exists")
			return nil
		}
	}

	// Create project role via v2 Project API
	_, err = projClient.AddProjectRole(ctx, &proj_pb.AddProjectRoleRequest{
		ProjectId:   projectID,
		RoleKey:     roleKey,
		DisplayName: displayName,
	})
	if err != nil {
		return fmt.Errorf("creating project role %q: %w", roleKey, err)
	}
	logger.Debug().Str("role", roleKey).Msg("Created project role")
	return nil
}

func (s *Seeder) ensureUserGrant(ctx context.Context, conn *zitadel.Connection, projectID string, orgID string, userID string, roleKeys []string) error {
	logger := log.ForContext(ctx)

	// Check if authorization exists using v2 Authorization API
	authzClient := authz_pb.NewAuthorizationServiceClient(conn)
	listResp, err := authzClient.ListAuthorizations(ctx, &authz_pb.ListAuthorizationsRequest{
		Filters: []*authz_pb.AuthorizationsSearchFilter{
			{
				Filter: &authz_pb.AuthorizationsSearchFilter_InUserIds{
					InUserIds: &filter_pb.InIDsFilter{
						Ids: []string{userID},
					},
				},
			},
			{
				Filter: &authz_pb.AuthorizationsSearchFilter_ProjectId{
					ProjectId: &filter_pb.IDFilter{
						Id: projectID,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("listing authorizations for user %q: %w", userID, err)
	}

	if len(listResp.GetAuthorizations()) > 0 {
		logger.Debug().Str("user-id", userID).Str("project-id", projectID).Msg("User grant already exists")
		return nil
	}

	// Create authorization via v2 Authorization API
	_, err = authzClient.CreateAuthorization(ctx, &authz_pb.CreateAuthorizationRequest{
		UserId:         userID,
		ProjectId:      projectID,
		OrganizationId: orgID,
		RoleKeys:       roleKeys,
	})
	if err != nil {
		return fmt.Errorf("creating authorization for user %q on project %q: %w", userID, projectID, err)
	}
	logger.Debug().Str("user-id", userID).Str("project-id", projectID).Msg("Created user authorization")
	return nil
}

func (s *Seeder) ensureProjectGrant(ctx context.Context, conn *zitadel.Connection, projectID string, grantingOrgID string, roleKeys []string) error {
	logger := log.ForContext(ctx)

	// Check if grant already exists using v2 project service
	projClient := proj_pb.NewProjectServiceClient(conn)
	listResp, err := projClient.ListProjectGrants(ctx, &proj_pb.ListProjectGrantsRequest{
		Filters: []*proj_pb.ProjectGrantSearchFilter{
			{
				Filter: &proj_pb.ProjectGrantSearchFilter_InProjectIdsFilter{
					InProjectIdsFilter: &filter_pb.InIDsFilter{
						Ids: []string{projectID},
					},
				},
			},
			{
				Filter: &proj_pb.ProjectGrantSearchFilter_GrantedOrganizationIdFilter{
					GrantedOrganizationIdFilter: &filter_pb.IDFilter{
						Id: grantingOrgID,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("listing project grants for %q: %w", projectID, err)
	}

	if len(listResp.GetProjectGrants()) > 0 {
		logger.Debug().Str("org-id", grantingOrgID).Str("project-id", projectID).Msg("Project grant already exists")
		return nil
	}

	_, err = projClient.CreateProjectGrant(ctx, &proj_pb.CreateProjectGrantRequest{
		ProjectId:             projectID,
		GrantedOrganizationId: grantingOrgID,
		RoleKeys:              roleKeys,
	})
	if err != nil {
		return fmt.Errorf("creating project grant for org %q on project %q: %w", grantingOrgID, projectID, err)
	}
	logger.Debug().Str("org-id", grantingOrgID).Str("project-id", projectID).Msg("Created project grant")
	return nil
}

func (s *Seeder) addIAMMember(ctx context.Context, conn *zitadel.Connection, userID string, roles []string) error {
	logger := log.ForContext(ctx)

	// Check if user already has IAM membership using v2 Internal Permission API
	permClient := perm_pb.NewInternalPermissionServiceClient(conn)
	listResp, err := permClient.ListAdministrators(ctx, &perm_pb.ListAdministratorsRequest{
		Filters: []*perm_pb.AdministratorSearchFilter{
			{
				Filter: &perm_pb.AdministratorSearchFilter_InUserIdsFilter{
					InUserIdsFilter: &filter_pb.InIDsFilter{
						Ids: []string{userID},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("listing administrators for user %q: %w", userID, err)
	}

	for _, admin := range listResp.GetAdministrators() {
		if admin.User != nil && admin.User.Id == userID {
			logger.Debug().Str("user-id", userID).Msg("User already has IAM membership")
			return nil
		}
	}

	// Create administrator with instance-level roles
	_, err = permClient.CreateAdministrator(ctx, &perm_pb.CreateAdministratorRequest{
		UserId: userID,
		Resource: &perm_pb.ResourceType{
			Resource: &perm_pb.ResourceType_Instance{Instance: true},
		},
		Roles: roles,
	})
	if err != nil {
		return fmt.Errorf("adding IAM member roles to user %q: %w", userID, err)
	}
	logger.Debug().Strs("roles", roles).Str("user-id", userID).Msg("Added IAM member roles")
	return nil
}

func (s *Seeder) confirmJWTAccess(ctx context.Context, keyJSON string) error {
	var (
		logger        = log.ForContext(ctx)
		confirmScopes = []string{"openid", "profile", "urn:zitadel:iam:org:project:id:zitadel:aud"}
	)

	// Create connection using in-memory key data
	ts := zitadelcommon.NewJWTProfileTokenSourceFromData(s.oauthURL, s.issuer, []byte(keyJSON))
	conn, err := zitadel.NewConnection(
		ctx, s.issuer, s.grpcAddr, confirmScopes,
		zitadel.WithTokenSource(ts),
	)
	if err != nil {
		return fmt.Errorf("failed to create auth connection: %w", err)
	}
	defer conn.Close()

	// Call GetMyUser via v1 Auth service (no v2 equivalent)
	authClient := auth_pb.NewAuthServiceClient(conn)
	resp, err := authClient.GetMyUser(ctx, &auth_pb.GetMyUserRequest{})
	if err != nil {
		return fmt.Errorf(" failed to get my user: %w", err)
	}

	logger.Debug().Str("username", resp.User.UserName).Str("id", resp.User.Id).Msg("Successfully authenticated")
	return nil
}

func (s *Seeder) cacheProjectURN(orgName, orgID, projectName, projectID string) {
	key := fmt.Sprintf("urn:zitadel:iam:org:%s:project:%s",
		strings.ToLower(orgName), strings.ToLower(projectName))
	s.projectIDs[key] = orgProject{
		orgID:     orgID,
		projectID: projectID,
	}
}

func (s *Seeder) resolveProjectURN(orgName, projectName string) (orgProject, bool) {
	key := fmt.Sprintf("urn:zitadel:iam:org:%s:project:%s",
		strings.ToLower(orgName), strings.ToLower(projectName))
	op, exists := s.projectIDs[key]
	return op, exists
}

func (s *Seeder) cacheProjectGrant(orgName, projectName string) {
	key := fmt.Sprintf("urn:zitadel:iam:org:%s:project:%s",
		strings.ToLower(orgName), strings.ToLower(projectName))
	s.projectGrants[key] = true
}

func (s *Seeder) resolveProjectGrant(orgName, projectName string) (string, bool) {
	key := fmt.Sprintf("urn:zitadel:iam:org:%s:project:%s",
		strings.ToLower(orgName), strings.ToLower(projectName))
	_, exists := s.projectGrants[key]
	return key, exists
}

// exportFields exports entity attributes using the configured exporters.
// The fields map contains available attributes (e.g. "id", "name") and their
// values. Each export entry references a field by name; if the field is missing
// from the map an error is returned.
func (s *Seeder) exportFields(ctx context.Context, fields map[string]string, exports []*exporters.Export, entityLabel string) error {
	for _, export := range exports {
		value, ok := fields[export.Field]
		if !ok {
			// Field not available (e.g. client_secret on idempotent re-run).
			// Skip rather than fail so bootstrap remains idempotent.
			log.ForContext(ctx).Info().
				Str("field", export.Field).
				Str("entity", entityLabel).
				Msg("Skipping export for unavailable field")

			continue
		}
		if err := s.exporters.Export(ctx, value, *export); err != nil {
			return fmt.Errorf("failed to export field %q for %s: %w", export.Field, entityLabel, err)
		}
	}
	return nil
}
