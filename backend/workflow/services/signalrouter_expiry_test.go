package services_test

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entprivacy "entgo.io/ent/privacy"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/enttest"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// seedCtx bypasses the privacy filters and suppresses CRUD events so fixtures
// can be written without going through the GraphQL layer.
func seedCtx(tenantID uuid.UUID) context.Context {
	ctx := request.Context(context.Background(), authn.SystemUser(), tenantID)
	ctx = feature.Context(ctx, feature.FEATURE_SUPPRESS_EVENTS)
	return entprivacy.DecisionContext(ctx, entprivacy.Allow)
}

func newExpiryTestClient(t *testing.T) *ent.Client {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t))
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})

	return client
}

// TestActiveWorkflowsWithSignals_ExpiryGate covers the read path the janitor
// test does not: a lapsed or soft-deleted subscription stops routing before it
// is reaped, while an unexpired one keeps routing.
func TestActiveWorkflowsWithSignals_ExpiryGate(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	client := newExpiryTestClient(t)
	ctx := seedCtx(tenantID)

	wf := client.Workflow.Create().
		SetTenantID(tenantID).
		SetName("wf_expiry").
		SetTaskQueue("test-queue").
		SaveX(ctx)

	newSignal := func(topic string, expiresAt time.Time) uuid.UUID {
		return client.WorkflowSignal.Create().
			SetTenantID(tenantID).
			SetWorkflowID(wf.ID).
			SetNatsTopic(topic).
			SetTemporalSignalType(entworkflowsignal.TemporalSignalTypeIntermediate).
			SetTemporalSignal("OrderCreated").
			SetWorkerID("worker-a").
			SetExpiresAt(expiresAt).
			SaveX(ctx).ID
	}

	var (
		past   = time.Now().UTC().Add(-time.Hour)
		future = time.Now().UTC().Add(time.Hour)
	)

	liveID := newSignal("pyck.live", future)
	expiredID := newSignal("pyck.expired", past)
	deletedID := newSignal("pyck.deleted", future)
	client.WorkflowSignal.UpdateOneID(deletedID).
		SetDeletedAt(time.Now().UTC()).
		SetDeletedBy(authn.SystemUser().ID).
		ExecX(ctx)

	router := services.NewSignalRouter(client, services.SignalRouterConfig{})

	wfs, err := router.ActiveWorkflowsWithSignals(ctx, tenantID)
	require.NoError(t, err)
	require.Len(t, wfs, 1, "the workflow still has live subscriptions")

	got := make(map[uuid.UUID]bool, len(wfs[0].Edges.WorkflowSignals))
	for _, s := range wfs[0].Edges.WorkflowSignals {
		got[s.ID] = true
	}

	assert.True(t, got[liveID], "an unexpired subscription must keep routing")
	assert.False(t, got[expiredID], "a lapsed subscription must stop routing before the janitor reaps it")
	assert.False(t, got[deletedID], "a soft-deleted subscription must not route")
}

// TestActiveWorkflowsWithSignals_AllExpired asserts a workflow drops out of the
// routing set once its last subscription lapses.
func TestActiveWorkflowsWithSignals_AllExpired(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	client := newExpiryTestClient(t)
	ctx := seedCtx(tenantID)

	wf := client.Workflow.Create().
		SetTenantID(tenantID).
		SetName("wf_all_expired").
		SetTaskQueue("test-queue").
		SaveX(ctx)

	client.WorkflowSignal.Create().
		SetTenantID(tenantID).
		SetWorkflowID(wf.ID).
		SetNatsTopic("pyck.expired").
		SetTemporalSignalType(entworkflowsignal.TemporalSignalTypeIntermediate).
		SetTemporalSignal("OrderCreated").
		SetWorkerID("worker-a").
		SetExpiresAt(time.Now().UTC().Add(-time.Hour)).
		SaveX(ctx)

	router := services.NewSignalRouter(client, services.SignalRouterConfig{})

	wfs, err := router.ActiveWorkflowsWithSignals(ctx, tenantID)
	require.NoError(t, err)
	assert.Empty(t, wfs, "a workflow whose every subscription lapsed must not route")
}
