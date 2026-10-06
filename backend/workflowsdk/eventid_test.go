package workflowsdk_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/eventid"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

// observed is what the test workflow saw: the event ID of its start and the
// (value, event ID) of each "sig" signal.
type observed struct {
	StartID    string
	StartHasID bool
	Signals    []observedSignal
}

type observedSignal struct {
	Value string
	ID    string
	HasID bool
}

// eventObserver records EventID at start and every "sig" signal through
// ReceiveEvent, until it gets "done".
func eventObserver(ctx workflow.Context) (observed, error) {
	var out observed

	id, ok := workflowsdk.EventID(ctx)
	out.StartHasID = ok
	out.StartID = id.String()

	if err := workflow.SetQueryHandler(ctx, "observed", func() (observed, error) { return out, nil }); err != nil {
		return out, err
	}

	sigCh := workflow.GetSignalChannel(ctx, "sig")
	doneCh := workflow.GetSignalChannel(ctx, "done")

	done := false
	for !done {
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(sigCh, func(c workflow.ReceiveChannel, _ bool) {
			ev, err := workflowsdk.ReceiveEvent[string](ctx, c)
			if err != nil {
				return
			}

			out.Signals = append(out.Signals, observedSignal{Value: ev.Value, ID: ev.ID.String(), HasID: ev.HasID()})
		})
		sel.AddReceive(doneCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			done = true
		})
		sel.Select(ctx)
	}

	return out, nil
}

// headerInjector stands in for the server: the testsuite delivers signals
// without a header, so this outermost interceptor writes one onto the signal's
// context before the event ID interceptor reads it. ids[signalValueIndex] is
// used for the n-th signal; an empty entry sends no header.
type headerInjector struct {
	interceptor.WorkerInterceptorBase

	ids []string
}

func (h *headerInjector) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &injectingInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, h: h}
}

type injectingInbound struct {
	interceptor.WorkflowInboundInterceptorBase

	h *headerInjector
	n int
}

func (i *injectingInbound) HandleSignal(ctx workflow.Context, in *interceptor.HandleSignalInput) error {
	if in.SignalName == "sig" {
		id := ""
		if i.n < len(i.h.ids) {
			id = i.h.ids[i.n]
		}

		i.n++

		if id != "" {
			p, err := eventid.Payload(uuid.MustParse(id))
			if err != nil {
				return err
			}

			interceptor.WorkflowHeader(ctx)[eventid.HeaderKey] = p
		}
	}

	return i.Next.HandleSignal(ctx, in)
}

func newEnvWithInjector(t *testing.T, ids ...string) *testsuite.TestWorkflowEnvironment {
	t.Helper()

	var suite testsuite.WorkflowTestSuite

	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&headerInjector{ids: ids},
		workflowsdk.NewEventIDInterceptor(nil),
	}})
	env.RegisterWorkflow(eventObserver)

	return env
}

func newEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()

	var suite testsuite.WorkflowTestSuite

	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{workflowsdk.NewEventIDInterceptor(nil)}})
	env.RegisterWorkflow(eventObserver)

	return env
}

func runObserver(t *testing.T, env *testsuite.TestWorkflowEnvironment, signals ...string) observed {
	t.Helper()

	for i, v := range signals {
		env.RegisterDelayedCallback(func() { env.SignalWorkflow("sig", v) }, time.Duration(i+1)*time.Millisecond)
	}

	env.RegisterDelayedCallback(func() { env.SignalWorkflow("done", nil) }, time.Duration(len(signals)+1)*time.Millisecond)
	env.ExecuteWorkflow(eventObserver)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var out observed
	require.NoError(t, env.GetWorkflowResult(&out))

	return out
}

func TestEventID_FromStartHeader(t *testing.T) {
	t.Parallel()

	id := uuid.Must(uuid.NewV7())
	payload, err := eventid.Payload(id)
	require.NoError(t, err)

	env := newEnv(t)
	env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{eventid.HeaderKey: payload}})

	out := runObserver(t, env)

	assert.True(t, out.StartHasID)
	assert.Equal(t, id.String(), out.StartID)
}

func TestEventID_MissingHeader(t *testing.T) {
	t.Parallel()

	out := runObserver(t, newEnv(t))

	assert.False(t, out.StartHasID, "no header: ok is false")
}

func TestEventID_MalformedHeader(t *testing.T) {
	t.Parallel()

	env := newEnv(t)
	env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{
		eventid.HeaderKey: {Data: []byte(`"not-a-uuid"`)},
	}})

	out := runObserver(t, env)

	assert.False(t, out.StartHasID, "a header that is not a UUID counts as missing")
}

func TestReceiveEvent_ReturnsEventIDPerSignal(t *testing.T) {
	t.Parallel()

	a := uuid.Must(uuid.NewV7()).String()
	b := uuid.Must(uuid.NewV7()).String()

	out := runObserver(t, newEnvWithInjector(t, a, "", b), "one", "two", "three")

	require.Len(t, out.Signals, 3)
	assert.Equal(t, observedSignal{Value: "one", ID: a, HasID: true}, out.Signals[0])
	assert.Equal(t, "two", out.Signals[1].Value)
	assert.False(t, out.Signals[1].HasID, "a signal without the header has no ID, and keeps its value")
	assert.Equal(t, observedSignal{Value: "three", ID: b, HasID: true}, out.Signals[2])
}

func TestReceiveEvent_PlainReceiveStillDecodes(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite

	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&headerInjector{ids: []string{uuid.Must(uuid.NewV7()).String()}},
		workflowsdk.NewEventIDInterceptor(nil),
	}})
	env.RegisterWorkflow(plainReceiver)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("sig", "plain") }, time.Millisecond)
	env.ExecuteWorkflow(plainReceiver)

	require.NoError(t, env.GetWorkflowError())

	var got string
	require.NoError(t, env.GetWorkflowResult(&got))
	assert.Equal(t, "plain", got, "the stamped metadata must not disturb ordinary Receive")
}

func plainReceiver(ctx workflow.Context) (string, error) {
	var v string
	workflow.GetSignalChannel(ctx, "sig").Receive(ctx, &v)

	return v, nil
}

func TestReceiveEvent_SignalWithoutHeader(t *testing.T) {
	t.Parallel()

	out := runObserver(t, newEnv(t), "hello")

	require.Len(t, out.Signals, 1)
	assert.Equal(t, "hello", out.Signals[0].Value)
	assert.False(t, out.Signals[0].HasID)
}

// TestEventID_WorkerInstallsInterceptorAutomatically pins that the worker's own
// setup (not the test) adds the interceptor: the interceptors configure() leaves
// on the client options are enough for workflow code to see the event ID.
func TestEventID_WorkerInstallsInterceptorAutomatically(t *testing.T) {
	t.Parallel()

	w := workflowsdk.NewIdentityWorker(temporalclientOptionsForTest())
	require.NoError(t, w.Configure(t.Context()))

	id := uuid.Must(uuid.NewV7())
	payload, err := eventid.Payload(id)
	require.NoError(t, err)

	var suite testsuite.WorkflowTestSuite

	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: w.WorkerInterceptors()})
	env.RegisterWorkflow(eventObserver)
	env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{eventid.HeaderKey: payload}})

	out := runObserver(t, env)

	assert.True(t, out.StartHasID, "configure() must install the event ID interceptor")
	assert.Equal(t, id.String(), out.StartID)
}

func temporalclientOptionsForTest() temporalclient.Options {
	return temporalclient.Options{Identity: "eventid-test"}
}
