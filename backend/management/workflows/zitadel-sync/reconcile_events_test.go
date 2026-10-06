package zitadel_sync_test

import (
	"context"
	"strings"
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
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	zitadelsync "github.com/pyck-ai/pyck/backend/management/workflows/zitadel-sync"
)

const testAudience = "test-audience"

// setupReconcile builds an in-memory client wired like pyck-management/main.go
// (real MutationEventHook, Tenant as its own tenant) and activities on top.
// The Zitadel connection settings are unreachable on purpose: the tests below
// only reach code paths that do not dial Zitadel.
func setupReconcile(t *testing.T) (*ent.Client, *zitadelsync.Activities) {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	client.Use(events.MutationEventHook(events.HookConfig{
		Service:           "management",
		StreamName:        "pyck",
		SelfTenantSchemas: core.SelfTenantSchemas(),
		EntityFetcher:     events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter:    events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	a := zitadelsync.NewActivities(client, nil, "http://127.0.0.1:0", "127.0.0.1:0", testAudience, "", "", true)
	return client, a
}

// runReconcile executes ReconcileTenantsActivity through Temporal's activity
// test environment (the activity needs an activity context for its logger).
func runReconcile(t *testing.T, a *zitadelsync.Activities, in zitadelsync.ReconcileTenantsActivityInput) error {
	t.Helper()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.ReconcileTenantsActivity)

	done := make(chan error, 1)
	go func() {
		_, err := env.ExecuteActivity(a.ReconcileTenantsActivity, in)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("ReconcileTenantsActivity did not return in 30s")
		return nil
	}
}

// seedTenant inserts a tenant for the Zitadel org with events suppressed, so
// only the reconcile's own events land in the outbox.
func seedTenant(t *testing.T, client *ent.Client, orgID, name string) uuid.UUID {
	t.Helper()
	id := authn.ComputeUUID(testAudience, orgID)
	ctx := feature.Context(authn.Context(context.Background(), authn.SystemUser()), feature.FEATURE_SUPPRESS_EVENTS)
	_, err := client.Tenant.Create().SetID(id).SetName(name).SetIdpOrgRef(orgID).Save(ctx)
	require.NoError(t, err)
	return id
}

func outboxTopics(t *testing.T, client *ent.Client) []string {
	t.Helper()
	rows, err := client.EntityEventsOutbox.Query().
		Order(ent.Asc(entityeventsoutbox.FieldCreatedAt)).
		All(context.Background())
	require.NoError(t, err)
	topics := make([]string, 0, len(rows))
	for _, r := range rows {
		topics = append(topics, r.Topic)
	}
	return topics
}

func hasTopic(topics []string, id uuid.UUID, op string) bool {
	for _, tp := range topics {
		if strings.Contains(tp, ".management.tenant."+id.String()+".") && strings.HasSuffix(tp, "."+op) {
			return true
		}
	}
	return false
}

// Tenants whose Zitadel org disappeared are soft-deleted one UpdateOneID each,
// so every one of them gets its own tenant.<id>.delete event (DisableTenantWorkflow
// + revocation eviction hang off it).
func TestReconcileTenants_SoftDeleteEmitsDeleteEventPerTenant(t *testing.T) {
	t.Parallel()

	client, a := setupReconcile(t)
	idA := seedTenant(t, client, "org-a", "A")
	idB := seedTenant(t, client, "org-b", "B")
	idKeep := seedTenant(t, client, "org-keep", "Keep")

	// org-a and org-b are gone from Zitadel. No org is in both lists, so no
	// Zitadel metadata fetch is attempted.
	err := runReconcile(t, a, zitadelsync.ReconcileTenantsActivityInput{
		ZitadelTenants: nil,
		DbTenants: []zitadelsync.Tenant{
			{ID: "org-a", Name: "A"},
			{ID: "org-b", Name: "B"},
		},
	})
	require.NoError(t, err)

	topics := outboxTopics(t, client)
	assert.Len(t, topics, 2, "one event per soft-deleted tenant: %v", topics)
	assert.True(t, hasTopic(topics, idA, "delete"), "missing delete event for A: %v", topics)
	assert.True(t, hasTopic(topics, idB, "delete"), "missing delete event for B: %v", topics)
	assert.False(t, hasTopic(topics, idKeep, "delete"))

	// Each event is published under its own tenant (tenantID == entityID).
	for _, id := range []uuid.UUID{idA, idB} {
		found := false
		for _, tp := range topics {
			if strings.HasPrefix(tp, "pyck."+id.String()+".crud.management.tenant."+id.String()+".") {
				found = true
			}
		}
		assert.True(t, found, "no event under tenant %s: %v", id, topics)
	}

	// Rows really are soft-deleted; the untouched tenant is still active.
	ctx := feature.Context(authn.Context(context.Background(), authn.SystemUser()), feature.FEATURE_SHOW_DELETED)
	for _, id := range []uuid.UUID{idA, idB} {
		row, err := client.Tenant.Get(ctx, id)
		require.NoError(t, err)
		assert.False(t, row.DeletedAt.IsZero(), "tenant %s must be soft-deleted", id)
	}
	keep, err := client.Tenant.Get(ctx, idKeep)
	require.NoError(t, err)
	assert.True(t, keep.DeletedAt.IsZero())
}

// A tenant whose name changed in Zitadel is renamed with a tenant-scoped
// UpdateOneID, which emits a tenant update event under that tenant.
func TestReconcileTenants_RenameEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	client, a := setupReconcile(t)
	id := seedTenant(t, client, "org-r", "Old Name")

	// The org is in both lists, so the activity fetches org metadata through a
	// Zitadel client. The address is unreachable: the fetch is best-effort and
	// logs and continues (fetchAllOrgMetadata), leaving zitadelData empty, so
	// only the name differs.
	require.NoError(t, runReconcile(t, a, zitadelsync.ReconcileTenantsActivityInput{
		ZitadelTenants: []zitadelsync.Tenant{{ID: "org-r", Name: "New Name"}},
		DbTenants:      []zitadelsync.Tenant{{ID: "org-r", Name: "Old Name"}},
	}))

	topics := outboxTopics(t, client)
	assert.Len(t, topics, 1, "topics: %v", topics)
	assert.True(t, hasTopic(topics, id, "update"), "missing update event: %v", topics)

	row, err := client.Tenant.Get(authn.Context(context.Background(), authn.SystemUser()), id)
	require.NoError(t, err)
	assert.Equal(t, "New Name", row.Name)
}

// seedSoftDeletedTenant inserts an already soft-deleted tenant for the Zitadel
// org (events suppressed), standing in for a tenant that was soft-deleted after
// the activity input was computed.
func seedSoftDeletedTenant(t *testing.T, client *ent.Client, orgID, name string) uuid.UUID {
	t.Helper()
	id := authn.ComputeUUID(testAudience, orgID)
	ctx := feature.Context(authn.Context(context.Background(), authn.SystemUser()), feature.FEATURE_SUPPRESS_EVENTS)
	_, err := client.Tenant.Create().SetID(id).SetName(name).SetIdpOrgRef(orgID).SetDeletedAt(time.Now().UTC()).Save(ctx)
	require.NoError(t, err)
	return id
}

// A tenant soft-deleted between the DB snapshot and the reconcile no longer
// matches UpdateOneID: it is skipped, and the other vanished tenants in the
// same transaction still commit and emit their delete events.
func TestReconcileTenants_SoftDeleteSkipsTenantAlreadyDeleted(t *testing.T) {
	t.Parallel()

	client, a := setupReconcile(t)
	idA := seedTenant(t, client, "org-a", "A")
	idGone := seedSoftDeletedTenant(t, client, "org-gone", "Gone")
	idB := seedTenant(t, client, "org-b", "B")

	// All three are missing from Zitadel and present in the snapshot; org-gone
	// is already soft-deleted in the DB. Map iteration order is random, so the
	// skipped tenant may come before, between or after the others.
	err := runReconcile(t, a, zitadelsync.ReconcileTenantsActivityInput{
		DbTenants: []zitadelsync.Tenant{
			{ID: "org-a", Name: "A"},
			{ID: "org-gone", Name: "Gone"},
			{ID: "org-b", Name: "B"},
		},
	})
	require.NoError(t, err, "a tenant that is already soft-deleted is not an error")

	topics := outboxTopics(t, client)
	assert.Len(t, topics, 2, "one event per tenant actually soft-deleted: %v", topics)
	assert.True(t, hasTopic(topics, idA, "delete"), "missing delete event for A: %v", topics)
	assert.True(t, hasTopic(topics, idB, "delete"), "missing delete event for B: %v", topics)
	assert.False(t, hasTopic(topics, idGone, "delete"), "no event for the skipped tenant: %v", topics)

	ctx := feature.Context(authn.Context(context.Background(), authn.SystemUser()), feature.FEATURE_SHOW_DELETED)
	for _, id := range []uuid.UUID{idA, idB} {
		row, err := client.Tenant.Get(ctx, id)
		require.NoError(t, err)
		assert.False(t, row.DeletedAt.IsZero(), "tenant %s must be soft-deleted and committed", id)
	}
}
