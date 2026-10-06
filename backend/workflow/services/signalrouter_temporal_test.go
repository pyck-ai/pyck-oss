package services_test

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	commontemporal "github.com/pyck-ai/pyck/backend/common/services/temporal"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

func nativeTemporalCLI(t *testing.T) string {
	t.Helper()

	// The tests that need a real Temporal server run only when TEMPORAL_CLI_PATH
	// points at a native `temporal` binary.
	path := os.Getenv("TEMPORAL_CLI_PATH")
	if path == "" {
		t.Skip("TEMPORAL_CLI_PATH is not set: point it at a native temporal CLI binary to run this test")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Skipf("no temporal CLI at %s (set TEMPORAL_CLI_PATH)", path)
	}
	defer func() { require.NoError(t, f.Close()) }()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "\x7fELF" {
		t.Skipf("%s is not a native binary (set TEMPORAL_CLI_PATH)", path)
	}

	return path
}

// routedCounter counts "sig" signals (query "count") until "done".
func routedCounter(ctx workflow.Context, _ any) (int, error) {
	count := 0
	if err := workflow.SetQueryHandler(ctx, "count", func() (int, error) { return count, nil }); err != nil {
		return 0, err
	}

	sigCh, doneCh := workflow.GetSignalChannel(ctx, "sig"), workflow.GetSignalChannel(ctx, "done")

	for done := false; !done; {
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(sigCh, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil); count++ })
		sel.AddReceive(doneCh, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil); done = true })
		sel.Select(ctx)
	}

	return count, nil
}

// temporalRouterEnv is a fanoutEnv whose router talks to a real Temporal dev
// server through the production client stack: commontemporal.NewTemporalClient
// for the root connection and DefaultClientFactory for the per-tenant
// namespace client derived from it.
type temporalRouterEnv struct {
	*fanoutEnv

	ctx    context.Context //nolint:containedctx // test helper
	client *commonworkflow.Client

	// raw is the tenant namespace's plain SDK client, for what the pyck client
	// does not offer (terminating a run).
	raw client.Client
}

func newTemporalRouterEnv(t *testing.T) *temporalRouterEnv {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
		ExistingPath: nativeTemporalCLI(t),
		LogLevel:     "error",
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, srv.Stop()) })

	root, err := commontemporal.NewTemporalClient(ctx, srv.FrontendHostPort(), 30*time.Second)
	require.NoError(t, err)

	factory := commonworkflow.NewDefaultClientFactory(ctx, root, srv.FrontendHostPort(), 30*time.Second)
	t.Cleanup(factory.Close)

	e := newFanoutEnv(t)
	e.router = services.NewSignalRouter(e.db, services.SignalRouterConfig{ClientFactory: factory})

	// Creates the tenant's namespace and search attributes, as the router's
	// first event for a tenant would.
	wfClient, err := factory.GetClient(ctx, e.tenantID.String())
	require.NoError(t, err)

	// A worker in the tenant's namespace serving the subscribed workflow.
	wc, err := client.DialContext(ctx, client.Options{HostPort: srv.FrontendHostPort(), Namespace: e.tenantID.String()})
	require.NoError(t, err)
	t.Cleanup(wc.Close)

	w := worker.New(wc, fanoutTaskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(routedCounter, workflow.RegisterOptions{Name: e.wf.Name})
	require.NoError(t, w.Start())
	t.Cleanup(w.Stop)

	return &temporalRouterEnv{fanoutEnv: e, ctx: ctx, client: wfClient, raw: wc}
}

func (e *temporalRouterEnv) count(t *testing.T, run client.WorkflowRun) int {
	t.Helper()

	var n int

	require.NoError(t, e.client.QueryWorkflow(e.ctx, run.GetID(), run.GetRunID(), "count", nil, &n))

	return n
}

// waitListed waits until visibility lists n running executions of the workflow,
// the query the router's signal fan-out uses.
func (e *temporalRouterEnv) waitListed(t *testing.T, n int) {
	t.Helper()

	query := `CloseTime is null AND TaskQueue = "` + fanoutTaskQueue + `" AND pyck_workflow_name = "` + e.wf.Name + `"`

	require.Eventually(t, func() bool {
		got, err := e.client.ListWorkflows(e.ctx, query)

		return err == nil && len(got) == n
	}, 30*time.Second, 100*time.Millisecond)
}

func TestRouter_Temporal_RedeliveredStartIsDropped(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigStart, "", "")

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())

	first, err := e.publishEvent("create", entityID, eventID)
	require.NoError(t, err)
	require.Len(t, first, 1)

	// DefaultCreatePolicy fails a second start of a taken workflow ID with
	// WorkflowExecutionAlreadyStarted. The same request ID must not.
	second, err := e.publishEvent("create", entityID, eventID)
	require.NoError(t, err, "a redelivery of the same event is not an error")
	require.Len(t, second, 1)

	t.Logf("first: %+v second: %+v", *first[0], *second[0])
	assert.Equal(t, first[0].RunID, second[0].RunID, "the redelivery returns the run the first delivery started")

	// Another event for the same entity is another request: the create policy
	// refuses the start, which the router treats as a finished outcome (no new
	// run, no error, nothing returned).
	third, err := e.publishEvent("create", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err, "a refused create start is not a failure")
	assert.Empty(t, third, "a different event ID for a taken workflow ID starts nothing")
}

func TestRouter_Temporal_RedeliveredStartAfterCloseStartsNothing(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigStart, "", "")

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())

	// DefaultUpdatePolicy allows a duplicate once the previous run has closed:
	// without a request ID the redelivery below would start a second run.
	first, err := e.publishEvent("update", entityID, eventID)
	require.NoError(t, err)
	require.Len(t, first, 1)

	require.NoError(t, e.client.SignalWorkflow(e.ctx, first[0].ID, first[0].RunID, "done", nil))

	var result int
	require.NoError(t, e.client.GetWorkflowResult(e.ctx, first[0].ID, first[0].RunID, &result))

	second, err := e.publishEvent("update", entityID, eventID)
	require.NoError(t, err)
	require.Len(t, second, 1)

	t.Logf("first: %s second: %s", first[0].RunID, second[0].RunID)
	assert.Equal(t, first[0].RunID, second[0].RunID, "the closed run is returned, no new run starts")

	// A different event for the same entity does start a new run.
	third, err := e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
	require.Len(t, third, 1)
	assert.NotEqual(t, first[0].RunID, third[0].RunID)
}

func TestRouter_Temporal_RedeliveredSignalIsDropped(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigInter, "sig", "")

	run, err := e.client.StartWorkflowWithOptions(e.ctx, e.wf.Name, nil, &commonworkflow.StartWorkflowOptions{
		ID: "running-" + uuid.NewString(), TaskQueue: fanoutTaskQueue,
	})
	require.NoError(t, err)
	t.Cleanup(func() { e.cancelRun(t, run.GetID(), run.GetRunID()) })

	e.waitListed(t, 1)

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())

	for range 2 {
		_, err := e.publishEvent("update", entityID, eventID)
		require.NoError(t, err)
	}

	// Let the worker drain, then check the count stays at one.
	require.Eventually(t, func() bool { return e.count(t, run) >= 1 }, 10*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, e.count(t, run), "the same event signalled twice is received once")

	_, err = e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return e.count(t, run) == 2 }, 10*time.Second, 50*time.Millisecond,
		"a different event is a new signal")
}

func TestRouter_Temporal_EventWithoutIDIsNotDeduplicated(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigInter, "sig", "")

	run, err := e.client.StartWorkflowWithOptions(e.ctx, e.wf.Name, nil, &commonworkflow.StartWorkflowOptions{
		ID: "running-" + uuid.NewString(), TaskQueue: fanoutTaskQueue,
	})
	require.NoError(t, err)
	t.Cleanup(func() { e.cancelRun(t, run.GetID(), run.GetRunID()) })

	e.waitListed(t, 1)

	entityID := uuid.New()

	for range 2 {
		_, err := e.publishEvent("update", entityID, uuid.Nil)
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool { return e.count(t, run) == 2 }, 10*time.Second, 50*time.Millisecond,
		"without an event ID the SDK's random request IDs apply, as before")
}

// executionsOf counts the executions visibility lists for the entity's workflow ID.
func (e *temporalRouterEnv) executionsOf(t *testing.T, entityID uuid.UUID) int {
	t.Helper()

	query := `WorkflowId = "` + e.wf.Name + "_" + entityID.String() + `"`

	got, err := e.client.ListWorkflows(e.ctx, query)
	require.NoError(t, err)

	return len(got)
}

// countByID queries the signal count of the entity's current run. It is -1 while
// the workflow is not started or not yet served by the worker.
func (e *temporalRouterEnv) countByID(t *testing.T, entityID uuid.UUID) int {
	t.Helper()

	id := e.wf.Name + "_" + entityID.String()

	runID := e.runningRun(id)
	if runID == "" {
		return -1
	}

	var n int
	if e.client.QueryWorkflow(e.ctx, id, runID, "count", nil, &n) != nil {
		return -1
	}

	return n
}

// TestRouter_Temporal_SWSTwiceForOneEntity: two events of one entity reach one
// execution, which gets one signal per event. The first event also started it,
// as input, which routedCounter ignores, so the count is the signals.
func TestRouter_Temporal_SWSTwiceForOneEntity(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigSWS, "sig", "")

	entityID := uuid.New()

	_, err := e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	id := e.wf.Name + "_" + entityID.String()
	t.Cleanup(func() { e.cancelCurrentAtCleanup(t, id) })

	require.Eventually(t, func() bool { return e.countByID(t, entityID) == 1 }, 15*time.Second, 50*time.Millisecond, "the first event started the workflow and is also its first signal")

	_, err = e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return e.countByID(t, entityID) == 2 }, 15*time.Second, 50*time.Millisecond,
		"the second event signalled the running execution")

	e.waitIndexed(t, entityID, 1)
	assert.Equal(t, 1, e.executionsOf(t, entityID), "one execution for two events")
}

// TestRouter_Temporal_SWSRedeliveryAddsNoSignal: the same event again, even with
// a different run already there, signals nothing more.
func TestRouter_Temporal_SWSRedeliveryAddsNoSignal(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigSWS, "sig", "")

	entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())

	for range 3 {
		_, err := e.publishEvent("update", entityID, eventID)
		require.NoError(t, err)
	}

	id := e.wf.Name + "_" + entityID.String()
	t.Cleanup(func() { e.cancelCurrentAtCleanup(t, id) })

	require.Eventually(t, func() bool { return e.countByID(t, entityID) >= 1 }, 15*time.Second, 50*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, 1, e.countByID(t, entityID), "a redelivery of the same event is dropped by its request ID")

	_, err := e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return e.countByID(t, entityID) == 2 }, 15*time.Second, 50*time.Millisecond)
}

// TestRouter_Temporal_ByIDWithoutExecutionStartsNothing: signal by ID never
// starts, and is not an error.
func TestRouter_Temporal_ByIDWithoutExecutionStartsNothing(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigByID, "sig", "")

	entityID := uuid.New()

	started, err := e.publishEvent("update", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err, "a policy drop is not an error")
	assert.Empty(t, started)

	time.Sleep(500 * time.Millisecond)
	assert.Zero(t, e.executionsOf(t, entityID), "nothing was started")
}

// TestRouter_Temporal_ByIDSignalsOnlyItsOwnEntity: the signal goes to the
// event's workflow, not to other running executions of the workflow.
func TestRouter_Temporal_ByIDSignalsOnlyItsOwnEntity(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigByID, "sig", "")

	mine, other := uuid.New(), uuid.New()

	for _, entity := range []uuid.UUID{mine, other} {
		id := e.wf.Name + "_" + entity.String()
		run, err := e.client.StartWorkflowWithOptions(e.ctx, e.wf.Name, nil, &commonworkflow.StartWorkflowOptions{ID: id, TaskQueue: fanoutTaskQueue})
		require.NoError(t, err)
		t.Cleanup(func() { e.cancelRun(t, run.GetID(), run.GetRunID()) })
	}

	_, err := e.publishEvent("update", mine, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return e.countByID(t, mine) == 1 }, 15*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, e.countByID(t, other), "another entity's execution is not signalled")
}

// waitIndexed waits until visibility lists n executions of the entity's workflow.
func (e *temporalRouterEnv) waitIndexed(t *testing.T, entityID uuid.UUID, n int) {
	t.Helper()

	require.Eventually(t, func() bool { return e.executionsOf(t, entityID) == n }, 15*time.Second, 100*time.Millisecond)
}

// TestRouter_Temporal_SWSAfterAnotherEventsRunFinishedFollowsTheReusePolicy
// records the limit of the redelivery check: Temporal does not say which signals
// a closed run received, so a Signal-With-Start for a run this event did not
// start (here one with no memo) follows the reuse policy. create: only after a
// failure (a completed run refuses), update: always a new run, delete: never.
func TestRouter_Temporal_SWSAfterAnotherEventsRunFinishedFollowsTheReusePolicy(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ newRun bool }{
		"create": {newRun: false},
		"update": {newRun: true},
		"delete": {newRun: false},
	}

	for op, want := range cases {
		e := newTemporalRouterEnv(t)
		e.subscribe("worker-a", sigSWS, "sig", "")

		entityID := uuid.New()
		id := e.wf.Name + "_" + entityID.String()

		first, err := e.client.StartWorkflowWithOptions(e.ctx, e.wf.Name, nil, &commonworkflow.StartWorkflowOptions{ID: id, TaskQueue: fanoutTaskQueue})
		require.NoError(t, err, op)
		require.NoError(t, e.client.SignalWorkflow(e.ctx, id, first.GetRunID(), "done", nil), op)

		var result int
		require.NoError(t, e.client.GetWorkflowResult(e.ctx, id, first.GetRunID(), &result), op)

		_, err = e.publishEvent(op, entityID, uuid.Must(uuid.NewV7()))
		require.NoError(t, err, "%s: a refusal is a drop, not an error", op)

		e.waitIndexed(t, entityID, map[bool]int{true: 2, false: 1}[want.newRun])
		t.Logf("%s after a completed run: new run = %v", op, want.newRun)

		e.cancelCurrentAtCleanup(t, id)
	}
}

// finishCurrentRun tells the current run of the entity's workflow to finish and
// waits for its result.
func (e *temporalRouterEnv) finishCurrentRun(t *testing.T, id string) string {
	t.Helper()

	info, err := e.client.DescribeLatestRun(e.ctx, id)
	require.NoError(t, err)

	runID := info.GetExecution().GetRunId()

	require.NoError(t, e.client.SignalWorkflow(e.ctx, id, runID, "done", nil))

	var result int
	require.NoError(t, e.client.GetWorkflowResult(e.ctx, id, runID, &result))

	return runID
}

// TestRouter_Temporal_SWSRedeliveryAfterTheRunFinishedStartsNoSecondRun: event E
// starts a run through Signal-With-Start and the run finishes before E is
// redelivered (another target of E failed, say). Temporal applies the reuse
// policy to a repeated call for a closed run without looking at its request ID,
// so the router describes the latest run, sees E's ID in its memo and sends
// nothing: no second run under update, no dropped target under create and
// delete.
func TestRouter_Temporal_SWSRedeliveryAfterTheRunFinishedStartsNoSecondRun(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"create", "update", "delete"} {
		e := newTemporalRouterEnv(t)
		e.subscribe("worker-a", sigSWS, "sig", "")

		entityID, eventID := uuid.New(), uuid.Must(uuid.NewV7())
		id := e.wf.Name + "_" + entityID.String()

		_, err := e.publishEvent(op, entityID, eventID)
		require.NoError(t, err, op)

		firstRun := e.finishCurrentRun(t, id)

		// The redelivery.
		e.delivery = 2
		e.trackRouting()

		_, err = e.publishEvent(op, entityID, eventID)
		require.NoError(t, err, op)

		targets := services.CollectedTargets(e.fanoutEnv.ctx)
		require.Len(t, targets, 1, op)
		assert.Equal(t, services.RoutingTargetSignalled, targets[0].Kind, "%s: delivered, not dropped (%s)", op, targets[0].Reason)
		assert.Equal(t, firstRun, targets[0].RunID, op)

		e.waitIndexed(t, entityID, 1)
		time.Sleep(300 * time.Millisecond)
		assert.Equal(t, 1, e.executionsOf(t, entityID), "%s: the redelivery started no second run", op)

		// A different event of the entity is not a redelivery: it follows the
		// reuse policy as before.
		e.delivery = 1
		_, err = e.publishEvent(op, entityID, uuid.Must(uuid.NewV7()))
		require.NoError(t, err, op)

		if op == "update" {
			e.waitIndexed(t, entityID, 2)
			e.cancelCurrentAtCleanup(t, id)
		}
	}
}

// cancelRun cancels a run at the end of a test. A run that already finished
// cannot be cancelled, so a failure is logged, not fatal.
func (e *temporalRouterEnv) cancelRun(t *testing.T, id, runID string) {
	t.Helper()

	if err := e.client.CancelWorkflow(context.Background(), id, runID); err != nil {
		t.Logf("cancel %s/%s: %v", id, runID, err)
	}
}

// cancelCurrentAtCleanup cancels the current run of a workflow ID at the end
// of a test, logging instead of failing like cancelRun.
func (e *temporalRouterEnv) cancelCurrentAtCleanup(t *testing.T, id string) {
	t.Helper()

	if err := e.cancelCurrent(id); err != nil {
		t.Logf("cancel current run of %s: %v", id, err)
	}
}

// cancelCurrent cancels the current run of a workflow ID.
func (e *temporalRouterEnv) cancelCurrent(id string) error {
	runID := e.runningRun(id)
	if runID == "" {
		return nil
	}

	return e.client.CancelWorkflow(context.Background(), id, runID)
}

// runningRun is the run ID of the running execution of a workflow ID, found
// through visibility, or "" if there is none.
func (e *temporalRouterEnv) runningRun(id string) string {
	got, err := e.client.ListWorkflows(context.Background(), `WorkflowId = "`+id+`" AND CloseTime is null`)
	if err != nil || len(got) == 0 {
		return ""
	}

	return got[0].GetExecution().GetRunId()
}

// TestRouter_Temporal_SWSCreateAfterAFailedRunStartsAgain: the create policy
// allows a new run after a failed (here terminated) one, the case the
// completed-run test above does not cover.
func TestRouter_Temporal_SWSCreateAfterAFailedRunStartsAgain(t *testing.T) {
	t.Parallel()

	e := newTemporalRouterEnv(t)
	e.subscribe("worker-a", sigSWS, "sig", "")

	entityID := uuid.New()
	id := e.wf.Name + "_" + entityID.String()

	first, err := e.client.StartWorkflowWithOptions(e.ctx, e.wf.Name, nil, &commonworkflow.StartWorkflowOptions{ID: id, TaskQueue: fanoutTaskQueue})
	require.NoError(t, err)

	e.waitIndexed(t, entityID, 1)
	// A terminated run ends in a failed status, which is what
	// ALLOW_DUPLICATE_FAILED_ONLY lets a new run replace.
	require.NoError(t, e.raw.TerminateWorkflow(e.ctx, id, first.GetRunID(), "test"))

	_, err = e.publishEvent("create", entityID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	e.waitIndexed(t, entityID, 2)
	t.Cleanup(func() { e.cancelCurrentAtCleanup(t, id) })
}

// TestRouter_Temporal_UpdateWhileAnUpdateRunsIsAlreadyRunning: one run per
// entity per workflow. A second update event for an entity whose update
// workflow still runs starts nothing and must not be reported as a start (the
// SDK used to hand back the running run, which the router recorded as
// "started"). A redelivery of the first event still returns its run.
func TestRouter_Temporal_UpdateWhileAnUpdateRunsIsAlreadyRunning(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"update", "delete"} {
		e := newTemporalRouterEnv(t)
		e.subscribe("worker-a", sigStart, "", "")

		entityID, eventA := uuid.New(), uuid.Must(uuid.NewV7())

		first, err := e.publishEvent(op, entityID, eventA)
		require.NoError(t, err, op)
		require.Len(t, first, 1, op)

		id := e.wf.Name + "_" + entityID.String()
		t.Cleanup(func() { e.cancelCurrentAtCleanup(t, id) })

		second, err := e.publishEvent(op, entityID, uuid.Must(uuid.NewV7()))
		require.NoError(t, err, "%s: a refused start is a finished outcome", op)
		assert.Empty(t, second, "%s: event B started nothing and must not report run A as its start", op)

		again, err := e.publishEvent(op, entityID, eventA)
		require.NoError(t, err, op)
		require.Len(t, again, 1, "%s: a redelivery of A is not an error", op)
		assert.Equal(t, first[0].RunID, again[0].RunID, "%s: a redelivery of A returns A's run", op)
	}
}
