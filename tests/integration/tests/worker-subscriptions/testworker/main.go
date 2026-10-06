// Command testworker is a minimal real workflowsdk worker for the
// worker-subscriptions integration suite. One process serves exactly one
// workflow, because the SDK takes its workflows from the package-level setup
// registry: the suite starts separate processes to get separate "worker types".
//
// Configuration comes from the environment, on top of the SDK's own
// (PYCK_GATEWAY_URL, PYCK_API_TOKEN, PYCK_API_TENANT_ID, TEMPORAL_*, PYCK_WORKER_*):
//
//	WS_WORKFLOW    workflow type name to register
//	WS_TASK_QUEUE  task queue it is served on
//	WS_TENANT_ID   tenant whose inventory item-create events start it
//	WS_FILTER_RULE optional FEEL filter rule on its start signal (empty: none)
//
// It runs workflowsdk.RunDefaultWorker, so SIGTERM triggers the SDK's normal
// Stop path (unregisterWorker, then worker shutdown). The health server is off:
// several processes run on one host and would fight over its port. The
// Temporal client options are built here from TEMPORAL_ADDRESS,
// TEMPORAL_NAMESPACE and TEMPORAL_API_KEY with TLS disabled, because the SDK's
// env loading cannot turn TLS off when an API key is set. The worker still
// gets its default per-instance identity, which the SDK sets after options.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	temporalclient "go.temporal.io/sdk/client"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/workflowsdk"
	"github.com/pyck-ai/pyck/backend/workflowsdk/registry"
	"github.com/pyck-ai/pyck/backend/workflowsdk/signal"
)

func main() {
	name := mustEnv("WS_WORKFLOW")
	queue := mustEnv("WS_TASK_QUEUE")

	filterRule := os.Getenv("WS_FILTER_RULE")

	tenantID, err := uuid.Parse(mustEnv("WS_TENANT_ID"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "testworker: WS_TENANT_ID: %v\n", err)
		os.Exit(2)
	}

	var signalOpts []signal.SignalOption
	if filterRule != "" {
		signalOpts = append(signalOpts, signal.WithFilterRule(filterRule))
	}

	workflowsdk.Setup(func(_ context.Context, r *registry.Registry) error {
		_, err := r.RegisterWorkflow(
			noop,
			registry.WithWorkflowRegisterOptions(temporalworkflow.RegisterOptions{Name: name}),
			registry.WithWorkflowStartOptions(temporalclient.StartWorkflowOptions{TaskQueue: queue}),
			registry.WithWorkflowSignals(signal.NewStartSignal(events.MutationEventWithReplyTopic{
				TenantID:      tenantID,
				ServiceName:   "inventory",
				SchemaName:    "Item",
				OperationName: "create",
			}, signalOpts...)),
		)

		return err
	})

	// Temporal's envconfig ignores TEMPORAL_TLS=false once an API key is set:
	// it leaves ConnectionOptions.TLS nil without setting TLSDisabled, and the
	// API-key credentials then switch TLS back on. The local frontend is
	// plaintext, so pass the client options with TLS explicitly disabled.
	clientOpts := temporalclient.Options{
		HostPort:          mustEnv("TEMPORAL_ADDRESS"),
		Namespace:         mustEnv("TEMPORAL_NAMESPACE"),
		Credentials:       temporalclient.NewAPIKeyStaticCredentials(mustEnv("TEMPORAL_API_KEY")),
		ConnectionOptions: temporalclient.ConnectionOptions{TLSDisabled: true},
	}

	workflowsdk.RunDefaultWorker(
		workflowsdk.WithoutHealthServer(),
		workflowsdk.WithClientOptions(clientOpts),
	)
}

// noop finishes immediately. The second parameter is required: the signal
// router forwards the event payload as the workflow input.
func noop(_ temporalworkflow.Context, _ any) error { return nil }

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "testworker: %s is required\n", key)
		os.Exit(2)
	}

	return v
}
