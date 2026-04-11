package workflows

import (
	imageanalyzer "github.com/pyck-ai/pyck/backend/file/workflows/image-analyzer"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	TemporalFileTaskQueue = "pyck-file-task-queue"

	AnalyzeImageWorkflow = "AnalyzeImageWorkflow"
)

type TemporalWorker struct {
	temporalWorker worker.Worker
	taskQueueName  string
}

func NewTemporalWorker(client client.Client, taskQueue string) (*TemporalWorker, error) {
	temporalWorker := worker.New(client, taskQueue, worker.Options{})
	return &TemporalWorker{
		temporalWorker: temporalWorker,
		taskQueueName:  taskQueue,
	}, nil
}

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
