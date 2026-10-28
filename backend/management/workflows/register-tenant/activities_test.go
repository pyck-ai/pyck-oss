package registertenant_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	authzv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	permv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/message"
	objectv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	orgv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/feature"
	jsonschema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"
	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
	"github.com/pyck-ai/pyck/backend/management/exec"
	"github.com/pyck-ai/pyck/backend/management/workerapi"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// assertRetryable states what Temporal will do with err. The failure
// converter, not errors.As, decides permanence on the wire: a non-retryable
// verdict reachable only through a wrapping error is converted to a
// retryable "wrapError" failure, which errors.As still matches. Asserting
// through the converter is therefore the only assertion that describes the
// server's behaviour.
func assertRetryable(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	info := temporal.GetDefaultFailureConverter().ErrorToFailure(err).GetApplicationFailureInfo()
	if info != nil {
		assert.False(t, info.GetNonRetryable(), "the step must keep retrying, got a permanent failure: %v", err)
	}
}

// testTenantData is the caller-supplied data a seeded pending row carries;
// the workflow reads the flavour from it.
var testTenantData = map[string]any{"flavour": "pyck-go"}

// testRegistration is the admin account a seeded pending row carries.
var testRegistration = &types.TenantRegistration{
	AdminUsername:  "admin",
	AdminEmail:     "admin@example.com",
	AdminFirstName: "Ada",
	AdminLastName:  "Admin",
	PasswordHash:   "$2a$12$hash",
}

// activityEnv is an in-memory Ent client with the production outbox hook
// installed, so the tests can assert the events an activity commits
// alongside its row change: the lifecycle trigger hangs off those
// events, and no event may carry the row's registration data.
type activityEnv struct {
	client *ent.Client
	a      *registertenant.Activities
	ctx    context.Context //nolint:containedctx // test fixture
}

func newActivityEnv(t *testing.T) *activityEnv {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        topic.ManagementService,
		StreamName:     events.DefaultStreamName,
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),

		SelfTenantSchemas: core.SelfTenantSchemas(),
	}))

	ctx := authn.Context(context.Background(), authn.SystemUser())
	ctx = feature.Context(ctx, feature.FEATURE_SHOW_DELETED)

	return &activityEnv{
		client: client,
		a:      registertenant.NewActivities(nil, client, nil, commonwf.NewNamespaceGetter("test-audience"), nil),
		ctx:    ctx,
	}
}

// seed inserts a tenant row in the given registration state (pending rows
// carry testRegistration) and clears the outbox, so each test asserts
// only the events of the activity under test. The insert runs in its own
// tx with a txid because the outbox hook refuses mutations without one.
func (e *activityEnv) seed(t *testing.T, status enttenant.Status, deleted bool) *ent.Tenant {
	t.Helper()
	id := uuid.New()
	seedCtx := txid.With(e.ctx, txid.New())
	tx, err := e.client.Tx(seedCtx)
	require.NoError(t, err)
	seedCtx = ent.NewTxContext(seedCtx, tx)

	q := tx.Tenant.Create().
		SetID(id).
		SetName("tenant-" + id.String()).
		SetIdpOrgRef("org-" + id.String()).
		SetStatus(status)
	if status == enttenant.StatusPending {
		q = q.SetRegistration(testRegistration).SetData(testTenantData)
	}
	if deleted {
		q = q.SetDeletedAt(time.Now().UTC()).SetDeletedBy(uuid.Max)
	}
	row, err := q.Save(seedCtx)
	require.NoError(t, err)
	_, err = tx.EntityEventsOutbox.Delete().Exec(seedCtx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	return row
}

func (e *activityEnv) get(t *testing.T, id uuid.UUID) *ent.Tenant {
	t.Helper()
	row, err := e.client.Tenant.Get(e.ctx, id)
	require.NoError(t, err)
	return row
}

// outboxTopics returns the topics of every outbox row, oldest first.
func (e *activityEnv) outboxTopics(t *testing.T) []string {
	t.Helper()
	rows, err := e.client.EntityEventsOutbox.Query().
		Order(ent.Asc(entityeventsoutbox.FieldCreatedAt)).
		All(e.ctx)
	require.NoError(t, err)
	topics := make([]string, 0, len(rows))
	for _, r := range rows {
		topics = append(topics, r.Topic)
	}
	return topics
}

// outboxPayloads returns the JSON of every outbox row's payload.
func (e *activityEnv) outboxPayloads(t *testing.T) []string {
	t.Helper()
	rows, err := e.client.EntityEventsOutbox.Query().All(e.ctx)
	require.NoError(t, err)
	payloads := make([]string, 0, len(rows))
	for _, r := range rows {
		b, err := json.Marshal(r.Payload)
		require.NoError(t, err)
		payloads = append(payloads, string(b))
	}
	return payloads
}

// outboxPayload returns the decoded payload of the one outbox row the
// activity under test committed.
func (e *activityEnv) outboxPayload(t *testing.T) map[string]any {
	t.Helper()
	rows, err := e.client.EntityEventsOutbox.Query().All(e.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "exactly one outbox row")
	return rows[0].Payload
}

// eventStatus reads payload[key].status the way a consumer does. key is
// data_before or data_after; both must decode to the row itself. A bulk
// Update().Where() would put the affected-row count in data_after
// instead, which every consumer drops without acting on the event.
func eventStatus(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	data, ok := payload[key].(map[string]any)
	require.True(t, ok, "%s must decode to the tenant row, got %T (%v)", key, payload[key], payload[key])
	status, ok := data["status"].(string)
	require.True(t, ok, "%s.status must be a string, got %T", key, data["status"])
	return status
}

// assertSingleEvent states that the activity published exactly the
// tenant's own event, topic spelled out in full. The tenant segment is
// the mutation tenant of the activity's request context (Tenant has no
// tenant_id column); without it the hook falls back to the wildcard "*",
// which the outbox publisher rejects outright.
func assertSingleEvent(t *testing.T, topics []string, id uuid.UUID, op string) {
	t.Helper()
	require.Len(t, topics, 1, "exactly one outbox row, got %v", topics)
	assert.Equal(t,
		fmt.Sprintf("%s.%s.crud.%s.%s.%s.%s",
			events.DefaultStreamName, id, topic.ManagementService, topic.TenantSchema, id, op),
		topics[0])
}

// =============================================================================
// Zitadel v2 client fakes
// =============================================================================
//
// Each fake implements the generated gRPC client interface by embedding
// it nil and overriding only the methods the activities drive: a call the
// test did not expect panics loudly. What the fakes capture are the
// request protobufs the activities put on the wire, which is the contract
// a live Zitadel enforces.

// fakeUserService fakes userv2.UserServiceClient for the two user imports
// and the worker's token.
type fakeUserService struct {
	userv2.UserServiceClient

	createErr error
	// lookupUser answers GetUserByID; nil answers NotFound, the way the
	// user projection answers before it has caught up with the create.
	lookupUser *userv2.User
	lookupErr  error
	// listUsers answers the instance-wide username-holder lookup; empty is
	// a projection that has not caught up with the holder yet.
	listUsers []*userv2.User
	listErr   error
	patErr    error
	// patToken is the token the PAT mint answers with; empty means the
	// default. A test that asserts the credential never reaches workflow
	// history sets a distinctive value so the assertion cannot pass on a
	// string that happens not to appear.
	patToken string

	capturedCreates []*userv2.CreateUserRequest
	lookedUp        []string
	capturedList    *userv2.ListUsersRequest
	capturedPATs    []*userv2.AddPersonalAccessTokenRequest
}

func (f *fakeUserService) CreateUser(_ context.Context, in *userv2.CreateUserRequest, _ ...grpc.CallOption) (*userv2.CreateUserResponse, error) {
	f.capturedCreates = append(f.capturedCreates, in)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &userv2.CreateUserResponse{Id: in.GetUserId()}, nil
}

func (f *fakeUserService) GetUserByID(_ context.Context, in *userv2.GetUserByIDRequest, _ ...grpc.CallOption) (*userv2.GetUserByIDResponse, error) {
	f.lookedUp = append(f.lookedUp, in.GetUserId())
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	if f.lookupUser == nil {
		return nil, status.Error(codes.NotFound, "Errors.User.NotFound")
	}
	return &userv2.GetUserByIDResponse{User: f.lookupUser}, nil
}

func (f *fakeUserService) ListUsers(_ context.Context, in *userv2.ListUsersRequest, _ ...grpc.CallOption) (*userv2.ListUsersResponse, error) {
	f.capturedList = in
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &userv2.ListUsersResponse{Result: f.listUsers}, nil
}

func (f *fakeUserService) AddPersonalAccessToken(_ context.Context, in *userv2.AddPersonalAccessTokenRequest, _ ...grpc.CallOption) (*userv2.AddPersonalAccessTokenResponse, error) {
	f.capturedPATs = append(f.capturedPATs, in)
	if f.patErr != nil {
		return nil, f.patErr
	}
	token := f.patToken
	if token == "" {
		token = "token"
	}
	return &userv2.AddPersonalAccessTokenResponse{TokenId: "pat-1", Token: token}, nil
}

// fakeOrgService fakes orgv2.OrganizationServiceClient for the org create,
// its conflict probes, and the metadata push.
type fakeOrgService struct {
	orgv2.OrganizationServiceClient

	addErr error
	// addResponseID is the ID the server "creates" the org under; empty
	// echoes the requested one (a pre-v4.10 Zitadel mints its own).
	addResponseID string
	deleteErr     error
	listErr       error
	// listByIDResult answers the conflict probe's lookup by ID,
	// listByNameResult the one by name.
	listByIDResult   []*orgv2.Organization
	listByNameResult []*orgv2.Organization
	metadataErr      error
	deactivateErr    error

	capturedAdd       *orgv2.AddOrganizationRequest
	capturedDelete    *orgv2.DeleteOrganizationRequest
	capturedIDQuery   string
	capturedNameQuery *orgv2.OrganizationNameQuery
	capturedMetadata  *orgv2.SetOrganizationMetadataRequest
	metadataCalls     int
	deactivated       []string
}

func (f *fakeOrgService) DeactivateOrganization(_ context.Context, in *orgv2.DeactivateOrganizationRequest, _ ...grpc.CallOption) (*orgv2.DeactivateOrganizationResponse, error) {
	f.deactivated = append(f.deactivated, in.GetOrganizationId())
	if f.deactivateErr != nil {
		return nil, f.deactivateErr
	}
	return &orgv2.DeactivateOrganizationResponse{}, nil
}

func (f *fakeOrgService) AddOrganization(_ context.Context, in *orgv2.AddOrganizationRequest, _ ...grpc.CallOption) (*orgv2.AddOrganizationResponse, error) {
	f.capturedAdd = in
	if f.addErr != nil {
		return nil, f.addErr
	}
	id := f.addResponseID
	if id == "" {
		id = in.GetOrganizationId()
	}
	return &orgv2.AddOrganizationResponse{OrganizationId: id}, nil
}

func (f *fakeOrgService) DeleteOrganization(_ context.Context, in *orgv2.DeleteOrganizationRequest, _ ...grpc.CallOption) (*orgv2.DeleteOrganizationResponse, error) {
	f.capturedDelete = in
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &orgv2.DeleteOrganizationResponse{}, nil
}

func (f *fakeOrgService) ListOrganizations(_ context.Context, in *orgv2.ListOrganizationsRequest, _ ...grpc.CallOption) (*orgv2.ListOrganizationsResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if nameQuery := in.GetQueries()[0].GetNameQuery(); nameQuery != nil {
		f.capturedNameQuery = nameQuery
		return &orgv2.ListOrganizationsResponse{Result: f.listByNameResult}, nil
	}
	f.capturedIDQuery = in.GetQueries()[0].GetIdQuery().GetId()
	return &orgv2.ListOrganizationsResponse{Result: f.listByIDResult}, nil
}

func (f *fakeOrgService) SetOrganizationMetadata(_ context.Context, in *orgv2.SetOrganizationMetadataRequest, _ ...grpc.CallOption) (*orgv2.SetOrganizationMetadataResponse, error) {
	f.metadataCalls++
	if f.metadataErr != nil {
		return nil, f.metadataErr
	}
	f.capturedMetadata = in
	return &orgv2.SetOrganizationMetadataResponse{}, nil
}

// fakeProjectService fakes projectv2.ProjectServiceClient for the project
// grant: its creation, and the menu read and replace of the widening an
// AlreadyExists answer leads to. existing is the grant the projection
// reports (nil: none yet).
type fakeProjectService struct {
	projectv2.ProjectServiceClient

	grantErr  error
	existing  *projectv2.ProjectGrant
	listErr   error
	updateErr error

	capturedGrants  []*projectv2.CreateProjectGrantRequest
	capturedLists   []*projectv2.ListProjectGrantsRequest
	capturedUpdates []*projectv2.UpdateProjectGrantRequest
}

func (f *fakeProjectService) CreateProjectGrant(_ context.Context, in *projectv2.CreateProjectGrantRequest, _ ...grpc.CallOption) (*projectv2.CreateProjectGrantResponse, error) {
	f.capturedGrants = append(f.capturedGrants, in)
	if f.grantErr != nil {
		return nil, f.grantErr
	}
	return &projectv2.CreateProjectGrantResponse{}, nil
}

func (f *fakeProjectService) ListProjectGrants(_ context.Context, in *projectv2.ListProjectGrantsRequest, _ ...grpc.CallOption) (*projectv2.ListProjectGrantsResponse, error) {
	f.capturedLists = append(f.capturedLists, in)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.existing == nil {
		return &projectv2.ListProjectGrantsResponse{}, nil
	}
	return &projectv2.ListProjectGrantsResponse{ProjectGrants: []*projectv2.ProjectGrant{f.existing}}, nil
}

func (f *fakeProjectService) UpdateProjectGrant(_ context.Context, in *projectv2.UpdateProjectGrantRequest, _ ...grpc.CallOption) (*projectv2.UpdateProjectGrantResponse, error) {
	f.capturedUpdates = append(f.capturedUpdates, in)
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &projectv2.UpdateProjectGrantResponse{}, nil
}

// fakeAuthzService fakes authzv2.AuthorizationServiceClient for the user
// grants: their creation, and the read and replace of the widening an
// AlreadyExists answer leads to. existing is the authorization the
// projection reports (nil: none yet).
type fakeAuthzService struct {
	authzv2.AuthorizationServiceClient

	createErr error
	existing  *authzv2.Authorization
	listErr   error
	updateErr error

	captured        []*authzv2.CreateAuthorizationRequest
	capturedLists   []*authzv2.ListAuthorizationsRequest
	capturedUpdates []*authzv2.UpdateAuthorizationRequest
}

func (f *fakeAuthzService) CreateAuthorization(_ context.Context, in *authzv2.CreateAuthorizationRequest, _ ...grpc.CallOption) (*authzv2.CreateAuthorizationResponse, error) {
	f.captured = append(f.captured, in)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &authzv2.CreateAuthorizationResponse{}, nil
}

func (f *fakeAuthzService) ListAuthorizations(_ context.Context, in *authzv2.ListAuthorizationsRequest, _ ...grpc.CallOption) (*authzv2.ListAuthorizationsResponse, error) {
	f.capturedLists = append(f.capturedLists, in)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.existing == nil {
		return &authzv2.ListAuthorizationsResponse{}, nil
	}
	return &authzv2.ListAuthorizationsResponse{Authorizations: []*authzv2.Authorization{f.existing}}, nil
}

func (f *fakeAuthzService) UpdateAuthorization(_ context.Context, in *authzv2.UpdateAuthorizationRequest, _ ...grpc.CallOption) (*authzv2.UpdateAuthorizationResponse, error) {
	f.capturedUpdates = append(f.capturedUpdates, in)
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &authzv2.UpdateAuthorizationResponse{}, nil
}

// authorizationWithRoles is the authorization the projection reports for
// an existing user grant carrying the given role keys.
func authorizationWithRoles(id string, keys ...string) *authzv2.Authorization {
	roles := make([]*authzv2.Role, 0, len(keys))
	for _, key := range keys {
		roles = append(roles, &authzv2.Role{Key: key})
	}
	return &authzv2.Authorization{Id: id, Roles: roles}
}

// fakePermService fakes permv2.InternalPermissionServiceClient for the
// owner swap.
type fakePermService struct {
	permv2.InternalPermissionServiceClient

	admins    []*permv2.Administrator
	listErr   error
	deleteErr error
	createErr error

	capturedList   *permv2.ListAdministratorsRequest
	deleted        []string
	capturedCreate *permv2.CreateAdministratorRequest
}

func (f *fakePermService) ListAdministrators(_ context.Context, in *permv2.ListAdministratorsRequest, _ ...grpc.CallOption) (*permv2.ListAdministratorsResponse, error) {
	f.capturedList = in
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &permv2.ListAdministratorsResponse{Administrators: f.admins}, nil
}

func (f *fakePermService) DeleteAdministrator(_ context.Context, in *permv2.DeleteAdministratorRequest, _ ...grpc.CallOption) (*permv2.DeleteAdministratorResponse, error) {
	f.deleted = append(f.deleted, in.GetUserId())
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &permv2.DeleteAdministratorResponse{}, nil
}

func (f *fakePermService) CreateAdministrator(_ context.Context, in *permv2.CreateAdministratorRequest, _ ...grpc.CallOption) (*permv2.CreateAdministratorResponse, error) {
	f.capturedCreate = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &permv2.CreateAdministratorResponse{}, nil
}

// fakeZitadel bundles the five service fakes behind the activities' dial
// seam; closeCalls counts the bundle's releases, one per activity attempt.
type fakeZitadel struct {
	users    *fakeUserService
	orgs     *fakeOrgService
	projects *fakeProjectService
	authz    *fakeAuthzService
	perms    *fakePermService

	dialErr    error
	closeCalls int
}

func newFakeZitadel() *fakeZitadel {
	return &fakeZitadel{
		users:    &fakeUserService{},
		orgs:     &fakeOrgService{},
		projects: &fakeProjectService{},
		authz:    &fakeAuthzService{},
		perms:    &fakePermService{},
	}
}

func (f *fakeZitadel) install(a *registertenant.Activities) {
	a.SetZitadelDialer(func(context.Context) (*registertenant.ZitadelClients, error) {
		if f.dialErr != nil {
			return nil, f.dialErr
		}
		return registertenant.NewZitadelClientsForTest(f.users, f.orgs, f.projects, f.authz, f.perms, &f.closeCalls), nil
	})
}

// newZitadelActivities builds activities with only the Zitadel seam wired.
func newZitadelActivities(f *fakeZitadel) *registertenant.Activities {
	a := registertenant.NewActivities(nil, nil, nil, commonwf.NewNamespaceGetter("test-audience"), nil)
	f.install(a)
	return a
}

// withZitadel wires the fake bundle behind the env's activities, for the
// steps that also read the tenant row.
func (e *activityEnv) withZitadel(f *fakeZitadel) {
	f.install(e.a)
}

// =============================================================================
// LoadTenantRegistrationActivity
// =============================================================================

// The workflow drives provisioning from this output, and it is persisted
// in history: the row's admin account and password hash must not be in it.
func TestLoadTenantRegistrationActivity_ReturnsRowWithoutRegistration(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)

	out, err := e.a.LoadTenantRegistrationActivity(e.ctx, registertenant.LoadTenantRegistrationActivityInput{TenantID: row.ID})
	require.NoError(t, err)

	assert.Equal(t, enttenant.StatusPending, out.Status)
	assert.Equal(t, row.IdpOrgRef, out.OrganizationID)
	assert.Equal(t, row.Name, out.Name)
	assert.Equal(t, testTenantData, out.Data, "the flavour gate and org metadata read the row's data")
	recorded := fmt.Sprintf("%+v", *out)
	assert.NotContains(t, recorded, testRegistration.PasswordHash, "the hash must not enter workflow history")
	assert.NotContains(t, recorded, testRegistration.AdminUsername)
	assert.Empty(t, e.outboxTopics(t), "a read emits nothing")
}

// A row that is gone or soft-deleted reads as "not pending": the workflow
// has nothing to provision, and no retry can change that.
func TestLoadTenantRegistrationActivity_MissingOrDeletedRowHasNoStatus(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	deleted := e.seed(t, enttenant.StatusPending, true)

	// The activity runs without FEATURE_SHOW_DELETED in production; the
	// env's ctx carries it for the assertions, so the call gets a bare one.
	for name, id := range map[string]uuid.UUID{"missing": uuid.New(), "soft-deleted": deleted.ID} {
		out, err := e.a.LoadTenantRegistrationActivity(t.Context(), registertenant.LoadTenantRegistrationActivityInput{TenantID: id})
		require.NoError(t, err, name)
		assert.Empty(t, out.Status, name)
	}
}

// =============================================================================
// SetTenantStatusActivity
// =============================================================================

// The flip to done is the last provisioning step: the update event it
// emits is what tells every consumer the org now exists, and the
// registration data (hash included) has no further use.
func TestSetTenantStatusActivity_DoneClearsRegistrationAndEmitsUpdate(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)

	err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{TenantID: row.ID, Status: enttenant.StatusDone})
	require.NoError(t, err)

	got := e.get(t, row.ID)
	assert.Equal(t, enttenant.StatusDone, got.Status)
	assert.Nil(t, got.Registration, "done must clear the registration data")
	assertSingleEvent(t, e.outboxTopics(t), row.ID, events.OpUpdate)
	for _, payload := range e.outboxPayloads(t) {
		assert.NotContains(t, payload, testRegistration.PasswordHash, "the hash must not reach the outbox")
		assert.NotContains(t, payload, "registration")
	}

	// The consumers read the transition off the payload; data_after must
	// carry the flipped row, not the affected-row count a bulk update
	// would hand the hook.
	payload := e.outboxPayload(t)
	assert.Equal(t, row.ID.String(), payload["tenant_id"], "the event must be addressed to the tenant it flipped")
	assert.Equal(t, enttenant.StatusPending.String(), eventStatus(t, payload, "data_before"))
	assert.Equal(t, enttenant.StatusDone.String(), eventStatus(t, payload, "data_after"))
}

// A failed registration keeps its registration data so a re-run from the
// row can still create the admin user.
func TestSetTenantStatusActivity_ErrorKeepsRegistration(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)

	err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{TenantID: row.ID, Status: enttenant.StatusError})
	require.NoError(t, err)

	got := e.get(t, row.ID)
	assert.Equal(t, enttenant.StatusError, got.Status)
	assert.Equal(t, testRegistration, got.Registration)
	assertSingleEvent(t, e.outboxTopics(t), row.ID, events.OpUpdate)

	payload := e.outboxPayload(t)
	assert.Equal(t, row.ID.String(), payload["tenant_id"], "the event must be addressed to the tenant it flipped")
	assert.Equal(t, enttenant.StatusPending.String(), eventStatus(t, payload, "data_before"))
	assert.Equal(t, enttenant.StatusError.String(), eventStatus(t, payload, "data_after"))
}

// A retry after a committed flip must succeed without writing again: a
// second same-value UPDATE would bump updated_at and fan out a spurious
// event.
func TestSetTenantStatusActivity_RetryAfterFlipIsNoOp(t *testing.T) {
	t.Parallel()
	for _, target := range []enttenant.Status{enttenant.StatusDone, enttenant.StatusError} {
		t.Run(target.String(), func(t *testing.T) {
			t.Parallel()
			e := newActivityEnv(t)
			row := e.seed(t, target, false)
			before := e.get(t, row.ID)

			err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{TenantID: row.ID, Status: target})
			require.NoError(t, err)

			after := e.get(t, row.ID)
			assert.Equal(t, target, after.Status)
			assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "no write on a retry")
			assert.Empty(t, e.outboxTopics(t), "no event on a retry")
		})
	}
}

// A row already closed with the other terminal status is not this run's
// to flip: done must not become error or vice versa.
func TestSetTenantStatusActivity_OtherTerminalStatusIsNonRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusDone, false)

	err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{TenantID: row.ID, Status: enttenant.StatusError})

	require.ErrorIs(t, err, registertenant.ErrTenantNotPending)
	assert.Equal(t, enttenant.StatusDone, e.get(t, row.ID).Status)
	assert.Empty(t, e.outboxTopics(t))
}

// No row means the input is wrong or the row was hard-deleted; reporting
// success would let the workflow return a tenant that is gone.
func TestSetTenantStatusActivity_MissingRowIsNonRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)

	err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{TenantID: uuid.New(), Status: enttenant.StatusDone})

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr, "must be a Temporal application error")
	assert.Equal(t, "tenant-row-missing", appErr.Type())
	assert.True(t, appErr.NonRetryable(), "retrying cannot make the row appear")
	assert.Empty(t, e.outboxTopics(t))
}

// The restated `status = pending` predicate is what keeps a row that was
// soft-deleted in the meantime (operator delete) from being flipped to
// done and thereby re-entering sync/expiry/lifecycle dispatch.
func TestSetTenantStatusActivity_SoftDeletedPendingRowIsNotFlipped(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, true)

	err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{TenantID: row.ID, Status: enttenant.StatusDone})

	require.ErrorIs(t, err, registertenant.ErrTenantRetired, "a deleted row is reported as retired, which the run answers by retiring the org")
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, "tenant-row-missing", appErr.Type())
	assert.True(t, appErr.NonRetryable())
	got := e.get(t, row.ID)
	assert.Equal(t, enttenant.StatusPending, got.Status, "a deleted row must stay pending")
	assert.False(t, got.DeletedAt.IsZero(), "deleted_at must be untouched")
	assert.Empty(t, e.outboxTopics(t))
}

// Production runs without FEATURE_SHOW_DELETED, so a row soft-deleted
// between the row load and the flip is invisible to the outbox hook's
// before-state fetch: the not-found arrives wrapped from the hook rather
// than raw from the UPDATE. Both origins must land on the same refusal,
// or an operator-deleted tenant is flipped to done and re-enters
// sync/expiry/lifecycle dispatch.
func TestSetTenantStatusActivity_SoftDeletedRowIsRefusedWithoutShowDeleted(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, true)

	err := e.a.SetTenantStatusActivity(t.Context(), registertenant.SetTenantStatusActivityInput{TenantID: row.ID, Status: enttenant.StatusDone})

	require.ErrorIs(t, err, registertenant.ErrTenantRetired, "a deleted row is reported as retired, which the run answers by retiring the org")
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, "tenant-row-missing", appErr.Type())
	assert.True(t, appErr.NonRetryable())
	got := e.get(t, row.ID)
	assert.Equal(t, enttenant.StatusPending, got.Status, "a deleted row must stay pending")
	assert.False(t, got.DeletedAt.IsZero(), "deleted_at must be untouched")
	assert.Empty(t, e.outboxTopics(t))
}

// A write that fails for any reason other than "no pending row matched"
// must surface as itself. Folding it into ErrTenantNotPending would tell
// the workflow the row is already closed and end the run permanently on
// what may be a transient fault.
func TestSetTenantStatusActivity_WriteErrorIsNotReportedAsNotPending(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)

	err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{TenantID: row.ID, Status: enttenant.Status("bogus")})

	require.Error(t, err)
	require.NotErrorIs(t, err, registertenant.ErrTenantNotPending, "a rejected write is not a closed row")
	assert.Equal(t, enttenant.StatusPending, e.get(t, row.ID).Status, "the row must be left for a retry")
	assert.Empty(t, e.outboxTopics(t))
}

// =============================================================================
// DeactivateRetiredOrganizationActivity
// =============================================================================

// A row deleteTenant retired mid-run gets its organization deactivated by
// the run; an organization already inactive or never created is the goal
// already met. The activity sees the deleted row on its own, without the
// caller's FEATURE_SHOW_DELETED.
func TestDeactivateRetiredOrganizationActivity_DeactivatesTheRetiredRowsOrg(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]error{
		"deactivated":      nil,
		"already inactive": status.Error(codes.FailedPrecondition, "Errors.Org.AlreadyDeactivated"),
		"never created":    status.Error(codes.NotFound, "Errors.Org.NotFound"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newActivityEnv(t)
			f := newFakeZitadel()
			f.orgs.deactivateErr = answer
			e.withZitadel(f)
			row := e.seed(t, enttenant.StatusPending, true)

			err := e.a.DeactivateRetiredOrganizationActivity(t.Context(), registertenant.DeactivateRetiredOrganizationActivityInput{TenantID: row.ID})

			require.NoError(t, err)
			assert.Equal(t, []string{row.IdpOrgRef}, f.orgs.deactivated)
			assert.Equal(t, 1, f.closeCalls, "the connection is released")
		})
	}
}

// A live row is never retired: a misread must not take a live tenant's
// organization down, and no retry changes the row.
func TestDeactivateRetiredOrganizationActivity_RefusesALiveRow(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	f := newFakeZitadel()
	e.withZitadel(f)
	row := e.seed(t, enttenant.StatusPending, false)

	err := e.a.DeactivateRetiredOrganizationActivity(t.Context(), registertenant.DeactivateRetiredOrganizationActivityInput{TenantID: row.ID})

	assertStepPermanent(t, err, "tenant-not-retired")
	assert.Empty(t, f.orgs.deactivated, "a live tenant's org is left alone")
}

// A row that was never there names no organization.
func TestDeactivateRetiredOrganizationActivity_MissingRowIsPermanent(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	f := newFakeZitadel()
	e.withZitadel(f)

	err := e.a.DeactivateRetiredOrganizationActivity(t.Context(), registertenant.DeactivateRetiredOrganizationActivityInput{TenantID: uuid.New()})

	assertStepPermanent(t, err, "tenant-row-missing")
	assert.Empty(t, f.orgs.deactivated)
}

// Zitadel being unreachable is an outage the retry policy waits out, not
// a refusal.
func TestDeactivateRetiredOrganizationActivity_OutageIsRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	f := newFakeZitadel()
	f.orgs.deactivateErr = status.Error(codes.Unavailable, "zitadel is restarting")
	e.withZitadel(f)
	row := e.seed(t, enttenant.StatusPending, true)

	err := e.a.DeactivateRetiredOrganizationActivity(t.Context(), registertenant.DeactivateRetiredOrganizationActivityInput{TenantID: row.ID})

	assertStepRetryable(t, err)
}

// =============================================================================
// CreateTenantActivity (Zitadel org)
// =============================================================================

// The org is created under the pre-chosen ID, and the output carries the
// namespace derived from the tenant ID the row already holds.
func TestCreateTenantActivity_CreatesOrgUnderPreChosenIDs(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	a := newZitadelActivities(f)
	nsGetter := commonwf.NewNamespaceGetter("test-audience")
	tenantID := uuid.New()

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: tenantID,
	})
	require.NoError(t, err)

	assert.Equal(t, "org-1", out.OrganizationID)
	assert.Equal(t, tenantID, out.TenantID)
	assert.Equal(t, nsGetter.GetNamespace(tenantID), out.TemporalNamespace)
	require.NotNil(t, f.orgs.capturedAdd)
	assert.Equal(t, "Acme", f.orgs.capturedAdd.GetName())
	assert.Equal(t, "org-1", f.orgs.capturedAdd.GetOrganizationId(), "the request must carry the row's pre-chosen ID")
	assert.Empty(t, f.orgs.capturedIDQuery, "no lookup on a clean create")
	assert.Equal(t, 1, f.closeCalls, "the dialled client must be released")
}

// A retried activity hits AlreadyExists for its own org; when an org with
// the requested ID exists, that is the idempotent success path.
func TestCreateTenantActivity_AlreadyExistsWithOwnIDIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.AlreadyExists, "Errors.Org.AlreadyExists")
	f.orgs.listByIDResult = []*orgv2.Organization{{Id: "org-1", Name: "Acme"}}
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})
	require.NoError(t, err)

	assert.Equal(t, "org-1", out.OrganizationID)
	assert.Equal(t, "org-1", f.orgs.capturedIDQuery, "AlreadyExists must be verified against the ID, not trusted")
	assert.Equal(t, 1, f.closeCalls)
}

// An AlreadyExists that survives the same-ID lookup means the NAME is held
// by another org; retrying cannot change that, and a retryable error would
// burn the activity's whole retry budget before the row is marked error.
func TestCreateTenantActivity_NameTakenIsNonRetryable(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.AlreadyExists, "Errors.Org.AlreadyExists")
	f.orgs.listByNameResult = []*orgv2.Organization{{Id: "375900000000000001", Name: "Acme"}}
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	require.ErrorContains(t, err, "Acme", "the caller must learn which name was refused so it can pick another")
	assertStepPermanent(t, err, "organization-name-taken")

	// The holding organization is operator-only. registerTenant is a public,
	// unauthenticated sign-up endpoint and this message travels back to the
	// caller verbatim, so naming the organization would turn a refused
	// registration into an instance-wide name-to-tenant oracle: tenant IDs are
	// ComputeUUID(audience, orgID) over a public audience, so the org ID is the
	// tenant ID. Asserted on the converted failure as well as the Go error,
	// because the converted message is what actually crosses the wire.
	const holderOrg = "375900000000000001"
	require.NotContains(t, err.Error(), holderOrg,
		"the holding organization must not reach the caller")
	wireMsg := temporal.GetDefaultFailureConverter().ErrorToFailure(err).GetMessage()
	require.NotContains(t, wireMsg, holderOrg,
		"the holding organization must not cross the wire in the failure message")
	require.NotNil(t, f.orgs.capturedNameQuery, "the name has to be probed before the conflict is called settled")
	assert.Equal(t, "Acme", f.orgs.capturedNameQuery.GetName())
	assert.Equal(t, objectv2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE, f.orgs.capturedNameQuery.GetMethod(),
		"Zitadel enforces name uniqueness ignoring case, so the probe must too")
	assert.Equal(t, 1, f.closeCalls)
}

// The lag window: AddOrganization refuses from the eventstore while both
// lookups read the organization projection, so an org this caller created
// a moment ago is invisible to them. Reporting a taken name here would
// fail a healthy registration permanently, so the conflict stays
// unresolved and the step retries.
func TestCreateTenantActivity_UnresolvedOwnerStaysRetryable(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.AlreadyExists, "Errors.Org.AlreadyExists")
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	require.ErrorIs(t, err, registertenant.ErrOrganizationOwnerUnresolved)
	require.NotErrorIs(t, err, registertenant.ErrOrganizationNameTaken, "an invisible org is not proof the name is taken")
	assertRetryable(t, err)
	assert.Equal(t, 1, f.closeCalls)
}

// The projection can catch up between the two lookups: the name then
// resolves to the organization this registration asked for, which is the
// same idempotent success as finding it by ID.
func TestCreateTenantActivity_NameResolvingToOwnOrgIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.AlreadyExists, "Errors.Org.AlreadyExists")
	f.orgs.listByNameResult = []*orgv2.Organization{{Id: "org-1", Name: "Acme"}}
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})
	require.NoError(t, err)
	assert.Equal(t, "org-1", out.OrganizationID)
}

// A removed organization still answers the name query; it holds no name,
// so treating it as the owner would refuse a name that is in fact free.
func TestCreateTenantActivity_RemovedOrgDoesNotHoldTheName(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.AlreadyExists, "Errors.Org.AlreadyExists")
	f.orgs.listByNameResult = []*orgv2.Organization{
		{Id: "375900000000000001", Name: "Acme", State: orgv2.OrganizationState_ORGANIZATION_STATE_REMOVED},
	}
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	require.ErrorIs(t, err, registertenant.ErrOrganizationOwnerUnresolved)
	assertRetryable(t, err)
}

// A lookup that fails is an infrastructure fault, the third outcome: it is
// neither "ours" nor "theirs" and must not be reported as either.
func TestCreateTenantActivity_LookupFaultIsNotAVerdict(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.AlreadyExists, "Errors.Org.AlreadyExists")
	f.orgs.listErr = status.Error(codes.Unavailable, "zitadel restarting")
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	require.NotErrorIs(t, err, registertenant.ErrOrganizationNameTaken)
	require.NotErrorIs(t, err, registertenant.ErrOrganizationOwnerUnresolved)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assertRetryable(t, err)
}

// A Zitadel that mints its own IDs instead of honouring the requested one
// would silently create an org whose tokens map to a tenant that does not
// exist: a deployment mismatch every retry would reproduce. The foreign
// org is removed again so its name does not block every later
// registration.
func TestCreateTenantActivity_IDMismatchDeletesForeignOrgAndIsNonRetryable(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addResponseID = "375900000000000001"
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	require.ErrorIs(t, err, registertenant.ErrOrganizationIDMismatch)
	assertStepPermanent(t, err, "organization-id-mismatch")
	require.NotNil(t, f.orgs.capturedDelete, "the org under the foreign ID must be deleted")
	assert.Equal(t, "375900000000000001", f.orgs.capturedDelete.GetOrganizationId())
	assert.Equal(t, 1, f.closeCalls)
}

// When the foreign org cannot be removed the operator needs both facts:
// the mismatch and the org that is now stranded in Zitadel.
func TestCreateTenantActivity_IDMismatchSurfacesFailedCleanup(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addResponseID = "375900000000000001"
	f.orgs.deleteErr = status.Error(codes.Unavailable, "zitadel restarting")
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	require.ErrorIs(t, err, registertenant.ErrOrganizationIDMismatch)
	require.ErrorContains(t, err, `delete organization "375900000000000001"`)
	assertStepPermanent(t, err, "organization-id-mismatch")
}

// A name Zitadel rejects is a deterministic answer: retrying would only
// spend the activity's budget before the row is marked error.
func TestCreateTenantActivity_InvalidNameIsNonRetryable(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.InvalidArgument, "Errors.Org.Invalid")
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	assertStepPermanent(t, err, "organization-invalid")
	assert.Equal(t, 1, f.closeCalls)
}

// Transport and server failures are passed through unchanged so the
// activity's retry policy gets to retry them.
func TestCreateTenantActivity_TransientErrorStaysRetryable(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.orgs.addErr = status.Error(codes.Unavailable, "zitadel restarting")
	a := newZitadelActivities(f)

	out, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
		Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
	})

	assert.Nil(t, out)
	assert.Equal(t, codes.Unavailable, status.Code(err), "the original gRPC status must reach the retry policy")
	var appErr *temporal.ApplicationError
	assert.NotErrorAs(t, err, &appErr, "a transient failure must not be classified as non-retryable")
	assert.Equal(t, 1, f.closeCalls)
}

// =============================================================================
// SetUserAsOrganizationOwnerActivity / AddProjectGrantActivity (idempotent)
// =============================================================================

// The admin is granted ORG_OWNER on exactly the organization's resource,
// and the administrators the run did not create are left alone: a tenant
// may hold several org admins, and removing them would strip an
// operator's access on a re-run of an existing registration.
func TestSetUserAsOrganizationOwnerActivity_AddsTheOwnerOnly(t *testing.T) {
	t.Parallel()
	f := newFakeZitadel()
	f.perms.admins = []*permv2.Administrator{
		{User: &permv2.User{Id: "bootstrap"}},
		{User: &permv2.User{Id: "admin-1"}},
	}
	a := newZitadelActivities(f)

	err := a.SetUserAsOrganizationOwnerActivity(t.Context(), registertenant.SetUserAsOrganizationOwnerActivityInput{
		OrganizationID: "org-1", UserID: "admin-1",
	})
	require.NoError(t, err)

	assert.Empty(t, f.perms.deleted, "no administrator of the organization is removed")
	require.NotNil(t, f.perms.capturedCreate)
	assert.Equal(t, "admin-1", f.perms.capturedCreate.GetUserId())
	assert.Equal(t, "org-1", f.perms.capturedCreate.GetResource().GetOrganizationId(),
		"ORG_OWNER must be granted on the organization resource")
	assert.Equal(t, []string{"ORG_OWNER"}, f.perms.capturedCreate.GetRoles())
	assert.Equal(t, 1, f.closeCalls)
}

// A retry (or an operator's re-run of a failed registration) finds the
// admin already an administrator: AlreadyExists is success, any other
// failure reaches the retry policy.
func TestSetUserAsOrganizationOwnerActivity_ExistingMembershipIsSuccess(t *testing.T) {
	t.Parallel()

	t.Run("already exists", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.perms.createErr = status.Error(codes.AlreadyExists, "ORG-poWwe")
		a := newZitadelActivities(f)

		err := a.SetUserAsOrganizationOwnerActivity(t.Context(), registertenant.SetUserAsOrganizationOwnerActivityInput{
			OrganizationID: "org-1", UserID: "admin-1",
		})

		require.NoError(t, err)
		assert.Equal(t, 1, f.closeCalls)
	})

	t.Run("other failure surfaces", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.perms.createErr = status.Error(codes.Unavailable, "zitadel restarting")
		a := newZitadelActivities(f)

		err := a.SetUserAsOrganizationOwnerActivity(t.Context(), registertenant.SetUserAsOrganizationOwnerActivityInput{
			OrganizationID: "org-1", UserID: "admin-1",
		})

		assert.Equal(t, codes.Unavailable, status.Code(err))
		assert.Equal(t, 1, f.closeCalls)
	})
}

// The project grant carries the granted organization and the whole role
// set; a grant that already exists (retry, operator re-run) is success
// outright.
func TestAddProjectGrantActivity_GrantsProjectAndToleratesExisting(t *testing.T) {
	t.Parallel()
	input := registertenant.AddProjectGrantActivityInput{ProjectID: "proj-1", OrganizationID: "org-1", Roles: []string{"admin"}}

	t.Run("new grant", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		a := newZitadelActivities(f)

		require.NoError(t, a.AddProjectGrantActivity(t.Context(), input))

		require.Len(t, f.projects.capturedGrants, 1)
		req := f.projects.capturedGrants[0]
		assert.Equal(t, "proj-1", req.GetProjectId())
		assert.Equal(t, "org-1", req.GetGrantedOrganizationId(), "the grant goes to the tenant's organization")
		assert.Equal(t, []string{"admin"}, req.GetRoleKeys())
		assert.Equal(t, 1, f.closeCalls)
	})

	t.Run("already exists with a menu carrying the roles is left untouched", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = status.Error(codes.AlreadyExists, "Errors.Project.Grant.AlreadyExists")
		f.projects.existing = &projectv2.ProjectGrant{GrantedOrganizationId: "org-1", GrantedRoleKeys: []string{"admin", "writer"}}
		a := newZitadelActivities(f)

		require.NoError(t, a.AddProjectGrantActivity(t.Context(), input))

		require.Len(t, f.projects.capturedLists, 1, "the existing menu is read once")
		assert.Empty(t, f.projects.capturedUpdates, "a menu that already carries the roles must not be rewritten")
		assert.Equal(t, 1, f.closeCalls)
	})

	// A re-run of a registration that a deploy adding a role interrupted:
	// the org's menu predates the role, and the user-grant step that
	// follows can only grant what the menu carries.
	t.Run("already exists with a stale menu is widened with the union", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = status.Error(codes.AlreadyExists, "Errors.Project.Grant.AlreadyExists")
		f.projects.existing = &projectv2.ProjectGrant{GrantedOrganizationId: "org-1", GrantedRoleKeys: []string{"reader", "writer"}}
		a := newZitadelActivities(f)

		require.NoError(t, a.AddProjectGrantActivity(t.Context(), input))

		require.Len(t, f.projects.capturedUpdates, 1)
		update := f.projects.capturedUpdates[0]
		assert.Equal(t, "proj-1", update.GetProjectId())
		assert.Equal(t, "org-1", update.GetGrantedOrganizationId())
		assert.Equal(t, []string{"reader", "writer", "admin"}, update.GetRoleKeys(),
			"the replace is whole-menu: the roles already granted must be re-sent or Zitadel revokes them")
		assert.Equal(t, 1, f.closeCalls)
	})

	// AlreadyExists comes from the eventstore, the menu read from a
	// projection that may not report the grant yet: that is lag, not a
	// missing grant, and the retry reads a fresher projection.
	t.Run("already exists but not yet projected stays retryable", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = status.Error(codes.AlreadyExists, "Errors.Project.Grant.AlreadyExists")
		a := newZitadelActivities(f)

		err := a.AddProjectGrantActivity(t.Context(), input)

		require.ErrorIs(t, err, sdk.ErrProjectGrantNotFound)
		assertStepRetryable(t, err)
		assert.Empty(t, f.projects.capturedUpdates)
	})

	t.Run("a refused widening ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = status.Error(codes.AlreadyExists, "Errors.Project.Grant.AlreadyExists")
		f.projects.existing = &projectv2.ProjectGrant{GrantedOrganizationId: "org-1", GrantedRoleKeys: []string{"writer"}}
		f.projects.updateErr = errRefusedByZitadel
		a := newZitadelActivities(f)

		err := a.AddProjectGrantActivity(t.Context(), input)

		assertStepPermanent(t, err, "project-grant-rejected")
		assert.Equal(t, 1, f.closeCalls)
	})

	t.Run("other failure surfaces", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = status.Error(codes.Unavailable, "zitadel restarting")
		a := newZitadelActivities(f)

		err := a.AddProjectGrantActivity(t.Context(), input)

		assert.Equal(t, codes.Unavailable, status.Code(err))
		assert.Equal(t, 1, f.closeCalls)
	})
}

// =============================================================================
// CreateZitadelUserActivity (admin user import)
// =============================================================================

// adminLookupUser is the user the projection answers for the tenant's own
// admin, owned by the row's org.
func adminLookupUser(row *ent.Tenant) *userv2.User {
	return &userv2.User{
		UserId:             registertenant.AdminUserID(row.ID),
		PreferredLoginName: "admin@" + row.IdpOrgRef,
		Details:            &objectv2.Details{ResourceOwner: row.IdpOrgRef},
	}
}

// The admin account (hash included) is read from the row inside the
// activity and imported into the row's org under the pre-chosen user ID:
// the input names only the tenant, so nothing secret is in history, and
// the import must be the hashed oneof (a clear-text import would store the
// hash as the password).
func TestCreateZitadelUserActivity_ImportsAdminFromRowWithHash(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	f.users.lookupUser = adminLookupUser(row)
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})
	require.NoError(t, err)

	require.Len(t, f.users.capturedCreates, 1)
	req := f.users.capturedCreates[0]
	assert.Equal(t, row.IdpOrgRef, req.GetOrganizationId(), "the user is created in the tenant's own org")
	assert.Equal(t, registertenant.AdminUserID(row.ID), req.GetUserId(),
		"the user ID is pre-chosen and deterministic, so a retry collides on it instead of on the username")
	assert.Equal(t, testRegistration.AdminUsername, req.GetUsername())
	human := req.GetHuman()
	require.NotNil(t, human, "the admin is a human user")
	assert.Equal(t, testRegistration.AdminFirstName, human.GetProfile().GetGivenName())
	assert.Equal(t, testRegistration.AdminLastName, human.GetProfile().GetFamilyName())
	assert.Equal(t, testRegistration.AdminEmail, human.GetEmail().GetEmail())
	assert.True(t, human.GetEmail().GetIsVerified(), "the e-mail is imported verified, no verification mail")
	assert.Equal(t, testRegistration.PasswordHash, human.GetHashedPassword().GetHash())
	assert.False(t, human.GetHashedPassword().GetChangeRequired())
	assert.Nil(t, human.GetPassword(), "the clear-text password oneof must stay empty")

	assert.Equal(t, &registertenant.CreateZitadelUserActivityOutput{
		UserID:    registertenant.AdminUserID(row.ID),
		LoginName: "admin@" + row.IdpOrgRef,
	}, out)
	assert.Equal(t, 1, f.closeCalls, "the dialled client must be released")
	assert.Empty(t, e.outboxTopics(t), "reading the row emits nothing")
}

// A retry (or an operator re-run) after the import already landed collides
// on the pre-chosen ID (FailedPrecondition from the eventstore) and the
// lookup by that ID resolves it to this run's own user instead of failing
// the registration for good.
func TestCreateZitadelUserActivity_RetryResolvesToOwnUserOnIDCollision(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	f.users.createErr = status.Error(codes.FailedPrecondition, "COMMAND-7yiox1isql Errors.User.AlreadyExisting")
	f.users.lookupUser = adminLookupUser(row)
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})
	require.NoError(t, err)

	assert.Equal(t, registertenant.AdminUserID(row.ID), out.UserID)
	assert.Equal(t, "admin@"+row.IdpOrgRef, out.LoginName)
	assert.Equal(t, []string{registertenant.AdminUserID(row.ID)}, f.users.lookedUp, "looked up by the pre-chosen ID only")
	assert.Equal(t, 1, f.closeCalls)
}

// AlreadyExists is a username collision with a different user BY ID (the
// pre-chosen ID collides as FailedPrecondition) but not necessarily a
// different identity: a pre-migration run imported this same admin under a
// Zitadel-minted ID. The instance-wide holder lookup decides: the tenant's
// own user is adopted, a foreign holder ends the run naming the username
// and the organization, and a holder the projection does not show yet
// keeps the step retryable: a genuinely foreign holder re-triages
// permanent on the next attempt once projected.
func TestCreateZitadelUserActivity_TriagesUsernameCollision(t *testing.T) {
	t.Parallel()
	alreadyExists := status.Error(codes.AlreadyExists, "User already exists (V3-DKcYh)")

	t.Run("the tenant's own pre-migration admin is adopted", func(t *testing.T) {
		t.Parallel()
		e := newActivityEnv(t)
		row := e.seed(t, enttenant.StatusPending, false)
		f := newFakeZitadel()
		f.users.createErr = alreadyExists
		f.users.listUsers = []*userv2.User{{
			UserId:             "v1-minted-id",
			PreferredLoginName: "admin@" + row.IdpOrgRef,
			Details:            &objectv2.Details{ResourceOwner: row.IdpOrgRef},
		}}
		e.withZitadel(f)

		out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})
		require.NoError(t, err)

		assert.Equal(t, "v1-minted-id", out.UserID, "the existing user's own ID is adopted, not the pre-chosen one")
		assert.Equal(t, "admin@"+row.IdpOrgRef, out.LoginName)
		require.NotNil(t, f.users.capturedList)
		nameQuery := f.users.capturedList.GetQueries()[0].GetUserNameQuery()
		require.NotNil(t, nameQuery)
		assert.Equal(t, testRegistration.AdminUsername, nameQuery.GetUserName())
		assert.Equal(t, objectv2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE, nameQuery.GetMethod(),
			"usernames are unique ignoring case, so the holder lookup must match on the same terms")
		assert.Equal(t, 1, f.closeCalls)
	})

	// Both branches of the triage answer the same opaque string:
	// registerTenant is reachable without authentication and its errors
	// travel to that caller verbatim, so a message that named the holding
	// organization or distinguished the two cases would be an
	// instance-wide username→tenant oracle.
	t.Run("a foreign holder is non-retryable and opaque", func(t *testing.T) {
		t.Parallel()
		e := newActivityEnv(t)
		row := e.seed(t, enttenant.StatusPending, false)
		f := newFakeZitadel()
		f.users.createErr = alreadyExists
		f.users.listUsers = []*userv2.User{{
			UserId:  "user-9",
			Details: &objectv2.Details{ResourceOwner: "some-other-org"},
		}}
		e.withZitadel(f)

		out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

		assert.Nil(t, out)
		require.ErrorContains(t, err, "admin username is not available")
		require.NotContains(t, err.Error(), "some-other-org",
			"the holding organization is operator-only: registerTenant is reachable without authentication, and naming the org would make the error an instance-wide username→tenant oracle")
		assertStepPermanent(t, err, "admin-username-taken")
		assert.Equal(t, 1, f.closeCalls)
	})

	// AlreadyExists proves a holder exists in the eventstore; the
	// projection may lag it, so a miss is lag, never a verdict.
	t.Run("a holder the projection does not show stays retryable", func(t *testing.T) {
		t.Parallel()
		e := newActivityEnv(t)
		row := e.seed(t, enttenant.StatusPending, false)
		f := newFakeZitadel()
		f.users.createErr = alreadyExists
		e.withZitadel(f)

		out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

		assert.Nil(t, out)
		assert.Equal(t, codes.AlreadyExists, status.Code(err), "the import's ORIGINAL answer stays the cause")
		assertRetryable(t, err)
	})

	t.Run("a failed holder lookup stays retryable", func(t *testing.T) {
		t.Parallel()
		e := newActivityEnv(t)
		row := e.seed(t, enttenant.StatusPending, false)
		f := newFakeZitadel()
		f.users.createErr = alreadyExists
		f.users.listErr = status.Error(codes.Unavailable, "zitadel restarting")
		e.withZitadel(f)

		out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

		assert.Nil(t, out)
		assert.Equal(t, codes.AlreadyExists, status.Code(err), "the import's ORIGINAL answer stays the cause")
		assertRetryable(t, err)
	})
}

// The import answers FailedPrecondition from Zitadel's eventstore for an
// organization its projection has not caught up with, too. The lookup then
// finds no user under the pre-chosen ID, which settles nothing: the step
// keeps the ORIGINAL error, stays retryable, and the budget bounds it.
func TestCreateZitadelUserActivity_UnprojectedOrgStaysRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	f.users.createErr = status.Error(codes.FailedPrecondition, "Errors.Org.NotFound")
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

	assert.Nil(t, out)
	require.ErrorContains(t, err, testRegistration.AdminUsername)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "the ORIGINAL error must reach the retry policy")
	assertStepRetryable(t, err)
	assert.Equal(t, []string{registertenant.AdminUserID(row.ID)}, f.users.lookedUp)
	assert.Equal(t, 1, f.closeCalls)
}

// The success verdict on an ID collision is read off the owning
// organization, so a user the lookup attributes to another org must not
// produce it: the step keeps the original error and retries within its
// budget rather than adopting a user that is not the tenant's.
func TestCreateZitadelUserActivity_ForeignOwnerOnIDCollisionStaysRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	f.users.createErr = status.Error(codes.FailedPrecondition, "COMMAND-7yiox1isql Errors.User.AlreadyExisting")
	f.users.lookupUser = &userv2.User{
		UserId:  registertenant.AdminUserID(row.ID),
		Details: &objectv2.Details{ResourceOwner: "some-other-org"},
	}
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

	assert.Nil(t, out)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assertStepRetryable(t, err)
}

// A lookup that fails after the ID collision settles nothing either; the
// step keeps the ORIGINAL error (naming the admin user it could not
// resolve) and the retry policy gets to retry it.
func TestCreateZitadelUserActivity_LookupFailureKeepsOriginalErrorRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	f.users.createErr = status.Error(codes.FailedPrecondition, "COMMAND-7yiox1isql Errors.User.AlreadyExisting")
	f.users.lookupErr = status.Error(codes.Unavailable, "zitadel restarting")
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

	assert.Nil(t, out)
	require.ErrorContains(t, err, testRegistration.AdminUsername)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assertStepRetryable(t, err)
}

// The read-back that fills the login name reads the user projection, which
// can lag the create; its failure must stay retryable: the next attempt
// resolves through the ID-collision path.
func TestCreateZitadelUserActivity_ReadBackFailureStaysRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	// The create succeeds; lookupUser stays nil, so the read-back answers
	// NotFound the way an unprojected user does.
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

	assert.Nil(t, out)
	require.ErrorContains(t, err, "read back admin user")
	assertStepRetryable(t, err)
	assert.Equal(t, 1, f.closeCalls)
}

// Transport and server failures of the import pass through unchanged so
// the activity's retry policy gets to retry them; no lookup is attempted.
// An account Zitadel rejects at import is a deterministic answer: no
// lookup, no retry. A refused value from the request is
// admin-account-invalid; a refused hash encoding (USER-JDk4t, carried as
// the error's ErrorDetail ID) is pyck's own bcrypt meeting a Zitadel
// configured without it, an operator fault with its own type. The ID is
// read from the detail, never from the message: a message that merely
// ends in "(USER-JDk4t)" without the detail stays admin-account-invalid.
func TestCreateZitadelUserActivity_InvalidAccountIsNonRetryable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		createErr error
		wantType  string
	}{
		{"a refused request value", status.Error(codes.InvalidArgument, "Errors.User.Email.Invalid (EMAIL-599BI)"), "admin-account-invalid"},
		{"an unsupported hash encoding", zitadelStatusError(t, codes.InvalidArgument, "USER-JDk4t", "Password hash encoding not supported (USER-JDk4t)"), "admin-hash-unsupported"},
		{"another refusal with an error ID", zitadelStatusError(t, codes.InvalidArgument, "EMAIL-599BI", "Errors.User.Email.Invalid (EMAIL-599BI)"), "admin-account-invalid"},
		{"the hash ID in the message only, without the detail", status.Error(codes.InvalidArgument, "Password hash encoding not supported (USER-JDk4t)"), "admin-account-invalid"},
		{"the hash ID in a different case", zitadelStatusError(t, codes.InvalidArgument, "user-jdk4t", "Password hash encoding not supported"), "admin-account-invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newActivityEnv(t)
			row := e.seed(t, enttenant.StatusPending, false)
			f := newFakeZitadel()
			f.users.createErr = tc.createErr
			e.withZitadel(f)

			out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

			assert.Nil(t, out)
			assertStepPermanent(t, err, tc.wantType)
			assert.Empty(t, f.users.lookedUp, "a rejected account is not looked up")
			assert.Equal(t, 1, f.closeCalls)
		})
	}
}

// The hash ID only decides between the two InvalidArgument types: the
// same detail on a transient code is still retried, so a Zitadel that
// attaches it to an Unavailable is not turned into a permanent failure.
func TestCreateZitadelUserActivity_HashErrorIDOnATransientCodeStaysRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	f.users.createErr = zitadelStatusError(t, codes.Unavailable, "USER-JDk4t", "zitadel restarting")
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

	assert.Nil(t, out)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assertStepRetryable(t, err)
}

// zitadelStatusError builds a gRPC error the way Zitadel's API layer does:
// the code, the message with the error ID appended, and the ID as a
// message.ErrorDetail.
func zitadelStatusError(t *testing.T, code codes.Code, id, msg string) error {
	t.Helper()
	st, err := status.New(code, msg).WithDetails(&message.ErrorDetail{Id: id, Message: msg})
	require.NoError(t, err)
	return st.Err()
}

func TestCreateZitadelUserActivity_TransientImportErrorStaysRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	f := newFakeZitadel()
	f.users.createErr = status.Error(codes.Unavailable, "zitadel restarting")
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

	assert.Nil(t, out)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	var appErr *temporal.ApplicationError
	assert.NotErrorAs(t, err, &appErr, "a transient failure must not be classified as non-retryable")
	assert.Empty(t, f.users.lookedUp)
	assert.Equal(t, 1, f.closeCalls)
}

// A row without registration data (cleared by a finished run, or written
// by something other than the resolver) cannot be provisioned by any
// retry: the failure is non-retryable and Zitadel is never dialled.
func TestCreateZitadelUserActivity_MissingRegistrationIsNonRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusDone, false)
	f := newFakeZitadel()
	e.withZitadel(f)

	out, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})

	assert.Nil(t, out)
	require.ErrorIs(t, err, registertenant.ErrRegistrationMissing)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, "registration-missing", appErr.Type())
	assert.Zero(t, f.closeCalls, "Zitadel must not be dialled without an account to create")
	assert.Nil(t, f.users.capturedCreates)
}

// A row that is gone (or soft-deleted, which the activity's context does
// not see) cannot yield an admin account, and no retry brings it back:
// the run must fail at once instead of burning its retry budget with the
// org already created.
func TestCreateZitadelUserActivity_MissingRowFails(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	deleted := e.seed(t, enttenant.StatusPending, true)
	f := newFakeZitadel()
	e.withZitadel(f)

	for name, id := range map[string]uuid.UUID{"missing": uuid.New(), "soft-deleted": deleted.ID} {
		out, err := e.a.CreateZitadelUserActivity(t.Context(), registertenant.CreateZitadelUserActivityInput{TenantID: id})
		assert.Nil(t, out, name)
		require.ErrorContains(t, err, "load tenant row", name)
		var appErr *temporal.ApplicationError
		require.ErrorAs(t, err, &appErr, "%s: must be a Temporal application error", name)
		assert.True(t, appErr.NonRetryable(), "%s: no retry brings a deleted row back", name)
		assert.Equal(t, "tenant-row-missing", appErr.Type(), name)
		assert.True(t, ent.IsNotFound(errors.Unwrap(appErr)), "%s: the missing row must be the cause", name)
	}
	assert.Nil(t, f.users.capturedCreates, "Zitadel must not be dialled without a row")
}

// =============================================================================
// AddDefaultDataTypesActivity (idempotent on slug)
// =============================================================================

// fakeDataTypeResolver records the slugs CreateDataType was asked for.
// Only that method is implemented; the embedded interface is nil so any
// other call panics loudly.
type fakeDataTypeResolver struct {
	exec.MutationResolver
	// err, when set, is what every CreateDataType answers, standing in
	// for a definition the resolver refuses or a database that is gone.
	err     error
	created []string
}

func (f *fakeDataTypeResolver) CreateDataType(_ context.Context, input ent.CreateDataTypeInput) (*ent.DataType, error) {
	f.created = append(f.created, *input.Slug)
	if f.err != nil {
		return nil, f.err
	}
	return &ent.DataType{Slug: *input.Slug}, nil
}

// withDataTypeResolver rebuilds the activities behind the given resolver,
// keeping the environment's ent client and its outbox hook.
func (e *activityEnv) withDataTypeResolver(r exec.MutationResolver) {
	e.a = registertenant.NewActivities(r, e.client, nil, commonwf.NewNamespaceGetter("test-audience"), nil)
}

// embeddedDefaultSlugs lists the slugs of the embedded default
// definitions; the activity must create exactly these on a fresh tenant.
func embeddedDefaultSlugs(t *testing.T) []string {
	t.Helper()
	definitions, err := registertenant.DefaultDataTypeDefinitions()
	require.NoError(t, err)
	slugs := make([]string, 0, len(definitions))
	for _, def := range definitions {
		slugs = append(slugs, def.Slug)
	}
	require.NotEmpty(t, slugs)
	return slugs
}

// seedDataType inserts a datatype row for the tenant under the given slug
// the way a previous run of the activity would have.
func (e *activityEnv) seedDataType(t *testing.T, tenantID uuid.UUID, slug string) {
	t.Helper()
	// TenantMixin stamps tenant_id from the request's mutation tenant.
	seedCtx := request.Context(e.ctx, authn.SystemUser(), tenantID)
	seedCtx = txid.With(seedCtx, txid.New())
	tx, err := e.client.Tx(seedCtx)
	require.NoError(t, err)
	seedCtx = ent.NewTxContext(seedCtx, tx)
	err = tx.DataType.Create().
		SetTenantID(tenantID).
		SetName(slug).
		SetSlug(slug).
		SetJSONSchema(`{"type":"object"}`).
		SetEntity("Tenant").
		Exec(seedCtx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

// A fresh tenant gets every embedded default; the resolver auto-assigns
// versions, so the activity must hand it each slug exactly once.
func TestAddDefaultDataTypesActivity_CreatesEveryDefaultOnce(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	resolver := &fakeDataTypeResolver{}
	e.a = registertenant.NewActivities(resolver, e.client, nil, commonwf.NewNamespaceGetter("test-audience"), nil)
	tenantID := uuid.New()

	err := e.a.AddDefaultDataTypesActivity(e.ctx, registertenant.AddDefaultDataTypesActivityInput{
		TenantID: tenantID, UserID: "user-1",
	})
	require.NoError(t, err)

	assert.ElementsMatch(t, embeddedDefaultSlugs(t), resolver.created)
}

// A re-run of the registration (retry after a partial failure, operator
// retry from the row) must not add a second version of a default the
// tenant already has: CreateDataType would silently assign MAX+1.
func TestAddDefaultDataTypesActivity_SkipsSlugsTheTenantAlreadyHas(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	resolver := &fakeDataTypeResolver{}
	e.a = registertenant.NewActivities(resolver, e.client, nil, commonwf.NewNamespaceGetter("test-audience"), nil)
	tenantID := uuid.New()
	slugs := embeddedDefaultSlugs(t)
	existing, missing := slugs[0], slugs[1:]
	e.seedDataType(t, tenantID, existing)
	// The same slug in another tenant must not count: the pre-check is
	// tenant-scoped.
	e.seedDataType(t, uuid.New(), missing[0])

	err := e.a.AddDefaultDataTypesActivity(e.ctx, registertenant.AddDefaultDataTypesActivityInput{
		TenantID: tenantID, UserID: "user-1",
	})
	require.NoError(t, err)

	assert.ElementsMatch(t, missing, resolver.created, "only the defaults the tenant lacks are created")
}

// Nothing to add is a no-op: no CreateDataType call, no event.
func TestAddDefaultDataTypesActivity_AllPresentIsNoOp(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	resolver := &fakeDataTypeResolver{}
	e.a = registertenant.NewActivities(resolver, e.client, nil, commonwf.NewNamespaceGetter("test-audience"), nil)
	tenantID := uuid.New()
	for _, slug := range embeddedDefaultSlugs(t) {
		e.seedDataType(t, tenantID, slug)
	}

	err := e.a.AddDefaultDataTypesActivity(e.ctx, registertenant.AddDefaultDataTypesActivityInput{
		TenantID: tenantID, UserID: "user-1",
	})
	require.NoError(t, err)

	assert.Empty(t, resolver.created)
}

// =============================================================================
// Retry classification (issue #1436)
// =============================================================================
//
// Every provisioning step of a registration has to answer the same
// question the same way: is this outcome one a retry can change? A step
// that returns a bare error is retried until its schedule-to-close budget
// is spent, which is right for a Zitadel restart and wrong for a name
// Zitadel will never accept: the run then holds its workflow ID and the
// row in pending for the whole budget instead of failing at once.

// assertStepPermanent asserts the activity ended the run on this attempt:
// a non-retryable application error typed with the failing step, with the
// dependency's own error still reachable underneath it. It reads the
// converted failure rather than errors.As, because the SDK type-switches
// on the error the activity returns: a verdict returned inside a wrapper
// is sent as a retryable failure that errors.As still finds.
func assertStepPermanent(t *testing.T, err error, wantType string) {
	t.Helper()
	info := temporal.GetDefaultFailureConverter().ErrorToFailure(err).GetApplicationFailureInfo()
	require.NotNil(t, info, "a refused request must be a temporal application error, got %v", err)
	assert.True(t, info.GetNonRetryable(), "%v must not be retried", err)
	assert.Equal(t, wantType, info.GetType(), "the error type names the failing step for the resolver message")
}

// assertStepRetryable asserts the activity left the outcome to the retry
// policy: the dependency may come back inside the activity's budget.
func assertStepRetryable(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		assert.False(t, appErr.NonRetryable(),
			"%v is a fault the dependency recovers from and must stay retryable", err)
	}
}

// errTransientZitadel is the outcome of a Zitadel that is restarting, being
// rolled out, or unreachable; the registration must survive it.
var errTransientZitadel = status.Error(codes.Unavailable, "zitadel is restarting")

// errRefusedByZitadel is the outcome of a request Zitadel rejects on its own
// terms; every attempt gets the same answer.
var errRefusedByZitadel = status.Error(codes.InvalidArgument, "Errors.Invalid.Argument")

// TestSetUserAsOrganizationOwnerActivity_ClassifiesOutcomes proves the
// ownership step distinguishes a Zitadel that is down from a membership
// Zitadel refuses.
func TestSetUserAsOrganizationOwnerActivity_ClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	input := registertenant.SetUserAsOrganizationOwnerActivityInput{OrganizationID: "org-1", UserID: "admin-1"}

	t.Run("a refused membership ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.perms.createErr = errRefusedByZitadel
		a := newZitadelActivities(f)

		assertStepPermanent(t, a.SetUserAsOrganizationOwnerActivity(t.Context(), input),
			"organization-owner-rejected")
		assert.Equal(t, 1, f.closeCalls, "the dialled client must be released on the failure path too")
	})

	t.Run("an unavailable zitadel is retried", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.perms.createErr = errTransientZitadel
		a := newZitadelActivities(f)

		assertStepRetryable(t, a.SetUserAsOrganizationOwnerActivity(t.Context(), input))
	})
}

// TestAddProjectGrantActivity_ClassifiesOutcomes proves the grant step
// tolerates its own retry before classification and leaves the projection
// lag of the objects it names to the retry policy.
func TestAddProjectGrantActivity_ClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	input := registertenant.AddProjectGrantActivityInput{ProjectID: "proj-1", OrganizationID: "org-1", Roles: []string{"admin"}}

	t.Run("a refused grant ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = errRefusedByZitadel
		a := newZitadelActivities(f)

		err := a.AddProjectGrantActivity(t.Context(), input)
		assertStepPermanent(t, err, "project-grant-rejected")
	})

	t.Run("an unavailable zitadel is retried", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = errTransientZitadel
		a := newZitadelActivities(f)

		err := a.AddProjectGrantActivity(t.Context(), input)
		assertStepRetryable(t, err)
	})

	// Zitadel validates the project and the granted organization against
	// projections the earlier steps' writes have to reach first, and
	// refuses with FailedPrecondition until they do. Ending the run there
	// would orphan the org and the admin over a lag.
	t.Run("an unprojected project or org is retried", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = status.Error(codes.FailedPrecondition, "Errors.Org.NotFound")
		a := newZitadelActivities(f)

		err := a.AddProjectGrantActivity(t.Context(), input)
		assertStepRetryable(t, err)
	})

	// The idempotency fallback has to be answered before classification:
	// an AlreadyExists is codes.AlreadyExists, which the classifier calls
	// permanent, so classifying first would fail every re-run.
	t.Run("an existing grant is success, not a permanent failure", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.projects.grantErr = status.Error(codes.AlreadyExists, "Errors.Project.Grant.AlreadyExists")
		f.projects.existing = &projectv2.ProjectGrant{GrantedOrganizationId: "org-1", GrantedRoleKeys: []string{"admin"}}
		a := newZitadelActivities(f)

		require.NoError(t, a.AddProjectGrantActivity(t.Context(), input))
	})
}

// TestAddUserGrantActivity_ClassifiesOutcomes proves the role step names
// the tenant's org in the request, treats an existing grant as success,
// and tells a refused role from a Zitadel that is down.
func TestAddUserGrantActivity_ClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	input := registertenant.AddUserGrantActivityInput{
		OrganizationID: "org-1", ProjectID: "proj-1", UserID: "admin-1",
		Roles: []string{"writer"},
	}

	t.Run("granted", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		a := newZitadelActivities(f)

		require.NoError(t, a.AddUserGrantActivity(t.Context(), input))
		require.Len(t, f.authz.captured, 1)
		req := f.authz.captured[0]
		assert.Equal(t, "org-1", req.GetOrganizationId(), "user grants are written on the tenant's own org")
		assert.Equal(t, "proj-1", req.GetProjectId())
		assert.Equal(t, "admin-1", req.GetUserId())
		assert.Equal(t, []string{"writer"}, req.GetRoleKeys())
		assert.Equal(t, 1, f.closeCalls, "the dialled client must be released")
	})

	t.Run("an existing grant carrying the roles is success without a rewrite", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.authz.createErr = status.Error(codes.AlreadyExists, "Errors.UserGrant.AlreadyExists")
		f.authz.existing = authorizationWithRoles("authz-1", "writer")
		a := newZitadelActivities(f)

		require.NoError(t, a.AddUserGrantActivity(t.Context(), input),
			"a retry of the step must not fail on the grant it wrote itself")
		require.Len(t, f.authz.capturedLists, 1)
		list := f.authz.capturedLists[0]
		require.Len(t, list.GetFilters(), 3, "the read is scoped by user, project and org: an org-wide page would miss the user past one page")
		assert.Equal(t, []string{"admin-1"}, list.GetFilters()[0].GetInUserIds().GetIds())
		assert.Equal(t, "proj-1", list.GetFilters()[1].GetProjectId().GetId(), "project and org must not be swapped: against Zitadel the read would match nothing and the step would retry into its budget")
		assert.Equal(t, "org-1", list.GetFilters()[2].GetOrganizationId().GetId())
		assert.Empty(t, f.authz.capturedUpdates, "a grant that already carries the roles must not be rewritten")
	})

	// A re-run of a registration under a build that grants the admin a role
	// the earlier build did not: the grant exists with the old roles, and
	// leaving it would register a tenant whose admin silently lacks the
	// role.
	t.Run("an existing grant with stale roles is widened with the union", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.authz.createErr = status.Error(codes.AlreadyExists, "Errors.UserGrant.AlreadyExists")
		f.authz.existing = authorizationWithRoles("authz-1", "reader")
		a := newZitadelActivities(f)

		require.NoError(t, a.AddUserGrantActivity(t.Context(), input))
		require.Len(t, f.authz.capturedLists, 1)
		assert.Equal(t, "proj-1", f.authz.capturedLists[0].GetFilters()[1].GetProjectId().GetId())
		assert.Equal(t, "org-1", f.authz.capturedLists[0].GetFilters()[2].GetOrganizationId().GetId())
		require.Len(t, f.authz.capturedUpdates, 1)
		update := f.authz.capturedUpdates[0]
		assert.Equal(t, "authz-1", update.GetId(), "the replace addresses the authorization by its id")
		assert.Equal(t, []string{"reader", "writer"}, update.GetRoleKeys(),
			"the replace is whole-set: the roles already held must be re-sent or Zitadel revokes them")
		assert.Equal(t, 1, f.closeCalls)
	})

	// AlreadyExists comes from the eventstore, the read from a projection
	// that may not report the grant yet: lag, not absence.
	t.Run("an existing grant not yet projected stays retryable", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.authz.createErr = status.Error(codes.AlreadyExists, "Errors.UserGrant.AlreadyExists")
		a := newZitadelActivities(f)

		err := a.AddUserGrantActivity(t.Context(), input)
		require.ErrorIs(t, err, sdk.ErrUserGrantNotFound)
		assertStepRetryable(t, err)
		assert.Empty(t, f.authz.capturedUpdates)
	})

	t.Run("a refused widening ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.authz.createErr = status.Error(codes.AlreadyExists, "Errors.UserGrant.AlreadyExists")
		f.authz.existing = authorizationWithRoles("authz-1", "reader")
		f.authz.updateErr = errRefusedByZitadel
		a := newZitadelActivities(f)

		assertStepPermanent(t, a.AddUserGrantActivity(t.Context(), input), "user-grant-rejected")
	})

	t.Run("a refused role ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.authz.createErr = errRefusedByZitadel
		a := newZitadelActivities(f)

		assertStepPermanent(t, a.AddUserGrantActivity(t.Context(), input), "user-grant-rejected")
	})

	// Zitadel validates the project grant against a projection the
	// previous step's write has to reach first, and answers NotFound or
	// FailedPrecondition until it does. Ending the run there would leave
	// an org, an admin user, a grant and a namespace behind for a grant
	// that exists.
	t.Run("a project grant the projection has not caught up with is retried", func(t *testing.T) {
		t.Parallel()
		for name, lag := range map[string]error{
			"not found":           status.Error(codes.NotFound, "Errors.Project.Grant.NotFound"),
			"failed precondition": status.Error(codes.FailedPrecondition, "COMMAND-m9gsd"),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newFakeZitadel()
				f.authz.createErr = lag
				a := newZitadelActivities(f)

				assertStepRetryable(t, a.AddUserGrantActivity(t.Context(), input))
			})
		}
	})

	t.Run("an unavailable zitadel is retried", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.authz.createErr = errTransientZitadel
		a := newZitadelActivities(f)

		assertStepRetryable(t, a.AddUserGrantActivity(t.Context(), input))
	})
}

// =============================================================================
// SetOrgMetadataActivity
// =============================================================================

// TestSetOrgMetadataActivity_MirrorsDataAndClassifiesOutcomes proves the
// metadata push mirrors the whole map in one request naming the tenant's
// org, and that a value Zitadel rejects ends the run while an unreachable
// Zitadel does not.
func TestSetOrgMetadataActivity_MirrorsDataAndClassifiesOutcomes(t *testing.T) {
	t.Parallel()

	// One call whatever the key count: with a round-trip per key, a
	// tenant registering with the resolver's maximum of 32 keys against a
	// slow Zitadel would never finish inside the activity's per-attempt
	// timeout, and no retry would get further than the first one.
	t.Run("every key is mirrored as its string form in one call", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		a := newZitadelActivities(f)

		require.NoError(t, a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
			OrganizationID: "org-1",
			Data:           map[string]any{"flavour": "pyck-go", "seats": float64(1000000), "trial": true},
		}))

		require.NotNil(t, f.orgs.capturedMetadata)
		assert.Equal(t, "org-1", f.orgs.capturedMetadata.GetOrganizationId())
		entries := f.orgs.capturedMetadata.GetMetadata()
		require.Len(t, entries, 3, "one request carries the whole map")
		assert.Equal(t, "flavour", entries[0].GetKey(), "keys are sent in sorted order")
		assert.Equal(t, []byte("pyck-go"), entries[0].GetValue(), "values are raw bytes, not base64")
		assert.Equal(t, "seats", entries[1].GetKey())
		assert.Equal(t, []byte("1000000"), entries[1].GetValue(),
			"a JSON number arrives as float64 and must be written in full, never as 1e+06")
		assert.Equal(t, "trial", entries[2].GetKey())
		assert.Equal(t, []byte("true"), entries[2].GetValue())
		assert.Equal(t, 1, f.orgs.metadataCalls)
		assert.Equal(t, 1, f.closeCalls)
	})

	// A tenant registered without data must not cost a Zitadel round-trip,
	// nor a dial that could fail and mark the row error for nothing.
	t.Run("no data dials nothing", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.dialErr = errors.New("must not be dialled")
		a := newZitadelActivities(f)

		require.NoError(t, a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
			OrganizationID: "org-1",
		}))
		assert.Zero(t, f.closeCalls, "an empty data map must return before the client is dialled")
	})

	t.Run("a value zitadel refuses ends the step as the caller's data", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.orgs.metadataErr = errRefusedByZitadel
		a := newZitadelActivities(f)

		err := a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
			OrganizationID: "org-1", Data: map[string]any{"flavour": "pyck-go"},
		})
		assertStepPermanent(t, err, "org-metadata-invalid")
	})

	t.Run("a refusal carrying a zitadel error ID is still the caller's data", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.orgs.metadataErr = zitadelStatusError(t, codes.InvalidArgument, "META-2n0fs", "Errors.Metadata.Invalid (META-2n0fs)")
		a := newZitadelActivities(f)

		err := a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
			OrganizationID: "org-1", Data: map[string]any{"flavour": "pyck-go"},
		})
		assertStepPermanent(t, err, "org-metadata-invalid")
	})

	// The permanent codes that are not a refusal of the data (a service
	// account without the metadata grant, a Zitadel without the API, a
	// limit on the platform's side) end the step under the operator-side
	// type, which registerTenant answers generically instead of blaming
	// the caller's data.
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unimplemented, codes.OutOfRange} {
		t.Run("an operator-side "+code.String()+" is not a refusal of the data", func(t *testing.T) {
			t.Parallel()
			f := newFakeZitadel()
			f.orgs.metadataErr = status.Error(code, "Errors.Token.Invalid")
			a := newZitadelActivities(f)

			err := a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
				OrganizationID: "org-1", Data: map[string]any{"flavour": "pyck-go"},
			})
			assertStepPermanent(t, err, "org-metadata-rejected")
		})
	}

	// A value this binary cannot render is pyck's own defect (the resolver
	// refuses non-scalars before the row exists), not the caller's data:
	// it stays the operator-side type and fails before Zitadel is dialled.
	t.Run("a value this binary cannot render is not a refusal of the data", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		a := newZitadelActivities(f)

		err := a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
			OrganizationID: "org-1", Data: map[string]any{"seats": 12},
		})
		assertStepPermanent(t, err, "org-metadata-rejected")
		assert.Zero(t, f.orgs.metadataCalls, "nothing is sent for a value that cannot be rendered")
	})

	t.Run("an unavailable zitadel is retried", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.orgs.metadataErr = errTransientZitadel
		a := newZitadelActivities(f)

		err := a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
			OrganizationID: "org-1", Data: map[string]any{"flavour": "pyck-go"},
		})
		assertStepRetryable(t, err)
	})
}

// =============================================================================
// ProvisionTenantWorkerIdentityActivity (the worker's Zitadel identity)
// =============================================================================

// workerLookupUser is the user the projection answers for the org's own
// worker.
func workerLookupUser(orgID string) *userv2.User {
	return &userv2.User{
		UserId:  registertenant.WorkerUserID(orgID),
		Details: &objectv2.Details{ResourceOwner: orgID},
	}
}

// newWorkerIdentityActivities wires both seams the merged identity step
// needs: the Zitadel fake that mints the user and its token, and a
// worker-api the credential is pushed to.
func newWorkerIdentityActivities(t *testing.T, f *fakeZitadel) *registertenant.Activities {
	t.Helper()
	a := newWorkerAPIActivities(t, http.StatusNoContent, "")
	f.install(a)
	return a
}

// TestProvisionTenantWorkerIdentityActivity_MintsIdentityAndClassifiesOutcomes
// proves the worker identity is a machine user under the tenant's org and
// its pre-chosen ID, that its token carries the expiry the v2 API demands
// and never leaves the activity, that a re-run reuses the user it created
// before, and that only an outcome Zitadel refuses ends the run.
func TestProvisionTenantWorkerIdentityActivity_MintsIdentityAndClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	input := registertenant.ProvisionTenantWorkerIdentityActivityInput{
		OrganizationID: "org-1", TenantID: uuid.New(), Key: "TEMPORAL_API_KEY",
	}

	t.Run("mints the identity and its token", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		a := newWorkerIdentityActivities(t, f)

		out, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		require.NoError(t, err)

		require.Len(t, f.users.capturedCreates, 1)
		req := f.users.capturedCreates[0]
		assert.Equal(t, "org-1", req.GetOrganizationId())
		assert.Equal(t, registertenant.WorkerUserID("org-1"), req.GetUserId(),
			"the worker's ID is pre-chosen and deterministic, like the admin's")
		assert.Equal(t, "worker-org-1", req.GetUsername(), "the worker's login name is derived from the org")
		require.NotNil(t, req.GetMachine(), "the worker is a machine user")
		assert.Equal(t, "Tenant Worker", req.GetMachine().GetName())

		require.Len(t, f.users.capturedPATs, 1)
		pat := f.users.capturedPATs[0]
		assert.Equal(t, registertenant.WorkerUserID("org-1"), pat.GetUserId())
		require.NotNil(t, pat.GetExpirationDate(), "the v2 API refuses a token without an expiry")
		assert.True(t, pat.GetExpirationDate().AsTime().After(time.Now().AddDate(1, 0, 0)),
			"the expiry stands in for the non-expiring tokens of before, so it must be far out")

		assert.Equal(t, registertenant.WorkerUserID("org-1"), out.UserID)
		assert.Equal(t, 1, f.closeCalls,
			"one dial per activity attempt: the user and its token share the connection, and a dial is a TCP handshake plus a token exchange")
	})

	// An operator re-running a failed registration collides on the worker's
	// pre-chosen ID; the step has to reuse that user rather than fail on a
	// FailedPrecondition the classifier would leave retrying for nothing.
	t.Run("a retry reuses its own user on the ID collision", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = status.Error(codes.FailedPrecondition, "COMMAND-k2una Errors.User.AlreadyExisting")
		f.users.lookupUser = workerLookupUser("org-1")
		a := newWorkerIdentityActivities(t, f)

		out, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, registertenant.WorkerUserID("org-1"), out.UserID)
		assert.Len(t, f.users.capturedPATs, 1, "the reused user still gets this run's token")
	})

	// A username collision here is the CERTAIN cross-version case: the
	// worker's username is deterministic, so a pre-migration worker of
	// this very tenant holds it under a Zitadel-minted ID and must be
	// adopted; only a foreign holder ends the run.
	t.Run("the tenant's own pre-migration worker is adopted", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = status.Error(codes.AlreadyExists, "User already exists (V3-DKcYh)")
		f.users.listUsers = []*userv2.User{{
			UserId:  "v1-minted-id",
			Details: &objectv2.Details{ResourceOwner: "org-1"},
		}}
		a := newWorkerIdentityActivities(t, f)

		out, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, "v1-minted-id", out.UserID, "the existing worker's own ID is adopted")
		require.Len(t, f.users.capturedPATs, 1, "the adopted worker still gets this run's token")
		assert.Equal(t, "v1-minted-id", f.users.capturedPATs[0].GetUserId())
	})

	t.Run("a foreign holder of the worker username ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = status.Error(codes.AlreadyExists, "User already exists (V3-DKcYh)")
		f.users.listUsers = []*userv2.User{{
			UserId:  "user-9",
			Details: &objectv2.Details{ResourceOwner: "some-other-org"},
		}}
		a := newWorkerIdentityActivities(t, f)

		_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		assertStepPermanent(t, err, "worker-identity-rejected")
		require.ErrorContains(t, err, "admin username is not available")
		require.NotContains(t, err.Error(), "some-other-org",
			"the holding organization stays out of client-visible errors (username→tenant oracle)")
	})

	t.Run("a worker-username holder the projection does not show stays retryable", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = status.Error(codes.AlreadyExists, "User already exists (V3-DKcYh)")
		a := newWorkerIdentityActivities(t, f)

		_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		assert.Equal(t, codes.AlreadyExists, status.Code(err), "the import's ORIGINAL answer stays the cause")
		assertRetryable(t, err)
	})

	t.Run("a failed worker-username holder lookup stays retryable", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = status.Error(codes.AlreadyExists, "User already exists (V3-DKcYh)")
		f.users.listErr = status.Error(codes.Unavailable, "zitadel restarting")
		a := newWorkerIdentityActivities(t, f)

		_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		assert.Equal(t, codes.AlreadyExists, status.Code(err), "the import's ORIGINAL answer stays the cause")
		assertRetryable(t, err)
	})

	// An organization the projection has not caught up with also answers
	// FailedPrecondition; the lookup settles nothing, so the step keeps
	// the original error and stays retryable within its short budget.
	t.Run("an unprojected org stays retryable", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = status.Error(codes.FailedPrecondition, "Errors.Org.NotFound")
		a := newWorkerIdentityActivities(t, f)

		_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
		assertStepRetryable(t, err)
	})

	t.Run("a refused service user ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = errRefusedByZitadel
		a := newWorkerIdentityActivities(t, f)

		_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		assertStepPermanent(t, err, "worker-identity-rejected")
	})

	t.Run("a refused token ends the step", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.patErr = status.Error(codes.PermissionDenied, "Errors.Permission.Denied")
		a := newWorkerIdentityActivities(t, f)

		_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		assertStepPermanent(t, err, "worker-identity-rejected")
	})

	t.Run("an unavailable zitadel is retried", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.users.createErr = errTransientZitadel
		a := newWorkerIdentityActivities(t, f)

		_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), input)
		assertStepRetryable(t, err)
	})
}

// =============================================================================
// CreateTemporalNamespaceActivity
// =============================================================================

// fakeNamespaceAPI stands in for the Temporal admin clients the namespace
// step otherwise dials against a live cluster.
type fakeNamespaceAPI struct {
	ensureErr error
	attrsErr  error

	ensured          []string
	ensuredNamespace string
	closeCalls       int
}

func (f *fakeNamespaceAPI) EnsureNamespace(_ context.Context, namespace string) error {
	f.ensured = append(f.ensured, namespace)
	return f.ensureErr
}

func (f *fakeNamespaceAPI) EnsureSearchAttributes(_ context.Context, namespace string) error {
	f.ensuredNamespace = namespace
	if f.attrsErr != nil {
		return f.attrsErr
	}
	return nil
}

func (f *fakeNamespaceAPI) Close() { f.closeCalls++ }

// newNamespaceActivities wires the fake behind the Temporal admin seam.
func newNamespaceActivities(f *fakeNamespaceAPI) *registertenant.Activities {
	a := registertenant.NewActivities(nil, nil, nil, commonwf.NewNamespaceGetter("test-audience"), nil)
	a.SetNamespaceAPI(func(context.Context, string) (registertenant.NamespaceAPI, error) { return f, nil })
	return a
}

// TestCreateTemporalNamespaceActivity_RegistersNamespaceAndAttributes
// proves the tenant's namespace carries the platform's whole search
// attribute set: a namespace without them serves every
// workflowExecutions(where:{transactionID}) query as an error.
func TestCreateTemporalNamespaceActivity_RegistersNamespaceAndAttributes(t *testing.T) {
	t.Parallel()
	f := &fakeNamespaceAPI{}
	a := newNamespaceActivities(f)

	require.NoError(t, a.CreateTemporalNamespaceActivity(t.Context(),
		registertenant.CreateTemporalNamespaceActivityInput{TemporalUrl: "temporal:7233", Namespace: "ns-1"}))

	assert.Equal(t, []string{"ns-1"}, f.ensured)
	// The attribute set and the list-missing-then-add semantics live in
	// commonwf.EnsureSearchAttributes, pinned by its own tests (including
	// the recompute-on-AlreadyExists case); this seam only proves the
	// activity routes the namespace through it.
	assert.Equal(t, "ns-1", f.ensuredNamespace,
		"the platform search attributes must be ensured on the tenant's namespace")
	assert.Equal(t, 1, f.closeCalls, "both Temporal connections must be released")
}

// TestCreateTemporalNamespaceActivity_ClassifiesOutcomes proves the
// namespace step tells a Temporal frontend that is restarting from a
// namespace name Temporal refuses. Temporal answers with serviceerror
// types, which carry Status() rather than GRPCStatus(): a classifier
// reading status.Code would see Unknown and retry every refusal for the
// whole budget.
func TestCreateTemporalNamespaceActivity_ClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	input := registertenant.CreateTemporalNamespaceActivityInput{TemporalUrl: "temporal:7233", Namespace: "ns-1"}

	// A namespace that already exists is what a retry of the step and an
	// operator's re-run both see; CreateTemporalNamespace answers it as
	// success, so it must never reach the classifier.
	t.Run("an existing namespace is success", func(t *testing.T) {
		t.Parallel()
		f := &fakeNamespaceAPI{}
		a := newNamespaceActivities(f)

		require.NoError(t, a.CreateTemporalNamespaceActivity(t.Context(), input))
	})

	// Attributes the namespace already carries never surface here: Ensure
	// converges to nil internally (list-missing-then-add, recompute on
	// AlreadyExists). An AlreadyExists that SURVIVES Ensure means its
	// internal retries were exhausted by pathological churn: a real
	// failure, no longer tolerated at the call site.
	t.Run("an AlreadyExists surviving Ensure ends the step", func(t *testing.T) {
		t.Parallel()
		f := &fakeNamespaceAPI{attrsErr: serviceerror.NewAlreadyExists("search attribute already exists")}
		a := newNamespaceActivities(f)

		err := a.CreateTemporalNamespaceActivity(t.Context(), input)
		assertStepPermanent(t, err, "temporal-namespace-rejected")
		assert.Equal(t, 1, f.closeCalls)
	})

	t.Run("a refused namespace ends the step", func(t *testing.T) {
		t.Parallel()
		f := &fakeNamespaceAPI{ensureErr: serviceerror.NewInvalidArgument("Namespace name is invalid")}
		a := newNamespaceActivities(f)

		err := a.CreateTemporalNamespaceActivity(t.Context(), input)
		assertStepPermanent(t, err, "temporal-namespace-rejected")
		require.ErrorContains(t, err, "ns-1", "the failing namespace must be named in the run's error")
		assert.Equal(t, 1, f.closeCalls, "both Temporal connections must be released on the failure path")
	})

	t.Run("a refused search attribute ends the step", func(t *testing.T) {
		t.Parallel()
		f := &fakeNamespaceAPI{attrsErr: serviceerror.NewInvalidArgument("invalid search attribute type")}
		a := newNamespaceActivities(f)

		assertStepPermanent(t, a.CreateTemporalNamespaceActivity(t.Context(), input),
			"temporal-namespace-rejected")
	})

	t.Run("an unavailable temporal is retried", func(t *testing.T) {
		t.Parallel()
		for name, f := range map[string]*fakeNamespaceAPI{
			"namespace registration": {ensureErr: serviceerror.NewUnavailable("frontend restarting")},
			"search attributes":      {attrsErr: serviceerror.NewUnavailable("frontend restarting")},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				assertStepRetryable(t, newNamespaceActivities(f).
					CreateTemporalNamespaceActivity(t.Context(), input))
			})
		}
	})
}

// =============================================================================
// worker-api steps
// =============================================================================

// newWorkerAPIActivities points the worker-api steps at a server that
// answers every request with the given status and body.
func newWorkerAPIActivities(t *testing.T, status int, body string) *registertenant.Activities {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body)) //nolint:errcheck // test server
		}
	}))
	t.Cleanup(srv.Close)

	return registertenant.NewActivities(nil, nil, nil,
		commonwf.NewNamespaceGetter("test-audience"), workerapi.New(srv.URL, "tok"))
}

// TestWorkerAPISteps_ClassifyByResponseStatus proves the two worker-api
// steps read the response status rather than the error text: a request
// worker-api refuses ends the run at once, and a worker-api that is down
// (rolling, unreachable) is retried until the activity's budget is spent.
func TestWorkerAPISteps_ClassifyByResponseStatus(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	identity := registertenant.ProvisionTenantWorkerIdentityActivityInput{
		OrganizationID: "org-1", TenantID: tenantID, Key: "TEMPORAL_API_KEY",
	}
	deployment := registertenant.CreateTenantWorkerDeploymentInput{
		Name: "pyckGo-ns", TenantID: tenantID, TemporalNamespace: "ns", Extension: "pyckGo",
	}

	steps := map[string]func(a *registertenant.Activities) error{
		"ProvisionTenantWorkerIdentityActivity": func(a *registertenant.Activities) error {
			// The Zitadel half succeeds; what the table classifies is the
			// secret push that follows it inside the same activity.
			newFakeZitadel().install(a)
			_, err := a.ProvisionTenantWorkerIdentityActivity(t.Context(), identity)
			return err
		},
		"CreateTenantWorkerDeploymentActivity": func(a *registertenant.Activities) error {
			return a.CreateTenantWorkerDeploymentActivity(t.Context(), deployment)
		},
	}

	for name, run := range steps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			t.Run("accepted", func(t *testing.T) {
				t.Parallel()
				require.NoError(t, run(newWorkerAPIActivities(t, http.StatusNoContent, "")))
			})

			t.Run("a refused request ends the step", func(t *testing.T) {
				t.Parallel()
				err := run(newWorkerAPIActivities(t, http.StatusUnprocessableEntity, `{"error":"extension unknown"}`))
				assertStepPermanent(t, err, "worker-api-rejected")
				assert.ErrorContains(t, err, "extension unknown", "worker-api's reason must reach the run's error")
			})

			t.Run("a worker-api that is down is retried", func(t *testing.T) {
				t.Parallel()
				assertStepRetryable(t, run(newWorkerAPIActivities(t, http.StatusServiceUnavailable, "")))
			})

			t.Run("a rate limit is retried", func(t *testing.T) {
				t.Parallel()
				assertStepRetryable(t, run(newWorkerAPIActivities(t, http.StatusTooManyRequests, "")))
			})

			// management's platform token is resolved by introspection
			// against Zitadel, so a 401 is as often a rotation window as
			// a bad token; every Zitadel step of the same run retries
			// through that window, and this one must too.
			t.Run("a rejected credential is retried", func(t *testing.T) {
				t.Parallel()
				assertStepRetryable(t, run(newWorkerAPIActivities(t, http.StatusUnauthorized, "")))
			})

			// A token worker-api accepts but does not grant this call is
			// a deployment mistake: no retry inside the run fixes it.
			t.Run("a token without the grant ends the step", func(t *testing.T) {
				t.Parallel()
				assertStepPermanent(t, run(newWorkerAPIActivities(t, http.StatusForbidden, "")), "worker-api-rejected")
			})

			// No worker-api URL is a deployment mistake, not a fault that
			// heals: retrying it spends the whole budget on a client that
			// will never be configured mid-run.
			t.Run("an unconfigured client ends the step", func(t *testing.T) {
				t.Parallel()
				a := registertenant.NewActivities(nil, nil, nil,
					commonwf.NewNamespaceGetter("test-audience"), workerapi.New("", "tok"))
				assertStepPermanent(t, run(a), "worker-api-rejected")
			})
		})
	}
}

// A deployment that already exists is the outcome of a retried or
// re-run registration and must stay success: it is a 409, which the
// shared classifier would otherwise call a permanent refusal.
func TestCreateTenantWorkerDeploymentActivity_ExistingDeploymentIsSuccess(t *testing.T) {
	t.Parallel()

	a := newWorkerAPIActivities(t, http.StatusConflict, "")
	require.NoError(t, a.CreateTenantWorkerDeploymentActivity(t.Context(),
		registertenant.CreateTenantWorkerDeploymentInput{Name: "pyckGo-ns", TenantID: uuid.New()}))
}

// =============================================================================
// AddDefaultDataTypesActivity
// =============================================================================

// TestAddDefaultDataTypesActivity_ClassifiesResolverOutcomes proves a
// definition the resolver refuses ends the run while a database that is
// briefly gone does not. The errors are the values CreateDataType really
// answers with: it carries neither a gRPC nor an HTTP status, so a status
// error injected here would prove only that the shared classifier works,
// not that this step ever reaches it.
func TestAddDefaultDataTypesActivity_ClassifiesResolverOutcomes(t *testing.T) {
	t.Parallel()

	// The schema of an embedded definition is compiled by the resolver
	// before anything is written; the error is the compiler's, which is
	// the same on every attempt.
	_, invalidSchema := jsonschema.CompileString("schema.json", `{"type": 42}`)
	require.Error(t, invalidSchema, "the fixture must be a schema the resolver refuses")

	refusals := map[string]error{
		"a schema that does not compile": invalidSchema,
		"a document that is not json":    mustCompileError(t, "{not json"),
	}
	for name, refusal := range refusals {
		t.Run(name+" ends the step", func(t *testing.T) {
			t.Parallel()
			e := newActivityEnv(t)
			e.withDataTypeResolver(&fakeDataTypeResolver{err: refusal})

			err := e.a.AddDefaultDataTypesActivity(e.ctx, registertenant.AddDefaultDataTypesActivityInput{
				TenantID: uuid.New(), UserID: "admin-1",
			})
			assertStepPermanent(t, err, "default-datatypes-rejected")
		})
	}

	// Everything the store answers with stays retryable: the connection
	// comes back, and a duplicate under the (tenant, slug, version) index
	// can only be a concurrent run of the same registration, which the
	// slug pre-check resolves on the next attempt.
	faults := map[string]error{
		"a database that is gone":         errors.New("driver: bad connection"),
		"a concurrent run's version race": db.ErrOCCConflict,
	}
	for name, fault := range faults {
		t.Run(name+" is retried", func(t *testing.T) {
			t.Parallel()
			e := newActivityEnv(t)
			e.withDataTypeResolver(&fakeDataTypeResolver{err: fault})

			err := e.a.AddDefaultDataTypesActivity(e.ctx, registertenant.AddDefaultDataTypesActivityInput{
				TenantID: uuid.New(), UserID: "admin-1",
			})
			assertStepRetryable(t, err)
		})
	}
}

// mustCompileError returns the error CompileString answers for a document
// it refuses, and fails the test when the document is accepted.
func mustCompileError(t *testing.T, schema string) error {
	t.Helper()
	_, err := jsonschema.CompileString("schema.json", schema)
	require.Error(t, err, "the fixture must be a document the resolver refuses")
	return err
}

// =============================================================================
// LoadTenantRegistrationActivity / SetTenantStatusActivity
// =============================================================================

// The row load is the run's first step and the only one that reads the
// registration; a database that is briefly unreachable must not fail a
// tenant whose row is perfectly fine.
func TestLoadTenantRegistrationActivity_DatabaseFailureIsRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	require.NoError(t, e.client.Close())

	_, err := e.a.LoadTenantRegistrationActivity(e.ctx, registertenant.LoadTenantRegistrationActivityInput{
		TenantID: row.ID,
	})
	assertStepRetryable(t, err)
}

// The status flip is what makes the run's outcome visible; a database
// that is briefly unreachable must be retried, or a fully provisioned
// tenant is left pending with no run behind it.
func TestSetTenantStatusActivity_DatabaseFailureIsRetryable(t *testing.T) {
	t.Parallel()
	e := newActivityEnv(t)
	row := e.seed(t, enttenant.StatusPending, false)
	require.NoError(t, e.client.Close())

	err := e.a.SetTenantStatusActivity(e.ctx, registertenant.SetTenantStatusActivityInput{
		TenantID: row.ID, Status: enttenant.StatusDone,
	})
	assertStepRetryable(t, err)
}

// =============================================================================
// TriggerTenantSyncActivity
// =============================================================================

// newSyncActivities points the sync trigger at a mocked Temporal client
// answering ExecuteWorkflow with the given result.
func newSyncActivities(t *testing.T, run temporalclient.WorkflowRun, err error) (*registertenant.Activities, *temporalmocks.Client) {
	t.Helper()
	tc := &temporalmocks.Client{}
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(run, err)
	t.Cleanup(func() { tc.AssertExpectations(t) })

	return registertenant.NewActivities(nil, nil, tc,
		commonwf.NewNamespaceGetter("test-audience"), nil), tc
}

// TestTriggerTenantSyncActivity_ClassifiesOutcomes proves the user
// projection is started under a deterministic ID, that a sync already
// running is success, and that only an outcome Temporal refuses ends the
// step. The workflow logs this step's failure rather than failing on it,
// so a wrong classification costs the run its budget rather than its
// outcome, which is exactly why the step has its own short budget.
func TestTriggerTenantSyncActivity_ClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	input := registertenant.TriggerTenantSyncActivityInput{OrganizationID: "org-1"}

	t.Run("starts the sync", func(t *testing.T) {
		t.Parallel()
		a, tc := newSyncActivities(t, &temporalmocks.WorkflowRun{}, nil)

		require.NoError(t, a.TriggerTenantSyncActivity(t.Context(), input))
		tc.AssertCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	// The ID is deterministic per org, so a sync already running for it is
	// the intended outcome, not a failure to classify.
	t.Run("a sync already running is success", func(t *testing.T) {
		t.Parallel()
		a, _ := newSyncActivities(t, nil,
			serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "sync-org-1", "run-1"))

		require.NoError(t, a.TriggerTenantSyncActivity(t.Context(), input))
	})

	t.Run("a refused start ends the step", func(t *testing.T) {
		t.Parallel()
		a, _ := newSyncActivities(t, nil, serviceerror.NewInvalidArgument("task queue name is invalid"))

		assertStepPermanent(t, a.TriggerTenantSyncActivity(t.Context(), input), "tenant-sync-rejected")
	})

	t.Run("an unavailable temporal is retried", func(t *testing.T) {
		t.Parallel()
		a, _ := newSyncActivities(t, nil, serviceerror.NewUnavailable("frontend restarting"))

		assertStepRetryable(t, a.TriggerTenantSyncActivity(t.Context(), input))
	})
}

// The two activities that already answered the outcomes they enumerate
// (a taken name, an account Zitadel rejects) still had every other
// refusal fall through as a bare error, which the retry policy then spent
// the whole activity budget on. The shared classification is what catches
// those, under the step's own error type.
func TestOrgAndAdminSteps_ClassifyRefusalsTheyDoNotEnumerate(t *testing.T) {
	t.Parallel()

	t.Run("an org creation zitadel refuses on permission", func(t *testing.T) {
		t.Parallel()
		f := newFakeZitadel()
		f.orgs.addErr = status.Error(codes.PermissionDenied, "Errors.Permission.Denied")
		a := newZitadelActivities(f)

		_, err := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
			Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
		})
		assertStepPermanent(t, err, "organization-rejected")
	})

	t.Run("an admin import zitadel refuses on permission", func(t *testing.T) {
		t.Parallel()
		e := newActivityEnv(t)
		row := e.seed(t, enttenant.StatusPending, false)
		f := newFakeZitadel()
		f.users.createErr = status.Error(codes.PermissionDenied, "Errors.Permission.Denied")
		e.withZitadel(f)

		_, err := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})
		assertStepPermanent(t, err, "admin-account-rejected")
	})

	// The same two steps must still leave a Zitadel that is down alone.
	t.Run("an unavailable zitadel is retried by both", func(t *testing.T) {
		t.Parallel()

		f := newFakeZitadel()
		f.orgs.addErr = errTransientZitadel
		a := newZitadelActivities(f)
		_, orgErr := a.CreateTenantActivity(t.Context(), registertenant.CreateTenantActivityInput{
			Name: "Acme", OrganizationID: "org-1", TenantID: uuid.New(),
		})
		assertStepRetryable(t, orgErr)

		e := newActivityEnv(t)
		row := e.seed(t, enttenant.StatusPending, false)
		fu := newFakeZitadel()
		fu.users.createErr = errTransientZitadel
		e.withZitadel(fu)
		_, adminErr := e.a.CreateZitadelUserActivity(e.ctx, registertenant.CreateZitadelUserActivityInput{TenantID: row.ID})
		assertStepRetryable(t, adminErr)
	})
}
