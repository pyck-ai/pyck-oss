package workflowsdk

// Test-only re-exports so the external workflowsdk_test package can exercise
// these package-private helpers.

import (
	"context"

	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"

	pyckworkflowapi "github.com/pyck-ai/pyck/backend/workflow/api"

	"github.com/pyck-ai/pyck/backend/workflowsdk/registry"
)

var (
	UIBundleMetadataEntries = uiBundleMetadataEntries
)

// TestWorker exposes the registration and shutdown plumbing of the unexported
// worker so tests can drive it against a fake workflow API without a Temporal
// server.
type TestWorker struct {
	w *worker
}

// NewTestWorker builds a worker with the given API client and identity.
func NewTestWorker(api pyckworkflowapi.Client, identity string) *TestWorker {
	return &TestWorker{w: &worker{
		pyckWorkflowAPI: api,
		clientOptions:   temporalclient.Options{Identity: identity},
	}}
}

// Identity is the client identity the worker registers under.
func (t *TestWorker) Identity() string { return t.w.clientOptions.Identity }

// RegisterLocal runs the same registration the Start path and the heartbeat use.
func (t *TestWorker) RegisterLocal(ctx context.Context, workflows []registry.WorkflowRegistryEntry) error {
	return t.w.registerLocalWorkflowsRetrying(ctx, workflows)
}

// RegisterSubscriptions runs the subscription registration step of Start,
// including its teardown when registration fails partway.
func (t *TestWorker) RegisterSubscriptions(ctx context.Context, workflows []registry.WorkflowRegistryEntry) error {
	t.w.mu.Lock()
	defer t.w.mu.Unlock()
	return t.w.registerSubscriptionsLocked(ctx, workflows)
}

// MarkRegistered records ctx as the Start context, as Start does once the
// worker's subscriptions for workflows exist.
func (t *TestWorker) MarkRegistered(ctx context.Context, workflows []registry.WorkflowRegistryEntry) {
	t.w.mu.Lock()
	defer t.w.mu.Unlock()
	t.w.startCtx = ctx
	t.w.registeredWorkerIDs = t.w.subscriptionWorkerIDs(workflows)
}

// StartHeartbeat starts the registration heartbeat as Start does; Stop cancels
// and waits for it.
func (t *TestWorker) StartHeartbeat(ctx context.Context, workflows []registry.WorkflowRegistryEntry) {
	t.w.mu.Lock()
	defer t.w.mu.Unlock()
	t.w.startHeartbeatLocked(ctx, workflows)
}

// Stop tears the worker down through the production Stop path.
func (t *TestWorker) Stop() { t.w.Stop() }

// NewIdentityWorker builds a worker through the same option application and
// identity defaulting as NewWorker, but without dialing anything or reading the
// environment. base stands in for the env-derived client options.
func NewIdentityWorker(base temporalclient.Options, opts ...WorkerOption) *TestWorker {
	return &TestWorker{w: newWorkerFrom(base, nil, opts...)}
}

// RunHeartbeat runs the registration heartbeat loop until ctx is cancelled.
func (t *TestWorker) RunHeartbeat(ctx context.Context, workflows []registry.WorkflowRegistryEntry) {
	t.w.runRegistrationHeartbeat(ctx, workflows)
}

// StartPromotion runs the promotion RunDefaultWorker triggers once its workers
// poll.
var StartPromotion = startPromotion

// CheckWorkerIDLengths is the early worker ID length check Start runs.
func (t *TestWorker) CheckWorkerIDLengths(workflows []registry.WorkflowRegistryEntry) error {
	return t.w.checkWorkerIDLengths(workflows)
}

// SubscriptionWorkerID is the worker ID the worker registers the subscriptions
// of taskQueue under.
func (t *TestWorker) SubscriptionWorkerID(taskQueue string) string {
	return t.w.subscriptionWorkerID(taskQueue)
}

// Configure runs the client-option setup Start runs before dialing.
func (t *TestWorker) Configure(ctx context.Context) error { return t.w.configure(ctx) }

// WorkerInterceptors returns the worker-side interceptors among the worker's
// configured client interceptors, as the SDK would pick them up when a worker
// is created from the dialed client.
func (t *TestWorker) WorkerInterceptors() []interceptor.WorkerInterceptor {
	var out []interceptor.WorkerInterceptor

	for _, ci := range t.w.clientOptions.Interceptors {
		if wi, ok := ci.(interceptor.WorkerInterceptor); ok {
			out = append(out, wi)
		}
	}

	return out
}
