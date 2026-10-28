package tenantexpirycheck_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"

	"github.com/pyck-ai/pyck/backend/management/core"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	enttenant "github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/types"
	tenantexpirycheck "github.com/pyck-ai/pyck/backend/management/workflows/tenant-expiry-check"
)

// testPasswordHash is the admin secret a not-yet-cleaned registration
// column holds; no outbox payload may carry it.
const testPasswordHash = "$2a$12$hash"

// setup builds an in-memory SQLite Ent client with the production outbox
// hook installed + a fresh Activities wired to it + a system-user
// context. The hook is the reason the sweep is observable at all:
// DisableTenantWorkflow and every service's cache eviction hang off the
// delete event it writes.
func setup(t *testing.T) (*ent.Client, *tenantexpirycheck.Activities, context.Context) {
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

	sysCtx := authn.Context(context.Background(), authn.SystemUser())
	return client, tenantexpirycheck.NewActivities(client), sysCtx
}

// seedTx runs a seeding mutation in its own transaction with a txid and
// drops whatever outbox rows it produced, so each test asserts only the
// events of the activity under test. The outbox hook refuses a mutation
// that carries no transaction id.
func seedTx(t *testing.T, ctx context.Context, client *ent.Client, fn func(ctx context.Context, tx *ent.Tx)) {
	t.Helper()
	seedCtx := txid.With(ctx, txid.New())
	tx, err := client.Tx(seedCtx)
	require.NoError(t, err)
	seedCtx = ent.NewTxContext(seedCtx, tx)

	fn(seedCtx, tx)

	_, err = tx.EntityEventsOutbox.Delete().Exec(seedCtx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

// createTenant inserts a tenant row. Pass time.Time{} for expiresAt or
// deletedAt to leave the column null.
func createTenant(t *testing.T, ctx context.Context, client *ent.Client, id uuid.UUID, expiresAt time.Time, deletedAt time.Time) *ent.Tenant {
	t.Helper()
	var row *ent.Tenant
	seedTx(t, ctx, client, func(ctx context.Context, tx *ent.Tx) {
		q := tx.Tenant.Create().
			SetID(id).
			SetName("test-tenant-" + id.String()).
			SetIdpOrgRef(id.String())
		if !expiresAt.IsZero() {
			q = q.SetExpiresAt(expiresAt)
		}
		if !deletedAt.IsZero() {
			q = q.SetDeletedAt(deletedAt)
		}
		var err error
		row, err = q.Save(ctx)
		require.NoError(t, err)
	})
	return row
}

// outboxRows returns every outbox row, oldest first.
func outboxRows(t *testing.T, ctx context.Context, client *ent.Client) []*ent.EntityEventsOutbox {
	t.Helper()
	rows, err := client.EntityEventsOutbox.Query().
		Order(ent.Asc(entityeventsoutbox.FieldCreatedAt)).
		All(ctx)
	require.NoError(t, err)
	return rows
}

// assertNoEvent states that nothing was published: a delete event with no
// soft delete behind it would deactivate a live tenant's Zitadel org and
// evict every service's token cache for it.
func assertNoEvent(t *testing.T, ctx context.Context, client *ent.Client) {
	t.Helper()
	rows := outboxRows(t, ctx, client)
	topics := make([]string, 0, len(rows))
	for _, r := range rows {
		topics = append(topics, r.Topic)
	}
	assert.Empty(t, topics, "a no-op sweep must publish nothing")
}

// assertDeleteEvent states that the sweep published exactly the tenant's
// own delete event and that its payload carries the soft-deleted row.
// The tenant segment of the topic comes from the mutation tenant in the
// activity's request context (Tenant has no tenant_id column); without
// it the hook writes no row at all. data_after must be the row, not the
// affected-row count a bulk Update().Where() hands the hook: consumers
// drop a payload of the wrong shape.
func assertDeleteEvent(t *testing.T, ctx context.Context, client *ent.Client, id uuid.UUID) map[string]any {
	t.Helper()
	rows := outboxRows(t, ctx, client)
	require.Len(t, rows, 1, "exactly one outbox row")

	assert.Equal(t,
		fmt.Sprintf("%s.%s.crud.%s.%s.%s.%s",
			events.DefaultStreamName, id, topic.ManagementService, topic.TenantSchema, id, events.OpDelete),
		rows[0].Topic)

	payload := rows[0].Payload
	assert.Equal(t, events.OpDelete, payload["operation"], "a soft delete must be classified as a delete")
	assert.Equal(t, id.String(), payload["tenant_id"])
	assert.Equal(t, id.String(), payload["id"])

	after, ok := payload["data_after"].(map[string]any)
	require.True(t, ok, "data_after must decode to the tenant row, got %T (%v)", payload["data_after"], payload["data_after"])
	assert.True(t, topic.IsDeletedAt(after["deleted_at"]),
		"data_after.deleted_at must mark the row deleted, got %v", after["deleted_at"])

	before, ok := payload["data_before"].(map[string]any)
	require.True(t, ok, "data_before must decode to the tenant row, got %T (%v)", payload["data_before"], payload["data_before"])
	assert.False(t, topic.IsDeletedAt(before["deleted_at"]),
		"data_before must be the live row the sweep found")

	return payload
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
	assertDeleteEvent(t, ctx, client, id)
}

// The sweep retries per item; a second run must leave the first run's
// tombstone and its single event alone: a second delete event would
// dispatch DisableTenantWorkflow again.
func TestSoftDeleteExpiredTenant_SecondRunEmitsNoSecondEvent(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	createTenant(t, ctx, client, id, time.Now().UTC().Add(-time.Hour), time.Time{})

	input := tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id}
	require.NoError(t, activities.SoftDeleteExpiredTenantActivity(ctx, input))
	first := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, first)

	require.NoError(t, activities.SoftDeleteExpiredTenantActivity(ctx, input), "the retry must succeed")

	second := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, second)
	assert.True(t, first.DeletedAt.Equal(second.DeletedAt), "the retry must not refresh deleted_at")
	assert.True(t, first.UpdatedAt.Equal(second.UpdatedAt), "the retry must not write")
	assertDeleteEvent(t, ctx, client, id)
}

// Find captured the candidate when it was expired; admin then extended
// the expiry; activity must not override it.
// A soft-deleted row must never hold an admin password hash, whoever set
// its status: the sweep clears the registration column with the delete.
func TestSoftDeleteExpiredTenant_ClearsRegistration(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)
	id := uuid.New()
	createTenant(t, ctx, client, id, time.Now().UTC().Add(-time.Hour), time.Time{})
	seedTx(t, ctx, client, func(ctx context.Context, tx *ent.Tx) {
		require.NoError(t, tx.Tenant.UpdateOneID(id).SetRegistration(&types.TenantRegistration{
			AdminUsername: "admin", AdminEmail: "admin@example.com", AdminFirstName: "Ada", AdminLastName: "Admin", PasswordHash: testPasswordHash,
		}).Exec(ctx))
	})

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.False(t, got.DeletedAt.IsZero())
	assert.Nil(t, got.Registration, "the tombstone must not carry the admin password hash")

	payload := assertDeleteEvent(t, ctx, client, id)
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), testPasswordHash, "the hash must not reach the outbox")
}

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
	assertNoEvent(t, ctx, client)
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
	assertNoEvent(t, ctx, client)
}

func TestSoftDeleteExpiredTenant_TenantNotInDb_NoOp(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: uuid.New()})
	require.NoError(t, err)
	assertNoEvent(t, ctx, client)
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
	assertNoEvent(t, ctx, client)
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
	assertDeleteEvent(t, ctx, client, id)
}

// ============================================================================
// Outbox event semantics: a predicate that matches nothing must not emit
// ============================================================================

// When the predicate-bound update matches no row the MutationEventHook
// must not write an outbox row. A spurious delete event with no row
// state behind it would trigger DisableTenantWorkflow erroneously and
// be much worse than the original M4 bug.
//
// The row is read bypassing the soft-delete filter to confirm it was not
// touched at all on the not-found path.
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
		"updated_at must not advance on the not-found path, proxy for 'no write happened'")
	assert.True(t, after.DeletedAt.IsZero())
	assertNoEvent(t, ctx, client)
}

// A database that is briefly unreachable must fail the activity so
// Temporal retries it. Reporting the fault as a no-op would leave an
// expired tenant live and unnoticed until the next sweep, and if the
// fault persists, silently forever.
func TestSoftDeleteExpiredTenant_DatabaseFailureIsRetried(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	createTenant(t, ctx, client, id, time.Now().UTC().Add(-time.Hour), time.Time{})
	require.NoError(t, client.Close())

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})

	require.Error(t, err, "an unreachable database must not be reported as a no-op")
	require.ErrorContains(t, err, "begin tx", "the error must name the step that failed")
}

// A write that the database rejects mid-transaction (constraint, hook
// failure, connection dropped after BEGIN) must surface so Temporal
// retries the item. Swallowing it would report the sweep as a successful
// no-op while the expired tenant stays live, its Zitadel org active and
// every service's token cache unevicted.
func TestSoftDeleteExpiredTenant_WriteFailureIsSurfaced(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	createTenant(t, ctx, client, id, time.Now().UTC().Add(-time.Hour), time.Time{})

	writeErr := errors.New("write rejected")
	client.Tenant.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpUpdateOne) {
				return nil, writeErr
			}
			return next.Mutate(ctx, m)
		})
	})

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})

	require.ErrorIs(t, err, writeErr, "a rejected write must not be reported as a no-op")
	require.ErrorContains(t, err, "soft-delete tenant", "the error must name the step that failed")

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.True(t, got.DeletedAt.IsZero(), "the failed write must be rolled back")
	assertNoEvent(t, ctx, client)
}

// ============================================================================
// Pending registrations: no Zitadel org exists, so the sweep must skip them
// ============================================================================

// createPendingTenant inserts an expired-but-still-pending tenant: the
// registerTenant resolver committed the row, RegisterTenantWorkflow has
// not finished (or failed before flipping it to done).
func createPendingTenant(t *testing.T, ctx context.Context, client *ent.Client, id uuid.UUID, expiresAt time.Time) {
	t.Helper()
	seedTx(t, ctx, client, func(ctx context.Context, tx *ent.Tx) {
		_, err := tx.Tenant.Create().
			SetID(id).
			SetName("pending-tenant-" + id.String()).
			SetIdpOrgRef(id.String()).
			SetStatus(enttenant.StatusPending).
			SetExpiresAt(expiresAt).
			Save(ctx)
		require.NoError(t, err)
	})
}

// createTenantInStatus inserts an expired tenant row in the given
// registration status.
func createTenantInStatus(t *testing.T, ctx context.Context, client *ent.Client, id uuid.UUID, status enttenant.Status, expiresAt time.Time) {
	t.Helper()
	seedTx(t, ctx, client, func(ctx context.Context, tx *ent.Tx) {
		_, err := tx.Tenant.Create().
			SetID(id).
			SetName(status.String() + "-tenant-" + id.String()).
			SetIdpOrgRef(id.String()).
			SetStatus(status).
			SetExpiresAt(expiresAt).
			Save(ctx)
		require.NoError(t, err)
	})
}

// runFind executes FindExpiredTenantsActivity under a real activity
// context (the activity logs through activity.GetLogger).
func runFind(t *testing.T, activities *tenantexpirycheck.Activities) []tenantexpirycheck.ExpiredTenantRef {
	t.Helper()
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(activities.FindExpiredTenantsActivity)
	val, err := env.ExecuteActivity(activities.FindExpiredTenantsActivity, tenantexpirycheck.FindExpiredTenantsActivityInput{})
	require.NoError(t, err)
	var out []tenantexpirycheck.ExpiredTenantRef
	require.NoError(t, val.Get(&out))
	return out
}

// Soft-deleting a pending tenant would dispatch DisableTenantWorkflow
// against an org that does not exist; the sweep must only return done rows.
func TestFindExpiredTenants_SkipsPendingButReturnsDone(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	pastExpiry := time.Now().UTC().Add(-1 * time.Hour)
	doneID := uuid.New()
	createTenant(t, ctx, client, doneID, pastExpiry, time.Time{})
	pendingID := uuid.New()
	createPendingTenant(t, ctx, client, pendingID, pastExpiry)
	// Not a candidate at all: no expiry.
	createTenant(t, ctx, client, uuid.New(), time.Time{}, time.Time{})

	got := runFind(t, activities)

	require.Len(t, got, 1, "exactly the expired done tenant")
	assert.Equal(t, doneID, got[0].TenantID)
	assert.Equal(t, doneID.String(), got[0].IdpOrgRef)
}

// A failed registration (status=error) has no usable org: expiring it
// would dispatch DisableTenantWorkflow against whatever half-made org is
// behind it. Retiring such a row is the operator's deleteTenant call, not
// the sweep's.
func TestFindExpiredTenants_SkipsErrorRows(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	pastExpiry := time.Now().UTC().Add(-1 * time.Hour)
	doneID := uuid.New()
	createTenant(t, ctx, client, doneID, pastExpiry, time.Time{})
	createTenantInStatus(t, ctx, client, uuid.New(), enttenant.StatusError, pastExpiry)

	got := runFind(t, activities)

	require.Len(t, got, 1, "exactly the expired done tenant")
	assert.Equal(t, doneID, got[0].TenantID)
}

// Defensive mirror of the find test for error rows: the restated
// predicate in the per-item activity must refuse to soft-delete one.
func TestSoftDeleteExpiredTenant_Error_NoOp(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	createTenantInStatus(t, ctx, client, id, enttenant.StatusError, time.Now().UTC().Add(-1*time.Hour))

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.True(t, got.DeletedAt.IsZero(), "a failed registration must not be expired away")
	assert.Equal(t, enttenant.StatusError, got.Status)
	assertNoEvent(t, ctx, client)
}

// Defensive mirror of the find test: even if a pending row reaches the
// per-item activity, the restated predicate must refuse to soft-delete it.
func TestSoftDeleteExpiredTenant_Pending_NoOp(t *testing.T) {
	t.Parallel()
	client, activities, ctx := setup(t)

	id := uuid.New()
	createPendingTenant(t, ctx, client, id, time.Now().UTC().Add(-1*time.Hour))

	err := activities.SoftDeleteExpiredTenantActivity(ctx,
		tenantexpirycheck.SoftDeleteExpiredTenantActivityInput{TenantID: id})
	require.NoError(t, err)

	got := getTenantRaw(t, ctx, client, id)
	require.NotNil(t, got)
	assert.True(t, got.DeletedAt.IsZero(), "a pending registration must not be expired away")
	assert.Equal(t, enttenant.StatusPending, got.Status)
	assertNoEvent(t, ctx, client)
}
