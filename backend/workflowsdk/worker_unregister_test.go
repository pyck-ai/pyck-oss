package workflowsdk_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporalclient "go.temporal.io/sdk/client"
	temporalworkflow "go.temporal.io/sdk/workflow"

	pyckworkflowapi "github.com/pyck-ai/pyck/backend/workflow/api"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
	"github.com/pyck-ai/pyck/backend/workflowsdk/registry"
)

var errUnregisterFailed = errors.New("workflow service unavailable")

// fakeWorkflowAPI is a hand-written pyckworkflowapi.Client. Only the calls the
// worker is allowed to make are implemented; the embedded nil interface panics
// on anything else. GetWorkflows and DeleteWorkflow are implemented solely to
// record that the SDK never calls them.
type fakeWorkflowAPI struct {
	pyckworkflowapi.Client

	mu           sync.Mutex
	registered   []pyckworkflowapi.RegisterWorkflowArgs
	unregistered []string
	getCalls     int
	deleteCalls  int

	registerErr error
	// failRegisters makes the first N RegisterWorkflow calls fail with
	// errUnregisterFailed before calls start succeeding.
	failRegisters int
	registerTimes []time.Time
	// events records "register" and "unregister" in the order the calls
	// returned or were made.
	events []string
	// registerGate, when set, makes the next RegisterWorkflow call signal
	// registerEntered and block until the gate is closed, ignoring its ctx
	// like a request already on the wire.
	registerGate    chan struct{}
	registerEntered chan struct{}
	// gateHonoursCtx makes the gated call behave like the real client: it
	// returns ctx.Err() as soon as its ctx is cancelled, while the request it
	// sent keeps running and "commits" (records its register event) only once
	// the gate opens.
	gateHonoursCtx bool
	// failWorkflows makes RegisterWorkflow fail persistently for the named
	// workflows.
	failWorkflows map[string]error
	unregisterErr error
	// block, when set, makes UnregisterWorker wait for ctx expiry.
	block bool
	// ctxErr records the ctx error seen when an unregister call returned.
	ctxErr error
}

func (f *fakeWorkflowAPI) GetWorkflows(context.Context, pyckworkflowapi.GetWorkflowsArgs) (*pyckworkflowapi.GetWorkflows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	return &pyckworkflowapi.GetWorkflows{}, nil
}

func (f *fakeWorkflowAPI) DeleteWorkflow(context.Context, pyckworkflowapi.DeleteWorkflowArgs) (*pyckworkflowapi.DeleteWorkflow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	return &pyckworkflowapi.DeleteWorkflow{}, nil
}

func (f *fakeWorkflowAPI) RegisterWorkflow(ctx context.Context, in pyckworkflowapi.RegisterWorkflowArgs) (*pyckworkflowapi.RegisterWorkflow, error) {
	f.mu.Lock()
	gate, entered, honours := f.registerGate, f.registerEntered, f.gateHonoursCtx
	f.registerGate = nil
	f.mu.Unlock()
	if gate != nil {
		close(entered)
		if honours {
			committed := make(chan struct{})
			go func() {
				<-gate
				_, _ = f.commitRegister(in) //nolint:errcheck // the caller already gave up on this request
				close(committed)
			}()
			select {
			case <-committed:
				return &pyckworkflowapi.RegisterWorkflow{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		<-gate
	}

	return f.commitRegister(in)
}

func (f *fakeWorkflowAPI) commitRegister(in pyckworkflowapi.RegisterWorkflowArgs) (*pyckworkflowapi.RegisterWorkflow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "register")
	f.registered = append(f.registered, in)
	f.registerTimes = append(f.registerTimes, time.Now())
	if err := f.failWorkflows[in.Input.Name]; err != nil {
		return nil, err
	}
	if f.failRegisters > 0 {
		f.failRegisters--
		return nil, errUnregisterFailed
	}
	return &pyckworkflowapi.RegisterWorkflow{}, f.registerErr
}

func (f *fakeWorkflowAPI) UnregisterWorker(ctx context.Context, in pyckworkflowapi.UnregisterWorkerArgs) (*pyckworkflowapi.UnregisterWorker, error) {
	f.mu.Lock()
	f.unregistered = append(f.unregistered, in.WorkerID)
	f.events = append(f.events, "unregister")
	block, err := f.block, f.unregisterErr
	f.mu.Unlock()

	if block {
		<-ctx.Done()
		f.mu.Lock()
		f.ctxErr = ctx.Err()
		f.mu.Unlock()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return &pyckworkflowapi.UnregisterWorker{}, nil
}

func (f *fakeWorkflowAPI) unregisterCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unregistered...)
}

func testEntries() []registry.WorkflowRegistryEntry {
	return []registry.WorkflowRegistryEntry{
		{
			RegisterOptions: temporalworkflow.RegisterOptions{Name: "wf_one"},
			StartOptions:    temporalclient.StartWorkflowOptions{TaskQueue: "queue-one"},
		},
		{
			RegisterOptions: temporalworkflow.RegisterOptions{Name: "wf_two"},
			StartOptions:    temporalclient.StartWorkflowOptions{TaskQueue: "queue-two"},
		},
	}
}

// setUnregisterConfig sets the process-wide SDK config for one test and
// restores it afterwards. Callers must not run in parallel.
func setUnregisterConfig(t *testing.T, enabled bool, timeout time.Duration) {
	t.Helper()

	prevEnabled, prevTimeout := workflowsdk.Config.UnregisterOnStop, workflowsdk.Config.UnregisterTimeout
	prevAttempts, prevBackoff := workflowsdk.Config.RegistrationRetryAttempts, workflowsdk.Config.RegistrationRetryBackoff
	t.Cleanup(func() {
		workflowsdk.Config.UnregisterOnStop, workflowsdk.Config.UnregisterTimeout = prevEnabled, prevTimeout
		workflowsdk.Config.RegistrationRetryAttempts, workflowsdk.Config.RegistrationRetryBackoff = prevAttempts, prevBackoff
	})

	workflowsdk.Config.UnregisterOnStop = enabled
	workflowsdk.Config.UnregisterTimeout = timeout
	workflowsdk.Config.RegistrationRetryAttempts = 1
	workflowsdk.Config.RegistrationRetryBackoff = time.Millisecond
}

func TestDefaultWorkerIdentity(t *testing.T) {
	t.Parallel()

	format := regexp.MustCompile(`^\d+@.+#[a-z0-9]{6}$`)

	a := workflowsdk.NewIdentityWorker(temporalclient.Options{}).Identity()
	b := workflowsdk.NewIdentityWorker(temporalclient.Options{}).Identity()

	assert.Regexp(t, format, a)
	assert.Regexp(t, format, b)
	assert.NotEqual(t, a, b, "each worker instance gets its own identity")

	assert.Equal(t, "custom-identity",
		workflowsdk.NewIdentityWorker(temporalclient.Options{Identity: "custom-identity"}).Identity(),
		"an identity from the env-derived options wins")
}

func TestDefaultWorkerIdentity_WithClientOptions(t *testing.T) {
	t.Parallel()

	format := regexp.MustCompile(`^\d+@.+#[a-z0-9]{6}$`)

	// WithClientOptions replaces the options wholesale; the default identity
	// must still be applied afterwards.
	w := workflowsdk.NewIdentityWorker(
		temporalclient.Options{Identity: "from-env"},
		workflowsdk.WithClientOptions(temporalclient.Options{HostPort: "localhost:7233"}),
	)
	assert.Regexp(t, format, w.Identity(), "options without an identity still get the default")

	kept := workflowsdk.NewIdentityWorker(
		temporalclient.Options{},
		workflowsdk.WithClientOptions(temporalclient.Options{Identity: "caller-identity"}),
	)
	assert.Equal(t, "caller-identity", kept.Identity(), "a caller-supplied identity is preserved")
}

// Replicas that pin one client identity (env or WithClientOptions) must not
// share subscription worker IDs: one replica's unregister would then delete the
// other's rows. The SDK appends a per-instance suffix to a caller-supplied
// identity for the worker ID only; the Temporal client identity is unchanged.
func TestSubscriptionWorkerID_PinnedIdentityGetsPerInstanceSuffix(t *testing.T) {
	t.Parallel()

	newPinned := func() *workflowsdk.TestWorker {
		return workflowsdk.NewIdentityWorker(
			temporalclient.Options{},
			workflowsdk.WithClientOptions(temporalclient.Options{Identity: "pinned"}),
		)
	}
	a, b := newPinned(), newPinned()

	assert.Equal(t, "pinned", a.Identity(), "the client identity stays as configured")
	assert.Equal(t, "pinned", b.Identity())

	idRe := regexp.MustCompile(`^pinned#[a-z0-9]{6}/orders$`)
	idA, idB := a.SubscriptionWorkerID("orders"), b.SubscriptionWorkerID("orders")
	assert.Regexp(t, idRe, idA)
	assert.Regexp(t, idRe, idB)
	assert.NotEqual(t, idA, idB,
		"two workers with the same configured identity need distinct worker IDs")
	assert.Equal(t, idA, a.SubscriptionWorkerID("orders"),
		"the ID is stable for one worker's lifetime")

	// A generated default identity is already unique and is used as is.
	d := workflowsdk.NewIdentityWorker(temporalclient.Options{})
	assert.Equal(t, d.Identity()+"/orders", d.SubscriptionWorkerID("orders"))
}

// The 255-byte worker ID limit must count the suffix of a pinned identity.
func TestCheckWorkerIDLengths_CountsPinnedIdentitySuffix(t *testing.T) {
	t.Parallel()

	queue := strings.Repeat("q", 200)
	entries := []registry.WorkflowRegistryEntry{{
		RegisterOptions: temporalworkflow.RegisterOptions{Name: "wf"},
		StartOptions:    temporalclient.StartWorkflowOptions{TaskQueue: queue},
	}}
	// identity + "/" + queue is exactly 255 bytes: fits without a suffix, not with.
	identity := strings.Repeat("i", 255-1-len(queue))
	w := workflowsdk.NewIdentityWorker(
		temporalclient.Options{},
		workflowsdk.WithClientOptions(temporalclient.Options{Identity: identity}),
	)

	require.ErrorIs(t, w.CheckWorkerIDLengths(entries), workflowsdk.ErrWorkerIDTooLong)
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStartupRegistration(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	api := &fakeWorkflowAPI{}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	require.NoError(t, w.RegisterLocal(context.Background(), testEntries()))

	assert.Zero(t, api.getCalls, "startup registration must not list other workers' workflows")
	assert.Zero(t, api.deleteCalls, "startup registration must not delete workflows")

	require.Len(t, api.registered, 2)
	for i, in := range api.registered {
		assert.Equal(t, w.Identity()+"/"+testEntries()[i].TaskQueue(), in.Input.WorkerID,
			"registration carries <identity>/<task queue>")
		assert.Equal(t, testEntries()[i].Type(), in.Input.Name)
		assert.Equal(t, testEntries()[i].TaskQueue(), in.Input.TaskQueue)
	}
}

// workerIDsByWorkflow maps each registered workflow name to the worker ID it
// was registered under.
func workerIDsByWorkflow(api *fakeWorkflowAPI) map[string]string {
	api.mu.Lock()
	defer api.mu.Unlock()
	out := make(map[string]string, len(api.registered))
	for _, in := range api.registered {
		out[in.Input.Name] = in.Input.WorkerID
	}
	return out
}

// Two task queues of one process must not share a worker ID: they would then
// share subscription rows, so one queue's unregister or refresh would touch the
// other's.
//
//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStartupRegistration_WorkerIDPerTaskQueue(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	entries := append(testEntries(), registry.WorkflowRegistryEntry{
		RegisterOptions: temporalworkflow.RegisterOptions{Name: "wf_three"},
		StartOptions:    temporalclient.StartWorkflowOptions{TaskQueue: "queue-one"},
	})
	api := &fakeWorkflowAPI{}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	require.NoError(t, w.RegisterLocal(context.Background(), entries))

	assert.Equal(t, map[string]string{
		"wf_one":   "1@host#abc123/queue-one",
		"wf_two":   "1@host#abc123/queue-two",
		"wf_three": "1@host#abc123/queue-one",
	}, workerIDsByWorkflow(api), "each workflow registers under its own task queue's worker ID")
}

// After a failed heartbeat the worker retries with a short backoff instead of
// waiting a whole interval (events are dropped while its rows are missing), and
// returns to the normal interval once a heartbeat succeeds.
//
//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestHeartbeat_RetriesSoonAfterFailureThenResumesInterval(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	prevInterval, prevBackoff := workflowsdk.Config.HeartbeatInterval, workflowsdk.Config.HeartbeatRetryBackoff
	t.Cleanup(func() {
		workflowsdk.Config.HeartbeatInterval = prevInterval
		workflowsdk.Config.HeartbeatRetryBackoff = prevBackoff
	})
	const interval = 400 * time.Millisecond
	workflowsdk.Config.HeartbeatInterval = interval
	workflowsdk.Config.HeartbeatRetryBackoff = 5 * time.Millisecond

	// Two failed heartbeats, then the service is back. A failed attempt stops
	// at the first workflow, so each failure is one call and the successful
	// attempt is two (wf_one, wf_two).
	api := &fakeWorkflowAPI{failRegisters: 2}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.RunHeartbeat(ctx, testEntries())
		close(done)
	}()
	// Failures 1 and 2, the recovery (2 calls), then the next regular tick (2).
	require.Eventually(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.registerTimes) >= 6
	}, 5*time.Second, 5*time.Millisecond)
	cancel()
	<-done

	api.mu.Lock()
	times := append([]time.Time(nil), api.registerTimes...)
	api.mu.Unlock()

	// times[0], times[1]: failed attempts; times[2]: the recovering attempt's
	// first call; times[4]: the following regular attempt.
	assert.Less(t, times[2].Sub(times[0]), interval/2,
		"retries after a failure must not wait a full interval")
	assert.GreaterOrEqual(t, times[4].Sub(times[2]), interval*3/4,
		"after a success the normal interval applies again")
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestHeartbeat_RefreshesEachTaskQueueUnderItsOwnWorkerID(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	prev := workflowsdk.Config.HeartbeatInterval
	t.Cleanup(func() { workflowsdk.Config.HeartbeatInterval = prev })
	workflowsdk.Config.HeartbeatInterval = 10 * time.Millisecond

	api := &fakeWorkflowAPI{}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.RunHeartbeat(ctx, testEntries())
		close(done)
	}()
	require.Eventually(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.registered) >= 2
	}, 5*time.Second, 5*time.Millisecond)
	cancel()
	<-done

	assert.Equal(t, map[string]string{
		"wf_one": "1@host#abc123/queue-one",
		"wf_two": "1@host#abc123/queue-two",
	}, workerIDsByWorkflow(api))
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStartupRegistration_TaskQueueChangedFailsFast(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)
	workflowsdk.Config.RegistrationRetryAttempts = 5

	api := &fakeWorkflowAPI{
		registerErr: errors.New(`workflow task queue cannot be changed: workflow "wf_one" is registered on task queue "a", not "b"`),
	}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	err := w.RegisterLocal(context.Background(), testEntries())
	require.Error(t, err)
	assert.Len(t, api.registered, len(testEntries()), "a task queue change is not a transient conflict: one attempt per workflow, no retry")
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_UnregistersWhenEnabled(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	api := &fakeWorkflowAPI{}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")
	w.MarkRegistered(context.Background(), testEntries())

	w.Stop()
	assert.Equal(t, []string{"1@host#abc123/queue-one", "1@host#abc123/queue-two"}, api.unregisterCalls(),
		"one unregister per worker ID the worker registered")

	w.Stop()
	assert.Len(t, api.unregisterCalls(), 2, "a repeated Stop must not unregister twice")
}

// A heartbeat refresh already in flight when Stop runs must finish before the
// unregister: registerWorkflow clears the stopped mark, so a refresh landing
// after the unregister would undo it.
//
//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_WaitsForInFlightHeartbeatBeforeUnregister(t *testing.T) {
	setUnregisterConfig(t, true, 5*time.Second)

	prev := workflowsdk.Config.HeartbeatInterval
	t.Cleanup(func() { workflowsdk.Config.HeartbeatInterval = prev })
	workflowsdk.Config.HeartbeatInterval = 5 * time.Millisecond

	gate := make(chan struct{})
	entered := make(chan struct{})
	api := &fakeWorkflowAPI{registerGate: gate, registerEntered: entered}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")
	entries := testEntries()[:1]

	ctx := context.Background()
	w.MarkRegistered(ctx, entries)
	w.StartHeartbeat(ctx, entries)

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the heartbeat never called RegisterWorkflow")
	}

	stopped := make(chan struct{})
	go func() {
		w.Stop()
		close(stopped)
	}()

	// The refresh is still on the wire: Stop must neither unregister nor return.
	assert.Never(t, func() bool {
		select {
		case <-stopped:
			return true
		default:
		}
		return len(api.unregisterCalls()) > 0
	}, 200*time.Millisecond, 10*time.Millisecond, "Stop must wait for the in-flight refresh")

	close(gate)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the refresh returned")
	}

	api.mu.Lock()
	events := append([]string(nil), api.events...)
	api.mu.Unlock()
	require.Contains(t, events, "unregister")
	require.Contains(t, events, "register")
	for i, e := range events {
		if e == "unregister" {
			assert.NotContains(t, events[i:], "register", "no refresh may return after the unregister")
		}
	}
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_SkipsUnregisterWhenDisabled(t *testing.T) {
	setUnregisterConfig(t, false, time.Second)

	api := &fakeWorkflowAPI{}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")
	w.MarkRegistered(context.Background(), testEntries())

	w.Stop()
	assert.Empty(t, api.unregisterCalls())
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_SkipsUnregisterWhenNeverRegistered(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	api := &fakeWorkflowAPI{}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	w.Stop()
	assert.Empty(t, api.unregisterCalls())
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_UnregisterSurvivesCancelledStartContext(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	api := &fakeWorkflowAPI{}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")
	ctx, cancel := context.WithCancel(context.Background())
	w.MarkRegistered(ctx, testEntries())
	cancel() // the usual shutdown trigger

	w.Stop()
	assert.Equal(t, []string{"1@host#abc123/queue-one", "1@host#abc123/queue-two"}, api.unregisterCalls(),
		"unregister must not inherit the cancellation")
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_UnregisterBoundedByTimeout(t *testing.T) {
	setUnregisterConfig(t, true, 100*time.Millisecond)

	api := &fakeWorkflowAPI{block: true}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")
	w.MarkRegistered(context.Background(), testEntries())

	done := make(chan struct{})
	start := time.Now()
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return although the unregister call blocks")
	}

	assert.Less(t, time.Since(start), 3*time.Second)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.ErrorIs(t, api.ctxErr, context.DeadlineExceeded, "the call is bounded by UnregisterTimeout")
}

//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_UnregisterFailureDoesNotBlockShutdown(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	api := &fakeWorkflowAPI{unregisterErr: errUnregisterFailed}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")
	w.MarkRegistered(context.Background(), testEntries())

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung after an unregister failure")
	}
	assert.Len(t, api.unregisterCalls(), 2, "a failed unregister does not stop the other worker IDs from being tried")
}

func TestStart_RejectsTooLongWorkerIDEarly(t *testing.T) {
	t.Parallel()

	w := workflowsdk.NewTestWorker(&fakeWorkflowAPI{}, "1@host#abc123")

	require.NoError(t, w.CheckWorkerIDLengths(testEntries()))

	// Exactly at the service limit (255 bytes) is fine, one byte over is not.
	identityLen := len(w.Identity()) + 1 // plus the "/"
	atLimit := strings.Repeat("q", 255-identityLen)
	require.NoError(t, w.CheckWorkerIDLengths([]registry.WorkflowRegistryEntry{{
		RegisterOptions: temporalworkflow.RegisterOptions{Name: "wf_limit"},
		StartOptions:    temporalclient.StartWorkflowOptions{TaskQueue: atLimit},
	}}))

	tooLong := atLimit + "q"
	err := w.CheckWorkerIDLengths([]registry.WorkflowRegistryEntry{{
		RegisterOptions: temporalworkflow.RegisterOptions{Name: "wf_long"},
		StartOptions:    temporalclient.StartWorkflowOptions{TaskQueue: tooLong},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), tooLong, "the error names the task queue")
	assert.Contains(t, err.Error(), "255")
}

// The real client returns the moment its ctx is cancelled, while the request it
// already sent keeps running in the gateway and the service and can commit
// later. Stop must therefore not treat the client returning as the refresh
// being finished: the refresh has to run on a ctx that survives the heartbeat
// cancellation, so Stop waits for the request itself.
//
//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStop_InFlightHeartbeatCannotCommitAfterUnregister(t *testing.T) {
	setUnregisterConfig(t, true, 5*time.Second)

	prev := workflowsdk.Config.HeartbeatInterval
	t.Cleanup(func() { workflowsdk.Config.HeartbeatInterval = prev })
	workflowsdk.Config.HeartbeatInterval = 5 * time.Millisecond

	gate := make(chan struct{})
	entered := make(chan struct{})
	api := &fakeWorkflowAPI{registerGate: gate, registerEntered: entered, gateHonoursCtx: true}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")
	entries := testEntries()[:1]

	ctx := context.Background()
	w.MarkRegistered(ctx, entries)
	w.StartHeartbeat(ctx, entries)

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the heartbeat never called RegisterWorkflow")
	}

	stopped := make(chan struct{})
	go func() {
		w.Stop()
		close(stopped)
	}()

	// Let the request commit a little later, as the gateway would.
	time.AfterFunc(100*time.Millisecond, func() { close(gate) })

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	// Give a request that outlived Stop time to commit.
	time.Sleep(200 * time.Millisecond)

	api.mu.Lock()
	events := append([]string(nil), api.events...)
	api.mu.Unlock()
	require.Contains(t, events, "unregister")
	for i, e := range events {
		if e == "unregister" {
			assert.NotContains(t, events[i:], "register", "no refresh may commit after the unregister")
		}
	}
}

// A heartbeat pass must refresh every workflow even when an earlier one fails
// persistently; otherwise the later workflows' subscriptions expire.
//
//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestHeartbeat_RefreshesRemainingWorkflowsAfterOneFails(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	prev := workflowsdk.Config.HeartbeatInterval
	t.Cleanup(func() { workflowsdk.Config.HeartbeatInterval = prev })
	workflowsdk.Config.HeartbeatInterval = 10 * time.Millisecond

	api := &fakeWorkflowAPI{failWorkflows: map[string]error{"wf_one": errUnregisterFailed}}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.RunHeartbeat(ctx, testEntries())
		close(done)
	}()
	require.Eventually(t, func() bool {
		return workerIDsByWorkflow(api)["wf_two"] != ""
	}, 5*time.Second, 5*time.Millisecond, "wf_two must be refreshed although wf_one fails every tick")
	cancel()
	<-done
}

// A registration pass reports every failure, not only the first, and still
// registers the workflows after a failing one.
//
//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStartupRegistration_ReportsAllFailures(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	api := &fakeWorkflowAPI{failWorkflows: map[string]error{
		"wf_one": errUnregisterFailed,
		"wf_two": errors.New("second failure"),
	}}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	err := w.RegisterLocal(context.Background(), testEntries())
	require.Error(t, err)
	require.ErrorIs(t, err, errUnregisterFailed)
	require.ErrorContains(t, err, "second failure")
	assert.Len(t, api.registered, 2)
}

// Start fails when registration fails partway; the workflows registered before
// the failure must be unregistered, because RunDefaultWorker exits without
// running a deferred Stop.
//
//nolint:paralleltest // mutates the process-wide workflowsdk.Config
func TestStartupRegistration_PartialFailureUnregistersRegistered(t *testing.T) {
	setUnregisterConfig(t, true, time.Second)

	api := &fakeWorkflowAPI{failWorkflows: map[string]error{
		"wf_two": errors.New(`workflow task queue cannot be changed: workflow "wf_two" is registered on task queue "a", not "b"`),
	}}
	w := workflowsdk.NewTestWorker(api, "1@host#abc123")

	err := w.RegisterSubscriptions(context.Background(), testEntries())
	require.Error(t, err)
	assert.Contains(t, api.unregisterCalls(), "1@host#abc123/queue-one",
		"the workflow registered before the failure must be unregistered")
}
