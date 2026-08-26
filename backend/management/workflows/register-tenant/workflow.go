package registertenant

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"

	"github.com/pyck-ai/pyck/backend/management/core"
)

var activities Activities

const (
	// pyckGoFlavour is the tenant-data flavour that gets a platform-managed
	// worker; pyckGoExtension is what worker-api knows that worker as.
	pyckGoFlavour   = "pyck-go"
	pyckGoExtension = "pyckGo"
	// pyckGoDeploymentPrefix names the deployment. Deliberately not derived
	// from pyckGoExtension: a deployment name is a DNS-1123 subdomain, which
	// forbids the capital in "pyckGo".
	pyckGoDeploymentPrefix = "pyck-go"

	// workerTemporalAPIKeySecret is the tenant secret worker-api projects into
	// the worker's Connection.
	workerTemporalAPIKeySecret = "TEMPORAL_API_KEY" //nolint:gosec // a secret KEY NAME, not a credential
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

	// Only pyck-go tenants get a platform-managed worker; a plain tenant brings
	// its own via `pyck deploy`.
	if core.DetectFlavour(input.Data) == pyckGoFlavour {
		if err := deployTenantWorker(ctx, organizationOutput, grantOutput); err != nil {
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

// deployTenantWorker gives a pyck-go tenant its worker. Everything on the
// worker cluster — namespace, Connection, secrets, the WorkerDeployment CRD —
// belongs to worker-api, which is the only component holding write credentials
// there; management mints the tenant's Zitadel credential and hands it over.
func deployTenantWorker(ctx workflow.Context, orgOutput CreateTenantActivityOutput, grantOutput Grant) error {
	// The worker's own Zitadel identity, minted here rather than in worker-api
	// so Zitadel write credentials stay in one place.
	var serviceUserOutput CreateTenantServiceUserOutput
	err := workflow.ExecuteActivity(ctx, activities.CreateTenantServiceUserActivity, createTenantServiceUserInput{
		OrganizationID: orgOutput.OrganizationID,
	}).Get(ctx, &serviceUserOutput)
	if err != nil {
		return err
	}

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

	// The credential has to land before the deployment: worker-api fails closed
	// on one whose TEMPORAL_API_KEY is missing.
	err = workflow.ExecuteActivity(ctx, activities.StoreTenantWorkerSecretActivity, storeTenantWorkerSecretInput{
		TenantID: orgOutput.TenantID,
		Key:      workerTemporalAPIKeySecret,
		Value:    serviceUserOutput.Token,
	}).Get(ctx, nil)
	if err != nil {
		return err
	}

	return workflow.ExecuteActivity(ctx, activities.CreateTenantWorkerDeploymentActivity, CreateTenantWorkerDeploymentInput{
		Name:              pyckGoDeploymentPrefix + "-" + orgOutput.TemporalNamespace,
		TenantID:          orgOutput.TenantID,
		TemporalNamespace: orgOutput.TemporalNamespace,
		Extension:         pyckGoExtension,
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
