package tenantexpirycheck_test

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	tenantexpirycheck "github.com/pyck-ai/pyck/backend/management/workflows/tenant-expiry-check"
)

// setup builds an in-memory SQLite Ent client + a fresh Activities
// wired to it + a system-user context.
func setup(t *testing.T) (*ent.Client, *tenantexpirycheck.Activities, context.Context) {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { _ = client.Close() })

	sysCtx := authn.Context(context.Background(), authn.SystemUser())
	return client, tenantexpirycheck.NewActivities(client), sysCtx
}

// createTenant inserts a tenant row. Pass time.Time{} for expiresAt or
// deletedAt to leave the column null.
func createTenant(t *testing.T, ctx context.Context, client *ent.Client, id uuid.UUID, expiresAt time.Time, deletedAt time.Time) *ent.Tenant {
	t.Helper()
	q := client.Tenant.Create().
		SetID(id).
		SetName("test-tenant-" + id.String()).
		SetIdpOrgRef(id.String())
	if !expiresAt.IsZero() {
		q = q.SetExpiresAt(expiresAt)
	}
	if !deletedAt.IsZero() {
		q = q.SetDeletedAt(deletedAt)
	}
	row, err := q.Save(ctx)
	require.NoError(t, err)
	return row
}

// getTenantRaw reads the tenant including soft-deleted rows. Tests
// need this to assert deleted_at after the activity supposedly fired.
func getTenantRaw(t *testing.T, ctx context.Context, client *ent.Client, id uuid.UUID) *ent.Tenant {
	t.Helper()
	rows, err := client.Tenant.Query().
		Where(enttenant.IDEQ(id)).
		Limit(1).
		All(feature.Context(ctx, feature.FEATURE_SHOW_DELETED))
	require.NoError(t, err)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// ============================================================================
// Happy path
// ============================================================================

func TestSoftDeleteExpiredTenant_HappyPath(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	pastExpiry := time.Now().UTC().Add(-1 * time.Hour)
	createTenant(t, ctx, client, id, pastExpiry, time.Time{})

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.False(t, got.DeletedAt.IsZero(), "deleted_at must be set on the happy path")
}

// Find captured the candidate when it was expired; admin then extended
// the expiry; activity must not override it.
func TestSoftDeleteExpiredTenant_ExpiryExtended_NoOp(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	futureExpiry := time.Now().UTC().Add(90 * 24 * time.Hour)
	createTenant(t, ctx, client, id, futureExpiry, time.Time{})

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err, "must not error — predicate-no-match is benign")

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.True(t, got.DeletedAt.IsZero(), "admin's expiry extension must survive")
	require.NotNil(t, got.ExpiresAt)
	assert.True(t, got.ExpiresAt.After(time.Now().UTC()))
}

// ============================================================================
// Idempotency cases — all of these must no-op
// ============================================================================

func TestSoftDeleteExpiredTenant_AlreadyDeleted_NoOp(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	pastExpiry := time.Now().UTC().Add(-1 * time.Hour)
	priorDelete := time.Now().UTC().Add(-30 * time.Minute)
	createTenant(t, ctx, client, id, pastExpiry, priorDelete)

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.True(t, got.DeletedAt.Truncate(time.Second).Equal(priorDelete.Truncate(time.Second)),
		"prior deleted_at must not be refreshed by a redundant retry")
}

func TestSoftDeleteExpiredTenant_TenantNotInDb_NoOp(t *testing.T) {
	t.Parallel()
	_, activities, ctx := setup(t)

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: uuid.New()})
	require.NoError(t, err)
}

// Defensive: even if a future find-query regression lets a nil-expiry
// tenant into the candidate set, the activity's predicate-restate must
// reject it.
func TestSoftDeleteExpiredTenant_ExpiresAtNil_NoOp(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	createTenant(t, ctx, client, id, time.Time{} /* no expiry */, time.Time{})

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.True(t, got.DeletedAt.IsZero(), "tenant without expiry must not be soft-deleted")
	assert.Nil(t, got.ExpiresAt)
}

// ============================================================================
// Edge case: expires_at exactly equals now (the LTE boundary)
// ============================================================================

func TestSoftDeleteExpiredTenant_ExpiryEqualsNow_BoundaryInclusive(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	// Set expiry to a time guaranteed to be <= time.Now() when the
	// activity fires (small clock-skew buffer).
	expiryAtBoundary := time.Now().UTC().Add(-1 * time.Millisecond)
	createTenant(t, ctx, client, id, expiryAtBoundary, time.Time{})

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.False(t, got.DeletedAt.IsZero(),
		"expires_at < now must soft-delete (LTE boundary inclusive)")
}

// ============================================================================
// Outbox event semantics — n==0 must not produce an event
// ============================================================================

// When the predicate-bound update matches 0 rows the MutationEventHook
// must not write an outbox row. A spurious delete event with no row
// state behind it would trigger DisableTenantWorkflow erroneously and
// be much worse than the original M4 bug.
//
// This test asserts the column directly via raw SQL — bypassing the
// soft-delete filter — to confirm the tenant row was not touched on
// any code path that took the n==0 branch.
func TestSoftDeleteExpiredTenant_NoMutation_NoEventStateChange(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	futureExpiry := time.Now().UTC().Add(90 * 24 * time.Hour)
	createTenant(t, ctx, client, id, futureExpiry, time.Time{})

	before := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, before)

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	after := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, after)

	// updated_at must not advance when nothing changed — Ent only bumps
	// it on actual writes. If it did advance, the predicate-bound update
	// silently fired a no-op write, which would also trigger the hook.
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt),
		"updated_at must not advance when n==0 — proxy for 'hook did not fire'")
	assert.True(t, after.DeletedAt.IsZero())
}
