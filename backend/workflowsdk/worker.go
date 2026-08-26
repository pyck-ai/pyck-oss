package workflowsdk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	temporalclient "go.temporal.io/sdk/client"
	temporalenvconfig "go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/contrib/opentelemetry"
	temporalworker "go.temporal.io/sdk/worker"

	pycklog "github.com/pyck-ai/pyck/backend/common/log"
	pycklogadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	pyckotel "github.com/pyck-ai/pyck/backend/common/otel"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	pyckworkflowapi "github.com/pyck-ai/pyck/backend/workflow/api"
	"github.com/pyck-ai/pyck/backend/workflow/model"

	"github.com/pyck-ai/pyck/backend/workflowsdk/registry"
)

var (
	ErrAlreadyRunning = fmt.Errorf("worker is already running")

	ErrReadBuildInfo = fmt.Errorf("failed to read build info")

	// ErrUnversionedBuild aliases the common/workflow sentinel rather than
	// copying it: versioning errors are raised there and must stay matchable
	// through this package too.
	ErrUnversionedBuild = commonworkflow.ErrUnversionedBuild
)

const (
	uiBundleStampRetryDelay    = 1 * time.Second
	uiBundleStampMaxRetryDelay = 30 * time.Second
)

// uiBundleMetadataEntries builds the ui.bundle.<Type>.{version,slug} metadata a
// worker stamps on its deployment version (#1317). Slug is stamped only when
// configured — shared flavour bundles have one, per-tenant bundles don't.
//
// An unset version stamps nothing. The build ID is not a usable fallback — the
// two only coincide when a deploy pins them together — so claiming it would
// point remoteUI at a bundle nobody uploaded, instead of letting it fall back to
// the configured default.
func uiBundleMetadataEntries(workflowTypes []string, bundle commonworkflow.UIBundle) map[string]interface{} {
	if bundle.Version == "" {
		return nil
	}

	entries := make(map[string]interface{}, len(workflowTypes)*2)
	for _, t := range workflowTypes {
		entries[commonworkflow.UIBundleVersionKey(t)] = bundle.Version
		if bundle.Slug != "" {
			entries[commonworkflow.UIBundleSlugKey(t)] = bundle.Slug
		}
	}
	return entries
}

// stampUIBundleMetadata writes ui.bundle metadata for the workflow types this
// worker serves onto its own deployment version, then marks the worker ready
// (#1317). Readiness gates the controller's promotion, so it retries until the
// stamp lands or the worker shuts down: no version may serve before its metadata
// exists.
func (w *worker) stampUIBundleMetadata(ctx context.Context, client temporalclient.Client, version temporalworker.WorkerDeploymentVersion, bundle commonworkflow.UIBundle, ready *atomic.Bool) {
	logger := pycklog.ForContext(ctx)

	workflows := w.registry.Workflows()
	types := make([]string, 0, len(workflows))
	for _, wf := range workflows {
		types = append(types, wf.Type())
	}

	entries := uiBundleMetadataEntries(types, bundle)
	if len(entries) == 0 {
		// Say so: otherwise "why is this workflow served the default bundle?"
		// has no answer anywhere.
		logger.Info().
			Str("build_id", version.BuildID).
			Msg("no UI bundle version configured; serving whatever default the backend has")
		ready.Store(true)
		return
	}

	opts := temporalclient.WorkerDeploymentUpdateVersionMetadataOptions{
		Version:        version,
		MetadataUpdate: temporalclient.WorkerDeploymentMetadataUpdate{UpsertEntries: entries},
	}
	handle := client.WorkerDeploymentClient().GetHandle(version.DeploymentName)

	// The version only registers a moment after the worker starts polling, so the
	// first attempts are expected to fail; back off rather than spam the log.
	delay := uiBundleStampRetryDelay
	for {
		_, err := handle.UpdateVersionMetadata(ctx, opts)
		if err == nil {
			ready.Store(true)
			logger.Info().
				Int("workflow_types", len(types)).
				Str("build_id", version.BuildID).
				Str("bundle_version", bundle.Version).
				Msg("stamped UI bundle metadata")
			return
		}
		logger.Warn().Err(err).Dur("retry_in", delay).Msg("stamp UI bundle metadata failed; worker stays not-ready until it lands")
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, uiBundleStampMaxRetryDelay)
	}
}

func RunDefaultWorker(opts ...WorkerOption) {
	ctx := context.Background()

	ctx, _ = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	info, ok := debug.ReadBuildInfo()
	if !ok {
		panic(ErrReadBuildInfo)
	}

	if err := LoadEnv(ctx); err != nil {
		log.Fatal(err)
		return
	}

	ctx, logger := pycklog.SetupLogger(ctx, "worker", Config.LogConfig)

	logger = logger.With().
		Str("version", info.Main.Version).
		Logger()

	ctx = pycklog.Context(ctx, logger)

	tracer, err := pyckotel.SetupTracer(info.Main.Path, Config.EnvironmentName, &Config.OTelConfig)
	if err != nil {
		logger.Fatal().
			Err(err).
			Msg("failed to setup tracer")
		return
	}
	defer tracer.Close()

	worker, err := NewWorker(ctx, opts...)
	if err != nil {
		logger.Fatal().
			Err(err).
			Msg("failed to create worker")
		return
	}

	// Worker Deployment Versioning (#1132): register a deployment version so
	// rolling deploys don't break in-flight executions. Must precede Start, which
	// passes workerOptions to every constructed worker.
	ready := &atomic.Bool{}
	depOpts, versioned, err := Config.DeploymentOptions(info.Main.Version, info.Main.Path)
	if err != nil {
		logger.Fatal().
			Err(err).
			Msg("worker versioning")
		return
	}
	if versioned {
		worker.workerOptions.DeploymentOptions = depOpts
		logger.Info().
			Str("deployment", depOpts.Version.DeploymentName).
			Str("build_id", depOpts.Version.BuildID).
			Msg("worker deployment versioning enabled (pinned)")
	} else {
		logger.Warn().
			Msg("worker deployment versioning disabled: unversioned build and PYCK_WORKER_REQUIRE_BUILD_ID=false")
		ready.Store(true) // nothing to stamp
	}

	defer worker.Stop()

	if err := worker.Start(ctx); err != nil {
		logger.Fatal().
			Err(err).
			Msg("failed to run worker")
		return
	}

	logger.Info().
		Msg("worker started")

	defer logger.Info().
		Msg("worker stopped")

	// Stamp UI bundle metadata (#1317) now that the worker polls and its deployment
	// version exists. Backgrounded so it never delays the health server; the client
	// is read here because Stop() nils worker.client on shutdown.
	if versioned {
		bundle := commonworkflow.UIBundle{Slug: Config.UIBundleSlug, Version: Config.UIBundleVersion}
		go worker.stampUIBundleMetadata(ctx, worker.client, depOpts.Version, bundle, ready)
	}

	// Spin up the health server. It uses a dedicated Temporal client
	// (dialed inside the probe loop) so probe gRPC traffic is isolated
	// from the worker's long-polls. The HTTP listener binds
	// unconditionally — if Temporal is unreachable it serves 503 rather
	// than leaving the port unbound, so fly gets a clean failing check
	// instead of connection-refused.
	if worker.healthConfig.Enabled {
		cfg := worker.healthConfig
		cfg.Identity = worker.clientOptions.Identity
		cfg.TaskQueues = worker.taskQueues
		go func() {
			if err := runHealthServer(ctx, cfg, worker.clientOptions, ready); err != nil {
				logger.Error().
					Err(err).
					Msg("health server exited with error")
			}
		}()
	}

	<-ctx.Done() // Wait for context cancellation

	if err := ctx.Err(); err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.Error().
				Err(err).
				Msg("context done with error")
		}
	}
}

func NewWorker(ctx context.Context, opts ...WorkerOption) (*worker, error) {
	var err error

	clientOpts, err := temporalenvconfig.LoadDefaultClientOptions()
	if err != nil {
		return nil, fmt.Errorf("failed to load Temporal client options from environment: %w", err)
	}

	// Pin an explicit client identity. The Temporal worker inherits the
	// client identity for its pollers (it only overrides when
	// WorkerOptions.Identity is set, which we don't), so this is the exact
	// string the health server matches against in DescribeTaskQueue —
	// independent of the SDK's internal default-identity format.
	if clientOpts.Identity == "" {
		clientOpts.Identity = defaultWorkerIdentity()
	}

	wfapi, err := pyckworkflowapi.DefaultClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Pyck Workflow API client: %w", err)
	}

	worker := &worker{
		clientOptions:   clientOpts,
		pyckWorkflowAPI: wfapi,
		healthConfig:    defaultHealthServerConfig(),
	}

	for _, opt := range opts {
		opt(worker)
	}

	return worker, nil
}

type worker struct {
	mu sync.Mutex

	client          temporalclient.Client
	clientOptions   temporalclient.Options
	pyckWorkflowAPI pyckworkflowapi.Client
	registry        registry.Registry
	workerErrs      chan error
	workerOptions   temporalworker.Options
	workers         map[string]temporalworker.Worker
	taskQueues      []string
	healthConfig    healthServerConfig
	heartbeatCancel context.CancelFunc
}

// defaultWorkerIdentity returns a stable per-process identity in the
// Temporal-conventional "<pid>@<host>" form. Stable for the process
// lifetime so the health server can match this worker's pollers across
// probes.
func defaultWorkerIdentity() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%d@%s", os.Getpid(), host)
}

func (w *worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
}

// stopLocked tears the worker down; the caller must hold w.mu. Start uses it on
// its failure path, where it already holds the lock and calling Stop would
// deadlock on the non-reentrant mutex.
func (w *worker) stopLocked() {
	if w.heartbeatCancel != nil {
		w.heartbeatCancel()
		w.heartbeatCancel = nil
	}

	if w.workers != nil {
		for _, wk := range w.workers {
			wk.Stop()
		}

		w.workers = nil
	}

	if w.client != nil {
		w.client.Close()
		w.client = nil
	}

	if w.workerErrs != nil {
		close(w.workerErrs)
		w.workerErrs = nil
	}
}

func (w *worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Guard against a second Start leaking the running workers and heartbeat.
	if w.workers != nil {
		return ErrAlreadyRunning
	}

	var (
		err    error
		logger = pycklog.ForContext(ctx)
	)

	if err := w.configure(ctx); err != nil {
		return fmt.Errorf("configure: %w", err)
	}

	w.client, err = temporalclient.DialContext(ctx, w.clientOptions)
	if err != nil {
		logger.Err(err).
			Str("host-port", w.clientOptions.HostPort).
			Str("namespace", w.clientOptions.Namespace).
			Msg("connect to Temporal server")
		return fmt.Errorf("dial client: %w", err)
	}

	logger.Debug().
		Str("host-port", w.clientOptions.HostPort).
		Str("namespace", w.clientOptions.Namespace).
		Msg("connected to Temporal server")

	if err := w.runAllSetupFuncs(ctx); err != nil {
		return fmt.Errorf("run setup funcs: %w", err)
	}

	var (
		activities = w.registry.Activities()
		workflows  = w.registry.Workflows()
	)

	w.registerAllWorkers(ctx, activities, workflows)

	go w.handleWorkerErrors(ctx)

	if err := w.registerAllActivities(ctx, activities); err != nil {
		return fmt.Errorf("register activities: %w", err)
	}

	if err := w.registerWorkflows(ctx, workflows); err != nil {
		return fmt.Errorf("register workflows: %w", err)
	}

	if err := w.registerAllWorkflowWithPyckRetrying(ctx, workflows); err != nil {
		return fmt.Errorf("register workflow signals: %w", err)
	}

	if err := w.startAllWorkers(ctx); err != nil {
		w.stopLocked()
		return fmt.Errorf("start workers: %w", err)
	}

	// Keep this worker's subscriptions alive past their TTL. Tie the heartbeat
	// to Stop() so a worker that shuts down (including on a poller error) stops
	// refreshing and lets its subscriptions expire.
	hbCtx, cancel := context.WithCancel(ctx)
	w.heartbeatCancel = cancel
	go w.runRegistrationHeartbeat(hbCtx, workflows)

	return nil
}

func (w *worker) configure(ctx context.Context) error {
	if w.clientOptions.Logger == nil {
		logger := pycklog.ForContext(ctx).With().
			Str("component", "temporal-client").
			Logger()
		w.clientOptions.Logger = pycklogadapter.TemporalSDKLogAdapter(logger)
	}

	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		return fmt.Errorf("create tracing interceptor: %w", err)
	}

	w.clientOptions.Interceptors = append(w.clientOptions.Interceptors, tracingInterceptor)

	return nil
}

func (w *worker) runAllSetupFuncs(ctx context.Context) error {
	for _, f := range defaultSetupRegistry.Items() {
		if err := f(ctx, &w.registry); err != nil {
			fptr := runtime.FuncForPC(reflect.ValueOf(f).Pointer())
			return fmt.Errorf("setup func %q: %w", fptr.Name(), err)
		}
	}

	return nil
}

func (w *worker) registerAllWorkers(ctx context.Context, activities []registry.ActivityRegistryEntry, workflows []registry.WorkflowRegistryEntry) {
	w.workers = make(map[string]temporalworker.Worker)
	w.workerErrs = make(chan error, 1)

	taskQueues := make(map[string]struct{}, len(activities)+len(workflows))

	for _, a := range activities {
		taskQueues[a.TaskQueue] = struct{}{}
	}

	for _, wf := range workflows {
		taskQueues[wf.TaskQueue()] = struct{}{}
	}

	w.taskQueues = make([]string, 0, len(taskQueues))
	for tq := range taskQueues {
		w.registerWorker(ctx, tq)
		w.taskQueues = append(w.taskQueues, tq)
	}
}

func (w *worker) registerWorker(ctx context.Context, taskQueue string) {
	w.workers[taskQueue] = temporalworker.New(w.client, taskQueue, w.workerOptions)

	pycklog.ForContext(ctx).Debug().
		Str("task-queue", taskQueue).
		Msg("worker registered")
}

func (w *worker) registerAllActivities(ctx context.Context, activities []registry.ActivityRegistryEntry) error {
	for _, activityStruct := range activities {
		if err := w.registerActivities(ctx, activityStruct); err != nil {
			return err
		}
	}

	return nil
}

func (w *worker) registerActivities(ctx context.Context, activity registry.ActivityRegistryEntry) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = fmt.Errorf("%w: %w", ErrRegisterActivity, e)
			} else {
				err = fmt.Errorf("%w: %v", ErrRegisterActivity, r)
			}
		}
	}()

	worker, ok := w.workers[activity.TaskQueue]
	if !ok {
		// this should never happen because workers are created for all task queues
		panic(fmt.Sprintf("no worker for task queue %q", activity.TaskQueue))
	}

	worker.RegisterActivity(activity.Activity)

	pycklog.ForContext(ctx).Debug().
		Str("activities-type", activity.Type).
		Str("worker", activity.TaskQueue).
		Msg("activities registered")

	return nil
}

func (w *worker) registerWorkflows(ctx context.Context, workflows []registry.WorkflowRegistryEntry) error {
	for _, workflow := range workflows {
		if err := w.registerWorkflow(ctx, workflow); err != nil {
			return err
		}
	}

	return nil
}

func (w *worker) registerWorkflow(ctx context.Context, workflow registry.WorkflowRegistryEntry) (err error) {
	queue := workflow.TaskQueue()

	worker, ok := w.workers[queue]
	if !ok {
		// this should never happen because workers are created for all task queues
		panic(fmt.Sprintf("no worker for task queue %q", queue))
	}

	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = fmt.Errorf("%w: %w", ErrRegisterWorkflow, e)
			} else {
				err = fmt.Errorf("%w: %v", ErrRegisterWorkflow, r)
			}
		}
	}()

	worker.RegisterWorkflowWithOptions(workflow.Workflow, workflow.RegisterOptions)

	pycklog.ForContext(ctx).Debug().
		Str("workflow-type", workflow.Type()).
		Str("task-queue", workflow.TaskQueue()).
		Msg("workflow registered")

	return nil
}

func (w *worker) registerAllWorkflowWithPyck(ctx context.Context, workflows []registry.WorkflowRegistryEntry) error {
	logger := pycklog.ForContext(ctx)

	// Build a set of local workflow names for quick lookup.
	workflowName := make(map[string]struct{}, len(workflows))
	for _, wf := range workflows {
		workflowName[wf.Type()] = struct{}{}
	}

	// Fetch remote workflows from the Pyck Workflow API.
	remoteWorkflows, err := w.pyckWorkflowAPI.GetWorkflows(ctx, pyckworkflowapi.GetWorkflowsArgs{})
	if err != nil {
		return fmt.Errorf("get registered pyck workflows: %w", err)
	}

	// Delete remote workflows that are not present in the local registry.
	for _, edge := range remoteWorkflows.Workflows.Edges {
		wfName := edge.Node.Name

		if _, ok := workflowName[wfName]; ok {
			continue // found
		}

		if _, err := w.pyckWorkflowAPI.DeleteWorkflow(ctx, pyckworkflowapi.DeleteWorkflowArgs{
			Id: edge.Node.ID,
		}); err != nil {
			return fmt.Errorf("delete pyck workflow %q not in local registry: %w", wfName, err)
		}

		logger.Debug().
			Str("workflow", wfName).
			Msg("deleted pyck workflow not in local registry")
	}

	return w.registerLocalWorkflows(ctx, workflows)
}

// registerLocalWorkflows (re-)registers every local workflow's signals. Unlike
// registerAllWorkflowWithPyck it skips the remote reconcile/delete, so it is
// safe to call repeatedly from the heartbeat to refresh subscription TTLs.
func (w *worker) registerLocalWorkflows(ctx context.Context, workflows []registry.WorkflowRegistryEntry) error {
	for _, wf := range workflows {
		if err := w.registerWorkflowWithPyck(ctx, wf); err != nil {
			return fmt.Errorf("register pyck workflow %q: %w", wf.Type(), err)
		}
	}

	return nil
}

// registerAllWorkflowWithPyckRetrying runs the initial registration, retrying
// on transient serialization/deadlock conflicts. The workflow service already
// retries these internally; this is the last-resort guard so a startup burst
// (e.g. a fleet-wide rollout) that outlasts the server-side budget refreshes
// rather than crash-looping the pod.
func (w *worker) registerAllWorkflowWithPyckRetrying(ctx context.Context, workflows []registry.WorkflowRegistryEntry) error {
	return RetryOnConflict(ctx, Config.RegistrationRetryAttempts, Config.RegistrationRetryBackoff, func() error {
		return w.registerAllWorkflowWithPyck(ctx, workflows)
	})
}

func RetryOnConflict(ctx context.Context, attempts int, baseBackoff time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}

	logger := pycklog.ForContext(ctx)

	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		if attempt >= attempts || !isRetryableRegistrationError(err) {
			return err
		}

		backoff := baseBackoff << (attempt - 1)
		if maxBackoff := time.Minute; backoff <= 0 || backoff > maxBackoff {
			backoff = maxBackoff // guard against a large-attempt shift overflowing
		}

		// Jitter to ±50%: replicas in a rollout fail in lockstep, so an
		// undithered backoff makes every retry wave re-collide.
		//nolint:gosec // non-crypto jitter
		backoff = backoff/2 + time.Duration(rand.Int64N(int64(backoff)))

		logger.Warn().
			Err(err).
			Int("attempt", attempt).
			Dur("backoff", backoff).
			Msg("conflict, retrying")

		// Check cancellation before waiting, not only inside the select. If
		// this goroutine is descheduled past the backoff, both select cases
		// below are ready at once and Go picks between them at random, so a
		// cancelled context would abort the wait only about half the time and
		// otherwise buy another attempt. Under load that is exactly what
		// happens; the select alone is only reliable when it is reached before
		// the timer fires.
		if err := ctx.Err(); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// runRegistrationHeartbeat periodically refreshes this worker's subscription
// TTLs until ctx is cancelled. A failed refresh is non-fatal: the next tick
// retries and the TTL leaves ample margin.
func (w *worker) runRegistrationHeartbeat(ctx context.Context, workflows []registry.WorkflowRegistryEntry) {
	interval := Config.HeartbeatInterval
	if interval <= 0 {
		return
	}

	logger := pycklog.ForContext(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.registerLocalWorkflows(ctx, workflows); err != nil {
				logger.Warn().Err(err).Msg("workflow subscription heartbeat failed")
			}
		}
	}
}

// isRetryableRegistrationError reports whether err is a transient PostgreSQL
// conflict that a fresh attempt can resolve. The error crosses the GraphQL
// boundary as text, so it is matched by message rather than by type.
//
// 23505 counts: replicas racing to create a brand-new workflow can lose with a
// plain duplicate-key error rather than 40001, and nothing retries that
// server-side. A retry then finds the winner's row and takes the update path.
func isRetryableRegistrationError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not serialize") ||
		strings.Contains(msg, "40001") ||
		strings.Contains(msg, "deadlock detected") ||
		strings.Contains(msg, "40p01") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}

func (w *worker) registerWorkflowWithPyck(ctx context.Context, wf registry.WorkflowRegistryEntry) error {
	signalInputs := make([]*model.RegisterWorkflowSignalInput, len(wf.Signals))

	for i, s := range wf.Signals {
		signalInputs[i] = &model.RegisterWorkflowSignalInput{
			NatsTopic:          s.Topic.String(),
			TemporalSignalType: s.SignalType,
			TemporalSignal:     s.SignalName,
			FilterRule:         s.FilterRule,
		}
	}

	// The client identity is stable for the process lifetime and unique per
	// worker, so it scopes this worker's subscriptions server-side.
	identity := w.clientOptions.Identity

	input := model.RegisterWorkflowWithSignalsInput{
		Name:      wf.Type(),
		TaskQueue: wf.TaskQueue(),
		WorkerID:  &identity,
		Signals:   signalInputs,
	}

	if wf.Data != nil {
		input.Data = wf.Data
	}

	if wf.DataTypeID != uuid.Nil {
		input.DataTypeID = &wf.DataTypeID
	}

	if wf.DataTypeSlug != "" {
		input.DataTypeSlug = &wf.DataTypeSlug
	}

	if _, err := w.pyckWorkflowAPI.RegisterWorkflow(ctx, pyckworkflowapi.RegisterWorkflowArgs{
		Input: input,
	}); err != nil {
		return err
	}

	pycklog.ForContext(ctx).Debug().
		Str("workflow-name", wf.Type()).
		Int("signals-count", len(signalInputs)).
		Msg("registered pyck workflow")

	return nil
}

func (w *worker) handleWorkerErrors(ctx context.Context) {
	logger := pycklog.ForContext(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-w.workerErrs:
			if !ok {
				// Channel closed, shutdown already initiated
				return
			}

			logger.Error().
				Err(err).
				Msg("worker error")

			w.Stop() // Initiate shutdown

			return
		}
	}
}

func (w *worker) startAllWorkers(ctx context.Context) error {
	for q, worker := range w.workers {
		if err := worker.Start(); err != nil {
			return fmt.Errorf("start worker %q: %w", q, err)
		}

		pycklog.ForContext(ctx).Debug().
			Str("task-queue", q).
			Msg("worker started")
	}

	return nil
}
