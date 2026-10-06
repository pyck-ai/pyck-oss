package registertenant

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/txid"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/exec"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

//go:embed datatypes/*.json
var datatypesFS embed.FS

// DataTypeDefinition represents a datatype definition from JSON files
type DataTypeDefinition struct {
	Name        string      `json:"name"`
	Slug        string      `json:"slug"`
	Description string      `json:"description"`
	Entity      string      `json:"entity"`
	JSONSchema  interface{} `json:"jsonSchema"`
	Default     bool        `json:"default"`
}

// Activities struct for methods that need dependencies
type Activities struct {
	resolver       exec.MutationResolver
	entClient      *ent.Client
	temporalClient temporalclient.Client
	nsGetter       workflow.NamespaceGetter
	// workerAPI owns the worker cluster; management never writes there itself.
	workerAPI *workerapi.Client
}

// NewActivities creates a new Activities instance with the provided resolver
func NewActivities(resolver exec.MutationResolver, entClient *ent.Client, temporalClient temporalclient.Client, nsGetter workflow.NamespaceGetter, workerAPI *workerapi.Client) *Activities {
	return &Activities{
		resolver:       resolver,
		entClient:      entClient,
		temporalClient: temporalClient,
		nsGetter:       nsGetter,
		workerAPI:      workerAPI,
	}
}

// AddDefaultDataTypesActivity creates multiple default DataTypes from embedded files using the GraphQL resolver
func (a Activities) AddDefaultDataTypesActivity(ctx context.Context, input AddDefaultDataTypesActivityInput) error {
	// Create a service user context with the organization as tenant
	tenantID := input.TenantID
	userID := a.nsGetter.GetUserID(input.UserID)

	systemUserCtx := authn.Context(ctx, &authn.User{
		ID:       userID,
		TenantID: tenantID,
		Roles: map[uuid.UUID]authn.Role{
			tenantID: authn.ROLE_ADMIN,
		},
	})
	systemUserCtx = tenant.Context(systemUserCtx, tenantID)
	// Inject a transaction ID so MutationEventHook can stamp outbox rows
	// (gqltx does this at BeginTx in the GraphQL path; we mirror it here
	// for the workflow-driven bootstrap path).
	systemUserCtx = txid.With(systemUserCtx, txid.New())

	tx, err := a.entClient.Tx(systemUserCtx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		} else {
			_ = tx.Commit()
		}
	}()
	systemUserCtx = ent.NewTxContext(systemUserCtx, tx)

	// Read all datatype definition files
	entries, err := datatypesFS.ReadDir("datatypes")
	if err != nil {
		return fmt.Errorf("failed to read datatypes directory: %w", err)
	}

	// Process each datatype file
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		// Read the file content
		filePath := filepath.Join("datatypes", entry.Name())
		content, err := datatypesFS.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read datatype file %s: %w", entry.Name(), err)
		}

		// Parse the datatype definition
		var dataTypeDef DataTypeDefinition
		if err := json.Unmarshal(content, &dataTypeDef); err != nil {
			return fmt.Errorf("failed to parse datatype file %s: %w", entry.Name(), err)
		}

		// Convert JSONSchema to string
		jsonSchemaBytes, err := json.Marshal(dataTypeDef.JSONSchema)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON schema for %s: %w", dataTypeDef.Name, err)
		}

		// Create the DataType using the GraphQL resolver
		if _, err := a.resolver.CreateDataType(systemUserCtx, ent.CreateDataTypeInput{
			Name:        &dataTypeDef.Name,
			Slug:        &dataTypeDef.Slug,
			Description: &dataTypeDef.Description,
			JSONSchema:  string(jsonSchemaBytes),
			Entity:      dataTypeDef.Entity,
			Default:     &dataTypeDef.Default,
		}); err != nil {
			return fmt.Errorf("failed to create DataType %s: %w", dataTypeDef.Name, err)
		}
	}

	return nil
}

func (a *Activities) CreateTenantActivity(ctx context.Context, input createTenantActivityInput) (*CreateTenantActivityOutput, error) {
	zitadelClient, err := getZitadelClient(ctx, "")
	if err != nil {
		return nil, err
	}
	defer zitadelClient.Close()

	organization, err := zitadelClient.AddOrganization(ctx, input.Name)
	if err != nil {
		return nil, err
	}

	tenantID := a.nsGetter.GetTenantID(organization.ID)
	temporalNS := a.nsGetter.GetNamespace(tenantID)

	return &CreateTenantActivityOutput{
		OrganizationID:    organization.ID,
		TenantID:          tenantID,
		TemporalNamespace: temporalNS,
	}, nil
}

func (*Activities) CreateZitadelUserActivity(ctx context.Context, input createZitadelUserActivityInput) (*CreateZitadelUserActivityOutput, error) {
	zitadelClient, err := getZitadelClient(ctx, input.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer zitadelClient.Close()

	user, err := zitadelClient.CreateHumanUser(
		ctx, input.Username, input.FirstName, input.LastName, input.Email, true, input.Password, false,
	)
	if err != nil {
		return nil, err
	}

	return &CreateZitadelUserActivityOutput{UserID: user.ID, LoginName: user.LoginName}, nil
}

func (*Activities) SetUserAsOrganizationOwnerActivity(ctx context.Context, input setUserAsOrganizationAdmin) error {
	zitadelClient, err := getZitadelClient(ctx, input.OrganizationID)
	if err != nil {
		return err
	}
	defer zitadelClient.Close()

	currentMembers, err := zitadelClient.OrganizationMembers(ctx)
	if err != nil {
		return err
	}

	for _, member := range currentMembers {
		if member.ID == input.UserID {
			continue
		}

		err = zitadelClient.RemoveOrganizationMember(ctx, member.ID)
		if err != nil {
			return err
		}
	}

	return zitadelClient.AddOrganizationMember(ctx, input.UserID, []string{"ORG_OWNER"})
}

func (*Activities) AddProjectGrantActivity(ctx context.Context, input addProjectGrantsInput) (*Grant, error) {
	zitadelClient, err := getZitadelClient(ctx, "")
	if err != nil {
		return nil, err
	}
	defer zitadelClient.Close()

	grantResp, err := zitadelClient.AddProjectGrant(ctx, input.ProjectID, input.OrganizationID, input.Roles)
	if err != nil {
		return nil, err
	}
	return &Grant{ID: grantResp.ID}, nil
}

func (*Activities) DeleteTenantActivity(ctx context.Context, input DeleteTenantActivityInput) error {
	zitadelClient, err := getZitadelClient(ctx, input.OrganizationID)
	if err != nil {
		return err
	}
	defer zitadelClient.Close()
	return zitadelClient.DeleteMyOrganization(ctx)
}

func (*Activities) AddUserGrantActivity(ctx context.Context, input addUserGrantInput) error {
	zitadelClient, err := getZitadelClient(ctx, input.OrganizationID)
	if err != nil {
		return err
	}
	defer zitadelClient.Close()

	err = zitadelClient.AddUserGrant(ctx, input.ProjectID, input.UserID, input.GrantID, input.Roles)
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

func getZitadelClient(ctx context.Context, orgId string) (*sdk.ZitadelSdkClient, error) {
	return sdk.SdkClient(ctx, core.Config.ZitadelAudience, core.Config.ZitadelGrpcAddr, core.Config.ZitadelOAuthURL, core.Config.ZitadelServiceKeyPath, orgId, core.Config.ZitadelTlsInsecure)
}

func isAlreadyExistsError(err error) bool {
	return strings.Contains(err.Error(), codes.AlreadyExists.String())
}

func (*Activities) CreateTemporalNamespaceActivity(ctx context.Context, input createTemporalNamespaceInput) error {
	nsClient, err := temporal.NewTemporalNamespaceClient(ctx, input.TemporalUrl)
	if err != nil {
		return err
	}
	defer nsClient.Close()

	if err := temporal.CreateTemporalNamespace(ctx, nsClient, input.Namespace); err != nil {
		return err
	}

	// Add search attributes to the newly created namespace
	temporalClient, err := temporal.NewTemporalClient(ctx, input.TemporalUrl, core.Config.TemporalDialTimeout)
	if err != nil {
		return fmt.Errorf("failed to create temporal client for search attributes: %w", err)
	}
	defer temporalClient.Close()

	searchAttributes := make(map[string]enums.IndexedValueType, len(workflow.SearchAttributes))
	for _, attr := range workflow.SearchAttributes {
		searchAttributes[attr.GetName()] = attr.GetValueType()
	}

	_, err = temporalClient.OperatorService().AddSearchAttributes(ctx, &operatorservice.AddSearchAttributesRequest{
		Namespace:        input.Namespace,
		SearchAttributes: searchAttributes,
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("failed to add search attributes: %w", err)
	}

	return nil
}

// SetOrgMetadataActivity writes the caller-supplied `Data` map to
// the Zitadel organization metadata. Tenant expiry is intentionally
// NOT written here — it lives only in the management.tenants.expires_at
// column (set by CreateTenantInDbActivity for new tenants and by the
// setTenantExpiry resolver thereafter).
func (*Activities) SetOrgMetadataActivity(ctx context.Context, input SetOrgMetadataActivityInput) error {
	if len(input.Data) == 0 {
		return nil
	}

	zitadelClient, err := getZitadelClient(ctx, input.OrganizationID)
	if err != nil {
		return err
	}
	defer zitadelClient.Close()

	for key, value := range input.Data {
		var strValue string
		switch v := value.(type) {
		case string:
			strValue = v
		default:
			strValue = fmt.Sprintf("%v", v)
		}

		if err := zitadelClient.SetOrgMetadata(ctx, key, strValue); err != nil {
			return err
		}
	}

	return nil
}

// CreateTenantInDbActivity writes the tenant row directly via Ent, bypassing
// the GraphQL/gRPC entry point. This violates the rule that workers only
// touch project state through the public API — accepted here as a workaround
// to avoid a race between this workflow and tenant-sync. Replace once the
// race is resolved at the API layer.
func (a *Activities) CreateTenantInDbActivity(ctx context.Context, input CreateTenantInDbActivityInput) (err error) {
	tenantID := a.nsGetter.GetTenantID(input.OrganizationID)
	// Mirror what gqltx does on the GraphQL path: explicit tx + per-attempt
	// txid. Events are suppressed below, so no outbox row is written for
	// this tx.
	serviceUserCtx := authn.Context(ctx, authn.SystemUser())
	// Tenant is self-tenant, so this write would emit a tenant event. The
	// registration workflow's tenant row writes are internal and must stay
	// event-free: suppress explicitly.
	serviceUserCtx = feature.Context(serviceUserCtx, feature.FEATURE_SUPPRESS_EVENTS)
	serviceUserCtx = txid.With(serviceUserCtx, txid.New())

	tx, err := a.entClient.Tx(serviceUserCtx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()
	serviceUserCtx = ent.NewTxContext(serviceUserCtx, tx)

	// UI bundle URL templates are no longer seeded into tenant.Data: the workflow
	// service renders a tenant-aware default at query time (#1317), and tenant.Data
	// holds only explicit overrides set via setTenantUITemplate.

	createOp := tx.Tenant.Create().
		SetID(tenantID).
		SetName(input.Name).
		SetIdpOrgRef(input.OrganizationID)

	if len(input.Data) > 0 {
		createOp = createOp.SetData(input.Data)
	}

	if input.ExpiresAt != nil {
		createOp = createOp.SetExpiresAt(input.ExpiresAt.UTC())
	}

	// On conflict, land the caller's input wholesale ("land my intent",
	// not "merge"). The id is deterministic (ComputeUUID over the org),
	// so a re-register collides with the tenant's prior row — including
	// a soft-deleted one. Clearing deleted_at/deleted_by resurrects it:
	// re-register yields an ACTIVE tenant, never a ghost that reports
	// success while staying soft-deleted and disabled. ExpiresAt and
	// Data are both written unconditionally (Set or Clear) so the
	// caller's intent fully replaces the prior value. Name + idp_org_ref
	// are immutable post-create.
	upsert := createOp.
		OnConflictColumns("id").
		Update(func(u *ent.TenantUpsert) {
			u.ClearDeletedAt()
			u.ClearDeletedBy()
			if input.ExpiresAt != nil {
				u.SetExpiresAt(input.ExpiresAt.UTC())
			} else {
				u.ClearExpiresAt()
			}
			if len(input.Data) > 0 {
				u.SetData(input.Data)
			} else {
				u.ClearData()
			}
		})
	if err := upsert.Exec(serviceUserCtx); err != nil {
		// Some drivers return sql.ErrNoRows on the upsert no-RETURNING
		// case even when the Update applied; not an error here.
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return nil
}

// DeleteTenantFromDbActivity carries the same direct-DB workaround as
// CreateTenantInDbActivity above: same rule violation, same race-with-
// tenant-sync rationale. Replace alongside the create-side fix.
func (a *Activities) DeleteTenantFromDbActivity(ctx context.Context, input DeleteTenantFromDbActivityInput) (err error) {
	tenantID := a.nsGetter.GetTenantID(input.OrganizationID)
	// Mirror what gqltx does on the GraphQL path: explicit tx + per-attempt
	// txid. Events are suppressed below, so no outbox row is written for
	// this tx.
	serviceUserCtx := authn.Context(ctx, authn.SystemUser())
	// Tenant is self-tenant, so this write would emit a tenant event. The
	// registration workflow's tenant row writes are internal and must stay
	// event-free: suppress explicitly.
	serviceUserCtx = feature.Context(serviceUserCtx, feature.FEATURE_SUPPRESS_EVENTS)
	serviceUserCtx = txid.With(serviceUserCtx, txid.New())

	tx, err := a.entClient.Tx(serviceUserCtx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()
	serviceUserCtx = ent.NewTxContext(serviceUserCtx, tx)

	err = tx.Tenant.UpdateOneID(tenantID).SetDeletedAt(time.Now().UTC()).Exec(serviceUserCtx)
	if ent.IsNotFound(err) {
		err = nil // idempotent: already gone / already deleted
		return nil
	}
	return err
}

// TriggerTenantSyncActivity starts the TenantSyncWorkflow for the newly created tenant.
// Uses deterministic workflow ID to prevent duplicate executions - if a sync is already
// running for this organization, the activity succeeds without starting a new workflow.
func (a *Activities) TriggerTenantSyncActivity(ctx context.Context, input TriggerTenantSyncActivityInput) error {
	// Build deterministic workflow ID - same org always gets same ID
	workflowID := zitadelsync.TenantWorkflowIDPrefix + input.OrganizationID
	workflowOptions := temporalclient.StartWorkflowOptions{
		ID:                    workflowID,
		TaskQueue:             zitadelsync.TenantSyncTaskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}

	_, err := a.temporalClient.ExecuteWorkflow(
		ctx,
		workflowOptions,
		zitadelsync.TenantSyncWorkflow,
		zitadelsync.TenantSyncWorkflowInput{
			TenantID: input.OrganizationID,
		},
	)
	if err != nil {
		// If workflow is already running, that's fine - sync will happen
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			return nil
		}
		return err
	}

	return nil
}

// CreateTenantServiceUserActivity creates a dedicated Zitadel service user for the tenant worker
// and returns a personal access token for it.
//
// TODO(george): The generated token currently leaks into Temporal History as part of the activity
// output. This is acceptable for now but must be fixed. The multi-tenant worker will add a KMS
// to securely handle these tokens instead of passing them through workflow state.
func (*Activities) CreateTenantServiceUserActivity(ctx context.Context, input createTenantServiceUserInput) (*CreateTenantServiceUserOutput, error) {
	zitadelClient, err := getZitadelClient(ctx, input.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer zitadelClient.Close()

	userName := fmt.Sprintf("worker-%s", input.OrganizationID)
	serviceUser, err := zitadelClient.AddServiceUser(ctx, userName, "Tenant Worker")
	if err != nil {
		if !isAlreadyExistsError(err) {
			return nil, err
		}
		serviceUser, err = zitadelClient.GetUserBy(ctx, userName, "")
		if err != nil {
			return nil, err
		}
	}

	token, err := zitadelClient.AddServiceUserToken(ctx, serviceUser.ID)
	if err != nil {
		return nil, err
	}

	return &CreateTenantServiceUserOutput{UserID: serviceUser.ID, Token: token.Token}, nil
}

// StoreTenantWorkerSecretActivity puts the tenant's worker credential in the
// platform secret store, from which worker-api materializes the worker's K8s
// Secret.
func (a *Activities) StoreTenantWorkerSecretActivity(ctx context.Context, input storeTenantWorkerSecretInput) error {
	return a.workerAPI.SetTenantSecret(ctx, input.TenantID, input.Key, input.Value)
}

// CreateTenantWorkerDeploymentActivity asks worker-api to create the tenant's
// worker. No image is sent: pyckGo is a shared-image extension, so worker-api
// resolves the platform's image itself. The environment is read here rather
// than carried in the workflow input, which would pin a deploy-time value into
// replay history.
func (a *Activities) CreateTenantWorkerDeploymentActivity(ctx context.Context, input CreateTenantWorkerDeploymentInput) error {
	return a.workerAPI.CreateDeployment(ctx, workerapi.CreateDeploymentInput{
		Name:              input.Name,
		TenantID:          input.TenantID.String(),
		TemporalNamespace: input.TemporalNamespace,
		Extension:         input.Extension,
		Environment:       core.Config.EnvironmentName,
	})
}
