package workflowsdk

import (
	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
)

type WorkerOption func(*worker)

func WithClientOptions(opts temporalclient.Options) WorkerOption {
	return func(w *worker) {
		w.clientOptions = opts
	}
}

func WithWorkerOptions(opts temporalworker.Options) WorkerOption {
	return func(w *worker) {
		w.workerOptions = opts
	}
}
