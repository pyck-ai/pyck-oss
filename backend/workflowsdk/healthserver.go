package workflowsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	pycklog "github.com/pyck-ai/pyck/backend/common/log"
)

// The health server exists to give fly.io (or any external supervisor) a
// reliable signal that this worker process is still polling Temporal. Fly
// only sees the firecracker VM; it cannot tell that the Go process inside
// has stopped polling. The failure mode we hit on 2026-05-18 was a silent
// gRPC long-poll drop where the VM stayed `started, HostStatus: ok` for 8h
// while no workflows ran. A failing /health for 3 consecutive probes
// triggers a machine restart and self-heals the cluster.
//
// The probe asks Temporal — via DescribeTaskQueue — whether *this* worker's
// identity is still registered as a live poller (a recent LastAccessTime)
// on every task queue it serves. That is the precise signal for the
// incident: a worker whose poll loop wedged disappears from the task
// queue's poller set even though a fresh gRPC connection to the same
// frontend would still succeed. Matching on our own identity also keeps
// multi-machine deployments honest — another machine still polling the
// shared queue must not mask this machine's dead worker.

const (
	healthPathDefault     = "/health"
	healthPortDefault     = 8080
	healthIntervalDefault = 10 * time.Second
	healthTimeoutDefault  = 3 * time.Second
	healthMaxStaleDefault = 90 * time.Second
)

// errNoLivePoller is the probe failure when this worker's identity is not a
// fresh poller on a task queue it serves — the wedged-worker signal.
var errNoLivePoller = errors.New("no live poller")

// healthServerConfig captures the runtime knobs for the health server.
// Enabled/Port/Interval/Timeout/MaxStale are env-derived defaults; Identity
// and TaskQueues are filled in by RunDefaultWorker once the worker is
// configured and tell the probe which pollers to look for.
type healthServerConfig struct {
	Enabled  bool
	Port     int
	Interval time.Duration
	Timeout  time.Duration
	MaxStale time.Duration

	// Identity is this worker's Temporal client identity; the probe treats
	// the worker as healthy only while a poller with this identity is fresh.
	Identity string
	// TaskQueues is the set of task queues this worker serves. Empty means
	// the probe falls back to a plain connectivity check.
	TaskQueues []string
}

func defaultHealthServerConfig() healthServerConfig {
	cfg := healthServerConfig{
		Enabled:  true,
		Port:     healthPortDefault,
		Interval: healthIntervalDefault,
		Timeout:  healthTimeoutDefault,
		MaxStale: healthMaxStaleDefault,
	}
	if os.Getenv("HEALTH_DISABLED") == "1" {
		cfg.Enabled = false
	}
	if v := os.Getenv("HEALTH_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Port = n
		}
	}
	if v := os.Getenv("HEALTH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Interval = d
		}
	}
	if v := os.Getenv("HEALTH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Timeout = d
		}
	}
	if v := os.Getenv("HEALTH_MAX_STALE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.MaxStale = d
		}
	}
	return cfg
}

// healthState is the source of truth for /health. The probe goroutine
// writes it; the HTTP handler reads it. Atomic fields keep both sides
// lock-free since this is a hot path during fly's check polling.
type healthState struct {
	lastSuccessUnixNano atomic.Int64           // 0 until first successful probe
	lastError           atomic.Pointer[string] // nil after a success
	totalProbes         atomic.Int64
	failedProbes        atomic.Int64
}

// runHealthServer brings up the probe loop + HTTP listener and blocks
// until ctx is cancelled. The probe loop owns a dedicated Temporal client
// (dialed inside the loop, see dialProbeClient) so we don't share a gRPC
// connection with the worker's long-poll traffic — a stuck probe must
// never queue behind a long-poll, and a flaky probe must never ripple into
// the worker's reconnection logic.
//
// The HTTP listener comes up unconditionally: if Temporal is unreachable
// at boot, the listener still binds and serves 503 (carrying the dial
// error) instead of leaving the port unbound — which fly would otherwise
// see as connection-refused and churn the machine on.
func runHealthServer(ctx context.Context, cfg healthServerConfig, clientOptions temporalclient.Options) error {
	logger := pycklog.ForContext(ctx).With().
		Str("component", "health-server").
		Logger()

	state := &healthState{}

	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		runProbeLoop(ctx, clientOptions, state, cfg, &logger)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc(healthPathDefault, healthHandler(state, cfg.MaxStale, &logger))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	srvDone := make(chan error, 1)
	go func() {
		logger.Info().
			Str("addr", srv.Addr).
			Dur("probe_interval", cfg.Interval).
			Dur("probe_timeout", cfg.Timeout).
			Dur("max_stale", cfg.MaxStale).
			Msg("health server listening")
		srvDone <- srv.ListenAndServe()
	}()

	select {
	case err := <-srvDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("health server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	// Derive from ctx (not Background) so it inherits request-scoped values,
	// but drop its cancellation — ctx is already done here and we still want a
	// bounded grace period to drain in-flight /health responses.
	shutCtx, shutCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Warn().Err(err).Msg("health server shutdown")
	}
	<-probeDone
	<-srvDone
	return nil
}

func runProbeLoop(ctx context.Context, clientOptions temporalclient.Options, state *healthState, cfg healthServerConfig, logger *pycklog.Logger) {
	client := dialProbeClient(ctx, clientOptions, state, cfg, logger)
	if client == nil {
		return // ctx cancelled before we ever connected
	}
	defer client.Close()

	// Warm the state immediately so the first /health after fly's
	// grace_period sees a real result instead of "never succeeded".
	probeOnce(ctx, client, state, cfg, logger)

	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info().
				AnErr("reason", ctx.Err()).
				Msg("health probe loop exiting")
			return
		case <-t.C:
			probeOnce(ctx, client, state, cfg, logger)
		}
	}
}

// dialProbeClient establishes the dedicated probe client, retrying every
// interval until it succeeds or ctx is cancelled (returns nil in that
// case). Each failed attempt is recorded in healthState so /health reports
// the dial error verbatim while the listener is already up serving 503.
// Once dialed, the client's own gRPC layer handles reconnects, so we never
// re-dial — a dropped connection surfaces as a failing GetSystemInfo.
//
//nolint:ireturn // temporalclient.DialContext only yields the client.Client interface; there is no concrete type to return.
func dialProbeClient(ctx context.Context, clientOptions temporalclient.Options, state *healthState, cfg healthServerConfig, logger *pycklog.Logger) temporalclient.Client {
	for {
		dialCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		client, err := temporalclient.DialContext(dialCtx, clientOptions)
		cancel()
		if err == nil {
			return client
		}

		state.totalProbes.Add(1)
		state.failedProbes.Add(1)
		msg := fmt.Sprintf("dial temporal: %v", err)
		state.lastError.Store(&msg)
		logger.Warn().
			Err(err).
			Int64("probes_total", state.totalProbes.Load()).
			Int64("probes_failed", state.failedProbes.Load()).
			Msg("health probe client dial failed; retrying")

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.Interval):
		}
	}
}

// probeOnce runs one health probe and records the outcome in healthState.
// The probe succeeds only when this worker is a live poller on every task
// queue it serves (see checkPollers).
func probeOnce(ctx context.Context, c temporalclient.Client, state *healthState, cfg healthServerConfig, logger *pycklog.Logger) {
	state.totalProbes.Add(1)
	start := time.Now()
	// cfg.Timeout bounds each individual RPC inside checkPollers, not the
	// whole probe — a worker on several task queues makes one DescribeTaskQueue
	// per queue, and they must not have to share a single deadline. The probe
	// runs in the background loop, so its total wall time doesn't gate /health
	// latency (the handler only reads cached state).
	err := checkPollers(ctx, c, cfg, start)
	latency := time.Since(start)

	if err != nil {
		state.failedProbes.Add(1)
		msg := err.Error()
		state.lastError.Store(&msg)
		logger.Warn().
			Err(err).
			Int64("latency_ms", latency.Milliseconds()).
			Int64("probes_total", state.totalProbes.Load()).
			Int64("probes_failed", state.failedProbes.Load()).
			Msg("temporal health probe failed")
		return
	}

	state.lastError.Store(nil)
	state.lastSuccessUnixNano.Store(time.Now().UnixNano())
	logger.Debug().
		Int("task_queues", len(cfg.TaskQueues)).
		Int64("latency_ms", latency.Milliseconds()).
		Msg("temporal health probe ok")
}

// checkPollers verifies, for every task queue the worker serves, that a
// poller with the worker's identity has a LastAccessTime within MaxStale.
// Any unreachable queue or missing/stale poller fails the probe with a
// message naming the offending queue. With no task queues (a degenerate
// worker) it falls back to a plain connectivity probe so /health still
// reflects whether Temporal is reachable.
func checkPollers(ctx context.Context, c temporalclient.Client, cfg healthServerConfig, now time.Time) error {
	if len(cfg.TaskQueues) == 0 {
		cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
		if _, err := c.WorkflowService().GetSystemInfo(cctx, &workflowservice.GetSystemInfoRequest{}); err != nil {
			return fmt.Errorf("temporal connectivity: %w", err)
		}
		return nil
	}

	for _, tq := range cfg.TaskQueues {
		fresh, err := taskQueueHasFreshPoller(ctx, c, tq, cfg.Identity, cfg.MaxStale, cfg.Timeout, now)
		if err != nil {
			return fmt.Errorf("describe task queue %q: %w", tq, err)
		}
		if !fresh {
			return fmt.Errorf("%w for identity %q on task queue %q within %s", errNoLivePoller, cfg.Identity, tq, cfg.MaxStale)
		}
	}
	return nil
}

// taskQueueHasFreshPoller reports whether a poller with the given identity
// has polled the task queue within maxStale. It checks both the workflow
// and activity poller sets and returns true on the first fresh match — a
// wedged worker drops out of both, while a queue that only polls one type
// must not be flagged unhealthy.
func taskQueueHasFreshPoller(ctx context.Context, c temporalclient.Client, taskQueue, identity string, maxStale, timeout time.Duration, now time.Time) (bool, error) {
	for _, tqType := range []enumspb.TaskQueueType{
		enumspb.TASK_QUEUE_TYPE_WORKFLOW,
		enumspb.TASK_QUEUE_TYPE_ACTIVITY,
	} {
		rpcCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := c.DescribeTaskQueue(rpcCtx, taskQueue, tqType)
		cancel()
		if err != nil {
			return false, err
		}
		for _, p := range resp.GetPollers() {
			if p.GetIdentity() != identity {
				continue
			}
			last := p.GetLastAccessTime()
			if last == nil {
				continue
			}
			if now.Sub(last.AsTime()) <= maxStale {
				return true, nil
			}
		}
	}
	return false, nil
}

func healthHandler(state *healthState, maxStale time.Duration, logger *pycklog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		last := state.lastSuccessUnixNano.Load()
		var lastSuccessAt time.Time
		if last > 0 {
			lastSuccessAt = time.Unix(0, last)
		}
		age := time.Since(lastSuccessAt)
		ok := last > 0 && age <= maxStale

		body := map[string]any{
			"ok":                ok,
			"last_success_at":   "",
			"age_seconds":       age.Seconds(),
			"max_stale_seconds": maxStale.Seconds(),
			"probes_total":      state.totalProbes.Load(),
			"probes_failed":     state.failedProbes.Load(),
		}
		if last > 0 {
			body["last_success_at"] = lastSuccessAt.UTC().Format(time.RFC3339Nano)
		}
		if errPtr := state.lastError.Load(); errPtr != nil {
			body["last_error"] = *errPtr
		}

		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}

		// Fly polls /health every ~30s so the log volume is low. The
		// entries are exactly what you want when investigating "why
		// did fly restart this machine?".
		logger.Info().
			Int("status", status).
			Bool("ok", ok).
			Float64("age_seconds", age.Seconds()).
			Str("remote", r.RemoteAddr).
			Int64("probes_total", state.totalProbes.Load()).
			Int64("probes_failed", state.failedProbes.Load()).
			Msg("health check response")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			logger.Warn().Err(err).Msg("health response encode failed")
		}
	}
}
