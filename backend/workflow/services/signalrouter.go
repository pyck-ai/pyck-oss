package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pbinitiative/feel"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/eventid"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	commontemporal "github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/std"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflow "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflow"
	"github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/model"
)

var (
	ErrInvalidEventMessage = errors.New("invalid workflow event message")
	ErrUnknownOperation    = errors.New("unknown operation for event")
	ErrUnknownSignalType   = errors.New("unknown signal type for workflow")

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
	// One run per entity per workflow: the workflow ID is <workflow>_<entity ID>,
	// so an update event that finds the entity's workflow still running starts
	// nothing. The router records it as already_running and acknowledges it. The
	// event's data is not delivered to the running execution. To receive every
	// update, subscribe with Signal-With-Start (or signal by ID) instead of Start.
	//
	// Behavior:
	//   - ALLOW_DUPLICATE: a new run may start once the previous run has closed.
	//     It says nothing about a run that is still running.
	//   - Conflict policy FAIL (explicit): the server refuses a start while the
	//     workflow ID is running.
	//   - WorkflowExecutionErrorWhenAlreadyStarted: true makes the SDK return that
	//     refusal. Without it the SDK would hand back the running run as if the
	//     start had succeeded, and the router would record a start that never
	//     happened. A redelivery of the event that started the run is not
	//     refused: its request ID makes the server return the run it created.
	//
	// For more details, see Temporal documentation:
	// https://docs.temporal.io/encyclopedia/detecting-activity-failures#workflow-id-reuse-policy
	DefaultUpdatePolicy = &workflow.StartWorkflowOptions{
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowIDConflictPolicy:                 enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}

	// DefaultDeletePolicy is the default workflow start policy for "delete" operations.
	//
	// One delete run per entity, and never a second one after it finished:
	//   - REJECT_DUPLICATE: a closed run under the workflow ID refuses a new one.
	//   - Conflict policy FAIL with WorkflowExecutionErrorWhenAlreadyStarted: a
	//     delete event that finds the entity's delete workflow running starts
	//     nothing; the router records it as already_running and acknowledges it.
	//     (USE_EXISTING would deduplicate just as well, but the server then
	//     answers with the existing run and no error, so the start could not be
	//     told from a real one.) A redelivery of the event that started the run
	//     returns that run.
	//
	// The workflow ID is deterministic (<workflow>_<entity ID>), which is what
	// makes the deduplication work.
	//
	// For more details, see Temporal documentation:
	// https://docs.temporal.io/encyclopedia/detecting-activity-failures#workflow-id-reuse-policy
	DefaultDeletePolicy = &workflow.StartWorkflowOptions{
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy:                 enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}
)

// SignalRouterConfig contains configuration for creating a SignalRouter.
type SignalRouterConfig struct {
	EventPublisher  *events.EventPublisher
	JetstreamClient jetstream.JetStream
	StreamName      string
	ServiceName     string
	// Consumer tunes the durable consumer. Zero fields take their defaults.
	Consumer ConsumerConfig
	// ClientFactory hands out the namespace-scoped Temporal clients the
	// router starts and signals workflows through. Required — the router
	// cannot build one itself because the factory must be constructed from
	// the application lifecycle context and the root client dialed at
	// startup. The router takes ownership and closes it in Stop.
	ClientFactory workflow.ClientFactory
}

// NewSignalRouter creates a new SignalRouter instance with the provided configuration.
func NewSignalRouter(entClient *ent.Client, cfg SignalRouterConfig) *SignalRouter {
	wr := &SignalRouter{
		clientFactory:   cfg.ClientFactory,
		eventPublisher:  cfg.EventPublisher,
		jetstreamClient: cfg.JetstreamClient,
		streamName:      cfg.StreamName,
		serviceName:     cfg.ServiceName,
		dbClient:        entClient,
		consumerCfg:     cfg.Consumer.withDefaults(),
	}
	wr.dispatch = wr.dispatchEvent

	return wr
}

// SignalRouter routes NATS events to Temporal workflows.
//
// The SignalRouter is the central component responsible for:
//  1. Consuming mutation events and workflow state changes from the durable
//     JetStream consumer (see ConsumerConfig)
//  2. Matching incoming events to workflow configurations stored in the database
//  3. Starting new workflows or signaling existing ones based on matching rules
//  4. Managing Temporal client connections per tenant namespace
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
// Delivery is at-least-once. Permanent failures (malformed events, bad
// subscriptions) are acknowledged, transient ones (Temporal or database
// errors) are redelivered with backoff until MaxDeliver, then given up on
// and counted. See ConsumerConfig and signalrouter_consumer.go. Two limits:
//   - An intermediate (broadcast) signal finds its target executions through
//     Temporal's visibility listing, which lags behind new starts. An
//     execution started a moment earlier can be missed; the event is then
//     recorded as dropped and acknowledged, not retried.
//   - After MaxDeliver failed deliveries the event is acknowledged and only
//     counted (workflow_signal_router_events_given_up_total) and logged. There
//     is no dead-letter.
//
// Deduplication:
// The router keeps no record of what it has delivered. Instead every Temporal
// start and signal it makes for an event carries a deterministic request ID
// (see requestID), and Temporal drops a repeated call with a request ID it has
// already seen. A redelivered event, two replicas handling the same event, or a
// crash between a Temporal call and the acknowledgement therefore only repeats
// calls that Temporal ignores. The event ID also travels to the workflow in the
// pyck-event-id header, where workflowsdk exposes it so the workflow can
// deduplicate too.
//
// The limits of this: a start's request ID is only remembered while its run is
// the workflow ID's current run, so an update event redelivered after a newer
// run of the same entity has replaced its own run starts again; and a signal to
// an execution that has closed is not retried (it is skipped, not an error).
type SignalRouter struct {
	clientFactory   workflow.ClientFactory
	eventPublisher  *events.EventPublisher
	jetstreamClient jetstream.JetStream
	streamName      string
	serviceName     string
	consumerCfg     ConsumerConfig
	dispatch        func(ctx context.Context, msg *nats.Msg) error
	run             *consumerRun
	routing         routingStatusStore
	wg              sync.WaitGroup
	dbClient        *ent.Client
}

// GetClient retrieves or creates a Temporal client for the given namespace.
// Clients are cached by the factory to avoid unnecessary connections.
// This method is thread-safe.
func (wr *SignalRouter) GetClient(ctx context.Context, namespace string) (*workflow.Client, error) {
	return wr.clientFactory.GetClient(ctx, namespace)
}

// activeWorkflowsWithSignals loads the tenant's workflows that still have at
// least one live signal subscription, eager-loading only those subscriptions.
// A subscription is live when it is not deleted and its TTL has not lapsed, so
// subscriptions left behind by crashed workers stop routing even before the
// janitor reaps them. Each workflow's loaded subscriptions are then narrowed by
// PreferRunning, so subscriptions of cleanly stopped workers only route while
// no running worker holds any on that workflow.
func (wr *SignalRouter) activeWorkflowsWithSignals(ctx context.Context, tenantID uuid.UUID) ([]*ent.Workflow, error) {
	live := LiveSignal(time.Now().UTC())

	wfs, err := wr.dbClient.Workflow.
		Query().
		Where(
			entworkflow.TenantIDEQ(tenantID),
			entworkflow.HasWorkflowSignalsWith(live),
		).
		WithWorkflowSignals(func(q *ent.WorkflowSignalQuery) { q.Where(live) }).
		AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, err
	}

	for _, wf := range wfs {
		wf.Edges.WorkflowSignals = PreferRunning(wf.Edges.WorkflowSignals)
	}

	return wfs, nil
}

// HandleMutationEvent parses a mutation event message and triggers matching workflows.
func (wr *SignalRouter) HandleMutationEvent(ctx context.Context, msg *nats.Msg) ([]*model.TemporalWorkflow, error) {
	var event events.MutationEventMessage

	if err := json.Unmarshal(msg.Data, &event); err != nil {
		return nil, permanent(fmt.Errorf("%w: %w", ErrInvalidEventMessage, err))
	}

	if event.Type == "" {
		return nil, permanent(fmt.Errorf("%w: missing event type", ErrInvalidEventMessage))
	}

	if event.TenantID == uuid.Nil {
		return nil, permanent(fmt.Errorf("%w: missing tenant ID", ErrInvalidEventMessage))
	}

	// Collect what is done for the event so the consumer can record it when it
	// settles the event (see recordOutcome).
	ctx = wr.trackRouting(ctx, &event)

	var wfStartOpts workflow.StartWorkflowOptions

	switch event.Operation {
	case "create":
		wfStartOpts = *DefaultCreatePolicy
	case "update":
		wfStartOpts = *DefaultUpdatePolicy
	case "delete":
		wfStartOpts = *DefaultDeletePolicy
	default:
		err := permanent(fmt.Errorf("%w: operation %q for event %q", ErrUnknownOperation, event.Operation, event.Type))
		routingFrom(ctx).recordErrors("", []error{err})

		return nil, err
	}

	ctx = request.Context(ctx, authn.SystemUser(), event.TenantID)

	// The event's own ID (the outbox entry ID), not the entity ID in event.ID:
	// two updates of one entity share the latter. A publisher that predates
	// event_id leaves it nil; then the calls go out without a deterministic
	// request ID or header rather than with an invented one.
	if event.EventID != uuid.Nil {
		ctx = eventid.WithEventID(ctx, event.EventID)
	} else {
		log.ForContext(ctx).Debug().
			Str("entity_id", event.ID.String()).
			Str("event_type", event.Type).
			Msg("mutation event has no event_id, Temporal calls are not deduplicated")
	}

	workflows, err := wr.triggerWorkflowsBySignal(ctx, event, &wfStartOpts, msg.Subject)
	if err != nil {
		return nil, fmt.Errorf("failed to trigger workflows by signal for event %q: %w", event.Type, err)
	}

	if len(workflows) > 0 {
		log.ForContext(ctx).Info().
			Str("entity_id", event.ID.String()).
			Stringer("event_id", event.EventID).
			Int("workflow_count", len(workflows)).
			Msg("workflows started by signal")
	}

	return workflows, nil
}

func (wr *SignalRouter) triggerWorkflowsBySignal(ctx context.Context, event events.MutationEventMessage, opts *workflow.StartWorkflowOptions, topic string) ([]*model.TemporalWorkflow, error) {
	var (
		startedWorkflows []*model.TemporalWorkflow
		errs             []error
	)

	logger := log.ForContext(ctx).With().
		Str("tenant_id", event.TenantID.String()).
		Str("entity_id", event.ID.String()).
		Stringer("event_id", event.EventID).
		Str("event_topic", topic).
		Logger()

	wfs, err := wr.activeWorkflowsWithSignals(ctx, event.TenantID)
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
		return nil, permanent(fmt.Errorf("failed to parse event topic %q: %w", topic, err))
	}

	for _, wf := range wfs {
		startedWorkflow, err := wr.handleWorkflowSignalTriggers(ctx, event, eventTopic, opts, wf)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to trigger workflow %q by signal: %w", wf.Name, err))
		}

		if startedWorkflow != nil {
			startedWorkflows = append(startedWorkflows, startedWorkflow)
		}
	}

	return startedWorkflows, errors.Join(errs...)
}

func (wr *SignalRouter) handleWorkflowSignalTriggers(ctx context.Context, event events.MutationEventMessage, eventTopic events.Topic, opts *workflow.StartWorkflowOptions, wf *ent.Workflow) (*model.TemporalWorkflow, error) {
	client, err := wr.GetClient(ctx, event.TenantID.String())
	if err != nil {
		return nil, err
	}

	match, errs := matchSubscriptions(ctx, wr, wf, eventTopic, event.DataAfter)
	routingFrom(ctx).recordErrors(wf.Name, errs) // subscriptions that could not be evaluated

	var started *model.TemporalWorkflow

	wfStartOpts := *opts
	wfStartOpts.ID = wf.Name + "_" + event.ID.String()
	wfStartOpts.TaskQueue = wf.TaskQueue
	wfStartOpts.TypedSearchAttributes = eventSearchAttributes(ctx, wf, event.WfSearchAttributes)

	if match.start && len(match.signalWithStart) == 0 {
		started, err = wr.startWorkflow(ctx, client, event.DataAfter, wf, &wfStartOpts)
		if err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, wr.deliverByID(ctx, client, event.DataAfter, wf, match, &wfStartOpts)...)

	for _, signal := range match.signals {
		if err := wr.signalWorkflowExcept(ctx, client, event.DataAfter, wf, signal, exceptIDs(match, wfStartOpts.ID, signal.TemporalSignal)); err != nil {
			errs = append(errs, err)
		}
	}

	return started, errors.Join(errs...)
}

// eventSearchAttributes builds the typed search attributes of a started
// workflow from the event's attributes. The workflow name is always included.
func eventSearchAttributes(ctx context.Context, wf *ent.Workflow, attrs map[string]string) temporal.SearchAttributes {
	searchAttrs := []temporal.SearchAttributeUpdate{workflow.PyckWorkflowName.ValueSet(wf.Name)}

	for k, v := range attrs {
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
		case "pyck_title":
			searchAttrs = append(searchAttrs, workflow.PyckTitle.ValueSet(v))
		case "pyck_group_title":
			searchAttrs = append(searchAttrs, workflow.PyckGroupTitle.ValueSet(v))
		case "pyck_transaction_id":
			searchAttrs = append(searchAttrs, workflow.PyckTransactionID.ValueSet(v))
		case "pyck_sort_key":
			sortKey, parseErr := strconv.ParseInt(v, 10, 64)
			if parseErr != nil {
				log.ForContext(ctx).Warn().
					Err(parseErr).
					Str("value", v).
					Msg("ignored non-integer pyck_sort_key search attribute")
				continue
			}
			searchAttrs = append(searchAttrs, workflow.PyckSortKey.ValueSet(sortKey))
		default:
			log.ForContext(ctx).Warn().
				Str("attribute", k).
				Msg("ignored unknown search attribute")
		}
	}

	return temporal.NewSearchAttributes(searchAttrs...)
}

// routedSubscriptions is what one event delivers to one workflow: whether to
// start it, and the signals to send, one per distinct signal name and kind.
//
// signals are broadcast to every running execution. signalWithStart and
// signalByID go to the workflow ID of the event (<workflow>_<id>): the first
// may start it, the second never does. Signal-With-Start carries the start, so
// a matching plain Start subscription adds nothing to it, and a by-ID signal of
// a name a Signal-With-Start already sends is dropped.
type routedSubscriptions struct {
	start           bool
	signals         []*ent.WorkflowSignal
	signalWithStart []*ent.WorkflowSignal
	signalByID      []*ent.WorkflowSignal
}

// matchSubscriptions collects every live subscription of wf whose topic
// matches eventTopic and whose filter accepts filterData. Filters are OR'd
// across subscriptions: several workers may register the same workflow with
// different filters, and the workflow is delivered to if any of them matches.
// Matching subscriptions collapse into at most one start and one signal per
// distinct signal name. A subscription that cannot be evaluated is recorded
// in the returned errors and does not stop the others from matching.
func matchSubscriptions(ctx context.Context, wr *SignalRouter, wf *ent.Workflow, eventTopic events.Topic, filterData any) (routedSubscriptions, []error) {
	var (
		match routedSubscriptions
		errs  []error
		seen  = make(map[string]bool)

		seenSWS  = make(map[string]bool)
		seenByID = make(map[string]bool)
	)

	logger := log.ForContext(ctx).With().Str("workflow_name", wf.Name).Logger()

	for _, signal := range wf.Edges.WorkflowSignals {
		signalTopic, err := events.Parse(signal.NatsTopic)
		if err != nil {
			errs = append(errs, permanent(fmt.Errorf("failed to parse NATS topic %q for workflow %q: %w", signal.NatsTopic, wf.Name, err)))
			continue
		}

		if !signalTopic.Matches(eventTopic) {
			logger.Debug().
				Str("signal_topic", signal.NatsTopic).
				Str("event_topic", eventTopic.String()).
				Msg("signal topic does not match event topic, skipping")
			continue
		}

		// An empty filter matches every event. A FEEL filter sees the top-level
		// fields of the data as variables.
		if signal.FilterRule != "" {
			ok, err := wr.EvalFilterRule(ctx, signal.FilterRule, filterData)
			if err != nil {
				errs = append(errs, permanent(fmt.Errorf("failed to evaluate filter rule for workflow %q: %w", wf.Name, err)))
				continue
			}

			if !ok {
				logger.Debug().Msg("filter rule evaluated to false, skipping subscription")
				continue
			}
		}

		switch signal.TemporalSignalType {
		case workflowsignal.TemporalSignalTypeStart:
			match.start = true
		case workflowsignal.TemporalSignalTypeIntermediate:
			if !seen[signal.TemporalSignal] {
				seen[signal.TemporalSignal] = true
				match.signals = append(match.signals, signal)
			}
		case workflowsignal.TemporalSignalTypeSignalWithStart:
			if !seenSWS[signal.TemporalSignal] {
				seenSWS[signal.TemporalSignal] = true
				match.signalWithStart = append(match.signalWithStart, signal)
			}
		case workflowsignal.TemporalSignalTypeSignalByID:
			if !seenByID[signal.TemporalSignal] {
				seenByID[signal.TemporalSignal] = true
				match.signalByID = append(match.signalByID, signal)
			}
		default:
			errs = append(errs, permanent(fmt.Errorf("%w: signal type %q for workflow %q", ErrUnknownSignalType, signal.TemporalSignalType, wf.Name)))
		}
	}

	// A Signal-With-Start of a name already covers the same name by ID.
	byID := match.signalByID[:0]

	for _, sig := range match.signalByID {
		if !seenSWS[sig.TemporalSignal] {
			byID = append(byID, sig)
		}
	}

	match.signalByID = byID

	return match, errs
}

func (wr *SignalRouter) handleTemporalWorkflowStateChange(ctx context.Context, msg *nats.Msg) ([]*model.TemporalWorkflow, error) {
	var event events.TemporalWorkflowStateChangeMessage

	// Parse & validate the incoming event message
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		return nil, permanent(fmt.Errorf("%w: %w", ErrInvalidEventMessage, err))
	}

	if event.Namespace == "" {
		return nil, permanent(fmt.Errorf("%w: missing namespace", ErrInvalidEventMessage))
	}

	if event.TaskQueue == "" {
		return nil, permanent(fmt.Errorf("%w: missing task queue", ErrInvalidEventMessage))
	}

	if event.WorkflowID == "" {
		return nil, permanent(fmt.Errorf("%w: missing workflow ID", ErrInvalidEventMessage))
	}

	if event.RunID == "" {
		return nil, permanent(fmt.Errorf("%w: missing run ID", ErrInvalidEventMessage))
	}

	if event.Status == "" {
		return nil, permanent(fmt.Errorf("%w: missing status", ErrInvalidEventMessage))
	}

	logger := log.ForContext(ctx).With().
		Str("workflow_id", event.WorkflowID).
		Str("run_id", event.RunID).
		Str("status", event.Status).
		Logger()

	// The namespace is the tenant ID. State changes of system namespaces such as
	// "default" or "temporal-system" are skipped by dispatchEvent before they get
	// here, because their subject does not parse. This check is a backstop for
	// a caller that reaches the handler directly, or a message whose body
	// namespace differs from its subject (for example a "*" subject token). It
	// is expected behavior and not an error condition.
	tenantID, err := uuid.Parse(event.Namespace)
	if err != nil {
		logger.Debug().
			Err(err).
			Str("namespace", event.Namespace).
			Msg("namespace is not a tenant UUID, skipping (expected for system namespaces)")
		return nil, nil
	}

	ctx = request.Context(ctx, authn.SystemUser(), tenantID)
	ctx = eventid.WithEventID(ctx, event.EventID())

	wfs, err := wr.activeWorkflowsWithSignals(ctx, tenantID)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	} else if len(wfs) == 0 || ent.IsNotFound(err) {
		logger.Debug().
			Msg("no active workflows with signals found for tenant")
		return nil, nil // no active workflows found -> skip
	}

	eventTopic, err := events.Parse(msg.Subject)
	if err != nil {
		return nil, permanent(fmt.Errorf("failed to parse event topic %q: %w", msg.Subject, err))
	}

	logger.Debug().
		Int("workflow_count", len(wfs)).
		Msg("found potential active workflows with signals for tenant")

	var (
		startedWorkflows []*model.TemporalWorkflow
		errs             []error
	)

	for _, wf := range wfs {
		startedWorkflow, err := wr.handleTemporalWorkflowStateChangeTrigger(ctx, tenantID, wf, eventTopic, &event)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to trigger workflow %q by signal: %w", wf.Name, err))
		}

		if startedWorkflow != nil {
			startedWorkflows = append(startedWorkflows, startedWorkflow)
		}
	}

	return startedWorkflows, errors.Join(errs...)
}

// stateChangeStartID derives the ID suffix of a workflow started by a state
// change. It is the state change's event ID, so a redelivered event starts
// nothing new.
func stateChangeStartID(event *events.TemporalWorkflowStateChangeMessage) string {
	return event.EventID().String()
}

// requestIDNamespace is the UUIDv5 namespace of Temporal request IDs. It is
// fixed forever: changing it would give a redelivered event new request IDs,
// which Temporal would not recognise.
var requestIDNamespace = uuid.MustParse("6c48311b-e8be-486e-89cc-1aba176e9ec5") //nolint:gochecknoglobals

// The kinds of Temporal call the router makes for an event. The kind is part
// of the request ID so a start and a signal of one event never share one.
const (
	callKindStart           = "start"
	callKindSignal          = "signal"
	callKindSignalWithStart = "signal-with-start"
	callKindSignalByID      = "signal-by-id"
)

// requestID derives the Temporal request ID of one call: a UUIDv5 of the event
// ID, the workflow's name, the call kind, the signal name (empty for a start)
// and the target, NUL-separated so no two calls share an input. The target is
// the execution the call goes to: the workflow ID for a start, and the
// workflow ID plus run ID of each running execution for a signal, because one
// signal fans out to every running execution and Temporal dedups per execution.
//
// It is the same for every attempt and every replica, which is the point: a
// repeated call carries a request ID Temporal has already seen.
func requestID(eventID uuid.UUID, workflowName, kind, signalName, target string) uuid.UUID {
	return uuid.NewSHA1(requestIDNamespace, []byte(strings.Join(
		[]string{eventID.String(), workflowName, kind, signalName, target}, "\x00",
	)))
}

// withRequestID sets the deterministic request ID for one call on ctx. The
// event ID comes from ctx (set by the handler). Without one, ctx is returned
// unchanged and the call keeps the SDK's random request ID.
func withRequestID(ctx context.Context, workflowName, kind, signalName, target string) context.Context {
	eventID, ok := eventid.FromContext(ctx)
	if !ok {
		return ctx
	}

	return commontemporal.WithRequestID(ctx, requestID(eventID, workflowName, kind, signalName, target).String())
}

func (wr *SignalRouter) handleTemporalWorkflowStateChangeTrigger(ctx context.Context, tenantID uuid.UUID, wf *ent.Workflow, eventTopic events.Topic, event *events.TemporalWorkflowStateChangeMessage) (*model.TemporalWorkflow, error) {
	client, err := wr.GetClient(ctx, tenantID.String())
	if err != nil {
		return nil, err
	}

	// The filter scope and the delivered payload are the state-change message
	// itself, so a FEEL filter sees its JSON fields (namespace, task_queue,
	// workflow_id, workflow_type_name, run_id, status) as variables.
	match, errs := matchSubscriptions(ctx, wr, wf, eventTopic, event)

	var started *model.TemporalWorkflow

	wfStartOpts := *DefaultCreatePolicy
	wfStartOpts.ID = wf.Name + "_" + stateChangeStartID(event)

	if match.start && len(match.signalWithStart) == 0 {
		started, err = wr.startWorkflow(ctx, client, event, wf, &wfStartOpts)
		if err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, wr.deliverByID(ctx, client, event, wf, match, &wfStartOpts)...)

	for _, signal := range match.signals {
		if err := wr.signalWorkflowExcept(ctx, client, event, wf, signal, exceptIDs(match, wfStartOpts.ID, signal.TemporalSignal)); err != nil {
			errs = append(errs, err)
		}
	}

	return started, errors.Join(errs...)
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

	node, err := parseFilterRule(filterRule)
	if err != nil {
		return false, err
	}

	scope, err := std.InterfaceToMap(data)
	if err != nil {
		return false, fmt.Errorf("failed to convert event data to map for FEEL evaluation: %w", err)
	}

	interpreter := feel.NewIntepreter()
	interpreter.Push(scope)

	result, err := evalFilterRule(node, interpreter)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate FEEL expression %q: %w", filterRule, err)
	}

	boolResult, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("failed to evaluate FEEL expression %q: %w: got %T", filterRule, ErrFilterRuleNotBoolean, result)
	}

	if !boolResult {
		return false, nil // filter rule did not match -> skip
	}

	return true, nil
}

// startWorkflow starts wf with the deterministic request ID of (event, wf,
// start, workflow ID). Repeating it for the same event returns the run the first
// call created instead of starting another, whatever the start policy.
//
// A start with a different request ID for a workflow ID that is running is
// refused with WorkflowExecutionAlreadyStarted under all three policies, and
// recorded as already_running. A refusal for a closed run (reuse policy) is the
// same error and the same outcome.
func (wr *SignalRouter) startWorkflow(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, wfStartOpts *workflow.StartWorkflowOptions) (*model.TemporalWorkflow, error) {
	ctx = withRequestID(ctx, wf.Name, callKindStart, "", wfStartOpts.ID)

	wfRun, err := client.StartWorkflowWithOptions(ctx, wf.Name, payload, wfStartOpts)
	if err != nil {
		// A start refused because the workflow ID exists is a finished
		// outcome, not a failure: the create policy refuses a duplicate on
		// purpose, and a redelivery hits it after a crash.
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			log.ForContext(ctx).Info().
				Str("workflow_name", wf.Name).
				Str("workflow_id", wfStartOpts.ID).
				Msg("workflow already started, nothing to do")

			routingFrom(ctx).add(RoutingTarget{Kind: RoutingTargetAlreadyRunning, Workflow: wf.Name, WorkflowID: wfStartOpts.ID})

			return nil, nil //nolint:nilnil // no new execution, not an error
		}

		routingFrom(ctx).recordErrors(wf.Name, []error{err})

		return nil, err
	}

	routingFrom(ctx).add(RoutingTarget{Kind: RoutingTargetStarted, Workflow: wf.Name, WorkflowID: wfRun.GetID(), RunID: wfRun.GetRunID()})

	return &model.TemporalWorkflow{
		Type:  wf.Name,
		ID:    wfRun.GetID(),
		RunID: wfRun.GetRunID(),
	}, nil
}

// signalWorkflow sends signal to every running execution of wf, each with its
// own deterministic request ID (see requestID). An execution that has closed by
// the time of a redelivery is no longer listed, so it is skipped rather than
// failed: the signal it already received is not sent again.
func (wr *SignalRouter) signalWorkflow(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, signal *ent.WorkflowSignal) error {
	return wr.signalWorkflowExcept(ctx, client, payload, wf, signal, nil)
}

// exceptIDs is the set of workflow IDs a broadcast of signalName must skip
// because the event already delivers that signal to them by ID.
func exceptIDs(match routedSubscriptions, workflowID, signalName string) map[string]bool {
	for _, list := range [][]*ent.WorkflowSignal{match.signalWithStart, match.signalByID} {
		for _, sig := range list {
			if sig.TemporalSignal == signalName {
				return map[string]bool{workflowID: true}
			}
		}
	}

	return nil
}

// signalWorkflowExcept is signalWorkflow skipping the executions whose workflow
// ID is in except.
func (wr *SignalRouter) signalWorkflowExcept(ctx context.Context, client *workflow.Client, payload any, wf *ent.Workflow, signal *ent.WorkflowSignal, except map[string]bool) error {
	wfExecutions, err := client.ListWorkflows(ctx, fmt.Sprintf("CloseTime is null AND TaskQueue = %q AND pyck_workflow_name = %q", wf.TaskQueue, wf.Name))
	if err != nil {
		routingFrom(ctx).add(RoutingTarget{Kind: RoutingTargetFailed, Workflow: wf.Name, Signal: signal.TemporalSignal, Error: err.Error()})

		return fmt.Errorf("failed to list workflows: %w", err)
	}

	var (
		errs    []error
		targets int
	)

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

		if except[wfExec.GetWorkflowId()] {
			continue // the event signals this execution by ID
		}

		// One request ID per target execution: the same event signals each
		// running execution once, and a redelivery repeats the same IDs.
		target := wfExec.GetWorkflowId() + "\x00" + wfExec.GetRunId()
		signalCtx := withRequestID(ctx, wf.Name, callKindSignal, signal.TemporalSignal, target)
		targets++

		err := client.SignalWorkflow(signalCtx, wfExec.GetWorkflowId(), wfExec.GetRunId(), signal.TemporalSignal, payload)

		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			// The execution closed between the listing and the signal.
			log.ForContext(ctx).Info().
				Str("workflow_name", wf.Name).
				Str("workflow_id", wfExec.GetWorkflowId()).
				Str("signal", signal.TemporalSignal).
				Msg("workflow closed before it could be signaled, skipping")

			routingFrom(ctx).add(RoutingTarget{
				Kind: RoutingTargetDropped, Workflow: wf.Name, WorkflowID: wfExec.GetWorkflowId(), RunID: wfExec.GetRunId(),
				Signal: signal.TemporalSignal, Reason: "the execution closed before it could be signalled",
			})

			continue
		}

		if err != nil {
			routingFrom(ctx).add(RoutingTarget{
				Kind: RoutingTargetFailed, Workflow: wf.Name, WorkflowID: wfExec.GetWorkflowId(), RunID: wfExec.GetRunId(),
				Signal: signal.TemporalSignal, Error: err.Error(),
			})

			errs = append(errs, fmt.Errorf("failed to signal workflow %q (ID: %q, RunID: %q): %w",
				wf.Name, wfExec.GetWorkflowId(), wfExec.GetRunId(), err))

			continue
		}

		routingFrom(ctx).add(RoutingTarget{
			Kind: RoutingTargetSignalled, Workflow: wf.Name, WorkflowID: wfExec.GetWorkflowId(), RunID: wfExec.GetRunId(),
			Signal: signal.TemporalSignal,
		})

		log.ForContext(ctx).Info().
			Str("workflow_name", wf.Name).
			Str("workflow_id", wfExec.GetWorkflowId()).
			Str("workflow_run_id", wfExec.GetRunId()).
			Str("signal", signal.TemporalSignal).
			Msg("workflow signaled successfully")
	}

	if targets == 0 {
		routingFrom(ctx).add(RoutingTarget{
			Kind: RoutingTargetDropped, Workflow: wf.Name, Signal: signal.TemporalSignal,
			Reason: "no running execution to signal",
		})
	}

	return errors.Join(errs...)
}
