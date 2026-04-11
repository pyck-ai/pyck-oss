package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pbinitiative/feel"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/workflow"
	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflow "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflow"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/model"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
)

const (
	// requestReplyQueueGroup is the queue group name for NATS request/reply subscribers.
	// Queue groups ensure that only one member receives each message for load balancing.
	requestReplyQueueGroup = "pyck-request-reply-queue"
)

var (
	ErrInvalidEventMessage = errors.New("invalid workflow event message")
	ErrUnknownOperation    = errors.New("unknown operation for event")
	ErrUnknownSignalType   = errors.New("unknown signal type for workflow")
	ErrSkipFilter          = errors.New("filter rule evaluated to false")

	// DefaultCreatePolicy is the default workflow start policy for "create" operations.
	//
	// This policy prevents duplicate workflows for create operations unless the previous
	// workflow has failed. This ensures that:
	// - Only one successful creation workflow runs per entity
	// - Failed creation workflows can be retried automatically
	// - Race conditions between multiple creation requests are prevented
	//
	// Behavior:
	// - ALLOW_DUPLICATE_FAILED_ONLY: Allows starting a new workflow only if the previous
	//   workflow with the same ID has failed (completed with error or was terminated)
	// - FAIL: If a workflow with the same ID is already running or completed successfully,
	//   starting a new one will fail with WorkflowExecutionAlreadyStartedError
	// - WorkflowExecutionErrorWhenAlreadyStarted: true ensures an error is returned instead
	//   of silently using the existing workflow
	//
	// Use Case:
	// Creating a new inventory item, order, or other entity where duplicate creations
	// should be prevented but retries after failure should be allowed.
	//
	// For more details, see Temporal documentation:
	// https://docs.temporal.io/encyclopedia/detecting-activity-failures#workflow-id-reuse-policy
	DefaultCreatePolicy = &workflow.StartWorkflowOptions{
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		WorkflowIDConflictPolicy:                 enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}

	// DefaultUpdatePolicy is the default workflow start policy for "update" operations.
	//
	// This policy allows multiple concurrent update workflows for the same entity.
	// This is necessary because:
	// - Updates may arrive while a previous update is still processing
	// - Rejecting updates could lead to lost changes
	// - The workflow logic itself should handle concurrent updates (e.g., using versioning)
	//
	// Behavior:
	// - ALLOW_DUPLICATE: Always allows starting a new workflow regardless of existing workflows
	// - UNSPECIFIED: Uses Temporal's default conflict resolution
	// - WorkflowExecutionErrorWhenAlreadyStarted: false allows the workflow to start silently
	//
	// Use Case:
	// Updating inventory quantities, order status, or other fields where concurrent updates
	// are expected and the workflow handles merging or sequencing the changes.
	//
	// Important: The workflow implementation MUST handle concurrent updates properly, e.g.:
	// - Using optimistic locking with version numbers
	// - Reading the latest state at the start of each update
	// - Using Temporal's signal handlers for ordered processing
	//
	// For more details, see Temporal documentation:
	// https://docs.temporal.io/encyclopedia/detecting-activity-failures#workflow-id-reuse-policy
	DefaultUpdatePolicy = &workflow.StartWorkflowOptions{
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowIDConflictPolicy:                 enums.WORKFLOW_ID_CONFLICT_POLICY_UNSPECIFIED,
		WorkflowExecutionErrorWhenAlreadyStarted: false,
	}

	// DefaultDeletePolicy is the default workflow start policy for "delete" operations.
	//
	// This policy prevents multiple concurrent delete workflows for the same entity by
	// reusing an existing running workflow if one exists. This ensures:
	// - Only one delete operation processes per entity at a time
	// - Duplicate delete requests don't cause errors
	// - Idempotency: multiple delete requests for the same entity are safely deduplicated
	//
	// Behavior:
	// - REJECT_DUPLICATE: If a workflow with the same ID is already running, use that
	//   workflow instead of starting a new one
	// - USE_EXISTING: Return the handle to the existing workflow without error
	// - WorkflowExecutionErrorWhenAlreadyStarted: false allows transparent reuse
	//
	// Use Case:
	// Deleting an inventory item, order, or other entity where multiple delete requests
	// should be idempotent and not cause conflicts.
	//
	// Important: The workflow ID should be deterministic (e.g., based on entity ID)
	// so that duplicate delete requests for the same entity will have the same
	// workflow ID and thus be properly deduplicated.
	//
	// For more details, see Temporal documentation:
	// https://docs.temporal.io/encyclopedia/detecting-activity-failures#workflow-id-reuse-policy
	DefaultDeletePolicy = &workflow.StartWorkflowOptions{
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy:                 enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowExecutionErrorWhenAlreadyStarted: false,
	}
)

// SignalRouterConfig contains configuration for creating a SignalRouter.
type SignalRouterConfig struct {
	TemporalURL     string
	EventPublisher  *events.EventPublisher
	NatsClient      *nats.Conn
	JetstreamClient jetstream.JetStream
	StreamName      string
	ServiceName     string
	// ClientFactory is optional. If nil, workflow.DefaultClientFactory will be used.
	ClientFactory workflow.ClientFactory
}

// NewSignalRouter creates a new SignalRouter instance with the provided configuration.
func NewSignalRouter(entClient *ent.Client, cfg SignalRouterConfig) *SignalRouter {
	// Use provided factory or create default one
	factory := cfg.ClientFactory
	if factory == nil {
		factory = workflow.NewDefaultClientFactory(
			cfg.TemporalURL,
			nil, // DefaultClientFactory will create its own cache
		)
	}

	return &SignalRouter{
		clientFactory:   factory,
		eventPublisher:  cfg.EventPublisher,
		natsClient:      cfg.NatsClient,
		jetstreamClient: cfg.JetstreamClient,
		streamName:      cfg.StreamName,
		serviceName:     cfg.ServiceName,
		dbClient:        entClient,
	}
}

// SignalRouter routes NATS events to Temporal workflows.
//
// The SignalRouter is the central component responsible for:
// 1. Subscribing to NATS topics for mutation events and workflow state changes
// 2. Matching incoming events to workflow configurations stored in the database
// 3. Starting new workflows or signaling existing ones based on matching rules
// 4. Managing Temporal client connections per tenant namespace
//
// Lifecycle:
// - Create with NewSignalRouter() during application initialization
// - Call Start() once during application startup to begin processing events
// - Call Stop() during graceful shutdown to close subscriptions and wait for in-flight requests
//
// Thread Safety:
// The SignalRouter is designed to handle concurrent events safely. The internal
// wait group (wg) tracks in-flight requests, and Stop() blocks until all handlers complete.
//
// Error Handling:
// - Invalid events are logged and replied to with error responses (for request/reply topics)
// - Transient errors (e.g., Temporal connection failures) are logged but don't crash the router
// - Database errors during workflow lookup cause the event to be rejected with an error reply
type SignalRouter struct {
	clientFactory   workflow.ClientFactory
	eventPublisher  *events.EventPublisher
	natsClient      *nats.Conn
	jetstreamClient jetstream.JetStream
	streamName      string
	serviceName     string
	consumer        jetstream.ConsumeContext
	wg              sync.WaitGroup
	dbClient        *ent.Client
}

// Start initializes the SignalRouter by subscribing to events and starting listeners.
//
// This method should be called exactly once during application startup, after the database
// and NATS connections have been established. It sets up queue subscribers for:
//
// 1. Mutation events with reply (request/reply pattern for CRUD operations)
// 2. Temporal workflow state change events (for chaining workflows)
//
// The context passed to Start() is used for logging and is captured for use in event handlers.
// Event handlers will continue processing even if this context is cancelled - they track
// their own lifecycle via the internal wait group.
//
// Start() returns immediately after setting up subscriptions. Events are processed
// asynchronously in goroutines. Each event handler increments the wait group on entry
// and decrements it on exit, allowing Stop() to wait for completion.
//
// Important: Start() does not block. The application must keep running (e.g., via a
// signal wait or HTTP server) to allow event processing to continue.
//
// Error Handling:
// Returns an error if subscription setup fails. If Start() returns an error, the
// router is in an undefined state and should not be used. Call Stop() to clean up.
//
// Example:
//
//	router := NewSignalRouter(entClient, cfg)
//	if err := router.Start(ctx); err != nil {
//	    log.Fatal(err)
//	}
//	defer router.Stop()
//	<- ctx.Done() // keep the application running...
func (wr *SignalRouter) Start(ctx context.Context) error {
	// TODO(michael): These queue subscribers have no backpressure limits, which could
	// lead to memory exhaustion under high load. Consider implementing:
	// 1. Semaphore-based concurrency limiting (e.g., max 100 concurrent handlers)
	// 2. Metrics for queue depth and processing time
	// 3. JetStream pull consumers with explicit MaxAckPending for rate limiting
	// 4. Circuit breaker pattern if Temporal is slow or unavailable

	// Handle fire-and-forget mutation events (asyncsignals mode)
	if _, err := wr.natsClient.QueueSubscribe(events.MutationEventTopic{
		StreamName: wr.streamName,
	}.String(), requestReplyQueueGroup, func(msg *nats.Msg) {
		wr.wg.Add(1)
		defer wr.wg.Done()

		logger := log.ForContext(ctx).With().
			Str("topic", msg.Subject).
			Logger()

		startedWorkflows, err := wr.HandleMutationEvent(ctx, msg)
		if err != nil {
			logger.Error().
				Err(err).
				Msg("failed to process fire-and-forget mutation event")
			return
		}

		logger.Info().
			Int("workflow_count", len(startedWorkflows)).
			Msg("fire-and-forget mutation event processed successfully")
	}); err != nil {
		return fmt.Errorf("failed to subscribe to fire-and-forget mutation events: %w", err)
	}

	// Handle request/reply mutation events
	if _, err := wr.natsClient.QueueSubscribe(events.MutationEventWithReplyTopic{
		StreamName: wr.streamName,
	}.String(), requestReplyQueueGroup, func(msg *nats.Msg) {
		wr.wg.Add(1)
		defer wr.wg.Done()

		logger := log.ForContext(ctx).With().
			Str("topic", msg.Subject).
			Logger()

		startedWorkflows, err := wr.HandleMutationEventWithReply(ctx, msg)
		if err != nil {
			logger.Error().
				Err(err).
				Msg("failed to process request/reply mutation event")
			wr.sendErrorReply(ctx, msg.Reply, err)
			return
		}

		if err := wr.sendSuccessReply(ctx, msg.Reply, startedWorkflows); err != nil {
			log.ForContext(ctx).Error().
				Err(err).
				Msg("failed to send success reply")
			return
		}

		logger.Info().
			Int("workflow_count", len(startedWorkflows)).
			Msg("request/reply mutation event processed successfully")
	}); err != nil {
		return fmt.Errorf("failed to subscribe to request/reply mutation events: %w", err)
	}

	// Handle temporal workflow state change events
	if _, err := wr.natsClient.QueueSubscribe(events.TemporalWorkflowStateChangeTopic{
		StreamName: wr.streamName,
	}.String(), requestReplyQueueGroup, func(msg *nats.Msg) {
		wr.wg.Add(1)
		defer wr.wg.Done()

		logger := log.ForContext(ctx).With().
			Str("topic", msg.Subject).
			Logger()

		startedWorkflows, err := wr.handleTemporalWorkflowStateChange(ctx, msg)
		if err != nil {
			logger.Error().
				Err(err).
				Msg("failed to process temporal workflow state change event")
			wr.sendErrorReply(ctx, msg.Reply, err)
			return
		}

		logger.Info().
			Int("workflow_count", len(startedWorkflows)).
			Msg("temporal workflow state change event processed successfully")
	}); err != nil {
		return fmt.Errorf("failed to subscribe to temporal workflow state change events: %w", err)
	}

	log.ForContext(ctx).Info().
		Str("service", wr.serviceName).
		Msg("workflow router started successfully")

	return nil
}

// Stop gracefully shuts down the SignalRouter and cleans up resources.
//
// Stop() performs the following steps in order:
// 1. Stops the NATS consumer (if using JetStream pull consumer)
// 2. Waits for all in-flight event handlers to complete (via wait group)
// 3. Closes all Temporal client connections via the client factory
//
// This method blocks until all in-flight requests have finished processing.
// Event handlers that are currently executing will run to completion before
// Stop() returns. No new events will be accepted once Stop() is called.
//
// Stop() is safe to call multiple times - subsequent calls are no-ops.
//
// Usage:
// Always call Stop() during application shutdown to ensure:
// - No events are lost mid-processing
// - Database transactions complete
// - Temporal workflows are properly started/signaled
// - Network connections are cleanly closed
//
// Example:
//
//	router := NewSignalRouter(entClient, cfg)
//	defer router.Stop()  // Ensures cleanup even on panic
//
//	if err := router.Start(ctx); err != nil {
//	    log.Fatal(err)
//	}
func (wr *SignalRouter) Stop() {
	if wr.consumer != nil {
		wr.consumer.Stop()
	}

	// Wait for all in-flight requests to complete
	wr.wg.Wait()

	if wr.clientFactory != nil {
		wr.clientFactory.Close()
	}
}

// GetClient retrieves or creates a Temporal client for the given namespace.
// Clients are cached by the factory to avoid unnecessary connections.
// This method is thread-safe.
func (wr *SignalRouter) GetClient(ctx context.Context, namespace string) (*workflow.Client, error) {
	return wr.clientFactory.GetClient(ctx, namespace)
}

// HandleMutationEvent parses a mutation event message and triggers matching workflows.
// It is the shared core used by both the request/reply and fire-and-forget subscriptions.
func (wr *SignalRouter) HandleMutationEvent(ctx context.Context, msg *nats.Msg) ([]*model.TemporalWorkflow, error) {
	var event events.MutationEventMessage

	if err := json.Unmarshal(msg.Data, &event); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEventMessage, err)
	}

	if event.Type == "" {
		return nil, fmt.Errorf("%w: missing event type", ErrInvalidEventMessage)
	}

	if event.TenantID == uuid.Nil {
		return nil, fmt.Errorf("%w: missing tenant ID", ErrInvalidEventMessage)
	}

	var wfStartOpts workflow.StartWorkflowOptions

	switch event.Operation {
	case "create":
		wfStartOpts = *DefaultCreatePolicy
	case "update":
		wfStartOpts = *DefaultUpdatePolicy
	case "delete":
		wfStartOpts = *DefaultDeletePolicy
	default:
		return nil, fmt.Errorf("%w: operation %q for event %q", ErrUnknownOperation, event.Operation, event.Type)
	}

	ctx = request.Context(ctx, authn.SystemUser(), event.TenantID)

	workflows, err := wr.triggerWorkflowsBySignal(ctx, event, &wfStartOpts, msg.Subject)
	if err != nil {
		return nil, fmt.Errorf("failed to trigger workflows by signal for event %q: %w", event.Type, err)
	}

	if len(workflows) > 0 {
		log.ForContext(ctx).Info().
			Str("event_id", event.ID.String()).
			Int("workflow_count", len(workflows)).
			Msg("workflows started by signal")
	}

	return workflows, nil
}

// HandleMutationEventWithReply wraps HandleMutationEvent for the request/reply pattern,
// additionally validating that a reply topic is present.
func (wr *SignalRouter) HandleMutationEventWithReply(ctx context.Context, msg *nats.Msg) ([]*model.TemporalWorkflow, error) {
	if msg.Reply == "" {
		return nil, fmt.Errorf("%w: missing reply topic", ErrInvalidEventMessage)
	}

	return wr.HandleMutationEvent(ctx, msg)
}

func (wr *SignalRouter) triggerWorkflowsBySignal(ctx context.Context, event events.MutationEventMessage, opts *workflow.StartWorkflowOptions, topic string) ([]*model.TemporalWorkflow, error) {
	var (
		startedWorkflows []*model.TemporalWorkflow
		errs             []error
	)

	logger := log.ForContext(ctx).With().
		Str("tenant_id", event.TenantID.String()).
		Str("event_id", event.ID.String()).
		Str("event_topic", topic).
		Logger()

	wfs, err := wr.dbClient.Workflow.
		Query().
		Where(
			entworkflow.TenantIDEQ(event.TenantID),
			entworkflow.HasWorkflowSignals(),
		).
		WithWorkflowSignals().
		All(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	} else if len(wfs) == 0 || ent.IsNotFound(err) {
		logger.Debug().
			Msg("no active workflows with signals found for tenant")
		return nil, nil // no active workflows found -> skip
	}

	logger.Debug().
		Int("workflow_count", len(wfs)).
		Msg("found potential active workflows with signals for tenant")

	eventTopic, err := events.Parse(topic)
	if err != nil {
		return nil, fmt.Errorf("failed to parse event topic %q: %w", topic, err)
	}

	for _, wf := range wfs {
		startedWorkflow, err := wr.handleWorkflowSignalTriggers(ctx, event, eventTopic, opts, wf)
		if err != nil {
			if !errors.Is(err, ErrSkipFilter) {
				errs = append(errs, fmt.Errorf("failed to trigger workflow %q by signal: %w", wf.Name, err))
			}

			continue
		}

		if startedWorkflow != nil {
			startedWorkflows = append(startedWorkflows, startedWorkflow)
		}
	}

	return startedWorkflows, errors.Join(errs...)
}

func (wr *SignalRouter) handleWorkflowSignalTriggers(ctx context.Context, event events.MutationEventMessage, eventTopic events.Topic, opts *workflow.StartWorkflowOptions, wf *ent.Workflow) (*model.TemporalWorkflow, error) {
	logger := log.ForContext(ctx).With().
		Str("workflow_name", wf.Name).
		Str("event_id", event.ID.String()).
		Logger()

	client, err := wr.GetClient(ctx, event.TenantID.String())
	if err != nil {
		return nil, err
	}

	// Check each signal defined for the workflow.
	// If any signal matches the event topic, triggering the workflow
	// If multiple signals match, the workflow is triggered only once
	for _, signal := range wf.Edges.WorkflowSignals {
		signalTopic, err := events.Parse(signal.NatsTopic)
		if err != nil {
			return nil, fmt.Errorf("failed to parse NATS topic %q for workflow %q: %w", signal.NatsTopic, wf.Name, err)
		}

		if !signalTopic.Matches(eventTopic) {
			logger.Debug().
				Str("signal_topic", signal.NatsTopic).
				Str("event_topic", eventTopic.String()).
				Msg("signal topic does not match event topic, skipping")
			continue // topic does not match event subject -> skip
		}

		// Evaluate FEEL filter rule, if defined. If it evaluates to false, skip triggering the workflow.
		// The rule has access to all top-level fields in event.DataAfter as variables.
		if signal.FilterRule != "" {
			if ok, err := wr.EvalFilterRule(ctx, signal.FilterRule, event.DataAfter); err != nil {
				return nil, fmt.Errorf("failed to evaluate filter rule for workflow %q: %w", wf.Name, err)
			} else if !ok {
				logger.Debug().
					Msg("filter rule evaluated to false, skipping trigger")
				return nil, ErrSkipFilter // filter rule evaluated to false -> skip
			}
		}

		switch signal.TemporalSignalType {
		case workflowsignal.TemporalSignalTypeStart:
			wfStartOpts := *opts
			wfStartOpts.ID = wf.Name + "_" + event.ID.String()
			wfStartOpts.TaskQueue = wf.TaskQueue

			// Build typed search attributes from the event
			var searchAttrs []temporal.SearchAttributeUpdate

			// Always include the workflow name
			searchAttrs = append(searchAttrs, workflow.PyckWorkflowName.ValueSet(wf.Name))

			for k, v := range event.WfSearchAttributes {
				switch k {
				case "pyck_tenant_id":
					searchAttrs = append(searchAttrs, workflow.PyckTenantID.ValueSet(v))
				case "pyck_data_id":
					searchAttrs = append(searchAttrs, workflow.PyckDataID.ValueSet(v))
				case "pyck_data_type":
					searchAttrs = append(searchAttrs, workflow.PyckDataType.ValueSet(v))
				case "pyck_service":
					searchAttrs = append(searchAttrs, workflow.PyckService.ValueSet(v))
				case "pyck_workflow_assignee":
					searchAttrs = append(searchAttrs, workflow.PyckWorkflowAssignee.ValueSet(v))
				case "pyck_group_by":
					searchAttrs = append(searchAttrs, workflow.PyckGroupBy.ValueSet(v))
				default:
					log.ForContext(ctx).Warn().
						Str("attribute", k).
						Msg("ignored unknown search attribute")
				}
			}

			wfStartOpts.TypedSearchAttributes = temporal.NewSearchAttributes(searchAttrs...)

			return wr.startWorkflow(ctx, client, event.DataAfter, wf, &wfStartOpts)
		case workflowsignal.TemporalSignalTypeIntermediate:
			err := wr.signalWorkflow(ctx, client, event.DataAfter, wf, signal)
			return nil, err
		default:
			return nil, fmt.Errorf("%w: signal type %q for workflow %q", ErrUnknownSignalType, signal.TemporalSignalType, wf.Name)
		}
	}

	return nil, nil
}

func (wr *SignalRouter) handleTemporalWorkflowStateChange(ctx context.Context, msg *nats.Msg) ([]*model.TemporalWorkflow, error) {
	var event events.TemporalWorkflowStateChangeMessage

	// Parse & validate the incoming event message
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEventMessage, err)
	}

	if event.Namespace == "" {
		return nil, fmt.Errorf("%w: missing namespace", ErrInvalidEventMessage)
	}

	if event.TaskQueue == "" {
		return nil, fmt.Errorf("%w: missing task queue", ErrInvalidEventMessage)
	}

	if event.WorkflowID == "" {
		return nil, fmt.Errorf("%w: missing workflow ID", ErrInvalidEventMessage)
	}

	if event.RunID == "" {
		return nil, fmt.Errorf("%w: missing run ID", ErrInvalidEventMessage)
	}

	if event.Status == "" {
		return nil, fmt.Errorf("%w: missing status", ErrInvalidEventMessage)
	}

	logger := log.ForContext(ctx).With().
		Str("workflow_id", event.WorkflowID).
		Str("run_id", event.RunID).
		Str("status", event.Status).
		Logger()

	// Parse namespace as tenant ID. Not all Temporal namespaces map to tenant UUIDs.
	// Some namespaces are reserved for internal use (e.g., "default", "temporal-system")
	// or may be used for non-tenant-specific workflows. These are intentionally skipped
	// as they do not belong to any specific tenant in our multi-tenant architecture.
	// This is expected behavior and not an error condition.
	tenantID, err := uuid.Parse(event.Namespace)
	if err != nil {
		logger.Debug().
			Err(err).
			Str("namespace", event.Namespace).
			Msg("namespace is not a tenant UUID, skipping (expected for system namespaces)")
		return nil, nil
	}

	ctx = request.Context(ctx, authn.SystemUser(), tenantID)

	wfs, err := wr.dbClient.Workflow.
		Query().
		Where(
			entworkflow.TenantIDEQ(tenantID),
			entworkflow.HasWorkflowSignals(),
		).
		WithWorkflowSignals().
		All(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	} else if len(wfs) == 0 || ent.IsNotFound(err) {
		logger.Debug().
			Msg("no active workflows with signals found for tenant")
		return nil, nil // no active workflows found -> skip
	}

	eventTopic, err := events.Parse(msg.Subject)
	if err != nil {
		return nil, fmt.Errorf("failed to parse event topic %q: %w", msg.Subject, err)
	}

	logger.Debug().
		Int("workflow_count", len(wfs)).
		Msg("found potential active workflows with signals for tenant")

	var (
		startedWorkflows []*model.TemporalWorkflow
		errs             []error
	)

	for _, wf := range wfs {
		startedWorkflow, err := wr.handleTemporalWorkflowStateChangeTrigger(ctx, tenantID, wf, eventTopic)
		if err != nil {
			if !errors.Is(err, ErrSkipFilter) {
				errs = append(errs, fmt.Errorf("failed to trigger workflow %q by signal: %w", wf.Name, err))
			}

			continue
		}

		if startedWorkflow != nil {
			startedWorkflows = append(startedWorkflows, startedWorkflow)
		}
	}

	return startedWorkflows, errors.Join(errs...)
}

func (wr *SignalRouter) handleTemporalWorkflowStateChangeTrigger(ctx context.Context, tenantID uuid.UUID, wf *ent.Workflow, eventTopic events.Topic) (*model.TemporalWorkflow, error) {
	logger := log.ForContext(ctx).With().
		Str("workflow_name", wf.Name).
		Logger()

	client, err := wr.GetClient(ctx, tenantID.String())
	if err != nil {
		return nil, err
	}

	// Check each signal defined for the workflow.
	// If any signal matches the event topic, triggering the workflow
	// If multiple signals match, the workflow is triggered only once
	for _, signal := range wf.Edges.WorkflowSignals {
		signalTopic, err := events.Parse(signal.NatsTopic)
		if err != nil {
			return nil, fmt.Errorf("failed to parse NATS topic %q for workflow %q: %w", signal.NatsTopic, wf.Name, err)
		}

		if !signalTopic.Matches(eventTopic) {
			logger.Debug().
				Str("signal_topic", signal.NatsTopic).
				Str("event_topic", eventTopic.String()).
				Msg("signal topic does not match event topic, skipping")
			continue // topic does not match event subject -> skip
		}

		// Evaluate FEEL filter rule, if defined. If it evaluates to false, skip triggering the workflow.
		// The rule has access to all top-level fields in event.DataAfter as variables.
		if signal.FilterRule != "" {
			if ok, err := wr.EvalFilterRule(ctx, signal.FilterRule, wf); err != nil {
				return nil, fmt.Errorf("failed to evaluate filter rule for workflow %q: %w", wf.Name, err)
			} else if !ok {
				logger.Debug().
					Msg("filter rule evaluated to false, skipping trigger")
				return nil, ErrSkipFilter // filter rule evaluated to false -> skip
			}
		}

		switch signal.TemporalSignalType {
		case workflowsignal.TemporalSignalTypeStart:
			wfStartOpts := *DefaultCreatePolicy
			wfStartOpts.ID = wf.Name + "_" + uuid.NewString()

			return wr.startWorkflow(ctx, client, wf, wf, &wfStartOpts)
		case workflowsignal.TemporalSignalTypeIntermediate:
			err := wr.signalWorkflow(ctx, client, wf, wf, signal)
			return nil, err
		default:
			return nil, fmt.Errorf("%w: signal type %q for workflow %q", ErrUnknownSignalType, signal.TemporalSignalType, wf.Name)
		}
	}

	return nil, nil
}

// sendSuccessReply sends a successful response to a NATS reply topic.
func (wr *SignalRouter) sendSuccessReply(_ context.Context, replyTopic string, workflows []*model.TemporalWorkflow) error {
	payload, err := json.Marshal(map[string]any{
		"success": true,
		"data":    workflows,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal response payload: %w", err)
	}

	if err := wr.natsClient.Publish(replyTopic, payload); err != nil {
		return fmt.Errorf("failed to publish response: %w", err)
	}

	return nil
}

// sendErrorReply sends an error response to a NATS reply topic.
// If replyTopic is empty, this function logs and returns without sending.
func (wr *SignalRouter) sendErrorReply(ctx context.Context, replyTopic string, err error) {
	logger := log.ForContext(ctx)

	if replyTopic == "" {
		logger.Error().Err(err).Msg("no reply topic provided for error response")
		return
	}

	errorPayload, marshalErr := json.Marshal(map[string]any{
		"success": false,
		"error":   err.Error(),
	})
	if marshalErr != nil {
		logger.Error().Err(marshalErr).Msg("failed to marshal error response payload")
		return
	}

	if publishErr := wr.natsClient.Publish(replyTopic, errorPayload); publishErr != nil {
		logger.Error().Err(publishErr).Msg("failed to send error response")
	}
}

func (wr *SignalRouter) EvalFilterRule(_ context.Context, filterRule string, data any) (bool, error) {
	// TODO(michael): Filter rule evaluation happens on the critical path for every event.
	// The FEEL expression is parsed (feel.ParseString) on every invocation, which is
	// CPU-intensive and wasteful. Consider implementing:
	// 1. Cache parsed FEEL expressions keyed by filter rule string
	// 2. Use sync.Map or similar for thread-safe caching
	// 3. Add metrics for evaluation time to identify slow expressions
	// 4. Consider using a TTL-based cache to handle rule updates
	if filterRule == "" {
		return false, nil // no filter rule defined -> skip
	}

	node, err := feel.ParseString(filterRule)
	if err != nil {
		return false, fmt.Errorf("failed to parse FEEL expression %q: %w", filterRule, err)
	}

	scope, err := std.InterfaceToMap(data)
	if err != nil {
		return false, fmt.Errorf("failed to convert event data to map for FEEL evaluation: %w", err)
	}

	interpreter := feel.NewIntepreter()
	interpreter.Push(scope)

	result, err := node.Eval(interpreter)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate FEEL expression %q: %w", filterRule, err)
	}

	boolResult, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("failed to evaluate FEEL expression %q: %w", filterRule, err)
	}

	if !boolResult {
		return false, nil // filter rule did not match -> skip
	}

	return true, nil
}

func (wr *SignalRouter) startWorkflow(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, wfStartOpts *workflow.StartWorkflowOptions) (*model.TemporalWorkflow, error) {
	wfRun, err := client.StartWorkflowWithOptions(ctx, wf.Name, payload, wfStartOpts)
	if err != nil {
		return nil, err
	}

	return &model.TemporalWorkflow{
		Type:  wf.Name,
		ID:    wfRun.GetID(),
		RunID: wfRun.GetRunID(),
	}, nil
}

func (wr *SignalRouter) signalWorkflow(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, signal *ent.WorkflowSignal) error {
	wfExecutions, err := client.ListWorkflows(ctx, fmt.Sprintf("CloseTime is null AND TaskQueue = %q AND pyck_workflow_name = %q", wf.TaskQueue, wf.Name))
	if err != nil {
		return fmt.Errorf("failed to list workflows: %w", err)
	}

	var errs []error

	for _, wfExecInfo := range wfExecutions {
		wfExec := wfExecInfo.GetExecution()
		if wfExec == nil {
			continue // should not happen
		}

		if wfExecInfo.GetStatus() != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
			continue // workflow is not running -> skip
		}

		if wfExecInfo.GetTaskQueue() != wf.TaskQueue {
			continue // task queue does not match -> skip
		}

		err := client.SignalWorkflow(ctx, wfExec.GetWorkflowId(), wfExec.GetRunId(), signal.TemporalSignal, payload)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to signal workflow %q (ID: %q, RunID: %q): %w",
				wf.Name, wfExec.GetWorkflowId(), wfExec.GetRunId(), err))
			continue
		}

		log.ForContext(ctx).Info().
			Str("workflow_name", wf.Name).
			Str("workflow_id", wfExec.GetWorkflowId()).
			Str("workflow_run_id", wfExec.GetRunId()).
			Str("signal", signal.TemporalSignal).
			Msg("workflow signaled successfully")
	}

	return errors.Join(errs...)
}
