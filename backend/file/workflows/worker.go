package workflows

import (
	"context"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	imageanalyzer "github.com/pyck-ai/pyck/backend/file/workflows/image-analyzer"
)

const (
	TemporalFileTaskQueue = "pyck-file-task-queue"

	AnalyzeImageWorkflow = "AnalyzeImageWorkflow"
)

type TemporalWorker struct {
	temporalWorker worker.Worker
	taskQueueName  string
	cli            client.Client
	versioning     commonworkflow.VersioningConfig
	depOpts        worker.DeploymentOptions
}

// NewTemporalWorker builds the file worker. Versioning registers a Temporal
// deployment version (#1132) so a rolling deploy leaves in-flight executions on
// the version they started on; the zero value keeps the worker unversioned.
func NewTemporalWorker(client client.Client, taskQueue string, versioning commonworkflow.VersioningConfig) (*TemporalWorker, error) {
	// Unversioned resolves to the zero DeploymentOptions, which is already
	// "no versioning" — no branch needed.
	depOpts, _, err := versioning.DeploymentOptionsFromBuild()
	if err != nil {
		return nil, err
	}

	return &TemporalWorker{
		temporalWorker: worker.New(client, taskQueue, worker.Options{DeploymentOptions: depOpts}),
		taskQueueName:  taskQueue,
		cli:            client,
		versioning:     versioning,
		depOpts:        depOpts,
	}, nil
}

// PromoteVersion makes this worker's version current, for deployments the
// temporal-worker-controller does not manage.
func (tw *TemporalWorker) PromoteVersion(ctx context.Context) {
	tw.versioning.StartPromotion(ctx, tw.cli, tw.depOpts)
}

// Versioned reports whether the worker registered a deployment version — an
// unversioned worker in a deployed environment is otherwise invisible.
func (tw *TemporalWorker) Versioned() bool { return tw.depOpts.UseVersioning }

func (tw *TemporalWorker) Start() error {
	return tw.temporalWorker.Start()
}

func (w *TemporalWorker) Stop() {
	w.temporalWorker.Stop()
}

func (tw *TemporalWorker) RegisterImageAnalyzerWorkflow() {
	// Register workflows and activities
	imageAnalyzerWorkflowOptions := workflow.RegisterOptions{
		Name: AnalyzeImageWorkflow,
	}

	tw.temporalWorker.RegisterWorkflowWithOptions(imageanalyzer.ImageAnalyzeWorkflow, imageAnalyzerWorkflowOptions)
	tw.temporalWorker.RegisterActivity(imageanalyzer.AnalyzeImageActivity)
}
