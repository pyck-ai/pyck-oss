package temporal_test

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
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pyck-ai/pyck/backend/common/services/temporal"
)

// temporalCLIPath returns TEMPORAL_CLI_PATH, the path of a native `temporal`
// binary (not a wrapper script). The dev-server tests skip when it is unset.
func temporalCLIPath() string {
	return os.Getenv("TEMPORAL_CLI_PATH")
}

// sigCounterWorkflow counts "sig" signals (exposed via the "count" query) and
// returns on a "done" signal or after a long timer.
func sigCounterWorkflow(ctx workflow.Context) (int, error) {
	count := 0

	if err := workflow.SetQueryHandler(ctx, "count", func() (int, error) { return count, nil }); err != nil {
		return 0, err
	}

	sigCh := workflow.GetSignalChannel(ctx, "sig")
	doneCh := workflow.GetSignalChannel(ctx, "done")
	timer := workflow.NewTimer(ctx, 5*time.Minute)

	done := false
	for !done {
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(sigCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			count++
		})
		sel.AddReceive(doneCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			done = true
		})
		sel.AddFuture(timer, func(workflow.Future) { done = true })
		sel.Select(ctx)
	}

	return count, nil
}

type target struct {
	name      string
	c         client.Client
	taskQueue string
}

func startTargets(t *testing.T) []target {
	t.Helper()

	if temporalCLIPath() == "" {
		t.Skip("TEMPORAL_CLI_PATH is not set: point it at a native temporal CLI binary to run these tests")
	}

	if !isNativeBinary(temporalCLIPath()) {
		t.Skipf("%s is not a native temporal CLI binary (set TEMPORAL_CLI_PATH)", temporalCLIPath())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
		ExistingPath: temporalCLIPath(),
		LogLevel:     "error",
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, srv.Stop()) })

	tracing, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	require.NoError(t, err)

	// Mirrors NewTemporalClient: root client with the tracing interceptor, plus
	// our gRPC interceptor on the connection.
	root, err := client.DialContext(ctx, client.Options{
		HostPort:     srv.FrontendHostPort(),
		Interceptors: []interceptor.ClientInterceptor{tracing},
		ConnectionOptions: client.ConnectionOptions{
			DialOptions: []grpc.DialOption{grpc.WithChainUnaryInterceptor(temporal.RequestIDInterceptor())},
		},
	})
	require.NoError(t, err)
	t.Cleanup(root.Close)

	// Mirrors DefaultClientFactory.deriveNamespaceClient.
	const tenantNS = "tenant-x"

	_, err = root.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace:                        tenantNS,
		WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour),
	})
	require.NoError(t, err)

	tracing2, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	require.NoError(t, err)

	var derived client.Client

	require.Eventually(t, func() bool {
		derived, err = client.NewClientFromExistingWithContext(ctx, root, client.Options{
			Namespace:    tenantNS,
			Interceptors: []interceptor.ClientInterceptor{tracing2},
		})

		return err == nil
	}, 30*time.Second, 200*time.Millisecond)
	t.Cleanup(derived.Close)

	out := []target{{"root", root, "tq-root"}, {"derived-namespace", derived, "tq-derived"}}

	for _, tg := range out {
		w := worker.New(tg.c, tg.taskQueue, worker.Options{})
		w.RegisterWorkflow(sigCounterWorkflow)
		require.NoError(t, w.Start())
		t.Cleanup(w.Stop)
	}

	// Namespace propagation in the dev server can lag; wait until a workflow runs in the derived namespace.
	require.Eventually(t, func() bool {
		id := "warmup-" + uuid.NewString()
		run, err := derived.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: "tq-derived"}, sigCounterWorkflow)
		if err != nil {
			return false
		}
		if _, err := derived.QueryWorkflow(ctx, id, run.GetRunID(), "count"); err != nil {
			return false
		}
		if err := derived.SignalWorkflow(ctx, id, "", "done", nil); err != nil {
			return false
		}

		return true
	}, 30*time.Second, 200*time.Millisecond)

	return out
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

func queryCount(ctx context.Context, t *testing.T, c client.Client, id string) int {
	t.Helper()

	v, err := c.QueryWorkflow(ctx, id, "", "count")
	require.NoError(t, err)

	var n int
	require.NoError(t, v.Get(&n))

	return n
}

func TestRequestID(t *testing.T) {
	// Parallel-safe: the dev server is private to this test (random free
	// port), every subtest works on its own workflow ID, and the environment
	// is only read.
	t.Parallel()

	targets := startTargets(t)

	for _, tg := range targets {
		t.Run(tg.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			c := tg.c
			opts := func(id string) client.StartWorkflowOptions {
				return client.StartWorkflowOptions{ID: id, TaskQueue: tg.taskQueue}
			}
			newWF := func(t *testing.T) string {
				t.Helper()

				id := "wf-" + uuid.NewString()
				_, err := c.ExecuteWorkflow(ctx, opts(id), sigCounterWorkflow)
				require.NoError(t, err)
				t.Cleanup(func() { terminate(ctx, t, c, id) })

				return id
			}

			t.Run("a_signal_same_id_dedupes", func(t *testing.T) {
				t.Parallel()

				id := newWF(t)
				rctx := temporal.WithRequestID(ctx, "sig-req-1")
				require.NoError(t, c.SignalWorkflow(rctx, id, "", "sig", nil))
				require.NoError(t, c.SignalWorkflow(rctx, id, "", "sig", nil))
				n := queryCount(ctx, t, c, id)
				t.Logf("same request ID, 2 signals -> count=%d", n)
				require.Equal(t, 1, n)
			})

			t.Run("a_control_different_ids", func(t *testing.T) {
				t.Parallel()

				id := newWF(t)
				require.NoError(t, c.SignalWorkflow(temporal.WithRequestID(ctx, "sig-a"), id, "", "sig", nil))
				require.NoError(t, c.SignalWorkflow(temporal.WithRequestID(ctx, "sig-b"), id, "", "sig", nil))
				n := queryCount(ctx, t, c, id)
				t.Logf("different request IDs, 2 signals -> count=%d", n)
				require.Equal(t, 2, n)
			})

			t.Run("a_control_no_request_id", func(t *testing.T) {
				t.Parallel()

				id := newWF(t)
				require.NoError(t, c.SignalWorkflow(ctx, id, "", "sig", nil))
				require.NoError(t, c.SignalWorkflow(ctx, id, "", "sig", nil))
				n := queryCount(ctx, t, c, id)
				t.Logf("no WithRequestID, 2 signals -> count=%d (SDK default is random)", n)
				require.Equal(t, 2, n)
			})

			// strict makes the SDK surface the server's AlreadyStarted error instead of
			// swallowing it and handing back the existing run (SDK default).
			strict := func(id string) client.StartWorkflowOptions {
				o := opts(id)
				o.WorkflowExecutionErrorWhenAlreadyStarted = true

				return o
			}

			t.Run("b_start_same_id_returns_same_run", func(t *testing.T) {
				t.Parallel()

				id := "wf-" + uuid.NewString()
				t.Cleanup(func() { terminate(ctx, t, c, id) })
				rctx := temporal.WithRequestID(ctx, "start-req-1")

				r1, err := c.ExecuteWorkflow(rctx, strict(id), sigCounterWorkflow)
				require.NoError(t, err)
				r2, err := c.ExecuteWorkflow(rctx, strict(id), sigCounterWorkflow)
				t.Logf("strict, same request ID: run1=%s run2=%s err=%v", r1.GetRunID(), runID(r2), err)
				require.NoError(t, err)
				require.Equal(t, r1.GetRunID(), r2.GetRunID())
			})

			t.Run("b_control_different_ids_strict", func(t *testing.T) {
				t.Parallel()

				id := "wf-" + uuid.NewString()
				t.Cleanup(func() { terminate(ctx, t, c, id) })

				r1, err := c.ExecuteWorkflow(temporal.WithRequestID(ctx, "start-x"), strict(id), sigCounterWorkflow)
				require.NoError(t, err)
				r2, err := c.ExecuteWorkflow(temporal.WithRequestID(ctx, "start-y"), strict(id), sigCounterWorkflow)
				t.Logf("strict, different request IDs: run1=%s run2=%s err=%v (%T)", r1.GetRunID(), runID(r2), err, err)
				var already *serviceerror.WorkflowExecutionAlreadyStarted
				require.ErrorAs(t, err, &already, "expected WorkflowExecutionAlreadyStarted")
			})

			t.Run("b_control_different_ids_default_options", func(t *testing.T) {
				t.Parallel()

				id := "wf-" + uuid.NewString()
				t.Cleanup(func() { terminate(ctx, t, c, id) })

				r1, err := c.ExecuteWorkflow(temporal.WithRequestID(ctx, "start-x"), opts(id), sigCounterWorkflow)
				require.NoError(t, err)
				r2, err := c.ExecuteWorkflow(temporal.WithRequestID(ctx, "start-y"), opts(id), sigCounterWorkflow)
				// Recorded: with SDK defaults AlreadyStarted is swallowed, so this is
				// indistinguishable from dedup at the call site.
				t.Logf("default options, different request IDs: run1=%s run2=%s err=%v", r1.GetRunID(), runID(r2), err)
				require.NoError(t, err)
			})

			t.Run("c_start_same_id_after_close", func(t *testing.T) {
				t.Parallel()

				id := "wf-" + uuid.NewString()
				rctx := temporal.WithRequestID(ctx, "start-closed-1")

				r1, err := c.ExecuteWorkflow(rctx, strict(id), sigCounterWorkflow)
				require.NoError(t, err)
				require.NoError(t, c.SignalWorkflow(ctx, id, "", "done", nil))
				var res int
				require.NoError(t, r1.Get(ctx, &res))

				r2, err := c.ExecuteWorkflow(rctx, strict(id), sigCounterWorkflow)
				t.Logf("after close, same request ID: run1=%s run2=%v err=%v", r1.GetRunID(), runID(r2), err)
				if err == nil {
					t.Cleanup(func() { terminate(ctx, t, c, id) })
					t.Logf("same run returned: %v", r1.GetRunID() == r2.GetRunID())
				}
				// Recorded, not asserted: see log for observed behaviour.
			})

			t.Run("c2_control_after_close_different_id", func(t *testing.T) {
				t.Parallel()

				id := "wf-" + uuid.NewString()

				r1, err := c.ExecuteWorkflow(temporal.WithRequestID(ctx, "closed-a"), opts(id), sigCounterWorkflow)
				require.NoError(t, err)
				require.NoError(t, c.SignalWorkflow(ctx, id, "", "done", nil))
				var res int
				require.NoError(t, r1.Get(ctx, &res))

				r2, err := c.ExecuteWorkflow(temporal.WithRequestID(ctx, "closed-b"), opts(id), sigCounterWorkflow)
				t.Logf("after close, different request ID: run1=%s run2=%v err=%v", r1.GetRunID(), runID(r2), err)
				if err == nil {
					t.Cleanup(func() { terminate(ctx, t, c, id) })
				}
			})

			t.Run("d_signal_with_start_same_id", func(t *testing.T) {
				t.Parallel()

				id := "wf-" + uuid.NewString()
				t.Cleanup(func() { terminate(ctx, t, c, id) })
				rctx := temporal.WithRequestID(ctx, "sws-req-1")

				r1, err := c.SignalWithStartWorkflow(rctx, id, "sig", nil, opts(id), sigCounterWorkflow)
				require.NoError(t, err)
				r2, err := c.SignalWithStartWorkflow(rctx, id, "sig", nil, opts(id), sigCounterWorkflow)
				require.NoError(t, err)
				n := queryCount(ctx, t, c, id)
				t.Logf("signal-with-start x2, same request ID: run1=%s run2=%s count=%d", r1.GetRunID(), r2.GetRunID(), n)
				require.Equal(t, 1, n)
			})

			t.Run("d_control_signal_with_start_different_ids", func(t *testing.T) {
				t.Parallel()

				id := "wf-" + uuid.NewString()
				t.Cleanup(func() { terminate(ctx, t, c, id) })

				_, err := c.SignalWithStartWorkflow(temporal.WithRequestID(ctx, "sws-a"), id, "sig", nil, opts(id), sigCounterWorkflow)
				require.NoError(t, err)
				_, err = c.SignalWithStartWorkflow(temporal.WithRequestID(ctx, "sws-b"), id, "sig", nil, opts(id), sigCounterWorkflow)
				require.NoError(t, err)
				n := queryCount(ctx, t, c, id)
				t.Logf("signal-with-start x2, different request IDs: count=%d", n)
				require.Equal(t, 2, n)
			})
		})
	}
}

func runID(r client.WorkflowRun) string {
	if r == nil {
		return "<nil>"
	}

	return r.GetRunID()
}

// isNativeBinary reports whether path is an ELF executable, so wrapper scripts
// (e.g. docker-compose shims) are skipped instead of hanging the dev-server start.
func isNativeBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read-only file, nothing to flush

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return false
	}

	return string(magic) == "\x7fELF"
}

func TestRequestIDFromContext(t *testing.T) {
	t.Parallel()

	_, ok := temporal.RequestIDFromContext(context.Background())
	if ok {
		t.Fatal("no ID on a plain context")
	}

	_, ok = temporal.RequestIDFromContext(temporal.WithRequestID(context.Background(), ""))
	if ok {
		t.Fatal("an empty ID counts as none")
	}

	id, ok := temporal.RequestIDFromContext(temporal.WithRequestID(context.Background(), "abc"))
	if !ok || id != "abc" {
		t.Fatalf("got %q, %v", id, ok)
	}
}
