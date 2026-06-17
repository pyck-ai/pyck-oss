//nolint:testpackage // white-box tests: they drive unexported helpers (healthHandler, checkPollers, taskQueueHasFreshPoller) and poke healthState's atomics directly.
package workflowsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeClient stubs the only Temporal client method the probe uses.
// DescribeTaskQueue is routed to the describe func; any other method panics
// via the nil embedded interface (none are exercised by these tests).
type fakeClient struct {
	temporalclient.Client
	describe func(ctx context.Context, taskQueue string, t enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error)
}

func (f *fakeClient) DescribeTaskQueue(ctx context.Context, taskQueue string, t enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
	return f.describe(ctx, taskQueue, t)
}

func poller(identity string, lastAccess time.Time) *taskqueuepb.PollerInfo {
	return &taskqueuepb.PollerInfo{
		Identity:       identity,
		LastAccessTime: timestamppb.New(lastAccess),
	}
}

func pollersResp(pollers ...*taskqueuepb.PollerInfo) *workflowservice.DescribeTaskQueueResponse {
	return &workflowservice.DescribeTaskQueueResponse{Pollers: pollers}
}

const (
	// testMaxStale is the freshness window used across the handler tests; it
	// matches healthMaxStaleDefault.
	testMaxStale = 90 * time.Second
	// testIdentity is this worker's Temporal identity, matched by the poller tests.
	testIdentity = "1234@worker-host"
)

// callHealth runs the handler against a fresh request and returns the
// status code plus the decoded JSON body.
func callHealth(t *testing.T, state *healthState) (int, map[string]any) {
	t.Helper()

	lg := zerolog.Nop()
	h := healthHandler(state, testMaxStale, &lg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, healthPathDefault, nil)
	h(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	return res.StatusCode, body
}

// storeSuccess marks a successful probe that happened `ago` in the past.
func storeSuccess(state *healthState, ago time.Duration) {
	state.lastSuccessUnixNano.Store(time.Now().Add(-ago).UnixNano())
	state.lastError.Store(nil)
}

func TestHealthHandler_NeverProbed(t *testing.T) {
	t.Parallel()

	state := &healthState{}

	status, body := callHealth(t, state)

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
	if body["ok"] != false {
		t.Errorf("ok = %v, want false", body["ok"])
	}
	if body["last_success_at"] != "" {
		t.Errorf("last_success_at = %q, want empty (never probed)", body["last_success_at"])
	}
	if _, present := body["last_error"]; present {
		t.Errorf("last_error should be absent before any probe, got %v", body["last_error"])
	}
}

func TestHealthHandler_FreshSuccess(t *testing.T) {
	t.Parallel()

	state := &healthState{}
	state.totalProbes.Store(3)
	storeSuccess(state, 5*time.Second)

	status, body := callHealth(t, state)

	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v, want true", body["ok"])
	}
	if body["last_success_at"] == "" {
		t.Error("last_success_at should be populated after a success")
	}
	if _, present := body["last_error"]; present {
		t.Errorf("last_error should be absent after a success, got %v", body["last_error"])
	}
}

func TestHealthHandler_StaleSuccess(t *testing.T) {
	t.Parallel()

	state := &healthState{}
	// Last success is older than maxStale: the worker went quiet (the
	// 2026-05-18 failure mode) so /health must flip to 503.
	storeSuccess(state, 5*time.Minute)

	status, body := callHealth(t, state)

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
	if body["ok"] != false {
		t.Errorf("ok = %v, want false", body["ok"])
	}
	if body["last_success_at"] == "" {
		t.Error("last_success_at should still report the last (stale) success")
	}
}

func TestHealthHandler_ReportsLastError(t *testing.T) {
	t.Parallel()

	state := &healthState{}
	state.totalProbes.Store(10)
	state.failedProbes.Store(4)
	msg := "dial temporal: context deadline exceeded"
	state.lastError.Store(&msg)
	// No success ever recorded -> 503, and the error must surface verbatim.

	status, body := callHealth(t, state)

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
	if body["last_error"] != msg {
		t.Errorf("last_error = %v, want %q", body["last_error"], msg)
	}
	// Counters are exposed for fly's check timeline; JSON numbers decode
	// as float64.
	if body["probes_total"] != float64(10) {
		t.Errorf("probes_total = %v, want 10", body["probes_total"])
	}
	if body["probes_failed"] != float64(4) {
		t.Errorf("probes_failed = %v, want 4", body["probes_failed"])
	}
}

func TestHealthHandler_RecoveryClearsError(t *testing.T) {
	t.Parallel()

	state := &healthState{}
	// A probe failed, then a later probe succeeded: lastError cleared,
	// success fresh -> back to 200 with no last_error.
	failMsg := "transient"
	state.lastError.Store(&failMsg)
	storeSuccess(state, time.Second)

	status, body := callHealth(t, state)

	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}
	if _, present := body["last_error"]; present {
		t.Errorf("last_error should be cleared after recovery, got %v", body["last_error"])
	}
}

func TestTaskQueueHasFreshPoller_FreshWorkflowPoller(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := &fakeClient{describe: func(_ context.Context, _ string, typ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		if typ == enumspb.TASK_QUEUE_TYPE_WORKFLOW {
			return pollersResp(poller(testIdentity, now.Add(-5*time.Second))), nil
		}
		return pollersResp(), nil
	}}

	fresh, err := taskQueueHasFreshPoller(context.Background(), c, "tq", testIdentity, 90*time.Second, time.Second, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fresh {
		t.Error("expected a fresh workflow poller to count as live")
	}
}

func TestTaskQueueHasFreshPoller_FreshActivityOnly(t *testing.T) {
	t.Parallel()

	// Workflow type has nothing; the match comes from the activity poller.
	// A queue that only polls one type must still count as healthy.
	now := time.Now()
	c := &fakeClient{describe: func(_ context.Context, _ string, typ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		if typ == enumspb.TASK_QUEUE_TYPE_ACTIVITY {
			return pollersResp(poller(testIdentity, now.Add(-3*time.Second))), nil
		}
		return pollersResp(), nil
	}}

	fresh, err := taskQueueHasFreshPoller(context.Background(), c, "tq", testIdentity, 90*time.Second, time.Second, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fresh {
		t.Error("expected a fresh activity-only poller to count as live")
	}
}

func TestTaskQueueHasFreshPoller_StalePoller(t *testing.T) {
	t.Parallel()

	// Our poller exists but last polled long ago — the wedged-worker case.
	now := time.Now()
	c := &fakeClient{describe: func(_ context.Context, _ string, _ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		return pollersResp(poller(testIdentity, now.Add(-10*time.Minute))), nil
	}}

	fresh, err := taskQueueHasFreshPoller(context.Background(), c, "tq", testIdentity, 90*time.Second, time.Second, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh {
		t.Error("a stale poller must not count as live")
	}
}

func TestTaskQueueHasFreshPoller_OtherMachineDoesNotMask(t *testing.T) {
	t.Parallel()

	// Another machine polls the shared queue with a fresh timestamp, but our
	// identity is absent. This machine's worker is dead and must read unhealthy.
	now := time.Now()
	c := &fakeClient{describe: func(_ context.Context, _ string, _ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		return pollersResp(poller("9999@other-host", now)), nil
	}}

	fresh, err := taskQueueHasFreshPoller(context.Background(), c, "tq", testIdentity, 90*time.Second, time.Second, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh {
		t.Error("another machine's poller must not mask this machine's dead worker")
	}
}

func TestTaskQueueHasFreshPoller_DescribeError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("unavailable")
	c := &fakeClient{describe: func(_ context.Context, _ string, _ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		return nil, wantErr
	}}

	_, err := taskQueueHasFreshPoller(context.Background(), c, "tq", testIdentity, 90*time.Second, time.Second, time.Now())
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCheckPollers_AllQueuesFresh(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := &fakeClient{describe: func(_ context.Context, _ string, typ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		if typ == enumspb.TASK_QUEUE_TYPE_WORKFLOW {
			return pollersResp(poller(testIdentity, now)), nil
		}
		return pollersResp(), nil
	}}
	cfg := healthServerConfig{Identity: testIdentity, MaxStale: 90 * time.Second, TaskQueues: []string{"tq-a", "tq-b"}}

	if err := checkPollers(context.Background(), c, cfg, now); err != nil {
		t.Errorf("expected healthy, got %v", err)
	}
}

func TestCheckPollers_OneQueueStaleNamesIt(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := &fakeClient{describe: func(_ context.Context, taskQueue string, _ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		if taskQueue == "tq-bad" {
			return pollersResp(poller(testIdentity, now.Add(-5*time.Minute))), nil
		}
		return pollersResp(poller(testIdentity, now)), nil
	}}
	cfg := healthServerConfig{Identity: testIdentity, MaxStale: 90 * time.Second, TaskQueues: []string{"tq-ok", "tq-bad"}}

	err := checkPollers(context.Background(), c, cfg, now)
	if err == nil {
		t.Fatal("expected a stale queue to fail the probe")
	}
	if !strings.Contains(err.Error(), "tq-bad") {
		t.Errorf("error should name the offending queue, got %v", err)
	}
}

func TestHealthHandler_BodyShape(t *testing.T) {
	t.Parallel()

	state := &healthState{}
	storeSuccess(state, time.Second)

	_, body := callHealth(t, state)

	for _, key := range []string{"ok", "last_success_at", "age_seconds", "max_stale_seconds", "probes_total", "probes_failed"} {
		if _, ok := body[key]; !ok {
			t.Errorf("health body missing required key %q", key)
		}
	}
	if body["max_stale_seconds"] != float64(90) {
		t.Errorf("max_stale_seconds = %v, want 90", body["max_stale_seconds"])
	}
}
