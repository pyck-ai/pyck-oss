package services_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/feature"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

func newWorkflow(t *testing.T, client *ent.Client, tenantID uuid.UUID, name, queue string) *ent.Workflow {
	t.Helper()
	return client.Workflow.Create().
		SetTenantID(tenantID).
		SetName(name).
		SetTaskQueue(queue).
		SaveX(seedCtx(tenantID))
}

func newSignalFor(t *testing.T, client *ent.Client, wf *ent.Workflow, worker string, expiresAt time.Time) {
	t.Helper()
	client.WorkflowSignal.Create().
		SetTenantID(wf.TenantID).
		SetWorkflowID(wf.ID).
		SetNatsTopic("pyck.topic." + worker).
		SetTemporalSignalType(entworkflowsignal.TemporalSignalTypeIntermediate).
		SetTemporalSignal("OrderCreated").
		SetWorkerID(worker).
		SetExpiresAt(expiresAt).
		SaveX(seedCtx(wf.TenantID))
}

func isWorkflowDeleted(t *testing.T, client *ent.Client, wf *ent.Workflow) bool {
	t.Helper()
	ctx := feature.Context(seedCtx(wf.TenantID), feature.FEATURE_SHOW_DELETED)
	got := client.Workflow.GetX(ctx, wf.ID)
	return !got.DeletedAt.IsZero()
}

func TestSubscriptionJanitor_Sweep(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	client := newExpiryTestClient(t)
	wf := newWorkflow(t, client, tenantID, "wf_sweep", "queue")
	newSignalFor(t, client, wf, "worker-dead", time.Now().UTC().Add(-time.Hour))
	newSignalFor(t, client, wf, "worker-live", time.Now().UTC().Add(time.Hour))

	n, err := services.NewSubscriptionJanitor(client, time.Minute).Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	remaining := client.WorkflowSignal.Query().Where(entworkflowsignal.WorkflowIDEQ(wf.ID)).AllX(seedCtx(tenantID))
	require.Len(t, remaining, 1)
	assert.Equal(t, "worker-live", remaining[0].WorkerID)
	assert.False(t, isWorkflowDeleted(t, client, wf))
}

// TestSubscriptionJanitor_NeverDeletesWorkflows pins that a workflow whose
// subscriptions have all expired keeps its row however long the janitor runs:
// Temporal's poller history is in memory with a short TTL, so "no pollers"
// also describes a worker outage or a Temporal restart, and deleting on it
// would lose the workflow (pyck#1564).
func TestSubscriptionJanitor_NeverDeletesWorkflows(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	client := newExpiryTestClient(t)
	wf := newWorkflow(t, client, tenantID, "wf_outage", "queue")
	newSignalFor(t, client, wf, "worker-dead", time.Now().UTC().Add(-time.Hour))

	// Let the janitor tick many times, then stop it before reading: the SQLite
	// test database rejects concurrent access.
	ctx, cancel := context.WithCancel(context.Background())
	services.NewSubscriptionJanitor(client, 10*time.Millisecond).Start(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()
	time.Sleep(100 * time.Millisecond)

	signals := client.WorkflowSignal.Query().Where(entworkflowsignal.WorkflowIDEQ(wf.ID)).CountX(seedCtx(tenantID))
	assert.Zero(t, signals, "the expired subscription is removed")
	assert.False(t, isWorkflowDeleted(t, client, wf), "the workflow row must never be soft-deleted")
}
