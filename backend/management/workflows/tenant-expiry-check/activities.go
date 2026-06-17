package tenantexpirycheck

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
)

type (
	// Activities groups the side-effect activities for the expiry
	// check. Dependency injection only — no globals.
	Activities struct {
		ent *ent.Client
	}
)

// activityRefs is a nil-receiver Activities used only so the workflow
// can pass type-safe method references to workflow.ExecuteActivity
// instead of string names. Same pattern as tenant-reconcile.
var activityRefs *Activities

// NewActivities wires the dependencies for the expiry-check activities.
func NewActivities(entClient *ent.Client) *Activities {
	return &Activities{ent: entClient}
}

// FindExpiredTenantsActivity returns every tenant whose expires_at is
// non-null, has passed, and which is not already soft-deleted. The
// query is cheap because the candidate set is normally tiny — most
// tenants either have no expiry or are still in the future.
func (a *Activities) FindExpiredTenantsActivity(ctx context.Context, _ FindExpiredTenantsActivityInput) ([]ExpiredTenantRef, error) {
	logger := activity.GetLogger(ctx)
	sysCtx := authn.Context(ctx, authn.SystemUser())

	now := time.Now().UTC()
	rows, err := a.ent.Tenant.Query().
		Where(
			enttenant.ExpiresAtNotNil(),
			enttenant.ExpiresAtLTE(now),
			enttenant.DeletedAtIsNil(),
		).
		AllPages(sysCtx, mixin.Limit)
	if err != nil {
		return nil, fmt.Errorf("query expired tenants: %w", err)
	}

	out := make([]ExpiredTenantRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, ExpiredTenantRef{TenantID: r.ID, IdpOrgRef: r.IdpOrgRef})
	}
	logger.Info("expired tenants computed", "now", now, "count", len(out))
	return out, nil
}

// SoftDeleteExpiredTenantActivity sets deleted_at on the tenant via a
// predicate-bound update that re-checks the expiry condition atomically
// with the write. The find activity's snapshot is information, not
// authority — an admin can extend the expiry or restore the tenant
// between find and per-item delete, and the schedule must respect the
// current DB state at write time.
//
// The MutationEventHook writes an outbox row in the same Ent tx;
// OutboxHandler publishes pyck.<tenant>.crud.management.tenant.<id>.deleted
// which drives DisableTenantWorkflow (Zitadel deactivation) and the
// per-service revocation-cache eviction.
//
// Idempotent: n == 0 means the predicate no longer matches (tenant
// gone, already deleted, expiry extended, or no expiry). Returns nil.
func (a *Activities) SoftDeleteExpiredTenantActivity(ctx context.Context, input SoftDeleteExpiredTenantActivityInput) (err error) {
	// txid stamps the outbox row's tx-scoped UUID (NATS message-ID
	// component for dedup). Without it MutationEventHook fails with
	// ErrNoTransactionID — the GraphQL path gets one from gqltx.
	sysCtx := authn.Context(ctx, authn.SystemUser())
	sysCtx = txid.With(sysCtx, txid.New())

	tx, err := a.ent.Tx(sysCtx)
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
	sysCtx = ent.NewTxContext(sysCtx, tx)

	now := time.Now().UTC()
	if _, err = tx.Tenant.Update().
		Where(
			enttenant.IDEQ(input.TenantID),
			enttenant.ExpiresAtNotNil(),
			enttenant.ExpiresAtLTE(now),
		).
		SetDeletedAt(now).
		Save(sysCtx); err != nil {
		return fmt.Errorf("soft-delete tenant: %w", err)
	}
	return nil
}
