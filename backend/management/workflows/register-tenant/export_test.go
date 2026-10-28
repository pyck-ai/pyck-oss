package registertenant

import (
	"context"

	authzv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	permv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	orgv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// CreateTenantActivityInput exposes the unexported org-creation input so
// the external test package can assert the IDs the workflow hands to
// CreateTenantActivity.
type CreateTenantActivityInput = createTenantActivityInput

// CreateZitadelUserActivityInput exposes the unexported admin-user input
// so the external test package can assert the workflow hands the
// activity nothing but the tenant ID.
type CreateZitadelUserActivityInput = createZitadelUserActivityInput

// SetUserAsOrganizationOwnerActivityInput exposes the unexported owner
// input so the external test package can drive the activity.
type SetUserAsOrganizationOwnerActivityInput = setUserAsOrganizationAdmin

// AddProjectGrantActivityInput exposes the unexported project-grant input
// so the external test package can drive the activity.
type AddProjectGrantActivityInput = addProjectGrantsInput

// ZitadelClients exposes the Zitadel v2 client bundle so the external
// test package can build one from fakes of the generated service
// interfaces: the package's test seam.
type ZitadelClients = zitadelClients

// NewZitadelClientsForTest builds a bundle from the given clients.
// closed counts the bundle's releases, so a test can assert the per-step
// defer ran on every path.
func NewZitadelClientsForTest(
	users userv2.UserServiceClient,
	orgs orgv2.OrganizationServiceClient,
	projects projectv2.ProjectServiceClient,
	authz authzv2.AuthorizationServiceClient,
	perms permv2.InternalPermissionServiceClient,
	closed *int,
) *ZitadelClients {
	return &zitadelClients{
		users:    users,
		orgs:     orgs,
		projects: projects,
		authz:    authz,
		perms:    perms,
		close:    func(context.Context) { *closed++ },
	}
}

// SetZitadelDialer replaces the Zitadel client factory.
func (a *Activities) SetZitadelDialer(f func(ctx context.Context) (*ZitadelClients, error)) {
	a.dialZitadel = f
}

// AdminUserID and WorkerUserID expose the deterministic Zitadel user ID
// derivations so the external test package can assert the requests carry
// the pre-chosen IDs.
var (
	AdminUserID  = adminUserID
	WorkerUserID = workerUserID
)

// Sentinels of the organization conflict resolution, exposed so the
// external test package can assert which verdict a conflict resolved to.
var (
	ErrOrganizationIDMismatch      = errOrganizationIDMismatch
	ErrOrganizationNameTaken       = errOrganizationNameTaken
	ErrOrganizationOwnerUnresolved = errOrganizationOwnerUnresolved
)

// DefaultDataTypeDefinitions exposes the embedded default datatype
// definitions so the external test package can derive the slugs the
// activity is expected to create.
var DefaultDataTypeDefinitions = defaultDataTypes

// NamespaceAPI exposes the Temporal admin seam so the external test
// package can substitute a fake for the namespace and operator clients the
// namespace step otherwise dials against a live cluster.
type NamespaceAPI = temporalNamespaceAPI

// SetNamespaceAPI replaces the Temporal admin client factory.
func (a *Activities) SetNamespaceAPI(f func(ctx context.Context, url string) (NamespaceAPI, error)) {
	a.dialNamespaceAPI = f
}

// AddUserGrantActivityInput exposes the unexported user-grant input so the
// external test package can drive the activity directly.
type AddUserGrantActivityInput = addUserGrantInput

// CreateTemporalNamespaceActivityInput exposes the unexported namespace
// input so the external test package can drive the activity directly.
type CreateTemporalNamespaceActivityInput = createTemporalNamespaceInput

// ProvisionTenantWorkerIdentityActivityInput exposes the unexported worker
// identity input so the external test package can drive the activity.
type ProvisionTenantWorkerIdentityActivityInput = provisionTenantWorkerIdentityInput

// Retry budget constants, exposed so the external test package can assert
// the arithmetic that keeps a retrying run inside ExecutionTimeout: a run
// that exceeds that bound closes TIMED_OUT without executing the status
// flip and leaves the row pending.
const (
	ActivityStartToClose          = activityStartToClose
	ActivityScheduleToClose       = activityScheduleToClose
	SyncScheduleToClose           = syncScheduleToClose
	WorkerIdentityScheduleToClose = workerIdentityScheduleToClose
	MaxActivitiesPerRun           = maxActivitiesPerRun
)

// RegistrationRetryPolicy exposes the policy every provisioning step runs
// under, so a test can assert it is bounded by the schedule-to-close
// budget rather than by an attempt cap.
var RegistrationRetryPolicy = registrationRetryPolicy
