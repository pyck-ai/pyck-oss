// Command testworker is the real workflowsdk worker of the event-delivery
// integration suite. It serves the observer workflow under a name and task
// queue given by the suite, and subscribes it to inventory item events:
//
//	ED_START    "true": a start signal on item create
//	ED_SIGNALS  comma-separated intermediate signals (observer.SignalA, ...)
//	            on item update
//
// Two processes with the same ED_WORKFLOW and ED_TASK_QUEUE are two workers of
// one workflow (each owns its own subscriptions, keyed by its worker ID); that
// is how the suite gets one event delivered to several signal names, and how
// it stops one of two workers. Everything else comes from the SDK's own
// environment (PYCK_GATEWAY_URL, PYCK_API_TOKEN, PYCK_API_TENANT_ID,
// TEMPORAL_*, PYCK_WORKER_*), as in the worker-subscriptions suite:
//
//	ED_WORKFLOW    workflow type name to register
//	ED_TASK_QUEUE  task queue it is served on
//	ED_TENANT_ID   tenant whose item events it subscribes to
//
// It runs workflowsdk.RunDefaultWorker, so SIGTERM triggers the SDK's normal
// Stop path. The health server is off because several processes share a host.
// The Temporal client options are built here with TLS disabled: the SDK's env
// loading cannot turn TLS off once an API key is set.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	temporalclient "go.temporal.io/sdk/client"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/workflowsdk"
	"github.com/pyck-ai/pyck/backend/workflowsdk/registry"
	"github.com/pyck-ai/pyck/backend/workflowsdk/signal"

	"github.com/pyck-ai/pyck/tests/integration/tests/event-delivery/observer"
)

func main() {
	name := mustEnv("ED_WORKFLOW")
	queue := mustEnv("ED_TASK_QUEUE")

	tenantID, err := uuid.Parse(mustEnv("ED_TENANT_ID"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "testworker: ED_TENANT_ID: %v\n", err)
		os.Exit(2)
	}

	itemTopic := func(operation string) events.MutationEventTopic {
		return events.MutationEventTopic{
			TenantID:      tenantID,
			ServiceName:   "inventory",
			SchemaName:    "Item",
			OperationName: operation,
		}
	}

	var signals []*signal.Signal

	if os.Getenv("ED_START") == "true" {
		signals = append(signals, signal.NewStartSignal(itemTopic("create")))
	}

	for _, sig := range strings.Split(os.Getenv("ED_SIGNALS"), ",") {
		if sig = strings.TrimSpace(sig); sig != "" {
			signals = append(signals, signal.NewIntermediateSignal(itemTopic("update"), sig))
		}
	}

	workflowsdk.Setup(func(_ context.Context, r *registry.Registry) error {
		_, err := r.RegisterWorkflow(
			observer.Workflow,
			registry.WithWorkflowRegisterOptions(temporalworkflow.RegisterOptions{Name: name}),
			registry.WithWorkflowStartOptions(temporalclient.StartWorkflowOptions{TaskQueue: queue}),
			registry.WithWorkflowSignals(signals...),
		)

		return err
	})

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

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "testworker: %s is required\n", key)
		os.Exit(2)
	}

	return v
}
