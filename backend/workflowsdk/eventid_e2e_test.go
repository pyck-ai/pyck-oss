package workflowsdk_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pyck-ai/pyck/backend/common/eventid"
	commontemporal "github.com/pyck-ai/pyck/backend/common/services/temporal"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

func temporalCLI(t *testing.T) string {
	t.Helper()

	// The dev-server tests run only when TEMPORAL_CLI_PATH points at a native
	// `temporal` binary.
	path := os.Getenv("TEMPORAL_CLI_PATH")
	if path == "" {
		t.Skip("TEMPORAL_CLI_PATH is not set: point it at a native temporal CLI binary to run this test")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Skipf("no temporal CLI at %s (set TEMPORAL_CLI_PATH)", path)
	}
	defer f.Close() //nolint:errcheck // read-only file, nothing to flush

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "\x7fELF" {
		t.Skipf("%s is not a native binary (set TEMPORAL_CLI_PATH)", path)
	}

	return path
}

// terminate stops a workflow started by a test. A workflow that already
// completed is fine; any other failure is reported.
func terminate(ctx context.Context, t *testing.T, c client.Client, id string) {
	t.Helper()

	err := c.TerminateWorkflow(ctx, id, "", "cleanup")

	var notFound *serviceerror.NotFound
	if err != nil && !errors.As(err, &notFound) {
		assert.NoError(t, err)
	}
}

// TestEventID_EndToEnd sends starts and signals through the production router
// client (commontemporal.NewTemporalClient, and a namespace client derived from
// it the way DefaultClientFactory does) to a worker set up the way
// workflowsdk's worker is, and checks the workflow sees the event IDs.
func TestEventID_EndToEnd(t *testing.T) {
	// Parallel-safe: the dev server is private to this test (random free
	// port), each subtest works on its own workflow ID, and the environment is
	// only read.
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	// Not defer: the parallel subtests run after this function returns.
	t.Cleanup(cancel)

	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
		ExistingPath: temporalCLI(t),
		LogLevel:     "error",
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, srv.Stop()) })

	hostPort := srv.FrontendHostPort()

	root, err := commontemporal.NewTemporalClient(ctx, hostPort, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(root.Close)

	const tenantNS = "tenant-x"

	_, err = root.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace:                        tenantNS,
		WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour),
	})
	require.NoError(t, err)

	var derived client.Client

	require.Eventually(t, func() bool {
		derived, err = client.NewClientFromExistingWithContext(ctx, root, client.Options{Namespace: tenantNS})

		return err == nil
	}, 30*time.Second, 200*time.Millisecond)
	t.Cleanup(derived.Close)

	// Workers: their own clients, with the interceptor installed the way
	// worker.configure does.
	for _, w := range []struct{ namespace, taskQueue string }{{"default", "tq-root"}, {tenantNS, "tq-derived"}} {
		wc, err := client.DialContext(ctx, client.Options{
			HostPort:     hostPort,
			Namespace:    w.namespace,
			Interceptors: workflowsdk.WorkerClientInterceptors(nil),
		})
		require.NoError(t, err)
		t.Cleanup(wc.Close)

		wrk := worker.New(wc, w.taskQueue, worker.Options{})
		wrk.RegisterWorkflow(eventObserver)
		require.NoError(t, wrk.Start())
		t.Cleanup(wrk.Stop)
	}

	targets := []struct {
		name      string
		c         client.Client
		taskQueue string
	}{{"root", root, "tq-root"}, {"derived-namespace", derived, "tq-derived"}}

	for _, tg := range targets {
		t.Run(tg.name, func(t *testing.T) {
			t.Parallel()

			// The namespace can lag behind its registration on the dev server.
			require.Eventually(t, func() bool {
				_, err := tg.c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "warmup-" + uuid.NewString(), TaskQueue: tg.taskQueue}, eventObserver)

				return err == nil
			}, 30*time.Second, 200*time.Millisecond)

			start := func(t *testing.T, ctx context.Context) string {
				t.Helper()

				id := "wf-" + uuid.NewString()
				_, err := tg.c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: tg.taskQueue}, eventObserver)
				require.NoError(t, err)
				t.Cleanup(func() { terminate(context.WithoutCancel(ctx), t, tg.c, id) })

				return id
			}

			query := func(t *testing.T, id string, wantSignals int) observed {
				t.Helper()

				var out observed

				require.Eventually(t, func() bool {
					v, err := tg.c.QueryWorkflow(ctx, id, "", "observed")
					if err != nil || v.Get(&out) != nil {
						return false
					}

					return len(out.Signals) >= wantSignals
				}, 10*time.Second, 50*time.Millisecond)

				return out
			}

			t.Run("start carries the event ID", func(t *testing.T) {
				t.Parallel()

				want := uuid.Must(uuid.NewV7())
				id := start(t, eventid.WithEventID(ctx, want))

				out := query(t, id, 0)
				t.Logf("start with event ID: %+v", out)
				assert.True(t, out.StartHasID)
				assert.Equal(t, want.String(), out.StartID)
			})

			t.Run("start without an event ID has none", func(t *testing.T) {
				t.Parallel()

				id := start(t, ctx)

				out := query(t, id, 0)
				t.Logf("start without event ID: %+v", out)
				assert.False(t, out.StartHasID)
			})

			t.Run("signals carry their own event IDs", func(t *testing.T) {
				t.Parallel()

				id := start(t, ctx)
				a, b := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

				require.NoError(t, tg.c.SignalWorkflow(eventid.WithEventID(ctx, a), id, "", "sig", "one"))
				require.NoError(t, tg.c.SignalWorkflow(ctx, id, "", "sig", "two"))
				require.NoError(t, tg.c.SignalWorkflow(eventid.WithEventID(ctx, b), id, "", "sig", "three"))

				out := query(t, id, 3)
				t.Logf("signals: %+v", out.Signals)
				require.Len(t, out.Signals, 3)
				assert.Equal(t, observedSignal{Value: "one", ID: a.String(), HasID: true}, out.Signals[0])
				assert.False(t, out.Signals[1].HasID)
				assert.Equal(t, observedSignal{Value: "three", ID: b.String(), HasID: true}, out.Signals[2])
			})

			t.Run("signal-with-start carries the event ID on both", func(t *testing.T) {
				t.Parallel()

				want := uuid.Must(uuid.NewV7())
				id := "wf-" + uuid.NewString()
				t.Cleanup(func() { terminate(context.WithoutCancel(ctx), t, tg.c, id) })

				_, err := tg.c.SignalWithStartWorkflow(eventid.WithEventID(ctx, want), id, "sig", "hello",
					client.StartWorkflowOptions{ID: id, TaskQueue: tg.taskQueue}, eventObserver)
				require.NoError(t, err)

				out := query(t, id, 1)
				t.Logf("signal-with-start: %+v", out)
				assert.Equal(t, want.String(), out.StartID, "the start side")
				assert.Equal(t, observedSignal{Value: "hello", ID: want.String(), HasID: true}, out.Signals[0], "the signal side")
			})

			t.Run("works next to the request ID interceptor", func(t *testing.T) {
				t.Parallel()

				want := uuid.Must(uuid.NewV7())
				id := start(t, ctx)
				rctx := commontemporal.WithRequestID(eventid.WithEventID(ctx, want), "req-"+want.String())

				require.NoError(t, tg.c.SignalWorkflow(rctx, id, "", "sig", "x"))
				require.NoError(t, tg.c.SignalWorkflow(rctx, id, "", "sig", "x"))

				query(t, id, 1)
				time.Sleep(200 * time.Millisecond)
				out := query(t, id, 1)
				t.Logf("two sends, same request ID and event ID: %+v", out.Signals)
				require.Len(t, out.Signals, 1, "Temporal dropped the repeat by request ID")
				assert.Equal(t, want.String(), out.Signals[0].ID)
			})
		})
	}
}
