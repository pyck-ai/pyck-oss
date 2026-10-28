package registertenant

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/serviceroles"

	"github.com/pyck-ai/pyck/backend/management/core"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

var activities Activities

// projectRoles is the pyck project's role-key ladder as Zitadel knows it.
// Every tenant's project grant carries all three so its users can be
// granted any of them, and pyck's auth layer parses them back with
// authn.RoleString, which is why the keys are derived from the Role enum
// here instead of being spelled out a second time.
var projectRoles = []string{
	authn.ROLE_ADMIN.String(),
	authn.ROLE_WRITER.String(),
	authn.ROLE_READER.String(),
}

const (
	// workerTemporalAPIKeySecret is the tenant secret worker-api projects into
	// the worker's Connection.
	workerTemporalAPIKeySecret = "TEMPORAL_API_KEY" //nolint:gosec // a secret KEY NAME, not a credential

	// activityStartToClose bounds one attempt of any activity; every step
	// is a handful of Zitadel, Temporal or worker-api round-trips.
	activityStartToClose = 30 * time.Second
	// activityScheduleToClose bounds one activity including every retry,
	// and is the only bound on a step whose dependency is down: the retry
	// policy has no attempt cap, so a Zitadel restart or a network
	// partition costs attempts here instead of failing the registration,
	// and a dependency that does not come back inside it ends the run with
	// the row marked error. It is the unit ExecutionTimeout is built from,
	// so growing it grows that bound too.
	activityScheduleToClose = 5 * time.Minute

	// syncScheduleToClose bounds the user-sync trigger, the one step whose
	// failure is only logged. It gets its own short budget so a Temporal
	// hiccup on a best-effort step cannot add minutes to a registration
	// that is otherwise complete.
	syncScheduleToClose = 1 * time.Minute

	// workerIdentityScheduleToClose bounds the worker's Zitadel identity,
	// the one step whose retries cost more than time: every attempt mints
	// a personal access token that nothing revokes, so a token committed
	// by Zitadel whose response is lost leaves a live credential behind.
	// The short budget is what bounds their number; the trade is that a
	// Zitadel outage longer than this fails a pyck-go registration that a
	// provisioning budget would have carried through.
	workerIdentityScheduleToClose = 1 * time.Minute

	// maxActivitiesPerRun is the most activities one run executes: the
	// fifteen steps of a pyck-go tenant (its mid-run pending re-check, the
	// worker's three and the sync trigger included) plus one more status
	// flip, because a run whose flip to done fails marks the row error on
	// top of it, plus the organization's retirement when that mark finds
	// the row deleted.
	maxActivitiesPerRun = 17

	// ExecutionTimeout is the server-side bound of a registration run,
	// which the tenant-lifecycle trigger sets when it starts one: every
	// activity at its full retry budget plus one budget of margin for
	// workflow-task scheduling, 90 minutes at the numbers above. It must
	// stay above the sum of the activity budgets, because a run that
	// exceeds it closes TIMED_OUT without executing the status flip and
	// leaves the row pending with no run behind it, the one outcome the
	// error status exists to rule out. It also bounds a run that cannot
	// complete a workflow task at all (a panic in workflow code, a
	// non-deterministic replay after a deploy), which Temporal would
	// otherwise retry forever, holding the workflow ID against the
	// operator's retry and the row in pending.
	ExecutionTimeout = (maxActivitiesPerRun + 1) * activityScheduleToClose

	// appErrTypeInvalidInput is the error type of a start whose input names
	// no tenant row; like the errType* constants in activities.go it is
	// read by callers and by the end-to-end suites.
	appErrTypeInvalidInput = "invalid-input"
)

// ErrMissingTenantID rejects an input that names no tenant row.
var ErrMissingTenantID = errors.New("register tenant: input has no tenant id")

// registrationRetryPolicy retries a provisioning step for as long as its
// ScheduleToCloseTimeout allows: a dependency that comes back inside that
// budget completes the registration, and one that does not ends the run
// with the row marked error. There is no attempt cap on purpose: a cap
// turns a Zitadel restart or a rollout into a permanently failed tenant.
// What keeps the budget from being spent on an answer that will not change
// is the classification in activities.go: an outcome the dependency refuses
// comes back non-retryable and fails the step on its first attempt.
//
// This is deliberately not commonwf.DefaultRetryPolicy: that one caps a
// step at three attempts, which is the right bound for work an operator
// or a schedule repeats, and the wrong one for a registration nothing
// repeats.
func registrationRetryPolicy() *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    1 * time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    30 * time.Second,
		MaximumAttempts:    0,
	}
}

// activityOptions bounds one attempt of a step by activityStartToClose
// (every step is a handful of round-trips, whatever it talks to) and all
// its attempts together by scheduleToClose. The second bound is the only
// one the retry policy has, so every step has to name one.
func activityOptions(ctx workflow.Context, scheduleToClose time.Duration) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    activityStartToClose,
		ScheduleToCloseTimeout: affordableBudget(ctx, scheduleToClose),
		RetryPolicy:            registrationRetryPolicy(),
	}
}

// affordableBudget caps want at the share of this execution's own recorded
// WorkflowExecutionTimeout that one activity may spend, so the budgets of
// a whole run always add up to less than the bound the run was started
// with. That bound is written into history when the trigger starts the
// run and never changes, while the constants above are read from whatever
// binary serves the workflow task: without the cap, a run started before
// a deploy that raises activityScheduleToClose could schedule more budget
// than it is allowed to spend and close TIMED_OUT, which never executes
// the status flip and leaves the row pending forever. Reading the timeout
// from workflow.GetInfo is replay-safe; a run started without one (the
// test environment) keeps want.
func affordableBudget(ctx workflow.Context, want time.Duration) time.Duration {
	recorded := workflow.GetInfo(ctx).WorkflowExecutionTimeout
	if recorded <= 0 {
		return want
	}
	return min(want, recorded/(maxActivitiesPerRun+1))
}

// RegisterTenantWorkflow provisions the tenant whose status=pending row the
// registerTenant resolver committed: that row carries the tenant's identity
// and its admin account, and this run turns it into a live tenant in Zitadel
// and Temporal. Each step is documented where it happens, in order.
//
// A row that is not pending when the run starts (a replayed create event, a
// registration already finished or already marked error) is a logged no-op,
// and a pyck-go run re-checks the row once more before it provisions the
// worker.
//
// The activity sequence is part of this workflow's replay contract and has
// no GetVersion gate: an execution recorded under a different sequence
// cannot replay against this code and blocks under the SDK's default
// WorkflowPanicPolicy until its ExecutionTimeout closes it. Deploying a
// change to the sequence therefore requires draining registrations first
// (docs/tenant-registration.md, "Changing the registration workflow").
//
// Every step goes through step(), so any failure marks the row status=error
// and ends the run with that step's error; nothing is compensated, and
// whatever was created stays behind the row for an operator to retry from or
// retire with deleteTenant.
//
// deleteTenant never touches a run: a run whose row is deleted under it
// notices on its own. The status flips refuse a deleted row, and a pyck-go
// run's re-check before the worker finds it gone; either way the run
// deactivates the organization the row names (retire) and ends.
func RegisterTenantWorkflow(context workflow.Context, input RegisterTenantWorkflowInput) (*RegisterTenantWorkflowOutput, error) {
	if input.TenantID == uuid.Nil {
		return nil, temporal.NewNonRetryableApplicationError(ErrMissingTenantID.Error(), appErrTypeInvalidInput, ErrMissingTenantID)
	}

	ctx := workflow.WithActivityOptions(context, activityOptions(context, activityScheduleToClose))
	logger := workflow.GetLogger(ctx)
	tenantID := input.TenantID

	// The registration data. The row is this run's only input: the resolver
	// put the organization ID, the name, the caller's data and the admin
	// account there. Its status decides whether this run still owns the
	// registration, which is what makes a replayed create event or a second
	// start harmless.
	var row LoadTenantRegistrationActivityOutput
	if err := step(ctx, tenantID, "LoadTenantRegistrationActivity",
		activities.LoadTenantRegistrationActivity, LoadTenantRegistrationActivityInput(input), &row); err != nil {
		return nil, err
	}
	if row.Status != enttenant.StatusPending {
		logger.Info("tenant registration skipped: row is not pending", "tenant_id", tenantID, "status", row.Status)
		return &RegisterTenantWorkflowOutput{TenantID: tenantID, Skipped: true}, nil
	}

	// The Zitadel organization, created under the ID the row already
	// references rather than one Zitadel mints, which is what keeps
	// tenant.id == ComputeUUID(audience, idp_org_ref) true and therefore
	// what every service's auth layer derives. Everything below is scoped
	// to this organization.
	var org CreateTenantActivityOutput
	if err := step(ctx, tenantID, "CreateTenantActivity",
		activities.CreateTenantActivity, createTenantActivityInput{
			Name:           row.Name,
			OrganizationID: row.OrganizationID,
			TenantID:       tenantID,
		}, &org); err != nil {
		return nil, err
	}

	// The tenant's admin user, imported into that organization. The
	// activity reads the account and its password hash from the row itself,
	// so no credential passes through workflow history. Its user ID is what
	// the ownership, role, datatype and projection steps below act on.
	var admin CreateZitadelUserActivityOutput
	if err := step(ctx, tenantID, "CreateZitadelUserActivity",
		activities.CreateZitadelUserActivity, createZitadelUserActivityInput(input), &admin); err != nil {
		return nil, err
	}

	// Organization ownership. The admin becomes the organization's owner
	// and is the tenant's only administrator until it invites others; this
	// is what lets it manage its own org in Zitadel.
	if err := step(ctx, tenantID, "SetUserAsOrganizationOwnerActivity",
		activities.SetUserAsOrganizationOwnerActivity, setUserAsOrganizationAdmin{
			OrganizationID: org.OrganizationID,
			UserID:         admin.UserID,
		}, nil); err != nil {
		return nil, err
	}

	// The project grant. The organization is granted the pyck project with
	// every role its users may ever hold: the reader/writer ladder plus the
	// per-service gate roles. It has to carry them all because a user can
	// only be granted a role the organization itself has, so this bounds
	// what the admin and the tenant's worker can receive.
	if err := step(ctx, tenantID, "AddProjectGrantActivity",
		activities.AddProjectGrantActivity, addProjectGrantsInput{
			ProjectID:      core.Config.ZitadelProjectId,
			OrganizationID: org.OrganizationID,
			Roles:          append(slices.Clone(projectRoles), serviceroles.ServiceRoleStrings()...),
		}, nil); err != nil {
		return nil, err
	}

	// The admin's role on that grant, which the user's token then carries
	// as a project role and pyck's auth layer reads to authorize its
	// requests. admin alone: the ladder is cumulative on every consumer
	// (the claim mapper keeps the highest role per organization and the
	// Temporal claim mapper expands it to a cumulative bitmask), so admin
	// already implies writer and reader.
	adminRoles := []string{authn.ROLE_ADMIN.String()}
	if err := step(ctx, tenantID, "AddUserGrantActivity",
		activities.AddUserGrantActivity, addUserGrantInput{
			OrganizationID: org.OrganizationID,
			ProjectID:      core.Config.ZitadelProjectId,
			UserID:         admin.UserID,
			Roles:          adminRoles,
		}, nil); err != nil {
		return nil, err
	}

	// The default datatypes, created through the GraphQL resolver acting as
	// the admin, so they are owned by the tenant and pass the same
	// validation as any datatype a tenant creates later. This needs the
	// admin's roles to be in place.
	if err := step(ctx, tenantID, "AddDefaultDataTypesActivity",
		activities.AddDefaultDataTypesActivity, AddDefaultDataTypesActivityInput{
			TenantID: org.TenantID,
			UserID:   admin.UserID,
		}, nil); err != nil {
		return nil, err
	}

	// The tenant's Temporal namespace, with the platform's search
	// attributes registered on it. Every workflow of this tenant runs in
	// it, so it must exist before the worker below is pointed at it.
	if err := step(ctx, tenantID, "CreateTemporalNamespaceActivity",
		activities.CreateTemporalNamespaceActivity, createTemporalNamespaceInput{
			TemporalUrl: core.Config.TemporalUrl,
			Namespace:   org.TemporalNamespace,
		}, nil); err != nil {
		return nil, err
	}

	// The organization metadata, mirroring the caller's data in one write
	// so it is readable from Zitadel too. The DB row stays the source of
	// truth: this is a push, never read back. Tenant expiry is
	// deliberately NOT written here: it lives in the expires_at column,
	// set by the resolver and by setTenantExpiry.
	if err := step(ctx, tenantID, "SetOrgMetadataActivity",
		activities.SetOrgMetadataActivity, SetOrgMetadataActivityInput{
			OrganizationID: org.OrganizationID,
			Data:           row.Data,
		}, nil); err != nil {
		return nil, err
	}

	// The user projection, so the admin also exists as a management.users
	// row (nothing else creates one). The workflow ID is deterministic, so
	// a sync already running for the organization is a no-op. This is the
	// one step whose failure is only logged: the scheduled orchestrator
	// repeats it within its cycle, and failing here would mark a tenant
	// that is fully provisioned as error.
	syncCtx := workflow.WithActivityOptions(ctx, activityOptions(ctx, syncScheduleToClose))
	if err := workflow.ExecuteActivity(syncCtx, activities.TriggerTenantSyncActivity,
		TriggerTenantSyncActivityInput{OrganizationID: org.OrganizationID}).Get(syncCtx, nil); err != nil {
		logger.Error("failed to trigger tenant sync", "err", err)
	}

	// The platform-managed worker, only for pyck-go tenants; a plain tenant
	// brings its own via `pyck deploy`. It needs the project grant for its
	// service user and the namespace to poll. It is the last provisioning
	// step on purpose: the worker starts as soon as its deployment exists
	// and carries a long-lived credential, so every step that can still
	// fail permanently runs before it, and only the done flip follows. A
	// failure above therefore ends the run before any worker exists.
	if IsPyckGo(row.Data) {
		// Re-read the row before the worker steps. Everything above is
		// bounded work inside Zitadel and Temporal, but the worker is a
		// deployment on the worker cluster and a live credential, and a
		// deleteTenant issued while the run was provisioning would
		// otherwise be noticed only at the final status flip, after the
		// worker exists, with no row left that names it. The re-check sits
		// inside this branch on purpose: it is the branch whose steps
		// leave something outside Zitadel behind, and keeping it there
		// leaves a plain tenant's activity sequence unchanged.
		stop, err := stopBeforeWorker(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if stop {
			return &RegisterTenantWorkflowOutput{TenantID: tenantID, Skipped: true}, nil
		}

		if err := deployTenantWorker(ctx, org); err != nil {
			return nil, err
		}
	}

	// The row is done. This is last on purpose: only done tenants take part
	// in the user sync, the expiry sweep and lifecycle dispatch, all of
	// which assume everything above exists. The activity also clears the
	// admin account from the row, so the password hash lives no longer than
	// the registration it served.
	if err := step(ctx, tenantID, "SetTenantStatusActivity",
		activities.SetTenantStatusActivity, SetTenantStatusActivityInput{
			TenantID: tenantID,
			Status:   enttenant.StatusDone,
		}, nil); err != nil {
		return nil, err
	}

	return &RegisterTenantWorkflowOutput{
		OrganizationID: org.OrganizationID,
		TenantID:       org.TenantID,
		LoginName:      admin.LoginName,
		UserID:         admin.UserID,
		UserRoles:      adminRoles,
	}, nil
}

// step runs one provisioning activity of a registration. A failure marks
// the tenant row status=error and comes back unchanged, so a caller only
// has to propagate it and no failure can end the run without the row
// recording it. name is the step the row's error log and the resolver's
// message report; out may be nil for an activity that returns nothing.
func step(ctx workflow.Context, tenantID uuid.UUID, name string, activity, in, out any) error {
	if err := workflow.ExecuteActivity(ctx, activity, in).Get(ctx, out); err != nil {
		return fail(ctx, tenantID, name, err)
	}
	return nil
}

// stopBeforeWorker re-reads the row before a pyck-go run's worker steps and
// reports whether the run must stop because the row is no longer pending.
// An empty status is a row deleteTenant retired: the organization the run
// created is its own to deactivate.
func stopBeforeWorker(ctx workflow.Context, tenantID uuid.UUID) (bool, error) {
	var current LoadTenantRegistrationActivityOutput
	if err := step(ctx, tenantID, "LoadTenantRegistrationActivity",
		activities.LoadTenantRegistrationActivity, LoadTenantRegistrationActivityInput{TenantID: tenantID}, &current); err != nil {
		return false, err
	}
	if current.Status == enttenant.StatusPending {
		return false, nil
	}
	workflow.GetLogger(ctx).Info("tenant registration stopped before the worker: row is no longer pending",
		"tenant_id", tenantID, "status", current.Status)
	if current.Status == "" {
		retire(ctx, tenantID)
	}
	return true, nil
}

// deployTenantWorker gives a pyck-go tenant its worker. Everything on the
// worker cluster — namespace, Connection, secrets, the WorkerDeployment CRD —
// belongs to worker-api, which is the only component holding write credentials
// there; management mints the tenant's Zitadel credential and hands it over.
func deployTenantWorker(ctx workflow.Context, org CreateTenantActivityOutput) error {
	// The worker's own Zitadel identity, minted here rather than in worker-api
	// so Zitadel write credentials stay in one place. The same step pushes
	// the credential to the tenant secret store: worker-api fails closed
	// on a deployment whose TEMPORAL_API_KEY is missing, and a token that
	// travelled back through workflow history would be readable there for
	// the history's retention. Every attempt mints a fresh token and
	// nothing revokes the previous one, which stays valid until it
	// expires, so this step retries on its own short budget instead of the
	// provisioning one.
	identityCtx := workflow.WithActivityOptions(ctx, activityOptions(ctx, workerIdentityScheduleToClose))
	var serviceUser ProvisionTenantWorkerIdentityOutput
	if err := step(identityCtx, org.TenantID, "ProvisionTenantWorkerIdentityActivity",
		activities.ProvisionTenantWorkerIdentityActivity, provisionTenantWorkerIdentityInput{
			OrganizationID: org.OrganizationID,
			TenantID:       org.TenantID,
			Key:            workerTemporalAPIKeySecret,
		}, &serviceUser); err != nil {
		return err
	}

	// The worker acts on the tenant's data, so its identity carries the
	// same writer role as a human writer.
	if err := step(ctx, org.TenantID, "AddUserGrantActivity",
		activities.AddUserGrantActivity, addUserGrantInput{
			OrganizationID: org.OrganizationID,
			ProjectID:      core.Config.ZitadelProjectId,
			UserID:         serviceUser.UserID,
			Roles:          []string{authn.ROLE_WRITER.String()},
		}, nil); err != nil {
		return err
	}

	return step(ctx, org.TenantID, "CreateTenantWorkerDeploymentActivity",
		activities.CreateTenantWorkerDeploymentActivity, CreateTenantWorkerDeploymentInput{
			Name:              PyckGoDeploymentName(org.TemporalNamespace),
			TenantID:          org.TenantID,
			TemporalNamespace: org.TemporalNamespace,
			Extension:         pyckGoExtension,
		}, nil)
}

// fail records a provisioning failure on the tenant row (status=error) and
// returns the step's error unchanged, which is what the run fails with and
// the resolver reports. The status flip runs on a disconnected context so
// a cancelled run still leaves its mark; when the flip itself fails the row
// stays pending and the resolver's re-read reports that instead; the
// original error is still the one that surfaces.
//
// The mark does not inherit the failing step's budget: a step that narrows
// its options (the worker identity runs on a minute to bound the tokens it
// mints) would otherwise shorten the one activity that guarantees the row
// reaches a terminal state.
func fail(ctx workflow.Context, tenantID uuid.UUID, step string, err error) error {
	logger := workflow.GetLogger(ctx)
	logger.Error("tenant registration failed", "tenant_id", tenantID, "step", step, "err", err)

	ctx = workflow.WithActivityOptions(ctx, activityOptions(ctx, activityScheduleToClose))
	statusCtx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	statusInput := SetTenantStatusActivityInput{TenantID: tenantID, Status: enttenant.StatusError}
	if statusErr := workflow.ExecuteActivity(statusCtx, activities.SetTenantStatusActivity, statusInput).Get(statusCtx, nil); statusErr != nil {
		logger.Error("could not mark tenant registration as failed", "tenant_id", tenantID, "err", statusErr)
		// The mark is a write, so its verdict on the row is the
		// authoritative one: a row it finds deleted was retired while the
		// run was in flight.
		var appErr *temporal.ApplicationError
		if errors.As(statusErr, &appErr) && appErr.Type() == appErrTypeTenantRowMissing {
			retire(statusCtx, tenantID)
		}
	}
	return err
}

// retire deactivates the organization of a row deleteTenant retired while
// the run was provisioning it. The delete event's DisableTenantWorkflow
// deactivates it too, but may have run before this run created it. A
// failure is only logged: the row is gone either way, and the organization
// grants nothing without a live tenant row.
func retire(ctx workflow.Context, tenantID uuid.UUID) {
	in := DeactivateRetiredOrganizationActivityInput{TenantID: tenantID}
	if err := workflow.ExecuteActivity(ctx, activities.DeactivateRetiredOrganizationActivity, in).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Error("could not deactivate the organization of a retired tenant", "tenant_id", tenantID, "err", err)
	}
}
