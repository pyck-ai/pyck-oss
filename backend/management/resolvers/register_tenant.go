package resolvers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
	"github.com/pyck-ai/pyck/backend/management/model"
	"github.com/pyck-ai/pyck/backend/management/workflows"
	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

const (
	// registrationBcryptCost is the work factor of the admin password hash
	// that sits on the pending tenant row until the workflow imports it
	// into Zitadel. 12 (~250 ms per hash on current server hardware) is
	// paid once per registration attempt; how many of those hashes a
	// replica runs at once is bounded by the admission cap this mutation
	// is registered under (PYCK_REGISTER_TENANT_MAX_INFLIGHT), not by the
	// caller. Zitadel re-hashes the password with its own hasher on the
	// first login, so the cost only has to protect the hash for the
	// lifetime of the row's registration data.
	registrationBcryptCost = 12
)

// errTenantExists is the refusal for a name already held by any tenant
// row, compared by name_key (types.TenantNameKey) and soft-deleted rows
// included, whether the pre-flight or the unique index caught it.
var errTenantExists = errors.New("tenant with the provided name already exists")

// insertPendingTenant commits the status=pending tenant row in its own
// transaction, mirroring what gqltx does for the request transaction:
// system user, a txid for the outbox row, and the explicit tx in the
// context so MutationEventHook writes the outbox entry into it. Tenant has
// no tenant_id column, so the row's own ID is the mutation tenant the hook
// derives the event topic from; without it no event is emitted and the
// workflow is never started. The row must be visible to the trigger
// consumer while this resolver still waits, which the request transaction
// (committed after the resolver returns) cannot provide.
func (r *mutationResolver) insertPendingTenant(ctx context.Context, tenantID uuid.UUID, orgID string, input model.RegisterTenantInput, passwordHash string) (err error) {
	sysCtx := request.Context(ctx, authn.SystemUser(), tenantID)
	sysCtx = txid.With(sysCtx, txid.New())

	tx, err := r.client.Tx(sysCtx)
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
	sysCtx = ent.NewTxContext(sysCtx, tx)

	create := tx.Tenant.Create().
		SetID(tenantID).
		SetName(input.Name).
		SetIdpOrgRef(orgID).
		SetStatus(enttenant.StatusPending).
		SetRegistration(&types.TenantRegistration{
			AdminUsername:  input.AdminUsername,
			AdminEmail:     input.AdminEmail,
			AdminFirstName: input.AdminFirstName,
			AdminLastName:  input.AdminLastName,
			PasswordHash:   passwordHash,
		})
	if len(input.Data) > 0 {
		create = create.SetData(input.Data)
	}
	if input.ExpiresAt != nil {
		create = create.SetExpiresAt(input.ExpiresAt.UTC())
	}
	if err := create.Exec(sysCtx); err != nil {
		return fmt.Errorf("insert pending tenant: %w", err)
	}
	return nil
}

// tenantNameTaken reports whether any tenant row, soft-deleted ones
// included, holds a name that folds to the same name_key as name: the
// comparison the tenant_name_key unique index behind it makes.
func (r *mutationResolver) tenantNameTaken(ctx context.Context, name string) (bool, error) {
	ctx = feature.Context(ctx, feature.FEATURE_SHOW_DELETED)
	return r.client.Tenant.Query().
		Where(enttenant.NameKey(types.TenantNameKey(name))).
		Exist(ctx)
}

// awaitRegistration blocks until the registration run of the tenant
// closes or ctx ends; the caller bounds ctx by registrationWait. The run
// is started by the tenant-lifecycle trigger on the row's create event, so
// it may not exist yet: NotFound is retried every registrationPoll until
// the wait ends.
// The wait holds no transaction: registerTenant runs under
// gqltx.WithoutTransaction, so no writer connection is pinned for its
// duration. A closed run that failed comes back as a
// *temporal.WorkflowExecutionError; any other error (the wait exhausted,
// Temporal unreachable, request cancelled) means the outcome could not be
// observed.
func (r *mutationResolver) awaitRegistration(ctx context.Context, tenantID uuid.UUID) error {
	workflowID := workflows.RegisterTenantWorkflowID(tenantID)
	for {
		err := r.workflowClient.AwaitWorkflowResult(ctx, workflowID, nil)
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return err
		}

		timer := time.NewTimer(r.registrationPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// readSettledTenant reads the tenant row once its registration run has
// closed. Plain reads go to the reader pool, which can lag the writer the
// run's final status flip went to, so a row still reported pending is read
// again every registrationPoll until it leaves pending or done is closed
// (the end of the registration wait); the last row read is returned.
func (r *mutationResolver) readSettledTenant(ctx context.Context, tenantID uuid.UUID, done <-chan struct{}) (*ent.Tenant, error) {
	for {
		t, err := r.client.Tenant.Get(ctx, tenantID)
		if err != nil || t.Status != enttenant.StatusPending {
			return t, err
		}

		timer := time.NewTimer(r.registrationPoll)
		select {
		case <-done:
			timer.Stop()
			return t, nil
		case <-timer.C:
		}
	}
}

// errRegistrationOutcomeUnknown is the answer to a registration whose
// result the resolver could not see within the registration wait: the run
// is still open or unreachable, or it succeeded but the row still reads
// pending. The row is left to its run; the client checks it by name.
var errRegistrationOutcomeUnknown = errors.New("tenant registration outcome unknown")

// errRegistrationFailed is the answer to a registration whose run marked
// the tenant row error.
var errRegistrationFailed = errors.New("tenant registration failed, tenant row marked error")

// errRegistrationUnsettled is the answer to a registration whose run
// failed with the tenant row in neither done nor error: the status flip
// after the failed step did not land.
var errRegistrationUnsettled = errors.New("tenant registration ended before the tenant row settled")

// errRegistrationInternal is the answer to a registration that failed on
// pyck's own side outside the registration run: the name pre-flight, the
// password hash, the pending row's transaction or the read of the row
// after the run. A failure at the commit or after the run leaves the
// outcome open (the row may exist) so the answer claims neither way and
// the client checks by name before it registers again.
var errRegistrationInternal = errors.New("tenant registration could not be processed")

// registrationInternalError logs err with the step that failed and returns
// errRegistrationInternal naming the tenant. err never reaches the answer:
// it carries the driver's text (Postgres messages naming tables, columns
// and constraints, connection addresses) or bcrypt's, and registerTenant
// is open to anonymous callers.
func registrationInternalError(ctx context.Context, name, step string, err error) error {
	log.ForContext(ctx).Error().Err(err).
		Str("tenant_name", name).
		Str("step", step).
		Msg("tenant registration failed outside its run")
	return fmt.Errorf("%w: tenant %q", errRegistrationInternal, name)
}

// registrationFailure is the error registerTenant answers a failed
// registration with: base, followed by the failing step and a fixed text
// for its application error type (registertenant.FailureMessage).
// No dependency text is ever part of it: Temporal's own error names the
// workflow ID, the run ID and the worker's identity, and an application
// error's message, or the last attempt's failure a timeout wraps, is the
// dependency's raw error (request paths, secret names, response bodies,
// dial addresses). registerTenant is open to anonymous callers, so the
// full chain is logged instead.
func registrationFailure(ctx context.Context, tenantID uuid.UUID, base, waitErr error) error {
	if waitErr == nil {
		return base
	}
	log.ForContext(ctx).Warn().Err(waitErr).
		Str("tenant_id", tenantID.String()).
		Msg("tenant registration failed")

	var actErr *temporal.ActivityError
	if !errors.As(waitErr, &actErr) {
		return base
	}
	// A timed-out step answers the generic text even when its last attempt
	// failed with a typed application error: the timeout is the verdict.
	detail := registertenant.FailureGeneric
	var appErr *temporal.ApplicationError
	var timeoutErr *temporal.TimeoutError
	if !errors.As(waitErr, &timeoutErr) && errors.As(waitErr, &appErr) {
		detail = registertenant.FailureMessage(appErr.Type())
	}
	return fmt.Errorf("%w: step %s, %s", base, actErr.ActivityType().GetName(), detail)
}

// deleteTenantStream removes the stream of a registration whose row was not
// inserted, even if the request is cancelled: an orphan would stay sourced by
// the aggregate. Best effort; a commit that landed despite its error gets the
// stream back from the tenant reconcile schedule (tenants.RepairStreams), and
// its events wait in the outbox until then. It waits for the stream lock at
// most PYCK_EVENT_STREAM_LOCK_WAIT, so a reconcile holding it cannot keep the
// request open; past it the empty stream stays, sourced by the aggregate.
func (r *mutationResolver) deleteTenantStream(ctx context.Context, tenantID uuid.UUID) {
	sysCtx := authn.Context(context.WithoutCancel(ctx), authn.SystemUser())
	if err := r.eventStreams.DeleteTenant(sysCtx, tenantID); err != nil {
		log.ForContext(ctx).Error().Err(err).
			Str("tenant_id", tenantID.String()).
			Msg("failed to delete the event stream of a tenant whose row was not inserted")
	}
}
