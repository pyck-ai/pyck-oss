package zitadelsetup

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
)

func ZitadelSetupWorkflow(wCtx workflow.Context, input ZitadelSetupWorkflowInput) (*ZitadelSetupWorkflowOutput, error) {
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    10,
		},
	}

	ctx := workflow.WithActivityOptions(wCtx, activityOptions)

	zitadelClientSetup := zitadelClientInput{
		Issuer:         input.ServiceInfo.Issuer,
		SdkClientAPI:   input.ServiceInfo.SdkClientAPI,
		JwtProfilePath: input.ServiceInfo.JwtProfilePath,
		TlsInsecure:    input.ServiceInfo.TlsInsecure,
	}

	// Wait for host to be reachable (DNS propagation)
	// Use extended timeout for DNS propagation check with heartbeats
	waitActivityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 11 * time.Minute,
		HeartbeatTimeout:    5 * time.Second, // Detect stalled workers
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    3, // Retry on genuine failures (worker crashes, etc)
		},
	}
	waitCtx := workflow.WithActivityOptions(wCtx, waitActivityOptions)
	err := workflow.ExecuteActivity(waitCtx, WaitForHostReachable, zitadelClientSetup).Get(waitCtx, nil)
	if err != nil {
		return nil, err
	}

	// Get Organization
	var org organization
	err = workflow.ExecuteActivity(ctx, GetOrgID, zitadelClientSetup).Get(ctx, &org)
	if err != nil {
		return nil, err
	}

	// Add Org Domain Suffix Pattern
	err = workflow.ExecuteActivity(ctx, AddOrgDomainSuffixPattern, zitadelClientSetup).Get(ctx, nil)
	if err != nil {
		return nil, err
	}

	// Create Admin User
	userInput := createUserInput{
		ZitadelClientInput: zitadelClientSetup,
		Username:           input.AdminProfile.Username,
		FirstName:          zitadelAdminFirstName,
		LastName:           zitadelAdminLastName,
		Email:              input.AdminProfile.Email,
		IsEmailVerified:    true,
		Password:           input.AdminProfile.Password,
		ChangePassword:     false,
	}
	var adminUser user
	err = workflow.ExecuteActivity(ctx, CreateUser, userInput).Get(ctx, &adminUser)
	if err != nil {
		return nil, err
	}
	input.AdminProfile.Username = adminUser.LoginName

	// Set Admin User as Org Owner
	setUserAdmin := setUserAdminInput{ZitadelClientInput: zitadelClientSetup, UserID: adminUser.ID}
	err = workflow.ExecuteActivity(ctx, SetUserAsAdmin, setUserAdmin).Get(ctx, nil)
	if err != nil {
		return nil, err
	}

	// Add project
	projInput := addProjectInput{ZitadelClientInput: zitadelClientSetup, ProjectName: zitadelProjectName}
	var projectOutput project
	err = workflow.ExecuteActivity(ctx, AddProject, projInput).Get(ctx, &projectOutput)
	if err != nil {
		return nil, err
	}

	// Add project roles
	projectRolesInput := addProjectRolesInput{
		ZitadelClientInput: zitadelClientSetup,
		ProjectID:          projectOutput.ID,
	}
	err = workflow.ExecuteActivity(ctx, AddProjectRoles, projectRolesInput).Get(ctx, nil)
	if err != nil {
		return nil, err
	}

	// Add apps to Project
	var createdApps []app
	for _, appCfg := range input.Apps {
		addAppInput := addAppToProjectInput{
			ZitadelClientInput: zitadelClientSetup,
			ProjectID:          projectOutput.ID,
			App:                appCfg,
		}
		var createdApp app
		err = workflow.ExecuteActivity(ctx, AddAppToProject, addAppInput).Get(ctx, &createdApp)
		if err != nil {
			return nil, err
		}

		if appCfg.GenerateKey {
			jsonAppKeyInput := addJsonAppKeyInput{
				ZitadelClientInput: zitadelClientSetup,
				ProjectID:          projectOutput.ID,
				AppID:              createdApp.ID,
			}
			var jsonKey jsonAppKey
			err = workflow.ExecuteActivity(ctx, AddJsonAppKey, jsonAppKeyInput).Get(ctx, &jsonKey)
			if err != nil {
				return nil, err
			}
			createdApp.KeyFileJSON = jsonKey.JsonBody
		}

		createdApp.K8sSecret = appCfg.K8sSecret
		createdApp.Name = appCfg.Name
		createdApps = append(createdApps, createdApp)
	}

	// Add Service User
	serviceUserInput := addServiceUserInput{ZitadelClientInput: zitadelClientSetup, Username: zitadelServiceUserUsername, Name: zitadelServiceUserName}
	var serviceUser user
	err = workflow.ExecuteActivity(ctx, AddServiceUser, serviceUserInput).Get(ctx, &serviceUser)
	if err != nil {
		return nil, err
	}

	// Add Service User Token
	serviceTokenInput := addServiceUserTokenInput{ZitadelClientInput: zitadelClientSetup, ServiceUserID: serviceUser.ID}
	var serviceTokenOutput serviceToken
	err = workflow.ExecuteActivity(ctx, AddServiceUserToken, serviceTokenInput).Get(ctx, &serviceTokenOutput)
	if err != nil {
		return nil, err
	}

	// Add service user grant
	serviceUserGrantInput := addServiceGrantForProjectInput{
		ZitadelClientInput: zitadelClientSetup,
		ProjectID:          projectOutput.ID,
		ServiceUserID:      serviceUser.ID,
		Roles:              []string{zitadel.ProjectRoleSystem},
	}
	err = workflow.ExecuteActivity(ctx, AddServiceGrantForProject, serviceUserGrantInput).Get(ctx, nil)
	if err != nil {
		return nil, err
	}

	//////////////////////////////////////////////////////////////////////////////////////
	// Tenant Setup
	//////////////////////////////////////////////////////////////////////////////////////

	// Register Organization
	var tenantOrganizationOutput organization
	tenantOrganizationInput := addOrganizationInput{
		ZitadelClientInput: zitadelClientSetup,
		OrganizationName:   input.TenantOrganization.Name,
	}
	err = workflow.ExecuteActivity(ctx, AddOrganization, tenantOrganizationInput).Get(ctx, &tenantOrganizationOutput)
	if err != nil {
		return nil, err
	}

	// Add project grant
	projectGrantInput := addProjectGrantInput{
		ZitadelClientInput: zitadelClientSetup,
		ProjectID:          projectOutput.ID,
		OrganizationID:     tenantOrganizationOutput.ID,
		Roles:              []string{zitadel.ProjectRoleReader, zitadel.ProjectRoleWriter},
	}
	var grantOutput grant
	err = workflow.ExecuteActivity(ctx, AddProjectGrant, projectGrantInput).Get(ctx, &grantOutput)
	if err != nil {
		return nil, err
	}

	tenantZitadelClientSetup := zitadelClientInput(zitadelClientSetup)
	tenantZitadelClientSetup.OrganizationID = tenantOrganizationOutput.ID

	// Add Service Worker User
	serviceWorkerUserInput := addServiceUserInput{
		ZitadelClientInput: tenantZitadelClientSetup,
		Username:           fmt.Sprintf("%s-%s", tenantOrganizationOutput.ID, "service-worker-user"),
		Name:               "Service Worker User",
	}
	var tenantServiceWorkerUser user
	err = workflow.ExecuteActivity(ctx, AddServiceUser, serviceWorkerUserInput).Get(ctx, &tenantServiceWorkerUser)
	if err != nil {
		return nil, err
	}

	// Add Service Worker User Token
	serviceWorkerTokenInput := addServiceUserTokenInput{
		ZitadelClientInput: tenantZitadelClientSetup,
		ServiceUserID:      tenantServiceWorkerUser.ID,
	}
	var tenantServiceWorkerTokenOutput serviceToken
	err = workflow.ExecuteActivity(ctx, AddServiceUserToken, serviceWorkerTokenInput).Get(ctx, &tenantServiceWorkerTokenOutput)
	if err != nil {
		return nil, err
	}

	// Add service worker user grant
	serviceWorkerUserGrantInput := addUserGrantInput{
		ZitadelClientInput: tenantZitadelClientSetup,
		ProjectID:          projectOutput.ID,
		UserID:             tenantServiceWorkerUser.ID,
		GrantID:            grantOutput.ID,
		Roles:              []string{zitadel.ProjectRoleWriter},
	}
	err = workflow.ExecuteActivity(ctx, AddUserGrant, serviceWorkerUserGrantInput).Get(ctx, nil)
	if err != nil {
		return nil, err
	}

	// Add tenant api user
	tenantApiUserInput := addServiceUserInput{
		ZitadelClientInput: tenantZitadelClientSetup,
		Username:           fmt.Sprintf("%s-%s", tenantOrganizationOutput.ID, "api-user"),
		Name:               "API User",
	}
	var tenantApiUser user
	err = workflow.ExecuteActivity(ctx, AddServiceUser, tenantApiUserInput).Get(ctx, &tenantApiUser)
	if err != nil {
		return nil, err
	}

	// Add tenant api user token
	tenantApiUserTokenInput := addServiceUserTokenInput{
		ZitadelClientInput: tenantZitadelClientSetup,
		ServiceUserID:      tenantApiUser.ID,
	}
	var tenantApiUserTokenOutput serviceToken
	err = workflow.ExecuteActivity(ctx, AddServiceUserToken, tenantApiUserTokenInput).Get(ctx, &tenantApiUserTokenOutput)
	if err != nil {
		return nil, err
	}

	// Add tenant api user grant
	tenantApiUserGrantInput := addUserGrantInput{
		ZitadelClientInput: tenantZitadelClientSetup,
		ProjectID:          projectOutput.ID,
		UserID:             tenantApiUser.ID,
		GrantID:            grantOutput.ID,
		Roles:              []string{zitadel.ProjectRoleWriter},
	}
	err = workflow.ExecuteActivity(ctx, AddUserGrant, tenantApiUserGrantInput).Get(ctx, nil)
	if err != nil {
		return nil, err
	}

	//////////////////////////////////////////////////////////////////////////////////////
	// Enable Features
	//////////////////////////////////////////////////////////////////////////////////////

	// Enable zitadel actions
	err = workflow.ExecuteActivity(ctx, EnableFeaturesActions, zitadelClientSetup).Get(ctx, nil)
	if err != nil {
		return nil, err
	}

	// Create add user targets
	for _, target := range input.ActionTargets {
		targetInput := addTargetInput{
			ZitadelClientInput:      zitadelClientSetup,
			Name:                    target.Name,
			WebHookUrl:              target.WebHookUrl,
			InterruptWebHookOnError: target.InterruptWebHookOnError,
		}
		var targetOutput addTargetOutput
		err = workflow.ExecuteActivity(ctx, AddOrUpdateActionTarget, targetInput).Get(ctx, &targetOutput)
		if err != nil {
			return nil, err
		}

		executionInput := addActionsOnTargetInput{
			ZitadelClientInput: zitadelClientSetup,
			TargetID:           targetOutput.TargetID,
			TriggerOnActions:   target.TriggerOnActions,
			WithResponse:       target.WithResponse,
		}

		err = workflow.ExecuteActivity(ctx, AddExecutionsOnTarget, executionInput).Get(ctx, nil)
		if err != nil {
			return nil, err
		}
	}

	// Create ZitadelSetupWorkflowOutput
	output := &ZitadelSetupWorkflowOutput{
		ServiceInfo: ServiceInfoOutput{
			Audience:     input.ServiceInfo.Issuer, // TODO: read the actual audience from Zitadel; issuer and audience will diverge in the future
			ServiceToken: serviceTokenOutput.Token,
		},
		AdminProfile: input.AdminProfile,
		Organization: OrganizationOutput(org),
		Project:      ProjectOutput(projectOutput),
		CreatedApps:  createdApps,
		TenantSetup: TenantSetupOutput{
			OrganizationID: tenantOrganizationOutput.ID,
			TenantID:       authn.ComputeUUID(input.ServiceInfo.Issuer, tenantOrganizationOutput.ID).String(), // TODO: read the actual audience from Zitadel; issuer and audience will diverge in the future

			ApiToken:           tenantApiUserTokenOutput.Token,
			ServiceWorkerToken: tenantServiceWorkerTokenOutput.Token,
		},
	}

	if input.K8sConfig.CreateSecrets {
		// Create K8s Secrets
		k8sInput := createK8sSecretsInput{
			Namespace:          input.K8sConfig.Namespace,
			ConfigPath:         input.K8sConfig.ConfigPath,
			IsInCluster:        input.K8sConfig.InCluster,
			ZitadelSetupOutput: output,
		}

		var createSecretsOutput createK8sSecretsOutput
		err = workflow.ExecuteActivity(ctx, CreateK8sSecrets, k8sInput).Get(ctx, &createSecretsOutput)
		if err != nil {
			return nil, err
		}

		output.K8sSecrets = createSecretsOutput
	}

	return output, nil
}
