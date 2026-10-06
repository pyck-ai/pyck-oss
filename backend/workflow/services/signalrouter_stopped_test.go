package services_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// stoppedFixture seeds one workflow and hands out subscriptions with a chosen
// owner, expiry and stopped state.
type stoppedFixture struct {
	t        *testing.T
	client   *ent.Client
	tenantID uuid.UUID
	wf       *ent.Workflow
	router   *services.SignalRouter
}

func newStoppedFixture(t *testing.T) *stoppedFixture {
	t.Helper()

	tenantID := uuid.New()
	client := newExpiryTestClient(t)

	return &stoppedFixture{
		t:        t,
		client:   client,
		tenantID: tenantID,
		wf:       newWorkflow(t, client, tenantID, "wf_stopped", "test-queue"),
		router:   services.NewSignalRouter(client, services.SignalRouterConfig{}),
	}
}

// add seeds a subscription with the given filter rule (used as its identity).
func (f *stoppedFixture) add(workerID, filter string, expiresAt time.Time, stopped bool) uuid.UUID {
	f.t.Helper()

	create := f.client.WorkflowSignal.Create().
		SetTenantID(f.tenantID).
		SetWorkflowID(f.wf.ID).
		SetNatsTopic("pyck.orders").
		SetTemporalSignalType(entworkflowsignal.TemporalSignalTypeIntermediate).
		SetTemporalSignal("OrderCreated-" + filter).
		SetFilterRule(filter).
		SetWorkerID(workerID).
		SetExpiresAt(expiresAt)
	if stopped {
		create.SetStoppedAt(time.Now().UTC())
	}

	return create.SaveX(seedCtx(f.tenantID)).ID
}

// routed returns the filter rules of the subscriptions the router would use.
func (f *stoppedFixture) routed() []string {
	f.t.Helper()

	wfs, err := f.router.ActiveWorkflowsWithSignals(seedCtx(f.tenantID), f.tenantID)
	require.NoError(f.t, err)

	var out []string
	for _, wf := range wfs {
		for _, s := range wf.Edges.WorkflowSignals {
			out = append(out, s.FilterRule)
		}
	}

	return out
}

func (f *stoppedFixture) expire(id uuid.UUID) {
	f.t.Helper()
	f.client.WorkflowSignal.UpdateOneID(id).
		SetExpiresAt(time.Now().UTC().Add(-time.Minute)).
		ExecX(seedCtx(f.tenantID))
}

// Jan's case: worker B crashed (live, never stopped) and worker A stopped
// cleanly. The running-but-possibly-dead row wins until it expires, then the
// stopped row bridges, then nothing routes.
func TestActiveWorkflowsWithSignals_CrashedWorkerBeatsStoppedWorker(t *testing.T) {
	t.Parallel()

	f := newStoppedFixture(t)
	future := time.Now().UTC().Add(time.Hour)

	a := f.add("worker-a", "a", future, true)
	b := f.add("worker-b", "b", future, false)

	assert.Equal(t, []string{"b"}, f.routed(), "the live unstopped row routes, the stopped one does not")

	f.expire(b)
	assert.Equal(t, []string{"a"}, f.routed(), "stopped rows bridge once no running row is left")

	f.expire(a)
	assert.Empty(t, f.routed(), "nothing routes once every row has expired")
}

// Rolling deploy: v1 stopped with filter X, v2 running with filter Y. Only Y
// routes, so v1's stale filter cannot start duplicate runs.
func TestActiveWorkflowsWithSignals_RollingDeployRoutesOnlyRunning(t *testing.T) {
	t.Parallel()

	f := newStoppedFixture(t)
	future := time.Now().UTC().Add(time.Hour)

	f.add("worker-v1", "X", future, true)
	f.add("worker-v2", "Y", future, false)

	assert.Equal(t, []string{"Y"}, f.routed())
}

// A stopped row keeps routing when it is the only live one: that is the gap
// between a clean stop and the replacement's first registration.
func TestActiveWorkflowsWithSignals_StoppedOnlyKeepsRouting(t *testing.T) {
	t.Parallel()

	f := newStoppedFixture(t)
	future := time.Now().UTC().Add(time.Hour)

	f.add("worker-a", "a1", future, true)
	f.add("worker-a", "a2", future, true)

	assert.ElementsMatch(t, []string{"a1", "a2"}, f.routed())
}

func TestPreferRunning(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	running := &ent.WorkflowSignal{FilterRule: "run"}
	stopped := &ent.WorkflowSignal{FilterRule: "stop", StoppedAt: &now}

	assert.Empty(t, services.PreferRunning(nil))
	assert.Equal(t, []*ent.WorkflowSignal{running}, services.PreferRunning([]*ent.WorkflowSignal{stopped, running}))
	assert.Equal(t, []*ent.WorkflowSignal{stopped}, services.PreferRunning([]*ent.WorkflowSignal{stopped}))
}
