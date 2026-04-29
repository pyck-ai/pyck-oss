package zitadel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	adminclient "github.com/zitadel/zitadel-go/v3/pkg/client/admin"
	managementclient "github.com/zitadel/zitadel-go/v3/pkg/client/management"
	"github.com/zitadel/zitadel-go/v3/pkg/client/middleware"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	app_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/app"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authn"
	pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	object_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object"
	org_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org"
	project_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project"
	user_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	ProjectRoleSystem         = "system"
	ProjectRoleAdmin          = "admin"
	ProjectRoleWriter         = "writer"
	ProjectRoleReader         = "reader"
	ProjectRoleTemporalReader = "temporal_reader"
	ProjectRoleTemporalWriter = "temporal_writer"
	ProjectRoleTemporalAdmin  = "temporal_admin"

	AppTypeOIDC  = "oidc"
	AppTypeAPI   = "api"
	defaultLimit = 1000

	orgOwner = "ORG_OWNER"
)

type ZitadelSdkClient struct {
	managementAPI *managementclient.Client
	adminAPI      *adminclient.Client
}

// SdkClient builds admin/management clients with JWT profile credentials.
// grpcAddr is the host:port for gRPC dial (e.g. "localhost:8080").
// apiURL is the full HTTP URL for token exchange (e.g. "http://localhost:8080").
// When insecure is true, the connection uses plaintext gRPC and a custom token
// source that bypasses OIDC discovery.
func SdkClient(ctx context.Context, issuer, grpcAddr, apiURL, jwtProfilePath, orgID string, insecure bool) (*ZitadelSdkClient, error) {
	options := sdkOptions(ctx, issuer, apiURL, jwtProfilePath, insecure)
	if orgID != "" {
		options = append(options, zitadel.WithOrgID(orgID))
	}

	managementClient, err := managementclient.NewClient(
		ctx, issuer, grpcAddr,
		[]string{oidc.ScopeOpenID, "urn:zitadel:iam:org:project:id:zitadel:aud"},
		options...,
	)
	if err != nil {
		return nil, err
	}

	adminClient, err := adminclient.NewClient(
		ctx, issuer, grpcAddr,
		[]string{oidc.ScopeOpenID, "urn:zitadel:iam:org:project:id:zitadel:aud"},
		options...,
	)
	if err != nil {
		return nil, err
	}

	return &ZitadelSdkClient{
		managementAPI: managementClient,
		adminAPI:      adminClient,
	}, nil
}

// sdkOptions returns the common zitadel.Option slice for SDK connections.
func sdkOptions(ctx context.Context, issuer, apiURL, jwtProfilePath string, insecure bool) []zitadel.Option {
	if insecure {
		return []zitadel.Option{
			zitadel.WithInsecure(),
			zitadel.WithTokenSource(NewJWTProfileTokenSource(apiURL, issuer, jwtProfilePath)),
		}
	}
	return []zitadel.Option{
		zitadel.WithJWTProfileTokenSource(middleware.JWTProfileFromPath(ctx, jwtProfilePath)),
	}
}

func (client *ZitadelSdkClient) Close() {
	logger := log.DefaultLogger().With().
		Str("component", "zitadel-client").
		Logger()
	if err := client.managementAPI.Connection.Close(); err != nil {
		logger.Warn().Err(err).Msg("could not close grpc management connection")
	}

	if err := client.adminAPI.Connection.Close(); err != nil {
		logger.Warn().Err(err).Msg("could not close grpc admin connection")
	}
}

func (client *ZitadelSdkClient) GetOrgID(ctx context.Context) (string, error) {
	resp, err := client.managementAPI.GetMyOrg(ctx, &pb.GetMyOrgRequest{})
	if err != nil {
		return "", err
	}

	return resp.Org.GetId(), nil
}

func (client *ZitadelSdkClient) UpdateDomainPolicy(ctx context.Context, loginMustBeDomain bool) error {
	reqPayload := &admin.UpdateDomainPolicyRequest{
		UserLoginMustBeDomain: loginMustBeDomain,
	}
	_, err := client.adminAPI.UpdateDomainPolicy(ctx, reqPayload)

	return err
}

func (client *ZitadelSdkClient) CreateHumanUser(ctx context.Context, username string, firstName string, lastName string, email string, isEmailVerified bool, password string, changePassword bool) (*UserBase, error) {
	request := &pb.ImportHumanUserRequest{
		UserName: username,
		Profile: &pb.ImportHumanUserRequest_Profile{
			FirstName: firstName,
			LastName:  lastName,
		},
		Email: &pb.ImportHumanUserRequest_Email{
			Email:           email,
			IsEmailVerified: isEmailVerified,
		},
		Password:               password,
		PasswordChangeRequired: changePassword,
	}

	createUserResp, err := client.managementAPI.ImportHumanUser(ctx, request)
	if err != nil {
		return nil, err
	}

	resp, err := client.managementAPI.GetUserByID(ctx, &pb.GetUserByIDRequest{Id: createUserResp.UserId})
	if err != nil {
		return nil, err
	}

	return &UserBase{ID: resp.GetUser().GetId(), LoginName: resp.GetUser().GetPreferredLoginName()}, nil
}

func (client *ZitadelSdkClient) GetUserBy(ctx context.Context, username string, email string) (*UserBase, error) {
	if username == "" && email == "" {
		return nil, fmt.Errorf("username or email must be provided")
	}

	var searchQueries []*user_pb.SearchQuery
	if username != "" {
		searchQueries = append(searchQueries, &user_pb.SearchQuery{
			Query: &user_pb.SearchQuery_UserNameQuery{
				UserNameQuery: &user_pb.UserNameQuery{UserName: username},
			},
		})
	}

	if email != "" {
		searchQueries = append(searchQueries, &user_pb.SearchQuery{
			Query: &user_pb.SearchQuery_EmailQuery{
				EmailQuery: &user_pb.EmailQuery{EmailAddress: email},
			},
		})
	}

	reqPayload := &pb.ListUsersRequest{
		Query:   &object_pb.ListQuery{Limit: 1},
		Queries: searchQueries,
	}
	resp, err := client.managementAPI.ListUsers(ctx, reqPayload)
	if err != nil {
		return nil, err
	}
	if len(resp.Result) == 0 {
		return nil, fmt.Errorf("user not found")
	}

	user := resp.Result[0]

	return &UserBase{ID: user.GetId(), LoginName: user.GetPreferredLoginName()}, nil
}

func (client *ZitadelSdkClient) AddIAMMember(ctx context.Context, userID string, roles []string) error {
	_, err := client.adminAPI.AddIAMMember(ctx, &admin.AddIAMMemberRequest{
		UserId: userID,
		Roles:  roles,
	})

	return err
}

func (client *ZitadelSdkClient) AddOrganizationMember(ctx context.Context, userID string, roles []string) error {
	_, err := client.managementAPI.AddOrgMember(ctx, &pb.AddOrgMemberRequest{
		UserId: userID,
		Roles:  roles,
	})

	return err
}

func (client *ZitadelSdkClient) OrganizationMembers(ctx context.Context) ([]*UserBase, error) {
	resp, err := client.managementAPI.ListOrgMembers(ctx, &pb.ListOrgMembersRequest{})
	if err != nil {
		return nil, err
	}

	users := []*UserBase{}
	for _, member := range resp.Result {
		users = append(users, &UserBase{ID: member.UserId, LoginName: member.PreferredLoginName})
	}

	return users, nil
}

func (client *ZitadelSdkClient) RemoveOrganizationMember(ctx context.Context, userID string) error {
	_, err := client.managementAPI.RemoveOrgMember(ctx, &pb.RemoveOrgMemberRequest{UserId: userID})

	return err
}

func (client *ZitadelSdkClient) AddProject(ctx context.Context, name string) (*Project, error) {
	projectResp, err := client.managementAPI.AddProject(ctx, &pb.AddProjectRequest{
		Name:                 name,
		ProjectRoleAssertion: true,
		ProjectRoleCheck:     true,
		HasProjectCheck:      true,
	})
	if err != nil {
		return nil, err
	}

	return &Project{ID: projectResp.GetId()}, nil
}

func (client *ZitadelSdkClient) GetProjectByName(ctx context.Context, name string) (*Project, error) {
	projQuery := &project_pb.ProjectQuery{
		Query: &project_pb.ProjectQuery_NameQuery{
			NameQuery: &project_pb.ProjectNameQuery{
				Name:   name,
				Method: object_pb.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
			},
		},
	}
	resp, err := client.managementAPI.ListProjects(ctx, &pb.ListProjectsRequest{
		Queries: []*project_pb.ProjectQuery{projQuery},
	})
	if err != nil {
		return nil, err
	}

	for _, project := range resp.Result {
		if project.Name == name {
			return &Project{ID: project.Id}, nil
		}
	}

	return nil, fmt.Errorf("project not found")
}

func (client *ZitadelSdkClient) AddProjectRoles(ctx context.Context, projectID string, projectRoles []string) error {
	roles := []*pb.BulkAddProjectRolesRequest_Role{}
	for _, role := range projectRoles {
		roles = append(roles, &pb.BulkAddProjectRolesRequest_Role{
			Key:         role,
			DisplayName: cases.Title(language.Und, cases.NoLower).String(role),
		})
	}

	_, err := client.managementAPI.BulkAddProjectRoles(ctx, &pb.BulkAddProjectRolesRequest{
		ProjectId: projectID,
		Roles:     roles,
	})

	return err
}

func (client *ZitadelSdkClient) AddApiAppToProject(ctx context.Context, projectID string, name string) (*App, error) {
	resp, err := client.managementAPI.AddAPIApp(ctx, &pb.AddAPIAppRequest{
		ProjectId:      projectID,
		Name:           name,
		AuthMethodType: app_pb.APIAuthMethodType_API_AUTH_METHOD_TYPE_PRIVATE_KEY_JWT,
	})
	if err != nil {
		return nil, err
	}

	return &App{ID: resp.GetAppId(), ClientID: resp.GetClientId(), ClientSecret: resp.GetClientSecret()}, nil
}

func (client *ZitadelSdkClient) AddWebAppToProject(ctx context.Context, projectID string, name string, authMethodType string, loginRedirectUrls []string, logoutRedirectUrls []string, isDevMode bool) (*App, error) {
	var authMethod app_pb.OIDCAuthMethodType
	switch strings.ToLower(authMethodType) {
	case "basic":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC
	case "post":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_POST
	default:
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE
	}

	requestPayload := &pb.AddOIDCAppRequest{
		ProjectId:                projectID,
		Name:                     name,
		RedirectUris:             loginRedirectUrls,
		ResponseTypes:            []app_pb.OIDCResponseType{app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE},
		GrantTypes:               []app_pb.OIDCGrantType{app_pb.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE, app_pb.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN},
		AppType:                  app_pb.OIDCAppType_OIDC_APP_TYPE_WEB,
		AuthMethodType:           authMethod,
		PostLogoutRedirectUris:   logoutRedirectUrls,
		Version:                  app_pb.OIDCVersion_OIDC_VERSION_1_0,
		DevMode:                  isDevMode,
		AccessTokenType:          app_pb.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
		AccessTokenRoleAssertion: true,
		IdTokenRoleAssertion:     true,
		IdTokenUserinfoAssertion: true,
	}
	resp, err := client.managementAPI.AddOIDCApp(ctx, requestPayload)
	if err != nil {
		return nil, err
	}

	return &App{ID: resp.GetAppId(), ClientID: resp.GetClientId(), ClientSecret: resp.GetClientSecret()}, nil
}

func (client *ZitadelSdkClient) AddSpaAppToProject(ctx context.Context, projectID string, name string, authMethodType string, loginRedirectUrls []string, logoutRedirectUrls []string, isDevMode bool) (*App, error) {
	var authMethod app_pb.OIDCAuthMethodType
	switch strings.ToLower(authMethodType) {
	case "basic":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC
	case "post":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_POST
	default:
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE
	}

	requestPayload := &pb.AddOIDCAppRequest{
		ProjectId:                projectID,
		Name:                     name,
		RedirectUris:             loginRedirectUrls,
		ResponseTypes:            []app_pb.OIDCResponseType{app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE},
		GrantTypes:               []app_pb.OIDCGrantType{app_pb.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE, app_pb.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN},
		AppType:                  app_pb.OIDCAppType_OIDC_APP_TYPE_USER_AGENT,
		AuthMethodType:           authMethod,
		PostLogoutRedirectUris:   logoutRedirectUrls,
		Version:                  app_pb.OIDCVersion_OIDC_VERSION_1_0,
		DevMode:                  isDevMode,
		AccessTokenType:          app_pb.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
		AccessTokenRoleAssertion: true,
		IdTokenRoleAssertion:     true,
		IdTokenUserinfoAssertion: true,
	}
	resp, err := client.managementAPI.AddOIDCApp(ctx, requestPayload)
	if err != nil {
		return nil, err
	}

	return &App{ID: resp.GetAppId(), ClientID: resp.GetClientId(), ClientSecret: resp.GetClientSecret()}, nil
}

func (client *ZitadelSdkClient) AddNativeAppToProject(ctx context.Context, projectID string, name string, authMethodType string, loginRedirectUrls []string, logoutRedirectUrls []string, isDevMode bool) (*App, error) {
	var authMethod app_pb.OIDCAuthMethodType
	switch strings.ToLower(authMethodType) {
	case "basic":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC
	case "post":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_POST
	default:
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE
	}

	requestPayload := &pb.AddOIDCAppRequest{
		ProjectId:                projectID,
		Name:                     name,
		RedirectUris:             loginRedirectUrls,
		ResponseTypes:            []app_pb.OIDCResponseType{app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE},
		GrantTypes:               []app_pb.OIDCGrantType{app_pb.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE, app_pb.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN},
		AppType:                  app_pb.OIDCAppType_OIDC_APP_TYPE_NATIVE,
		AuthMethodType:           authMethod,
		PostLogoutRedirectUris:   logoutRedirectUrls,
		Version:                  app_pb.OIDCVersion_OIDC_VERSION_1_0,
		DevMode:                  isDevMode,
		AccessTokenType:          app_pb.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
		AccessTokenRoleAssertion: true,
		IdTokenRoleAssertion:     true,
		IdTokenUserinfoAssertion: true,
	}
	resp, err := client.managementAPI.AddOIDCApp(ctx, requestPayload)
	if err != nil {
		return nil, err
	}

	return &App{ID: resp.GetAppId(), ClientID: resp.GetClientId(), ClientSecret: resp.GetClientSecret()}, nil
}

func (client *ZitadelSdkClient) AddOIDCAppToProject(ctx context.Context, projectID, name string, redirectURIs []string, postLogoutRedirectURIs []string) (*App, error) {
	resp, err := client.managementAPI.AddOIDCApp(ctx, &pb.AddOIDCAppRequest{
		ProjectId:                projectID,
		Name:                     name,
		RedirectUris:             redirectURIs,
		PostLogoutRedirectUris:   postLogoutRedirectURIs,
		ResponseTypes:            []app_pb.OIDCResponseType{app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE},
		GrantTypes:               []app_pb.OIDCGrantType{app_pb.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE},
		AppType:                  app_pb.OIDCAppType_OIDC_APP_TYPE_WEB,
		AuthMethodType:           app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC,
		Version:                  app_pb.OIDCVersion_OIDC_VERSION_1_0,
		ClockSkew:                durationpb.New(0),
		DevMode:                  true,
		AccessTokenType:          app_pb.OIDCTokenType_OIDC_TOKEN_TYPE_BEARER,
		AccessTokenRoleAssertion: true,
		IdTokenRoleAssertion:     true,
		IdTokenUserinfoAssertion: true,
	})
	if err != nil {
		return nil, err
	}

	return &App{ID: resp.GetAppId(), ClientID: resp.GetClientId(), ClientSecret: resp.GetClientSecret()}, nil
}

func (client *ZitadelSdkClient) UpdateOIDCAppConfig(ctx context.Context, projectID string, appId string, authMethodType string, loginRedirectUrls []string, logoutRedirectUrls []string, isDevMode bool) (*pb.UpdateOIDCAppConfigResponse, error) {
	var authMethod app_pb.OIDCAuthMethodType
	switch strings.ToLower(authMethodType) {
	case "basic":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC
	case "post":
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_POST
	default:
		authMethod = app_pb.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE
	}

	req := &pb.UpdateOIDCAppConfigRequest{
		ProjectId:                projectID,
		AppId:                    appId,
		RedirectUris:             loginRedirectUrls,
		ResponseTypes:            []app_pb.OIDCResponseType{app_pb.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE},
		GrantTypes:               []app_pb.OIDCGrantType{app_pb.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE, app_pb.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN},
		AppType:                  app_pb.OIDCAppType_OIDC_APP_TYPE_WEB,
		AuthMethodType:           authMethod,
		PostLogoutRedirectUris:   logoutRedirectUrls,
		DevMode:                  isDevMode,
		AccessTokenType:          app_pb.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
		AccessTokenRoleAssertion: true,
		IdTokenRoleAssertion:     true,
		IdTokenUserinfoAssertion: true,
	}

	return client.managementAPI.UpdateOIDCAppConfig(ctx, req)
}

func (client *ZitadelSdkClient) GetAppByName(ctx context.Context, projectID string, name string, appType string) (*App, error) {
	appQuery := &app_pb.AppQuery{
		Query: &app_pb.AppQuery_NameQuery{
			NameQuery: &app_pb.AppNameQuery{
				Name:   name,
				Method: object_pb.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
			},
		},
	}
	resp, err := client.managementAPI.ListApps(ctx, &pb.ListAppsRequest{
		ProjectId: projectID,
		Queries:   []*app_pb.AppQuery{appQuery},
	})
	if err != nil {
		return nil, err
	}
	for _, app := range resp.Result {
		if app.Name == name {
			clientID := ""
			switch appType {
			case AppTypeOIDC:
				clientID = app.GetOidcConfig().GetClientId()
			case AppTypeAPI:
				clientID = app.GetApiConfig().GetClientId()
			}
			return &App{ID: app.GetId(), ClientID: clientID}, nil
		}
	}

	return nil, fmt.Errorf("app not found")
}

func (client *ZitadelSdkClient) AddJsonAppKey(ctx context.Context, projectID, appID string) (*AppKey, error) {
	resp, err := client.managementAPI.AddAppKey(ctx, &pb.AddAppKeyRequest{
		ProjectId: projectID,
		AppId:     appID,
		Type:      authn.KeyType_KEY_TYPE_JSON,
	})
	if err != nil {
		return nil, err
	}

	return &AppKey{ID: resp.GetId(), JSON: string(resp.GetKeyDetails())}, nil
}

func (client *ZitadelSdkClient) GetJsonAppKeys(ctx context.Context, projectID string, appID string) ([]JsonAppKeyInfo, error) {
	resp, err := client.managementAPI.ListAppKeys(ctx, &pb.ListAppKeysRequest{
		ProjectId: projectID,
		AppId:     appID,
	})
	if err != nil {
		return nil, err
	}

	jsonAppKeys := []JsonAppKeyInfo{}
	for _, key := range resp.Result {
		if key.Type == authn.KeyType_KEY_TYPE_JSON {
			var expiration time.Time
			if key.ExpirationDate != nil {
				expiration = key.ExpirationDate.AsTime()
			}
			jsonAppKeys = append(jsonAppKeys, JsonAppKeyInfo{
				ID:         key.GetId(),
				Expiration: expiration,
			})
		}
	}

	return jsonAppKeys, nil
}

func (client *ZitadelSdkClient) AddServiceUser(ctx context.Context, userName, name string) (*UserBase, error) {
	resp, err := client.managementAPI.AddMachineUser(ctx, &pb.AddMachineUserRequest{
		UserName:        userName,
		Name:            name,
		AccessTokenType: 0, // Bearer
	})
	if err != nil {
		return nil, err
	}

	return &UserBase{ID: resp.GetUserId()}, nil
}

func (client *ZitadelSdkClient) AddServiceUserToken(ctx context.Context, userID string) (*PatToken, error) {
	resp, err := client.managementAPI.AddPersonalAccessToken(ctx, &pb.AddPersonalAccessTokenRequest{
		UserId: userID,
	})
	if err != nil {
		return nil, err
	}

	return &PatToken{ID: resp.GetTokenId(), Token: resp.GetToken()}, nil
}

func (client *ZitadelSdkClient) AddUserGrantForProject(ctx context.Context, projectID, userID string, roleKeys []string) error {
	_, err := client.managementAPI.AddUserGrant(ctx, &pb.AddUserGrantRequest{
		UserId:    userID,
		ProjectId: projectID,
		RoleKeys:  roleKeys,
	})

	return err
}

func (client *ZitadelSdkClient) AddOrganization(ctx context.Context, name string) (*Org, error) {
	resp, err := client.managementAPI.AddOrg(ctx, &pb.AddOrgRequest{Name: name})
	if err != nil {
		return nil, err
	}

	return &Org{ID: resp.GetId()}, nil
}

func (client *ZitadelSdkClient) DeleteMyOrganization(ctx context.Context) error {
	_, err := client.managementAPI.RemoveOrg(ctx, &pb.RemoveOrgRequest{})

	return err
}

func (client *ZitadelSdkClient) GetOrganizationByName(ctx context.Context, name string) (*Org, error) {
	nameQuery := &org_pb.OrgQuery{
		Query: &org_pb.OrgQuery_NameQuery{
			NameQuery: &org_pb.OrgNameQuery{
				Name:   name,
				Method: object_pb.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
			},
		},
	}
	reqPayload := &admin.ListOrgsRequest{
		Query:   &object_pb.ListQuery{Limit: 1},
		Queries: []*org_pb.OrgQuery{nameQuery},
	}
	resp, err := client.adminAPI.ListOrgs(ctx, reqPayload)
	if err != nil {
		return nil, err
	}
	if len(resp.Result) == 0 {
		return nil, fmt.Errorf("organization not found")
	}

	return &Org{ID: resp.Result[0].Id}, nil
}

func (client *ZitadelSdkClient) AddProjectGrant(ctx context.Context, projectID string, orgID string, roleKeys []string) (*Grant, error) {
	resp, err := client.managementAPI.AddProjectGrant(ctx, &pb.AddProjectGrantRequest{
		ProjectId:    projectID,
		GrantedOrgId: orgID,
		RoleKeys:     roleKeys,
	})
	if err != nil {
		return nil, err
	}

	return &Grant{ID: resp.GetGrantId()}, nil
}

func (client *ZitadelSdkClient) GetProjectGrant(ctx context.Context, projectID string, organizationID string) (*Grant, error) {
	resp, err := client.managementAPI.ListProjectGrants(ctx, &pb.ListProjectGrantsRequest{ProjectId: projectID})
	if err != nil {
		return nil, err
	}
	for _, grant := range resp.Result {
		if grant.GrantedOrgId == organizationID {
			return &Grant{ID: grant.GrantId}, nil
		}
	}

	return nil, fmt.Errorf("project grant not found")
}

func (client *ZitadelSdkClient) AddUserGrant(ctx context.Context, projectID, userID, grantID string, roleKeys []string) error {
	_, err := client.managementAPI.AddUserGrant(ctx, &pb.AddUserGrantRequest{
		UserId:         userID,
		ProjectId:      projectID,
		ProjectGrantId: grantID,
		RoleKeys:       roleKeys,
	})

	return err
}

func (client *ZitadelSdkClient) GetOrganizations(ctx context.Context, skip uint64, limit uint32) ([]*Org, error) {
	paginationQuery := &object_pb.ListQuery{Limit: limit, Offset: skip}
	resp, err := client.adminAPI.ListOrgs(ctx, &admin.ListOrgsRequest{Query: paginationQuery})
	if err != nil {
		return nil, err
	}

	orgs := []*Org{}
	for _, org := range resp.Result {
		orgs = append(orgs, &Org{ID: org.Id, Name: org.Name})
	}

	return orgs, nil
}

func (client *ZitadelSdkClient) GetAllOrganizations(ctx context.Context) ([]*Org, error) {
	orgs := []*Org{}
	var skip uint64
	var limit uint32 = defaultLimit
	for {
		currentOrgs, err := client.GetOrganizations(ctx, skip, limit)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, currentOrgs...)
		if len(currentOrgs) < int(limit) {
			break
		}
		skip += uint64(limit)
	}

	return orgs, nil
}

func (client *ZitadelSdkClient) GetOrganizationUsers(ctx context.Context, skip uint64, limit uint32) ([]*UserProfile, error) {
	paginationQuery := &object_pb.ListQuery{Limit: limit, Offset: skip}
	resp, err := client.managementAPI.ListUsers(ctx, &pb.ListUsersRequest{Query: paginationQuery})
	if err != nil {
		return nil, err
	}

	users := []*UserProfile{}
	for _, user := range resp.Result {
		firstName := ""
		lastName := ""
		email := ""

		switch user.GetType().(type) {
		case *user_pb.User_Machine:
			machineUser := user.GetType().(*user_pb.User_Machine).Machine
			splitName := strings.SplitN(machineUser.Name, " ", 2)
			firstName = splitName[0]
			if len(splitName) > 1 {
				lastName = splitName[1]
			}
		case *user_pb.User_Human:
			humanUser := user.GetType().(*user_pb.User_Human).Human
			firstName = humanUser.Profile.FirstName
			lastName = humanUser.Profile.LastName
			email = humanUser.Email.Email
		}
		users = append(users, &UserProfile{
			ID:        user.Id,
			Username:  user.UserName,
			Email:     email,
			FirstName: firstName,
			LastName:  lastName,
		})
	}

	return users, nil
}

func (client *ZitadelSdkClient) GetAllOrganizationUsers(ctx context.Context) ([]*UserProfile, error) {
	users := []*UserProfile{}
	var skip uint64
	var limit uint32 = defaultLimit
	for {
		currentUsers, err := client.GetOrganizationUsers(ctx, skip, limit)
		if err != nil {
			return nil, err
		}
		users = append(users, currentUsers...)
		if len(currentUsers) < int(limit) {
			break
		}
		skip += uint64(limit)
	}

	return users, nil
}

func (client *ZitadelSdkClient) GetOrganizationUsersRoles(ctx context.Context, projectID string, skip uint64, limit uint32) ([]*UserRoles, error) {
	paginationQuery := &object_pb.ListQuery{Limit: limit, Offset: skip}
	projectIdQuery := &user_pb.UserGrantQuery_ProjectIdQuery{
		ProjectIdQuery: &user_pb.UserGrantProjectIDQuery{ProjectId: projectID},
	}
	request := &pb.ListUserGrantRequest{
		Query:   paginationQuery,
		Queries: []*user_pb.UserGrantQuery{{Query: projectIdQuery}},
	}
	grants, err := client.managementAPI.ListUserGrants(ctx, request)
	if err != nil {
		return nil, err
	}

	userRoles := []*UserRoles{}
	for _, grant := range grants.Result {
		userRoles = append(userRoles, &UserRoles{ID: grant.UserId, Roles: grant.RoleKeys})
	}

	return userRoles, nil
}

func (client *ZitadelSdkClient) GetAllOrganizationUsersRoles(ctx context.Context, projectID string) ([]*UserRoles, error) {
	userRoles := []*UserRoles{}
	var skip uint64
	var limit uint32 = defaultLimit
	for {
		currentUserRoles, err := client.GetOrganizationUsersRoles(ctx, projectID, skip, limit)
		if err != nil {
			return nil, err
		}
		userRoles = append(userRoles, currentUserRoles...)
		if len(currentUserRoles) < int(limit) {
			break
		}
		skip += uint64(limit)
	}

	return userRoles, nil
}

func (client *ZitadelSdkClient) GetOrganizationOwners(ctx context.Context, skip uint64, limit uint32) ([]string, error) {
	paginationQuery := &object_pb.ListQuery{Limit: limit, Offset: skip}
	response, err := client.managementAPI.ListOrgMembers(ctx, &pb.ListOrgMembersRequest{Query: paginationQuery})
	if err != nil {
		return nil, err
	}

	owners := []string{}
	for _, member := range response.Result {
		if slices.Contains(member.Roles, orgOwner) {
			owners = append(owners, member.UserId)
		}
	}

	return owners, nil
}

func (client *ZitadelSdkClient) SetOrgMetadata(ctx context.Context, key string, value string) error {
	//nolint:staticcheck // TODO: migrate to organization service v2 when available
	_, err := client.managementAPI.SetOrgMetadata(ctx, &pb.SetOrgMetadataRequest{
		Key:   key,
		Value: []byte(value),
	})

	return err
}

// ListOrgMetadataForOrg fetches metadata for a specific organization by
// injecting the org ID as gRPC call metadata. This allows reusing a single
// client connection across multiple orgs instead of creating a new client per org.
func (client *ZitadelSdkClient) ListOrgMetadataForOrg(ctx context.Context, orgID string) (map[string]string, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "x-zitadel-orgid", orgID)
	return client.ListOrgMetadata(ctx)
}

func (client *ZitadelSdkClient) ListOrgMetadata(ctx context.Context) (map[string]string, error) {
	//nolint:staticcheck // TODO: migrate to organization service v2 when available
	resp, err := client.managementAPI.ListOrgMetadata(ctx, &pb.ListOrgMetadataRequest{})
	if err != nil {
		return nil, err
	}

	metadata := make(map[string]string)
	for _, m := range resp.GetResult() {
		metadata[m.GetKey()] = string(m.GetValue())
	}

	return metadata, nil
}

func (client *ZitadelSdkClient) GetAllOrganizationOwners(ctx context.Context) ([]string, error) {
	owners := []string{}
	var skip uint64
	var limit uint32 = defaultLimit
	for {
		currentOwners, err := client.GetOrganizationOwners(ctx, skip, limit)
		if err != nil {
			return nil, err
		}
		owners = append(owners, currentOwners...)
		if len(currentOwners) < int(limit) {
			break
		}
		skip += uint64(limit)
	}

	return owners, nil
}
