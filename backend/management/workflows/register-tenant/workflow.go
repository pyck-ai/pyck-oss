package registertenant

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"

	"github.com/pyck-ai/pyck/backend/management/core"
)

var (
	activities                  Activities
	errWorkerImageNotConfigured = errors.New("PYCK_FLAVOUR_GO_WORKER_IMAGE is not set")
)

func RegisterTenantWorkflow(context workflow.Context, input RegisterTenantWorkflowInput) (*RegisterTenantWorkflowOutput, error) {
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    3,
		},
	}
	ctx := workflow.WithActivityOptions(context, activityOptions)

	// Create Organization

	organizationInput := createTenantActivityInput{
		Name: input.Name,
	}

	var organizationOutput CreateTenantActivityOutput
	err := workflow.ExecuteActivity(ctx, activities.CreateTenantActivity, organizationInput).Get(ctx, &organizationOutput)
	if err != nil {
		return nil, err
	}

	// Create Tenant in Database
	dbTenantInput := CreateTenantInDbActivityInput{
		OrganizationID: organizationOutput.OrganizationID,
		Name:           input.Name,
		Data:           input.Data,
		ExpiresAt:      input.ExpiresAt,
	}
	err = workflow.ExecuteActivity(ctx, activities.CreateTenantInDbActivity, dbTenantInput).Get(ctx, nil)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	// Create Zitadel User
	userInput := createZitadelUserActivityInput{
		OrganizationID: organizationOutput.OrganizationID,
		Username:       input.AdminUsername,
		Email:          input.AdminEmail,
		FirstName:      input.AdminFirstName,
		LastName:       input.AdminLastName,
		Password:       input.AdminPassword,
	}
	var userOutput CreateZitadelUserActivityOutput
	err = workflow.ExecuteActivity(ctx, activities.CreateZitadelUserActivity, userInput).Get(ctx, &userOutput)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	// Set User as Organization Admin
	setUserAsAdminInput := setUserAsOrganizationAdmin{
		OrganizationID: organizationOutput.OrganizationID,
		UserID:         userOutput.UserID,
	}
	err = workflow.ExecuteActivity(ctx, activities.SetUserAsOrganizationOwnerActivity, setUserAsAdminInput).Get(ctx, nil)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	projectGrantInput := addProjectGrantsInput{
		ProjectID:      core.Config.ZitadelProjectId,
		OrganizationID: organizationOutput.OrganizationID,
		Roles:          append([]string{sdk.ProjectRoleReader, sdk.ProjectRoleWriter}, serviceroles.ServiceRoleStrings()...),
	}
	var grantOutput Grant
	err = workflow.ExecuteActivity(ctx, activities.AddProjectGrantActivity, projectGrantInput).Get(ctx, &grantOutput)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	userGrantInput := addUserGrantInput{
		OrganizationID: organizationOutput.OrganizationID,
		ProjectID:      core.Config.ZitadelProjectId,
		UserID:         userOutput.UserID,
		GrantID:        grantOutput.ID,
		Roles:          []string{sdk.ProjectRoleWriter},
	}

	err = workflow.ExecuteActivity(ctx, activities.AddUserGrantActivity, userGrantInput).Get(ctx, nil)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	// Add Default DataTypes
	defaultDataTypesInput := AddDefaultDataTypesActivityInput{
		TenantID:  organizationOutput.TenantID,
		UserID:    userOutput.UserID,
		UserRoles: userGrantInput.Roles,
	}

	err = workflow.ExecuteActivity(ctx, activities.AddDefaultDataTypesActivity, defaultDataTypesInput).Get(ctx, nil)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	// Create Temporal Namespace
	namespaceInput := createTemporalNamespaceInput{
		TemporalUrl: core.Config.TemporalUrl,
		Namespace:   organizationOutput.TemporalNamespace,
	}

	err = workflow.ExecuteActivity(ctx, activities.CreateTemporalNamespaceActivity, namespaceInput).Get(ctx, nil)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	// Deploy Tenant Worker via temporal-worker-controller (only for pyck-go tenants)
	if core.DetectFlavour(input.Data) == "pyck-go" {
		if err := deployTenantWorker(ctx, input, organizationOutput, grantOutput); err != nil {
			rollback(ctx, organizationOutput.OrganizationID, true)
			return nil, err
		}
	}

	// Set Organization Metadata — caller-supplied `Data` keys only.
	// Tenant expiry is NOT written here; it lives in the DB column
	// (set by CreateTenantInDbActivity above for new tenants and by
	// the setTenantExpiry resolver thereafter).
	metadataInput := SetOrgMetadataActivityInput{
		OrganizationID: organizationOutput.OrganizationID,
		Data:           input.Data,
	}

	err = workflow.ExecuteActivity(ctx, activities.SetOrgMetadataActivity, metadataInput).Get(ctx, nil)
	if err != nil {
		rollback(ctx, organizationOutput.OrganizationID, true)
		return nil, err
	}

	// Trigger tenant sync to synchronize users from Zitadel
	// Uses deterministic workflow ID - if sync is already running, this is a no-op
	syncInput := TriggerTenantSyncActivityInput{
		OrganizationID: organizationOutput.OrganizationID,
	}
	err = workflow.ExecuteActivity(ctx, activities.TriggerTenantSyncActivity, syncInput).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Error("failed to trigger tenant sync", "err", err)
	}

	return &RegisterTenantWorkflowOutput{
		OrganizationID: organizationOutput.OrganizationID,
		TenantID:       organizationOutput.TenantID,
		LoginName:      userOutput.LoginName,
		UserID:         userOutput.UserID,
		UserRoles:      userGrantInput.Roles,
	}, nil
}

func deployTenantWorker(ctx workflow.Context, input RegisterTenantWorkflowInput, orgOutput CreateTenantActivityOutput, grantOutput Grant) error {
	if input.WorkerImage == "" {
		return errWorkerImageNotConfigured
	}

	workersNamespace := "pyck-" + core.Config.EnvironmentName + "-workers"
	connectionName := fmt.Sprintf("pyck-go-%s", orgOutput.TemporalNamespace)
	secretName := fmt.Sprintf("temporal-api-key-%s", orgOutput.TemporalNamespace)

	// 1. Upsert shared workers namespace
	err := workflow.ExecuteActivity(ctx, activities.UpsertK8sWorkersNamespaceActivity, upsertK8sWorkersNamespaceInput{
		Namespace:   workersNamespace,
		IsInCluster: true,
	}).Get(ctx, nil)
	if err != nil {
		return err
	}

	// 2. Create dedicated Zitadel service user and API key for tenant worker
	var serviceUserOutput CreateTenantServiceUserOutput
	err = workflow.ExecuteActivity(ctx, activities.CreateTenantServiceUserActivity, createTenantServiceUserInput{
		OrganizationID: orgOutput.OrganizationID,
	}).Get(ctx, &serviceUserOutput)
	if err != nil {
		return err
	}

	// 2b. Grant writer role to service user on PYCK project
	err = workflow.ExecuteActivity(ctx, activities.AddUserGrantActivity, addUserGrantInput{
		OrganizationID: orgOutput.OrganizationID,
		ProjectID:      core.Config.ZitadelProjectId,
		UserID:         serviceUserOutput.UserID,
		GrantID:        grantOutput.ID,
		Roles:          []string{sdk.ProjectRoleWriter},
	}).Get(ctx, nil)
	if err != nil {
		return err
	}

	// 3. Store the API key as a K8s secret in the workers namespace
	err = workflow.ExecuteActivity(ctx, activities.CreateK8sTenantSecretActivity, createK8sTenantSecretInput{
		Namespace:   workersNamespace,
		SecretName:  secretName,
		SecretKey:   "api-key",
		Token:       serviceUserOutput.Token,
		IsInCluster: true,
	}).Get(ctx, nil)
	if err != nil {
		return err
	}

	// 4. Create temporal Connection per tenant
	err = workflow.ExecuteActivity(ctx, activities.CreateK8sTemporalConnectionActivity, createK8sTemporalConnectionInput{
		Namespace:   workersNamespace,
		Name:        connectionName,
		HostPort:    input.WorkerEnvVars["TEMPORAL_ADDRESS"],
		IsInCluster: true,
	}).Get(ctx, nil)
	if err != nil {
		return err
	}

	// 5. Create WorkerDeployment for this tenant
	return workflow.ExecuteActivity(ctx, activities.CreateK8sWorkerDeploymentActivity, createK8sWorkerDeploymentInput{
		Namespace:           workersNamespace,
		Name:                connectionName,
		ConnectionName:      connectionName,
		TemporalNamespace:   orgOutput.TemporalNamespace,
		Image:               input.WorkerImage,
		TenantID:            orgOutput.TenantID.String(),
		Replicas:            input.WorkerReplicas,
		EnvVars:             input.WorkerEnvVars,
		ImagePullSecretName: input.WorkerEnvVars["IMAGE_PULL_SECRET_NAME"],
		APIKeySecretName:    secretName,
		APIKeySecretKey:     "api-key",
		IsInCluster:         true,
	}).Get(ctx, nil)
}

func rollback(ctx workflow.Context, organizationID string, deleteFromDb bool) {
	// Rollback: Delete from DB first (if created)
	if deleteFromDb {
		deleteDbInput := DeleteTenantFromDbActivityInput{
			OrganizationID: organizationID,
		}
		err := workflow.ExecuteActivity(ctx, activities.DeleteTenantFromDbActivity, deleteDbInput).Get(ctx, nil)
		if err != nil {
			workflow.GetLogger(ctx).Error("failed to rollback db tenant", "organization_id", organizationID, "err", err)
		}
	}

	// Rollback: Delete Zitadel Organization
	deleteTenantInput := DeleteTenantActivityInput{
		OrganizationID: organizationID,
	}
	err := workflow.ExecuteActivity(ctx, activities.DeleteTenantActivity, deleteTenantInput).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Error("failed to rollback zitadel organization", "organization_id", organizationID, "err", err)
	}
}
