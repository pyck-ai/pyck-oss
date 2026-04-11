package workflowsdk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"reflect"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"

	"github.com/google/uuid"
	temporalclient "go.temporal.io/sdk/client"
	temporalenvconfig "go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/contrib/opentelemetry"
	temporalworker "go.temporal.io/sdk/worker"

	pycklog "github.com/pyck-ai/pyck/backend/common/log"
	pycklogadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	pyckotel "github.com/pyck-ai/pyck/backend/common/otel"
	pyckworkflowapi "github.com/pyck-ai/pyck/backend/workflow/api"
	"github.com/pyck-ai/pyck/backend/workflow/model"

	"github.com/pyck-ai/pyck/backend/workflowsdk/registry"
)

var (
	ErrReadBuildInfo  = fmt.Errorf("failed to read build info")
	ErrAlreadyRunning = fmt.Errorf("worker is already running")
)

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

	wfapi, err := pyckworkflowapi.DefaultClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Pyck Workflow API client: %w", err)
	}

	worker := &worker{
		clientOptions:   clientOpts,
		pyckWorkflowAPI: wfapi,
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
}

func (w *worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.workers != nil {
		for _, w := range w.workers {
			w.Stop()
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

	if err := w.registerAllWorkflowWithPyck(ctx, workflows); err != nil {
		return fmt.Errorf("register workflow signals: %w", err)
	}

	if err := w.startAllWorkers(ctx); err != nil {
		w.Stop()
		return fmt.Errorf("start workers: %w", err)
	}

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

	for tq := range taskQueues {
		w.registerWorker(ctx, tq)
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

	// Register signals for each workflow in the local registry.
	for _, wf := range workflows {
		if err := w.registerWorkflowWithPyck(ctx, wf); err != nil {
			return fmt.Errorf("register pyck workflow %q: %w", wf.Type(), err)
		}
	}

	return nil
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

	input := model.RegisterWorkflowWithSignalsInput{
		Name:      wf.Type(),
		TaskQueue: wf.TaskQueue(),
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
