package zitadelsetup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pyck-ai/pyck/backend/common/services/kubernetes"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"go.temporal.io/sdk/activity"
	"google.golang.org/grpc/codes"
)

var ErrInvalidIssuerHostname = errors.New("could not extract hostname from issuer URL")

func WaitForHostReachable(ctx context.Context, input zitadelClientInput) error {
	parsedURL, err := url.Parse(input.Issuer)
	if err != nil {
		return fmt.Errorf("invalid issuer URL: %w", err)
	}

	host := parsedURL.Hostname()
	if host == "" {
		return fmt.Errorf("%w: %s", ErrInvalidIssuerHostname, input.Issuer)
	}

	// Recover from last heartbeat if activity was retried
	startTime := time.Now()
	if activity.HasHeartbeatDetails(ctx) {
		var lastCheckNano int64
		if err := activity.GetHeartbeatDetails(ctx, &lastCheckNano); err == nil {
			startTime = time.Unix(0, lastCheckNano)
		}
	}

	timeout := 10 * time.Minute
	deadline := startTime.Add(timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: nil,
		},
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Check DNS resolution
			resolver := &net.Resolver{}
			ips, err := resolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				// Report progress via heartbeat
				activity.RecordHeartbeat(ctx, time.Now().UnixNano())

				if time.Now().After(deadline) {
					return fmt.Errorf("DNS resolution failed for host %s after %v: %w", host, timeout, err)
				}
				continue
			}

			// Check HTTP reachability
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.Issuer, nil)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			resp, err := httpClient.Do(req)
			if err == nil {
				resp.Body.Close()
				return nil
			}

			// Report progress via heartbeat
			activity.RecordHeartbeat(ctx, time.Now().UnixNano())

			if time.Now().After(deadline) {
				return fmt.Errorf("host %s not reachable after %v: %w", host, timeout, err)
			}
		}
	}
}

func GetOrgID(ctx context.Context, input zitadelClientInput) (*organization, error) {
	zitadelClient, err := getZitadelClient(ctx, input)
	if err != nil {
		return nil, err
	}

	orgID, err := zitadelClient.GetOrgID(ctx)
	if err != nil {
		return nil, err
	}

	return &organization{ID: orgID}, nil
}

func AddOrgDomainSuffixPattern(ctx context.Context, input zitadelClientInput) error {
	zitadelClient, err := getZitadelClient(ctx, input)
	if err != nil {
		return err
	}

	err = zitadelClient.UpdateDomainPolicy(ctx, true)
	if err != nil && !strings.Contains(err.Error(), "Org IAM Policy has not been changed") {
		return err
	}
	return nil
}

func CreateUser(ctx context.Context, input createUserInput) (*user, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	response, err := zitadelClient.CreateHumanUser(
		ctx, input.Username, input.FirstName, input.LastName, input.Email, input.IsEmailVerified, input.Password, input.ChangePassword,
	)

	if err == nil {
		return &user{ID: response.ID, LoginName: response.LoginName}, nil
	}

	if isAlreadyExistsError(err) {
		response, err = zitadelClient.GetUserBy(ctx, input.Username, input.Email)
		if err != nil {
			return nil, err
		}
		return &user{ID: response.ID, LoginName: response.LoginName}, nil
	}
	return nil, err
}

func SetUserAsAdmin(ctx context.Context, input setUserAdminInput) error {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return err
	}

	err = zitadelClient.AddIAMMember(ctx, input.UserID, []string{"IAM_OWNER"})
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

func AddProject(ctx context.Context, input addProjectInput) (*project, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	proj, err := zitadelClient.AddProject(ctx, input.ProjectName)
	if err == nil {
		return &project{ID: proj.ID}, nil
	}

	if isAlreadyExistsError(err) {
		proj, err = zitadelClient.GetProjectByName(ctx, input.ProjectName)
		if err != nil {
			return nil, err
		}

		return &project{ID: proj.ID}, nil
	}
	return nil, err
}

func AddProjectRoles(ctx context.Context, input addProjectRolesInput) error {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return err
	}

	projectRoles := []string{zitadel.ProjectRoleSystem, zitadel.ProjectRoleAdmin, zitadel.ProjectRoleWriter, zitadel.ProjectRoleReader, zitadel.ProjectRoleTemporalReader, zitadel.ProjectRoleTemporalWriter, zitadel.ProjectRoleTemporalAdmin}
	err = zitadelClient.AddProjectRoles(ctx, input.ProjectID, projectRoles)
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

func AddAppToProject(ctx context.Context, input addAppToProjectInput) (*app, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	appInput := input.App
	appType := strings.ToLower(appInput.AppType)
	if appType == "" {
		appType = "web"
	}

	var resolvedAppType string
	if appType == "api" {
		resolvedAppType = zitadel.AppTypeAPI
	} else {
		resolvedAppType = zitadel.AppTypeOIDC
	}

	existingApp, err := zitadelClient.GetAppByName(ctx, input.ProjectID, appInput.Name, resolvedAppType)
	if err == nil {
		if appType != "api" {
			_, err = zitadelClient.UpdateOIDCAppConfig(ctx, input.ProjectID, existingApp.ID, appInput.AuthMethodType, appInput.LoginRedirectUrls, appInput.LogoutRedirectUrls, appInput.IsDevMode)
			if err != nil && !strings.Contains(err.Error(), "No changes") {
				return nil, err
			}
		}

		return &app{
			ID:           existingApp.ID,
			ClientID:     existingApp.ClientID,
			ClientSecret: existingApp.ClientSecret,
		}, nil
	}

	var createdApp *zitadel.App
	switch appType {
	case "web":
		createdApp, err = zitadelClient.AddWebAppToProject(ctx, input.ProjectID, appInput.Name, appInput.AuthMethodType, appInput.LoginRedirectUrls, appInput.LogoutRedirectUrls, appInput.IsDevMode)
	case "spa":
		createdApp, err = zitadelClient.AddSpaAppToProject(ctx, input.ProjectID, appInput.Name, appInput.AuthMethodType, appInput.LoginRedirectUrls, appInput.LogoutRedirectUrls, appInput.IsDevMode)
	case "native":
		createdApp, err = zitadelClient.AddNativeAppToProject(ctx, input.ProjectID, appInput.Name, appInput.AuthMethodType, appInput.LoginRedirectUrls, appInput.LogoutRedirectUrls, appInput.IsDevMode)
	case "api":
		createdApp, err = zitadelClient.AddApiAppToProject(ctx, input.ProjectID, appInput.Name)
	default:
		return nil, fmt.Errorf("unsupported app type: %s", appType)
	}
	if err != nil {
		return nil, err
	}

	return &app{
		ID:           createdApp.ID,
		ClientID:     createdApp.ClientID,
		ClientSecret: createdApp.ClientSecret,
	}, nil
}

func AddJsonAppKey(ctx context.Context, input addJsonAppKeyInput) (*jsonAppKey, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	existingKeys, err := zitadelClient.GetJsonAppKeys(ctx, input.ProjectID, input.AppID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	for _, keyInfo := range existingKeys {
		if keyInfo.Expiration.IsZero() || keyInfo.Expiration.After(now) {
			return &jsonAppKey{
				KeyID:    keyInfo.ID,
				JsonBody: "",
			}, nil
		}
	}

	key, err := zitadelClient.AddJsonAppKey(ctx, input.ProjectID, input.AppID)
	if err != nil {
		return nil, err
	}
	return &jsonAppKey{KeyID: key.ID, JsonBody: key.JSON}, nil
}

func AddServiceUser(ctx context.Context, input addServiceUserInput) (*user, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	serviceUser, err := zitadelClient.AddServiceUser(ctx, input.Username, input.Name)
	if err == nil {
		return &user{ID: serviceUser.ID}, nil
	}

	if isAlreadyExistsError(err) {
		serviceUser, err = zitadelClient.GetUserBy(ctx, input.Username, "")
		if err != nil {
			return nil, err
		}

		return &user{ID: serviceUser.ID}, nil
	}
	return nil, err
}

func AddServiceUserToken(ctx context.Context, input addServiceUserTokenInput) (*serviceToken, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	token, err := zitadelClient.AddServiceUserToken(ctx, input.ServiceUserID)
	if err != nil {
		return nil, err
	}
	return &serviceToken{ID: token.ID, Token: token.Token}, nil
}

func AddServiceGrantForProject(ctx context.Context, input addServiceGrantForProjectInput) error {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return err
	}

	err = zitadelClient.AddUserGrantForProject(ctx, input.ProjectID, input.ServiceUserID, input.Roles)
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

func AddUserGrant(ctx context.Context, input addUserGrantInput) error {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return err
	}

	err = zitadelClient.AddUserGrant(ctx, input.ProjectID, input.UserID, input.GrantID, input.Roles)
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

func AddOrganization(ctx context.Context, input addOrganizationInput) (*organization, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	resp, err := zitadelClient.AddOrganization(ctx, input.OrganizationName)
	if err == nil {
		return &organization{ID: resp.ID}, nil
	}

	if !isAlreadyExistsError(err) {
		return nil, err
	}

	existingOrg, err := zitadelClient.GetOrganizationByName(ctx, input.OrganizationName)
	if err != nil {
		return nil, err
	}
	return &organization{ID: existingOrg.ID}, nil
}

func AddProjectGrant(ctx context.Context, input addProjectGrantInput) (*grant, error) {
	zitadelClient, err := getZitadelClient(ctx, input.ZitadelClientInput)
	if err != nil {
		return nil, err
	}

	resp, err := zitadelClient.AddProjectGrant(ctx, input.ProjectID, input.OrganizationID, input.Roles)
	if err == nil {
		return &grant{ID: resp.ID}, nil
	}

	if !isAlreadyExistsError(err) {
		return nil, err
	}

	existingGrant, err := zitadelClient.GetProjectGrant(ctx, input.ProjectID, input.OrganizationID)
	if err != nil {
		return nil, err
	}
	return &grant{ID: existingGrant.ID}, nil
}

func CreateK8sSecrets(ctx context.Context, input createK8sSecretsInput) (*createK8sSecretsOutput, error) {
	if input.Namespace == "" {
		return nil, errors.New("namespace is required")
	}

	k8sClient, err := kubernetes.NewK8sClient(input.Namespace, input.IsInCluster, input.ConfigPath)
	if err != nil {
		return nil, err
	}

	secretsData := map[string][]byte{
		"pyck-service-token":      []byte(input.ZitadelSetupOutput.ServiceInfo.ServiceToken),
		"pyck-zitadel-org-id":     []byte(input.ZitadelSetupOutput.Organization.ID),
		"pyck-zitadel-project-id": []byte(input.ZitadelSetupOutput.Project.ID),
		"pyck-zitadel-audience":   []byte(input.ZitadelSetupOutput.ServiceInfo.Audience),
	}

	var appSecretsData map[string][]byte
	var appSecretsDataMap = make(map[string]map[string][]byte)
	for _, createdApp := range input.ZitadelSetupOutput.CreatedApps {
		appSecretsData = make(map[string][]byte)
		for k, v := range secretsData {
			appSecretsData[k] = v
		}

		normalizedName := strings.ToLower(strings.ReplaceAll(createdApp.Name, " ", "-"))
		appSecretsData[fmt.Sprintf("%s-id", normalizedName)] = []byte(createdApp.ID)
		appSecretsData[fmt.Sprintf("%s-client-id", normalizedName)] = []byte(createdApp.ClientID)

		if createdApp.KeyFileJSON != "" {
			keyFileSecretName := fmt.Sprintf("%s-keyfile.json", normalizedName)
			appSecretsData[keyFileSecretName] = []byte(createdApp.KeyFileJSON)
		}

		if createdApp.ClientSecret != "" {
			secretKeyName := fmt.Sprintf("%s-client-secret", normalizedName)
			appSecretsData[secretKeyName] = []byte(createdApp.ClientSecret)
		}

		err = k8sClient.UpsertSecrets(ctx, createdApp.K8sSecret, appSecretsData)
		if err != nil {
			return nil, err
		}

		appSecretsDataMap[createdApp.Name] = appSecretsData
	}

	return &createK8sSecretsOutput{appSecretsDataMap}, nil
}

func EnableFeaturesActions(ctx context.Context, input zitadelClientInput) error {
	zitadelClient := getZitadelHttpClient(input)

	settings, err := zitadelClient.GetFutureSettings(zitadel.FeatureLevelInstance)
	if err != nil {
		return err
	}

	if settings.Actions.Enabled {
		return nil
	}

	err = zitadelClient.UpdateActionFeature(zitadel.FeatureLevelInstance, !settings.Actions.Enabled)
	if err != nil {
		return err
	}
	return nil
}

func AddOrUpdateActionTarget(ctx context.Context, input addTargetInput) (*addTargetOutput, error) {
	zitadelClient := getZitadelHttpClient(input.ZitadelClientInput)

	target, err := zitadelClient.CreateOrUpdateActionTarget(input.Name, input.InterruptWebHookOnError, input.WebHookUrl, time.Second*10)
	if err != nil {
		return nil, err
	}
	return &addTargetOutput{TargetID: target.ID}, nil
}

func AddExecutionsOnTarget(ctx context.Context, input addActionsOnTargetInput) error {
	zitadelClient := getZitadelHttpClient(input.ZitadelClientInput)

	for _, action := range input.TriggerOnActions {
		err := zitadelClient.CreateExecution(input.TargetID, action, input.WithResponse)
		if err != nil {
			return err
		}
	}
	return nil
}

func getZitadelClient(ctx context.Context, input zitadelClientInput) (*zitadel.ZitadelSdkClient, error) {
	return zitadel.SdkClient(ctx, input.Issuer, input.SdkClientAPI, input.JwtProfilePath, input.OrganizationID)
}

func getZitadelHttpClient(input zitadelClientInput) *zitadel.ZitadelHttpClient {
	return zitadel.HttpClient(input.Issuer, input.JwtProfilePath, input.TlsInsecure)
}

func isAlreadyExistsError(err error) bool {
	return strings.Contains(err.Error(), codes.AlreadyExists.String())
}
