package registertenant

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	authzv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	permv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	objectv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	orgv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/feature"
	jsonschema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/txid"
	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	entdatatype "github.com/pyck-ai/pyck/backend/management/ent/gen/datatype"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/exec"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

//go:embed datatypes/*.json
var datatypesFS embed.FS

// ErrTenantNotPending is returned when the row to close is already closed
// with a different status than the one requested; the run must not report
// success for a row it did not own.
var ErrTenantNotPending = errors.New("tenant row is not pending")

// ErrTenantRetired is returned when the row to close is gone: deleteTenant
// soft-deleted it while the run was in flight (rows are never hard-deleted).
// The run reacts by retiring the organization the row names.
var ErrTenantRetired = errors.New("tenant row was deleted while its registration ran")

// errTenantNotRetired refuses to deactivate the organization of a row that
// is still live.
var errTenantNotRetired = errors.New("tenant row is not deleted")

// ErrRegistrationMissing is returned when a pending row carries no
// registration data, so no admin user can be created for it: the row was
// not written by the registerTenant resolver, or was cleared by hand.
var ErrRegistrationMissing = errors.New("tenant row has no registration data")

// The errType* constants are the error types of a failed registration
// step. Each names the step whose refusal ended the run: it is the type
// of the non-retryable application
// error the run fails with, which is what the resolver's message and the
// end-to-end suites read, so changing one changes that contract.
// appErrTypeTenantRowUnreadable and appErrTypeTenantRowUnwritable are the two
// types no run has produced yet: the two row steps fail with ent and driver
// errors, which carry no status and stay retryable. They keep those steps
// on the same classified shape as every other one, so a store that does
// answer with a status cannot make a permanent failure look like an outage.
const (
	appErrTypeTenantRowUnreadable    = "tenant-row-unreadable"
	appErrTypeTenantRowMissing       = "tenant-row-missing"
	appErrTypeTenantRowUnwritable    = "tenant-row-unwritable"
	appErrTypeTenantNotPending       = "tenant-not-pending"
	appErrTypeRegistrationMissing    = "registration-missing"
	appErrTypeOrganizationRejected   = "organization-rejected"
	appErrTypeOrganizationNameTaken  = "organization-name-taken"
	appErrTypeOrganizationInvalid    = "organization-invalid"
	appErrTypeOrganizationIDMismatch = "organization-id-mismatch"
	appErrTypeOrganizationOwner      = "organization-owner-rejected"
	appErrTypeAdminAccountRejected   = "admin-account-rejected"
	appErrTypeAdminAccountInvalid    = "admin-account-invalid"
	appErrTypeAdminHashUnsupported   = "admin-hash-unsupported"
	appErrTypeAdminUsernameTaken     = "admin-username-taken"
	appErrTypeProjectGrant           = "project-grant-rejected"
	appErrTypeUserGrant              = "user-grant-rejected"
	appErrTypeDefaultDataTypes       = "default-datatypes-rejected"
	appErrTypeTemporalNamespace      = "temporal-namespace-rejected"
	appErrTypeOrgMetadata            = "org-metadata-rejected"
	appErrTypeOrgMetadataInvalid     = "org-metadata-invalid"
	appErrTypeWorkerIdentity         = "worker-identity-rejected"
	appErrTypeWorkerAPI              = "worker-api-rejected"
	appErrTypeTenantSync             = "tenant-sync-rejected"
	appErrTypeOrganizationRetire     = "organization-retire-rejected"
	appErrTypeTenantNotRetired       = "tenant-not-retired"

	// orgOwnerRole is the Zitadel role of an organization's owner, the one
	// membership role a registration grants.
	orgOwnerRole = "ORG_OWNER"

	// workerTokenLifetimeYears is the lifetime of the worker's personal
	// access token. The user v2 API refuses a token without an expiry, and
	// nothing in the platform rotates this one: no component re-mints it,
	// and a worker whose token lapsed stops polling its namespace with no
	// signal other than idle workflows. The value is therefore chosen to
	// outlive the deployment rather than to bound the credential: the
	// bound is the tenant's lifetime, and revocation is deleting the
	// service user with the tenant. Shortening it is only safe once a
	// re-mint exists; until then a shorter lifetime turns a live tenant
	// into a silently dead worker.
	workerTokenLifetimeYears = 20
)

// classifyAfterWrite is commonwf.Classify for a step that acts on an object
// an earlier step of this run created: the organization, its users, the
// Temporal namespace. Their read models lag the write, so the NotFound or
// FailedPrecondition such a step meets is almost always the lag and not a
// refusal, and the identical request succeeds moments later. Calling it a
// refusal would end the run on its first attempt and orphan everything
// created before that step, so the lag stays retryable and the step's
// ScheduleToCloseTimeout is the bound: an object that really is missing
// costs one step's budget instead of a half-provisioned tenant. A step that
// did not write the object it names calls commonwf.Classify directly, where
// the same codes are permanent.
func classifyAfterWrite(ctx context.Context, errType string, err error) error {
	if err != nil && (projectionLag(err) || namespaceCacheLag(err)) {
		return err
	}
	return commonwf.Classify(ctx, errType, err)
}

// DataTypeDefinition represents a datatype definition from JSON files
type DataTypeDefinition struct {
	Name        string      `json:"name"`
	Slug        string      `json:"slug"`
	Description string      `json:"description"`
	Entity      string      `json:"entity"`
	JSONSchema  interface{} `json:"jsonSchema"`
	Default     bool        `json:"default"`
}

// temporalNamespaceAPI is the slice of the Temporal admin API the namespace
// step uses: registering the tenant's namespace and putting the platform's
// search attributes on it. The narrow interface is the test seam for the
// classification of Temporal outcomes, which a live server otherwise has to
// produce.
type temporalNamespaceAPI interface {
	// EnsureNamespace registers the namespace and tolerates one that
	// already exists, so a retry of the step is a no-op.
	EnsureNamespace(ctx context.Context, namespace string) error
	// EnsureSearchAttributes registers every platform search attribute the
	// namespace does not yet carry, list-missing-then-add, never a blind
	// batch: Temporal rejects a whole batch with AlreadyExists when any one
	// attribute exists, which would silently skip the rest.
	EnsureSearchAttributes(ctx context.Context, namespace string) error
	Close()
}

// Activities struct for methods that need dependencies
type Activities struct {
	resolver       exec.MutationResolver
	entClient      *ent.Client
	temporalClient temporalclient.Client
	nsGetter       commonwf.NamespaceGetter
	// workerAPI owns the worker cluster; management never writes there itself.
	workerAPI *workerapi.Client
	// dialZitadel opens the Zitadel v2 service clients of core.Config, one
	// connection per activity attempt, released by the activity's defer.
	// Immutable after construction; tests substitute a dial that hands out
	// fakes of the generated client interfaces.
	dialZitadel func(ctx context.Context) (*zitadelClients, error)
	// dialNamespaceAPI opens the Temporal admin API of the cluster at url.
	// Immutable after construction.
	dialNamespaceAPI func(ctx context.Context, url string) (temporalNamespaceAPI, error)
}

// NewActivities creates a new Activities instance with the provided resolver
func NewActivities(resolver exec.MutationResolver, entClient *ent.Client, temporalClient temporalclient.Client, nsGetter commonwf.NamespaceGetter, workerAPI *workerapi.Client) *Activities {
	return &Activities{
		resolver:         resolver,
		entClient:        entClient,
		temporalClient:   temporalClient,
		nsGetter:         nsGetter,
		workerAPI:        workerAPI,
		dialZitadel:      dialZitadel,
		dialNamespaceAPI: dialTemporalNamespaceAPI,
	}
}

// dialTemporalNamespaceAPI is the production dialNamespaceAPI: the
// namespace client that registers the namespace and the workflow client
// whose operator service carries the search attributes.
//
//nolint:iface // temporalNamespaceAPI is the test seam for the Temporal outcome classification.
func dialTemporalNamespaceAPI(ctx context.Context, url string) (temporalNamespaceAPI, error) { //nolint:ireturn // the seam is the interface; its production dial returns the real clients.
	nsClient, err := temporal.NewTemporalNamespaceClient(ctx, url)
	if err != nil {
		return nil, err
	}

	svcClient, err := temporal.NewTemporalClient(ctx, url, core.Config.TemporalDialTimeout)
	if err != nil {
		nsClient.Close()
		return nil, fmt.Errorf("create temporal client for search attributes: %w", err)
	}

	return &temporalNamespaceClient{namespaces: nsClient, service: svcClient}, nil
}

// temporalNamespaceClient serves temporalNamespaceAPI from the two SDK
// clients it takes: namespace registration and the operator service live on
// different connections.
type temporalNamespaceClient struct {
	namespaces temporalclient.NamespaceClient
	service    temporalclient.Client
}

// namespaceCacheLag reports whether err is Temporal answering for a
// namespace it has registered but not yet loaded into every host's
// namespace registry cache, which refreshes on a timer: the search-attribute
// call that follows RegisterNamespace in the same step is refused with
// NamespaceNotFound until the frontend serving it has refreshed. Only
// meaningful right after this run registered the namespace.
func namespaceCacheLag(err error) bool {
	var notFound *serviceerror.NamespaceNotFound
	return errors.As(err, &notFound)
}

// EnsureNamespace registers the namespace, tolerating one that exists.
func (c *temporalNamespaceClient) EnsureNamespace(ctx context.Context, namespace string) error {
	return temporal.CreateTemporalNamespace(ctx, c.namespaces, namespace)
}

// EnsureSearchAttributes registers the platform search attributes the
// namespace is still missing, through the shared list-missing-then-add
// (see commonwf.EnsureSearchAttributes).
func (c *temporalNamespaceClient) EnsureSearchAttributes(ctx context.Context, namespace string) error {
	return commonwf.EnsureSearchAttributes(ctx, c.service.OperatorService(), namespace)
}

// Close releases both connections.
func (c *temporalNamespaceClient) Close() {
	c.service.Close()
	c.namespaces.Close()
}

// AddDefaultDataTypesActivity creates the default DataTypes from the
// embedded definitions through the GraphQL resolver. Idempotent: a slug
// the tenant already has is skipped, because CreateDataType would
// otherwise add a new version of it on every re-run of the registration.
func (a *Activities) AddDefaultDataTypesActivity(ctx context.Context, input AddDefaultDataTypesActivityInput) (err error) {
	// Registered before the transaction's defer so it runs after it: the
	// commit is part of the outcome being classified.
	defer func() { err = commonwf.Classify(ctx, appErrTypeDefaultDataTypes, err) }()

	definitions, err := defaultDataTypes()
	if err != nil {
		// The definitions are embedded in this binary: one that does not
		// parse, or names no slug, parses no better on a later attempt.
		return sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeDefaultDataTypes, err)
	}

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
			if rbErr := tx.Rollback(); rbErr != nil {
				log.ForContext(ctx).Warn().Err(rbErr).Msg("rollback failed")
			}
			return
		}
		err = tx.Commit()
	}()
	systemUserCtx = ent.NewTxContext(systemUserCtx, tx)

	// The context above is the tenant's admin user, so TenantIDQueryFilter
	// already scopes this query to tenantID; the explicit predicate ties the
	// scope to the input parameter rather than to how the context was built,
	// so a later change of caller identity (e.g. the system user, which
	// skips the filter) cannot widen the pre-check to other tenants' slugs.
	existing, err := tx.DataType.Query().
		Where(entdatatype.TenantID(tenantID)).
		Select(entdatatype.FieldSlug).
		AllPages(systemUserCtx, mixin.Limit)
	if err != nil {
		return fmt.Errorf("list existing datatypes: %w", err)
	}
	existingSlugs := make(map[string]struct{}, len(existing))
	for _, dt := range existing {
		existingSlugs[dt.Slug] = struct{}{}
	}

	for _, dataTypeDef := range definitions {
		if _, ok := existingSlugs[dataTypeDef.Slug]; ok {
			continue
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
			err = fmt.Errorf("create datatype %s: %w", dataTypeDef.Name, err)
			if refusedDataType(err) {
				return sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeDefaultDataTypes, err)
			}
			return err
		}
	}

	return nil
}

// refusedDataType reports whether err is the resolver's verdict on the
// definition rather than a fault of the store underneath it. A schema that
// does not compile and a field value Ent rejects are the same answer on
// every attempt, and neither carries a gRPC or an HTTP status: the shared
// classifier reads both as a transport fault and would spend the step's
// whole retry budget on an answer that cannot change. A constraint
// violation is deliberately absent: the slug pre-check above makes a
// duplicate the artefact of a concurrent run, which a retry resolves.
func refusedDataType(err error) bool {
	return errors.Is(err, jsonschema.ErrInvalidSchema) || ent.IsValidationError(err)
}

// errDataTypeDefinitionSlug rejects an embedded default definition that
// names no slug. The activity skips the definitions a tenant already has
// by slug, so one without it would be created again on every re-run of a
// registration, adding a version each time.
var errDataTypeDefinitionSlug = errors.New("default datatype definition has no slug")

// defaultDataTypes parses the embedded default datatype definitions once
// per process. The files are compiled into the binary, so the outcome
// cannot change between calls, while parsing them per registration
// re-read and re-decoded the whole set for every tenant. The error is
// memoized with the value: a defect of the embedded files is reported as
// a refusal on every call, not retried into a different answer.
var defaultDataTypes = sync.OnceValues(defaultDataTypeDefinitions)

// defaultDataTypeDefinitions parses every embedded default datatype
// definition, in directory order, and refuses one that names no slug.
// Every error it returns is a defect of this binary's embedded files, so
// the caller reports them as a refusal rather than retrying them. Call
// defaultDataTypes instead; this is its one-shot body.
func defaultDataTypeDefinitions() ([]DataTypeDefinition, error) {
	entries, err := datatypesFS.ReadDir("datatypes")
	if err != nil {
		return nil, fmt.Errorf("failed to read datatypes directory: %w", err)
	}

	definitions := make([]DataTypeDefinition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		content, err := datatypesFS.ReadFile(filepath.Join("datatypes", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read datatype file %s: %w", entry.Name(), err)
		}

		var dataTypeDef DataTypeDefinition
		if err := json.Unmarshal(content, &dataTypeDef); err != nil {
			return nil, fmt.Errorf("failed to parse datatype file %s: %w", entry.Name(), err)
		}
		if dataTypeDef.Slug == "" {
			return nil, fmt.Errorf("datatype file %s: %w", entry.Name(), errDataTypeDefinitionSlug)
		}
		definitions = append(definitions, dataTypeDef)
	}
	return definitions, nil
}

// CreateTenantActivity creates the Zitadel org under the pre-chosen ID.
// Idempotent under retry: addOrganizationWithID answers success when the
// ID is already taken, by this registration, since the ID is random per
// registration. Three outcomes no retry can change are non-retryable: a
// name held by another org (a concurrent same-name registration or a stale
// org from an earlier failed one), an InvalidArgument is a name Zitadel
// rejects, and a Zitadel that mints its own IDs instead of honouring the
// requested one is a deployment mismatch. An ownership the conflict probe
// cannot yet resolve is left retryable on purpose; see
// errOrganizationOwnerUnresolved.
func (a *Activities) CreateTenantActivity(ctx context.Context, input createTenantActivityInput) (out *CreateTenantActivityOutput, err error) {
	defer func() { err = commonwf.Classify(ctx, appErrTypeOrganizationRejected, err) }()

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return nil, err
	}
	defer zc.Close(ctx)

	if err := addOrganizationWithID(ctx, zc.orgs, input.Name, input.OrganizationID); err != nil {
		switch {
		case errors.Is(err, errOrganizationNameTaken):
			return nil, sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeOrganizationNameTaken, err)
		case errors.Is(err, errOrganizationIDMismatch):
			return nil, sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeOrganizationIDMismatch, err)
		case status.Code(err) == codes.InvalidArgument:
			return nil, sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeOrganizationInvalid, err)
		}
		return nil, err
	}

	return &CreateTenantActivityOutput{
		OrganizationID:    input.OrganizationID,
		TenantID:          input.TenantID,
		TemporalNamespace: a.nsGetter.GetNamespace(input.TenantID),
	}, nil
}

// LoadTenantRegistrationActivity reads the non-secret part of the tenant
// row the run provisions. A row that does not exist or is soft-deleted
// comes back with an empty Status, which the workflow treats like any
// other non-pending row: nothing to do.
func (a *Activities) LoadTenantRegistrationActivity(ctx context.Context, input LoadTenantRegistrationActivityInput) (out *LoadTenantRegistrationActivityOutput, err error) {
	defer func() { err = commonwf.Classify(ctx, appErrTypeTenantRowUnreadable, err) }()

	row, err := a.entClient.Tenant.Query().
		Where(enttenant.IDEQ(input.TenantID)).
		Only(authn.Context(ctx, authn.SystemUser()))
	if ent.IsNotFound(err) {
		return &LoadTenantRegistrationActivityOutput{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load tenant row: %w", err)
	}
	return &LoadTenantRegistrationActivityOutput{
		Status:         row.Status,
		OrganizationID: row.IdpOrgRef,
		Name:           row.Name,
		Data:           row.Data,
	}, nil
}

// CreateZitadelUserActivity imports the tenant's admin user into the
// org from the row's registration data. The password hash is read here,
// not carried in the input, so it never enters workflow history. The user
// ID is chosen here rather than by Zitadel, derived from the tenant row,
// so every attempt of the step sends the same one: a retry of an import
// whose response was lost collides on the ID (its own user, resolved by
// a lookup) never on the username. A username collision is a different
// user's by ID: either the tenant's own admin from a pre-migration run
// (adopted) or a foreign organization's user, which ends the run;
// usernames are unique across the whole Zitadel instance under the
// default domain policy. An account Zitadel rejects (InvalidArgument, an
// unsupported hash encoding included), a row without registration data,
// and a row that is gone (soft-deleted by deleteTenant while the run was
// in flight) end it too.
//
// It classifies after a write: the import runs against the organization
// the previous step created, so Zitadel answering NotFound or
// FailedPrecondition for it is a projection still catching up.
func (a *Activities) CreateZitadelUserActivity(ctx context.Context, input createZitadelUserActivityInput) (out *CreateZitadelUserActivityOutput, err error) {
	defer func() { err = classifyAfterWrite(ctx, appErrTypeAdminAccountRejected, err) }()

	row, err := a.entClient.Tenant.Query().
		Where(enttenant.IDEQ(input.TenantID)).
		Only(authn.Context(ctx, authn.SystemUser()))
	if ent.IsNotFound(err) {
		return nil, sdktemporal.NewNonRetryableApplicationError(fmt.Sprintf("load tenant row: %v", err), appErrTypeTenantRowMissing, err)
	}
	if err != nil {
		return nil, fmt.Errorf("load tenant row: %w", err)
	}
	if row.Registration == nil {
		return nil, sdktemporal.NewNonRetryableApplicationError(ErrRegistrationMissing.Error(), appErrTypeRegistrationMissing, ErrRegistrationMissing)
	}
	reg := row.Registration

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return nil, err
	}
	defer zc.Close(ctx)

	adminID := adminUserID(input.TenantID)
	resp, createErr := zc.users.CreateUser(ctx, &userv2.CreateUserRequest{
		OrganizationId: row.IdpOrgRef,
		UserId:         &adminID,
		Username:       &reg.AdminUsername,
		UserType: &userv2.CreateUserRequest_Human_{Human: &userv2.CreateUserRequest_Human{
			Profile: &userv2.SetHumanProfile{
				GivenName:  reg.AdminFirstName,
				FamilyName: reg.AdminLastName,
			},
			Email: &userv2.SetHumanEmail{
				Email:        reg.AdminEmail,
				Verification: &userv2.SetHumanEmail_IsVerified{IsVerified: true},
			},
			// The hash goes over the wire verbatim, in the hashed-password
			// oneof: sent as the clear-text password it would be stored as
			// the literal password and the admin could never log in.
			// Zitadel verifies it with passwap on the first login and
			// re-hashes it with its own configured algorithm.
			PasswordType: &userv2.CreateUserRequest_Human_HashedPassword{HashedPassword: &userv2.HashedPassword{
				Hash:           reg.PasswordHash,
				ChangeRequired: false,
			}},
		}},
	})
	switch status.Code(createErr) {
	case codes.OK:
	case codes.InvalidArgument:
		// A value Zitadel refuses gets the same answer on every attempt.
		// An unsupported hash encoding is rejected here at import, not at
		// login; the hash is pyck's own bcrypt, so that refusal is a
		// Zitadel configured without it (not something the caller can
		// correct) and gets its own type. Every other refusal is a value
		// from the registration request.
		if zitadelErrorID(createErr) == zitadelErrIDHashUnsupported {
			return nil, sdktemporal.NewNonRetryableApplicationError(createErr.Error(), appErrTypeAdminHashUnsupported, createErr)
		}
		return nil, sdktemporal.NewNonRetryableApplicationError(createErr.Error(), appErrTypeAdminAccountInvalid, createErr)
	case codes.AlreadyExists:
		// A different user holds the username, different by ID, not
		// necessarily by identity: a pre-migration run imported this same
		// admin under a Zitadel-minted ID, so on a cross-version re-run
		// the tenant's OWN user is the holder and must be adopted, not
		// refused. resolveTakenUsername tells the two apart; only a holder
		// in a foreign organization ends the run.
		holder, holderErr := resolveTakenUsername(ctx, zc.users, row.IdpOrgRef, reg.AdminUsername, appErrTypeAdminUsernameTaken, createErr)
		if holderErr != nil {
			return nil, holderErr
		}
		return &CreateZitadelUserActivityOutput{
			UserID:    holder.GetUserId(),
			LoginName: holder.GetPreferredLoginName(),
		}, nil
	case codes.FailedPrecondition:
		// Two states answer FailedPrecondition: the pre-chosen ID already
		// carries a user (this run's own import whose response was lost,
		// COMMAND-7yiox1isql) and an organization whose projection has not
		// caught up (Errors.Org.NotFound). The lookup by the pre-chosen ID
		// settles the first; everything else keeps the ORIGINAL error,
		// which classifyAfterWrite leaves retryable within the step's
		// budget.
		found, lookupErr := zc.users.GetUserByID(ctx, &userv2.GetUserByIDRequest{UserId: adminID})
		if lookupErr == nil && found.GetUser().GetDetails().GetResourceOwner() == row.IdpOrgRef {
			return &CreateZitadelUserActivityOutput{
				UserID:    found.GetUser().GetUserId(),
				LoginName: found.GetUser().GetPreferredLoginName(),
			}, nil
		}
		return nil, fmt.Errorf("create admin user %q: %w", reg.AdminUsername, createErr)
	default:
		return nil, createErr
	}

	// The create answers only the ID; the login name the workflow output
	// reports is read back. A user projection that has not caught up
	// answers NotFound, which stays retryable and resolves through the
	// FailedPrecondition path on the next attempt.
	created, err := zc.users.GetUserByID(ctx, &userv2.GetUserByIDRequest{UserId: resp.GetId()})
	if err != nil {
		return nil, fmt.Errorf("read back admin user %q: %w", reg.AdminUsername, err)
	}

	return &CreateZitadelUserActivityOutput{UserID: resp.GetId(), LoginName: created.GetUser().GetPreferredLoginName()}, nil
}

// adminUserID is the pre-chosen Zitadel user ID of the tenant's admin,
// derived deterministically from the tenant so every attempt of the import
// sends the same one, which is what turns "my own retry" into an ID
// collision the activity can resolve by lookup instead of a username
// collision indistinguishable from another tenant's.
func adminUserID(tenantID uuid.UUID) string {
	return authn.ComputeUUID(tenantID.String(), "admin").String()
}

// errUsernameUnavailable is the one answer both branches of
// resolveTakenUsername give the caller. registerTenant is reachable
// without authentication and its errors travel to that caller verbatim, so
// a message that distinguished "held by another organization" from "held
// by something the projection does not show yet" would be an
// instance-wide username→tenant oracle. The distinction lives in the Info
// logs beside each branch, and in the retryability of the error.
var errUsernameUnavailable = errors.New("admin username is not available")

// resolveTakenUsername triages the AlreadyExists a CreateUser with a
// pre-chosen user ID answers for a colliding username. AlreadyExists
// proves a holder exists in the eventstore; the projection may lag it;
// the instance-wide lookup below decides ownership, and a miss is lag,
// never a verdict. Three outcomes: the holder is in the tenant's own
// organization (a user a pre-migration run created under a Zitadel-minted
// ID, which the pre-chosen ID therefore never collides with) and is
// adopted; the holder is a foreign organization's user, which no retry
// frees, a non-retryable error of type errType naming the username and
// the holding organization; or no holder is projected yet, which keeps
// createErr as the retryable cause so the next attempt re-triages once
// the projection has caught up. The lookup searches the whole instance
// (an org-scoped miss could not tell a foreign holder from an unprojected
// one) and matches ignoring case, mirroring Zitadel's case-insensitive
// username uniqueness.
func resolveTakenUsername(ctx context.Context, users userv2.UserServiceClient, orgID, username, errType string, createErr error) (*userv2.User, error) {
	resp, err := users.ListUsers(ctx, &userv2.ListUsersRequest{
		Query: &objectv2.ListQuery{Limit: 1},
		Queries: []*userv2.SearchQuery{{
			Query: &userv2.SearchQuery_UserNameQuery{UserNameQuery: &userv2.UserNameQuery{
				UserName: username,
				Method:   objectv2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE,
			}},
		}},
	})
	if err != nil {
		// The lookup's own fault settles nothing; the retryable error
		// below carries the import's answer, so the lookup's is logged.
		log.ForContext(ctx).Warn().Err(err).Str("username", username).
			Msg("could not look up the holder of a taken username")
	}
	if err != nil || len(resp.GetResult()) == 0 {
		log.ForContext(ctx).Info().
			Str("username", username).
			Str("organization_id", orgID).
			Str("error_type", errType).
			Msg("registration username is taken by a holder the projection does not show yet")
		return nil, sdktemporal.NewApplicationErrorWithOptions(
			errUsernameUnavailable.Error(),
			errType, sdktemporal.ApplicationErrorOptions{Cause: createErr})
	}

	holder := resp.GetResult()[0]
	if holderOrg := holder.GetDetails().GetResourceOwner(); holderOrg != orgID {
		// Which organization holds it is for operators only, logged here
		// and never put in the error.
		log.ForContext(ctx).Info().
			Str("username", username).
			Str("holder_org", holderOrg).
			Str("error_type", errType).
			Msg("registration username is held by another organization")
		return nil, sdktemporal.NewNonRetryableApplicationError(
			errUsernameUnavailable.Error(), errType, createErr)
	}
	return holder, nil
}

// SetUserAsOrganizationOwnerActivity makes the admin user an owner of the
// organization. It only adds: a tenant may hold several org admins (the
// service account that created the organization is one, and a tenant that
// promotes a second administrator later is another) and removing the
// ones this run did not create would strip an operator's access from a
// re-run of an existing registration.
//
// Idempotent under retry: a membership that already exists answers
// AlreadyExists, which is success. Both the organization and the user were
// created by earlier steps of the same run, so a NotFound from Zitadel is
// its member projection catching up rather than a missing object.
func (a *Activities) SetUserAsOrganizationOwnerActivity(ctx context.Context, input setUserAsOrganizationAdmin) (err error) {
	defer func() { err = classifyAfterWrite(ctx, appErrTypeOrganizationOwner, err) }()

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return err
	}
	defer zc.Close(ctx)

	_, err = zc.perms.CreateAdministrator(ctx, &permv2.CreateAdministratorRequest{
		UserId: input.UserID,
		Resource: &permv2.ResourceType{
			Resource: &permv2.ResourceType_OrganizationId{OrganizationId: input.OrganizationID},
		},
		Roles: []string{orgOwnerRole},
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}
	return nil
}

// AddProjectGrantActivity grants the pyck project to the org the previous
// step created. Idempotent under retry: a project has at most one grant
// per organization, so an AlreadyExists can only be this registration's
// own. A grant that already exists has its role menu widened
// to input.Roles: a re-run of a registration that a deploy adding a role
// interrupted would otherwise keep the old menu, and the user-grant step
// that follows can only grant roles the menu carries. A project or
// organization the grant projection has not caught up with answers
// FailedPrecondition, and a grant the projection does not report yet
// answers ErrProjectGrantNotFound; both stay retryable within the step's
// budget.
//
// The widening is a read, a union and a full replace with no
// compare-and-set (see sdk.ExtendProjectGrantRoles). The only other writer
// of this org's menu is assignRoles/removeRoles, which addresses a tenant
// through its projected users. In a first run there are none yet: this run
// projects the admin itself, in TriggerTenantSyncActivity after this step.
// A re-run of a failed registration finds the admin already projected, and
// what keeps tenant callers out then is the liveness gate, which refuses
// every tenant whose row is not done. A system caller skips that gate, so
// its concurrent assignRoles can still have its addition silently dropped
// by this replace.
func (a *Activities) AddProjectGrantActivity(ctx context.Context, input addProjectGrantsInput) (err error) {
	defer func() { err = classifyAfterWrite(ctx, appErrTypeProjectGrant, err) }()

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return err
	}
	defer zc.Close(ctx)

	_, err = zc.projects.CreateProjectGrant(ctx, &projectv2.CreateProjectGrantRequest{
		ProjectId:             input.ProjectID,
		GrantedOrganizationId: input.OrganizationID,
		RoleKeys:              input.Roles,
	})
	if err == nil {
		return nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return err
	}

	if _, _, err := sdk.ExtendProjectGrantRoles(ctx, zc.grants(), input.ProjectID, input.OrganizationID, input.Roles); err != nil {
		return fmt.Errorf("widen existing project grant: %w", err)
	}
	return nil
}

// AddUserGrantActivity grants the project roles to a user of the tenant.
// Idempotent under retry: a grant that already exists has its role set
// widened to input.Roles, for the same re-run after a deploy that
// AddProjectGrantActivity widens the menu for. Zitadel resolves the project
// grant itself (no grant ID is sent) against a projection the previous
// step's write has to reach first, so its NotFound and FailedPrecondition
// stay retryable, as does an existing grant the authorization projection
// does not report yet (ErrUserGrantNotFound).
func (a *Activities) AddUserGrantActivity(ctx context.Context, input addUserGrantInput) (err error) {
	defer func() { err = classifyAfterWrite(ctx, appErrTypeUserGrant, err) }()

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return err
	}
	defer zc.Close(ctx)

	_, err = zc.authz.CreateAuthorization(ctx, &authzv2.CreateAuthorizationRequest{
		UserId:         input.UserID,
		ProjectId:      input.ProjectID,
		OrganizationId: input.OrganizationID,
		RoleKeys:       input.Roles,
	})
	if err == nil {
		return nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return err
	}

	if _, _, err := sdk.ExtendUserAuthorizationRoles(ctx, zc.grants(), input.ProjectID, input.OrganizationID, input.UserID, input.Roles); err != nil {
		return fmt.Errorf("widen existing user grant: %w", err)
	}
	return nil
}

// CreateTemporalNamespaceActivity gives the tenant the Temporal namespace
// every one of its workflows runs in, with the platform's search attributes
// registered on it. Idempotent under retry: an existing namespace and an
// attribute already registered are both success. The attribute call names
// the namespace the same activity just registered, which Temporal serves
// from a cache that lags the registration, so its NotFound is retryable.
func (a *Activities) CreateTemporalNamespaceActivity(ctx context.Context, input createTemporalNamespaceInput) (err error) {
	defer func() { err = classifyAfterWrite(ctx, appErrTypeTemporalNamespace, err) }()

	nsAPI, err := a.dialNamespaceAPI(ctx, input.TemporalUrl)
	if err != nil {
		return err
	}
	defer nsAPI.Close()

	if err := nsAPI.EnsureNamespace(ctx, input.Namespace); err != nil {
		return fmt.Errorf("register temporal namespace %q: %w", input.Namespace, err)
	}

	// List-missing-then-add through the shared implementation: a blind
	// batch would be rejected wholesale with AlreadyExists when any one
	// attribute exists, silently skipping the genuinely-missing rest; a
	// registration retry that crossed a deploy which grew the platform set
	// would leave the namespace partially attributed.
	// No AlreadyExists tolerance here: Ensure converges to nil when every
	// attribute is present, so a surviving error is a real failure.
	if err := nsAPI.EnsureSearchAttributes(ctx, input.Namespace); err != nil {
		return fmt.Errorf("ensure search attributes on %q: %w", input.Namespace, err)
	}

	return nil
}

// SetOrgMetadataActivity writes the caller-supplied `Data` map to
// the Zitadel organization metadata. It writes to the organization an
// earlier step created, so a NotFound is that organization's projection
// catching up. The whole map goes in one round-trip, so the attempt costs
// the same for one key as for the maximum the resolver accepts: a
// per-key round-trip would let a caller's key count decide whether any
// attempt can finish inside activityStartToClose, and no number of
// retries would then complete the step. Tenant expiry is intentionally
// NOT written here — it lives only in the management.tenants.expires_at
// column (set by the registerTenant resolver for new tenants and by the
// setTenantExpiry resolver thereafter).
func (a *Activities) SetOrgMetadataActivity(ctx context.Context, input SetOrgMetadataActivityInput) (err error) {
	defer func() { err = classifyAfterWrite(ctx, appErrTypeOrgMetadata, err) }()

	if len(input.Data) == 0 {
		return nil
	}

	// Keys go in sorted order so the same map produces the same request,
	// and the values as raw bytes: base64 is a REST-transcoding rule, not
	// a gRPC one.
	entries := make([]*orgv2.Metadata, 0, len(input.Data))
	for _, key := range slices.Sorted(maps.Keys(input.Data)) {
		str, err := metadataValue(input.Data[key])
		if err != nil {
			// A value org metadata cannot carry renders no better on a
			// later attempt, so the step fails now instead of spending its
			// whole budget on a defect of this binary (the same call as
			// AddDefaultDataTypesActivity makes for its parse failure).
			err = fmt.Errorf("tenant data %q: %w", key, err)
			return sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeOrgMetadata, err)
		}
		entries = append(entries, &orgv2.Metadata{Key: key, Value: []byte(str)})
	}

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return err
	}
	defer zc.Close(ctx)

	_, err = zc.orgs.SetOrganizationMetadata(ctx, &orgv2.SetOrganizationMetadataRequest{
		OrganizationId: input.OrganizationID,
		Metadata:       entries,
	})
	if status.Code(err) == codes.InvalidArgument {
		// Zitadel refusing a key or a value refuses the caller's tenant
		// data. Every other permanent code (PermissionDenied, Unimplemented,
		// OutOfRange) is the platform's own fault (a missing grant, a
		// Zitadel without the API) and stays appErrTypeOrgMetadata.
		return sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeOrgMetadataInvalid, err)
	}

	return err
}

// errTenantDataValue rejects a tenant-data value org metadata cannot carry.
// The resolver's validateTenantData already refuses non-scalars, so this
// is the invariant restated where the value is rendered rather than a case
// a caller reaches.
var errTenantDataValue = errors.New("value is not a scalar org metadata can carry")

// metadataValue renders one tenant-data value as the string Zitadel stores.
// Every scalar is rendered explicitly rather than through %v, which prints
// a float64 in Go's shortest form (1e+06 for a million) and would put
// that exponent notation in the tenant's org metadata, where it is read
// back as text.
//
// JSON decoding produces float64 for every number unless a decoder was set
// to UseNumber, so both cases are handled. Anything else is an error: a
// silent %v would ship a Go struct dump as a tenant's metadata.
//
// Twin of dataindex.renderListElement, which renders the same three scalars
// the same way for the search index. The two differ where their domains do:
// a nil is an empty index entry there and a refusal here (a metadata key
// whose value vanished is a caller mistake, not an empty string), and
// json.Number is handled here only, because a tenant's data map reaches
// this activity through Temporal's payload codec rather than the index's
// own decoder. Kept apart rather than shared: neither package's contract
// may drift to follow the other's.
func metadataValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case float64:
		// 'f' with precision -1: the shortest decimal that round-trips,
		// never an exponent.
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case json.Number:
		// Rendered through float64 so this branch answers exactly what the
		// float64 branch answers for the same number. v.String() would
		// hand back the caller's own spelling, and "1e6" is a valid JSON
		// number, the exponent notation this function exists to keep out
		// of a tenant's org metadata, arriving through the one decoder
		// (json.Decoder.UseNumber, which gqlgen's HTTP transport sets)
		// that preserves it.
		f, ferr := v.Float64()
		if ferr != nil {
			return "", fmt.Errorf("%w: %s", errTenantDataValue, v.String())
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("%w: %T", errTenantDataValue, value)
	}
}

// SetTenantStatusActivity closes the pending tenant row with the given
// terminal status. done also clears the registration data (the admin
// account is in Zitadel now, the hash has no further use); error keeps it
// so a re-run can still create the user. Two guards protect the write:
// HistoryMixin's mutation filter excludes soft-deleted rows (a retired
// row still says status=pending, so the predicate alone would not), and
// the restated pending predicate makes the flip happen at most once.
//
// The update is single-row (UpdateOneID): MutationEventHook refuses a bulk
// Update().Where() that matches rows, because it can emit at most one
// event for it and that event would carry an affected-row count instead
// of the tenant.
//
// A row that is gone, soft-deleted or no longer pending matches nothing
// and surfaces as ent.IsNotFound, either wrapped from the hook's
// before-state fetch or raw from the UPDATE itself. Idempotent: a retry
// after a committed flip finds the row in the requested status and
// succeeds without writing. Any other state is a non-retryable failure:
// appErrTypeTenantRowMissing for a row that is gone (ErrTenantRetired
// when deleteTenant soft-deleted it, which the run answers by retiring the
// organization) and appErrTypeTenantNotPending (ErrTenantNotPending) for
// a row closed with another status.
func (a *Activities) SetTenantStatusActivity(ctx context.Context, input SetTenantStatusActivityInput) (err error) {
	// Registered before the transaction's defer so it runs after it: the
	// commit is part of the outcome being classified.
	defer func() { err = commonwf.Classify(ctx, appErrTypeTenantRowUnwritable, err) }()

	// Mirror what gqltx does on the GraphQL path: explicit tx + per-attempt
	// txid so MutationEventHook can stamp the outbox row. Tenant has no
	// tenant_id column, so the row's own ID is the mutation tenant the hook
	// derives the event topic from.
	serviceUserCtx := request.Context(ctx, authn.SystemUser(), input.TenantID)
	serviceUserCtx = txid.With(serviceUserCtx, txid.New())

	tx, err := a.entClient.Tx(serviceUserCtx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				log.ForContext(ctx).Warn().Err(rbErr).Msg("rollback failed")
			}
			return
		}
		err = tx.Commit()
	}()
	serviceUserCtx = ent.NewTxContext(serviceUserCtx, tx)

	update := tx.Tenant.UpdateOneID(input.TenantID).
		Where(enttenant.StatusEQ(enttenant.StatusPending)).
		SetStatus(input.Status)
	if input.Status == enttenant.StatusDone {
		update.ClearRegistration()
	}
	_, err = update.Save(serviceUserCtx)
	if err == nil {
		return nil
	}
	if !ent.IsNotFound(err) {
		return fmt.Errorf("set tenant status %s: %w", input.Status, err)
	}

	closed, err := tx.Tenant.Query().
		Where(enttenant.IDEQ(input.TenantID), enttenant.StatusEQ(input.Status)).
		Exist(serviceUserCtx)
	if err != nil {
		return fmt.Errorf("check tenant status: %w", err)
	}
	if closed {
		return nil
	}
	row, err := tx.Tenant.Query().
		Where(enttenant.IDEQ(input.TenantID)).
		Only(feature.Context(serviceUserCtx, feature.FEATURE_SHOW_DELETED))
	switch {
	case ent.IsNotFound(err):
		return sdktemporal.NewNonRetryableApplicationError(fmt.Sprintf("load tenant row: %v", err), appErrTypeTenantRowMissing, err)
	case err != nil:
		return fmt.Errorf("check tenant row: %w", err)
	case !row.DeletedAt.IsZero():
		return sdktemporal.NewNonRetryableApplicationError(ErrTenantRetired.Error(), appErrTypeTenantRowMissing, ErrTenantRetired)
	default:
		return sdktemporal.NewNonRetryableApplicationError(ErrTenantNotPending.Error(), appErrTypeTenantNotPending, ErrTenantNotPending)
	}
}

// DeactivateRetiredOrganizationActivity deactivates the Zitadel
// organization of a tenant row deleteTenant retired while the registration
// run was provisioning it. The delete event's DisableTenantWorkflow
// deactivates the organization as well, but it may have run before this
// run created it; this is the run cleaning up after itself.
//
// Only a soft-deleted row is acted on: a live one is refused, so a misread
// row can never take a live tenant's organization down. An organization
// already inactive (FailedPrecondition) or never created (NotFound)
// satisfies the goal.
func (a *Activities) DeactivateRetiredOrganizationActivity(ctx context.Context, input DeactivateRetiredOrganizationActivityInput) (err error) {
	defer func() { err = commonwf.Classify(ctx, appErrTypeOrganizationRetire, err) }()

	row, err := a.entClient.Tenant.Query().
		Where(enttenant.IDEQ(input.TenantID)).
		Only(feature.Context(authn.Context(ctx, authn.SystemUser()), feature.FEATURE_SHOW_DELETED))
	if ent.IsNotFound(err) {
		return sdktemporal.NewNonRetryableApplicationError(fmt.Sprintf("load tenant row: %v", err), appErrTypeTenantRowMissing, err)
	}
	if err != nil {
		return fmt.Errorf("load tenant row: %w", err)
	}
	if row.DeletedAt.IsZero() {
		return sdktemporal.NewNonRetryableApplicationError(errTenantNotRetired.Error(), appErrTypeTenantNotRetired, errTenantNotRetired)
	}

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return err
	}
	defer zc.Close(ctx)

	_, err = zc.orgs.DeactivateOrganization(ctx, &orgv2.DeactivateOrganizationRequest{OrganizationId: row.IdpOrgRef})
	switch status.Code(err) {
	case codes.OK, codes.FailedPrecondition, codes.NotFound:
		return nil
	default:
		return fmt.Errorf("deactivate organization %s: %w", row.IdpOrgRef, err)
	}
}

// TriggerTenantSyncActivity starts the TenantSyncWorkflow for the newly created tenant.
// Uses deterministic workflow ID to prevent duplicate executions - if a sync is already
// running for this organization, the activity succeeds without starting a new workflow.
func (a *Activities) TriggerTenantSyncActivity(ctx context.Context, input TriggerTenantSyncActivityInput) (err error) {
	defer func() { err = commonwf.Classify(ctx, appErrTypeTenantSync, err) }()

	// Build deterministic workflow ID - same org always gets same ID
	workflowID := zitadelsync.TenantWorkflowIDPrefix + input.OrganizationID
	// ALLOW_DUPLICATE, not REJECT_DUPLICATE: the scheduled orchestrator
	// starts the same workflow ID every cycle, so a sync that already ran
	// and CLOSED during a slow registration would otherwise reject this
	// trigger, silently skipping the one sync that beats the cycle
	// latency for the just-imported admin. A sync still RUNNING answers
	// AlreadyStarted regardless of the policy, tolerated below.
	workflowOptions := temporalclient.StartWorkflowOptions{
		ID:                    workflowID,
		TaskQueue:             zitadelsync.TenantSyncTaskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}

	_, err = a.temporalClient.ExecuteWorkflow(
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

// ProvisionTenantWorkerIdentityActivity gives a pyck-go tenant's worker its
// Zitadel identity and hands the resulting credential to the platform
// secret store, from which worker-api materializes the worker's K8s Secret.
//
// Minting and storing are one activity because the credential must not
// leave it: an activity that returned the token would write it into
// workflow history, where it stays readable for the history's retention
// and is replayed on every worker restart. This mirrors the admin import,
// which reads the password hash from the tenant row inside the activity
// for the same reason. The invariant: the personal access token appears in
// no activity input and no activity output; only the user ID does.
//
// The service user is created in the organization an earlier step created,
// so Zitadel refusing it with FailedPrecondition is its projection
// catching up rather than a missing organization. Like the admin import,
// the user ID is pre-chosen and derived from the organization, so a retry
// collides on the ID (its own user, resolved by lookup) and a username
// collision is either this tenant's pre-migration worker (adopted) or a
// foreign organization's user, which ends the run.
//
// Every attempt mints a token and nothing revokes the one a previous
// attempt left behind, so this step runs under workerIdentityScheduleToClose
// rather than the provisioning budget: its retries are bounded by the number
// of live credentials one registration may leave, not by the outage the rest
// of the run survives. A token minted by an attempt whose secret-store push
// then failed is such a credential; the next attempt mints a fresh one and
// overwrites the secret, so the worker never runs on a token the store does
// not hold.
func (a *Activities) ProvisionTenantWorkerIdentityActivity(ctx context.Context, input provisionTenantWorkerIdentityInput) (out *ProvisionTenantWorkerIdentityOutput, err error) {
	userID, token, err := a.mintWorkerCredential(ctx, input.OrganizationID)
	if err != nil {
		return nil, err
	}

	if err := classifyWorkerAPI(ctx, a.workerAPI.SetTenantSecret(ctx, input.TenantID, input.Key, token)); err != nil {
		return nil, err
	}

	return &ProvisionTenantWorkerIdentityOutput{UserID: userID}, nil
}

// mintWorkerCredential creates (or adopts) the worker's Zitadel service
// user and issues its personal access token, on one connection: a dial
// builds a gRPC connection and exchanges a JWT profile for a token, so the
// step's one-minute budget does not pay for it twice (see zitadelClients:
// one dial per activity attempt). Split out of the activity so the Zitadel
// half keeps its own error classification, separate from the worker-api
// half the activity classifies itself.
func (a *Activities) mintWorkerCredential(ctx context.Context, organizationID string) (userID, token string, err error) {
	defer func() { err = classifyAfterWrite(ctx, appErrTypeWorkerIdentity, err) }()

	zc, err := a.dialZitadel(ctx)
	if err != nil {
		return "", "", err
	}
	defer zc.Close(ctx)

	userID, err = mintWorkerIdentity(ctx, zc, organizationID)
	if err != nil {
		return "", "", err
	}

	// The token is returned to the activity and never further: see the
	// invariant on ProvisionTenantWorkerIdentityActivity.
	pat, err := zc.users.AddPersonalAccessToken(ctx, &userv2.AddPersonalAccessTokenRequest{
		UserId:         userID,
		ExpirationDate: timestamppb.New(time.Now().AddDate(workerTokenLifetimeYears, 0, 0)),
	})
	if err != nil {
		return "", "", err
	}

	return userID, pat.GetToken(), nil
}

// mintWorkerIdentity creates (or adopts) the worker's Zitadel service user
// on the caller's connection and returns its ID.
func mintWorkerIdentity(ctx context.Context, zc *zitadelClients, organizationID string) (string, error) {
	workerID := workerUserID(organizationID)
	userName := fmt.Sprintf("worker-%s", organizationID)
	resp, createErr := zc.users.CreateUser(ctx, &userv2.CreateUserRequest{
		OrganizationId: organizationID,
		UserId:         &workerID,
		Username:       &userName,
		UserType: &userv2.CreateUserRequest_Machine_{Machine: &userv2.CreateUserRequest_Machine{
			Name: "Tenant Worker",
		}},
	})
	switch status.Code(createErr) {
	case codes.OK:
		return resp.GetId(), nil
	case codes.AlreadyExists:
		// The same cross-version case as the admin import's, certain
		// here, because the worker's username is deterministic: a worker a
		// pre-migration run created under a Zitadel-minted ID holds it and
		// must be adopted, not refused. Only a foreign holder ends the run.
		holder, holderErr := resolveTakenUsername(ctx, zc.users, organizationID, userName, appErrTypeWorkerIdentity, createErr)
		if holderErr != nil {
			return "", holderErr
		}
		return holder.GetUserId(), nil
	case codes.FailedPrecondition:
		// A duplicate pre-chosen ID answers FailedPrecondition for a
		// machine user just as for a human one (COMMAND-k2una), and so
		// does an organization whose projection lags. The lookup settles
		// the first; everything else keeps the ORIGINAL error, which
		// classifyAfterWrite leaves retryable within the step's budget.
		found, lookupErr := zc.users.GetUserByID(ctx, &userv2.GetUserByIDRequest{UserId: workerID})
		if lookupErr != nil || found.GetUser().GetDetails().GetResourceOwner() != organizationID {
			return "", fmt.Errorf("create worker user %q: %w", userName, createErr)
		}
		return found.GetUser().GetUserId(), nil
	default:
		return "", createErr
	}
}

// workerUserID is the worker twin of adminUserID, derived from the
// organization because that is all the worker step's input names; the
// organization is itself unique per tenant.
func workerUserID(orgID string) string {
	return authn.ComputeUUID(orgID, "worker").String()
}

// CreateTenantWorkerDeploymentActivity asks worker-api to create the tenant's
// worker. No image is sent: pyckGo is a shared-image extension, so worker-api
// resolves the platform's image itself. The environment is read here rather
// than carried in the workflow input, which would pin a deploy-time value into
// replay history.
func (a *Activities) CreateTenantWorkerDeploymentActivity(ctx context.Context, input CreateTenantWorkerDeploymentInput) error {
	return classifyWorkerAPI(ctx, a.workerAPI.CreateDeployment(ctx, workerapi.CreateDeploymentInput{
		Name:              input.Name,
		TenantID:          input.TenantID.String(),
		TemporalNamespace: input.TemporalNamespace,
		Extension:         input.Extension,
		Environment:       core.Config.EnvironmentName,
	}))
}

// classifyWorkerAPI classifies a worker-api outcome. A client without a
// worker-api URL is a deployment mistake rather than a fault the dependency
// recovers from, so it ends the run on the first attempt; every other
// outcome follows the shared HTTP and transport classification, under which
// a refusal (4xx) is permanent and a fault (5xx, connection error) is
// retried until the activity's budget is spent.
func classifyWorkerAPI(ctx context.Context, err error) error {
	if errors.Is(err, workerapi.ErrNotConfigured) {
		return sdktemporal.NewNonRetryableApplicationError(err.Error(), appErrTypeWorkerAPI, err)
	}

	return commonwf.Classify(ctx, appErrTypeWorkerAPI, err)
}
