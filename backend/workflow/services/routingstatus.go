package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// RoutingOutcome is how the router settled an event.
type RoutingOutcome string

// RoutingTargetKind is what happened to one target of an event.
type RoutingTargetKind string

// RoutingTarget is one workflow start or signal the router attempted for an
// event, and what came of it.
type RoutingTarget struct {
	Kind       RoutingTargetKind `json:"kind"`
	Workflow   string            `json:"workflow,omitempty"`
	WorkflowID string            `json:"workflow_id,omitempty"`
	RunID      string            `json:"run_id,omitempty"`
	Signal     string            `json:"signal,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// RoutingStatus is the stored record of one routed event: the value of the
// routing status bucket, under the key RoutingStatusKey.
type RoutingStatus struct {
	TenantID      uuid.UUID       `json:"tenant_id"`
	TransactionID uuid.UUID       `json:"transaction_id"`
	EventID       uuid.UUID       `json:"event_id"`
	Outcome       RoutingOutcome  `json:"outcome"`
	Sequence      uint64          `json:"sequence"`
	Targets       []RoutingTarget `json:"targets"`
	RecordedAt    time.Time       `json:"recorded_at"`
}

const (
	// RoutingOutcomeDone means the router finished with the event: every target
	// was delivered, or refused for good (a malformed event or subscription).
	RoutingOutcomeDone RoutingOutcome = "done"

	// RoutingOutcomeGaveUp means the router stopped retrying the event after
	// the last delivery. Its failed targets say what was left undone.
	RoutingOutcomeGaveUp RoutingOutcome = "gave_up"

	// RoutingTargetStarted: a workflow was started, or the start returned the
	// run an earlier delivery of the same event had started.
	RoutingTargetStarted RoutingTargetKind = "started"

	// RoutingTargetAlreadyRunning: the start was refused because the workflow
	// ID is taken by another event's run.
	RoutingTargetAlreadyRunning RoutingTargetKind = "already_running"

	// RoutingTargetSignalled: a running execution was signalled.
	RoutingTargetSignalled RoutingTargetKind = "signalled"

	// RoutingTargetDropped: nothing was delivered, for the reason given.
	RoutingTargetDropped RoutingTargetKind = "dropped"

	// RoutingTargetFailed: the delivery failed with the error given.
	RoutingTargetFailed RoutingTargetKind = "failed"

	// routingStatusTTL is how long a routing status lives: the event stream's
	// MaxAge, so a status outlives the event it describes.
	routingStatusTTL = 72 * time.Hour

	// routingWriteTimeout bounds one status write. The write is detached from the
	// handler's context so that a shutdown does not lose the status of an event
	// that was just settled.
	routingWriteTimeout = 5 * time.Second

	// transactionIDAttribute is the search attribute that carries a mutation's
	// transaction ID on its events.
	transactionIDAttribute = "pyck_transaction_id"

	// routingReadTimeout bounds one transactionRouting lookup.
	routingReadTimeout = 10 * time.Second
)

// RoutingStatusBucket is the name of the key-value bucket holding the routing
// statuses of the events on stream.
func RoutingStatusBucket(stream string) string { return stream + "_routing_status" }

// RoutingStatusKey is the key of one event's routing status:
// <tenant>.<transaction ID>.<event ID>.
func RoutingStatusKey(tenantID, transactionID, eventID uuid.UUID) string {
	return tenantID.String() + "." + transactionID.String() + "." + eventID.String()
}

// routingCollector gathers what the handlers did for one event while it is
// handled, so the consumer can store it when the event is settled. The
// handlers find it on the context; a nil collector (the direct calls in tests,
// state changes, events without the IDs) makes every method a no-op.
type routingCollector struct {
	mu      sync.Mutex
	tenant  uuid.UUID
	tx      uuid.UUID
	event   uuid.UUID
	targets []RoutingTarget
}

type routingCollectorKey struct{}

func routingFrom(ctx context.Context) *routingCollector {
	c, _ := ctx.Value(routingCollectorKey{}).(*routingCollector)

	return c
}

// add records one target.
func (c *routingCollector) add(t RoutingTarget) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.targets = append(c.targets, t)
}

// recordErrors records each error as a target of workflow: a permanent error
// as dropped (the event or subscription is malformed, nothing was delivered),
// any other as failed.
func (c *routingCollector) recordErrors(workflowName string, errs []error) {
	for _, err := range errs {
		if isPermanent(err) {
			c.add(RoutingTarget{Kind: RoutingTargetDropped, Workflow: workflowName, Reason: err.Error()})
		} else {
			c.add(RoutingTarget{Kind: RoutingTargetFailed, Workflow: workflowName, Error: err.Error()})
		}
	}
}

// snapshot returns the targets collected so far, never nil.
func (c *routingCollector) snapshot() []RoutingTarget {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]RoutingTarget{}, c.targets...)
}

// routingStatusStore holds the collectors of the events being handled and the
// bucket handle, created on first use.
type routingStatusStore struct {
	mu      sync.Mutex
	pending map[string]*routingCollector
	kv      jetstream.KeyValue
}

// dropBucket forgets the cached bucket handle if it is still kv.
func (s *routingStatusStore) dropBucket(kv jetstream.KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.kv == kv {
		s.kv = nil
	}
}

// begin registers a fresh collector for the event under key, replacing the one
// of an earlier delivery: only the last delivery's targets are recorded. A
// collector lives until its event is settled (take) or NAKed (discard), however
// long the handler runs.
func (s *routingStatusStore) begin(key string, tenant, tx, event uuid.UUID) *routingCollector {
	c := &routingCollector{tenant: tenant, tx: tx, event: event}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending == nil {
		s.pending = make(map[string]*routingCollector)
	}

	s.pending[key] = c

	return c
}

// discard forgets the collector registered under key, for an event that was
// NAKed: its redelivery, here or on another replica, starts a fresh one.
func (s *routingStatusStore) discard(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.pending, key)
}

// take removes and returns the collector registered under key.
func (s *routingStatusStore) take(key string) *routingCollector {
	s.mu.Lock()
	defer s.mu.Unlock()

	c := s.pending[key]
	delete(s.pending, key)

	return c
}

// routingIdentity returns the three IDs an event is recorded under. ok is false
// when the event lacks the transaction ID or the event ID: an event from a
// publisher that predates them, or one the outbox published outside a
// transaction. Such an event is routed but not recorded.
func routingIdentity(event *events.MutationEventMessage) (tenant, tx, eventID uuid.UUID, ok bool) {
	if event.TenantID == uuid.Nil || event.EventID == uuid.Nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}

	tx, err := uuid.Parse(event.WfSearchAttributes[transactionIDAttribute])
	if err != nil || tx == uuid.Nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}

	return event.TenantID, tx, event.EventID, true
}

// trackRouting starts collecting what the handlers do for event, if it can be
// recorded. The returned context carries the collector; recordOutcome picks it
// up again when the consumer settles the event. Without a JetStream client
// (nothing will ever settle the event) or without the IDs it returns ctx.
func (wr *SignalRouter) trackRouting(ctx context.Context, event *events.MutationEventMessage) context.Context {
	if wr.jetstreamClient == nil {
		return ctx
	}

	tenant, tx, eventID, ok := routingIdentity(event)
	if !ok {
		log.ForContext(ctx).Debug().
			Str("entity_id", event.ID.String()).
			Str("event_type", event.Type).
			Msg("mutation event has no transaction ID or event ID, routing status is not recorded")

		return ctx
	}

	c := wr.routing.begin(RoutingStatusKey(tenant, tx, eventID), tenant, tx, eventID)

	return context.WithValue(ctx, routingCollectorKey{}, c)
}

// recordOutcome stores the routing status of a mutation event the consumer has
// just settled, before it is acknowledged. err is the handler's error: for an
// event the router gave up on it becomes a failed target if no target carries
// it already.
//
// It never fails the caller. A problem is logged and the event is acknowledged
// anyway: the status is a convenience for clients, not part of delivery.
func (wr *SignalRouter) recordOutcome(ctx context.Context, msg jetstream.Msg, outcome RoutingOutcome, err error) {
	if wr.jetstreamClient == nil {
		return
	}

	if topic, parseErr := events.Parse(msg.Subject()); parseErr != nil || topic.Type() != events.TopicTypeMutationEvent {
		return // state changes have no mutation transaction
	}

	var event events.MutationEventMessage
	if json.Unmarshal(msg.Data(), &event) != nil {
		return
	}

	tenant, tx, eventID, ok := routingIdentity(&event)
	if !ok {
		return
	}

	key := RoutingStatusKey(tenant, tx, eventID)

	c := wr.routing.take(key)
	if c == nil {
		return // the handler failed before it could collect anything
	}

	targets := c.snapshot()

	// An error no target carries (the handler failed before it got to any, for
	// example on an unknown operation or a database error) would otherwise read
	// as "finished, nothing to do". Record it as a target of its own.
	if err != nil && !hasTarget(targets, RoutingTargetFailed) && !hasTarget(targets, RoutingTargetDropped) {
		if outcome == RoutingOutcomeGaveUp {
			targets = append(targets, RoutingTarget{Kind: RoutingTargetFailed, Error: err.Error()})
		} else {
			targets = append(targets, RoutingTarget{Kind: RoutingTargetDropped, Reason: err.Error()})
		}
	}

	status := RoutingStatus{
		TenantID:      tenant,
		TransactionID: tx,
		EventID:       eventID,
		Outcome:       outcome,
		Targets:       targets,
		RecordedAt:    time.Now().UTC(),
	}

	if meta, metaErr := msg.Metadata(); metaErr == nil {
		status.Sequence = meta.Sequence.Stream
	}

	if writeErr := wr.writeRoutingStatus(ctx, key, &status); writeErr != nil {
		log.ForContext(ctx).Warn().
			Err(writeErr).
			Str("key", key).
			Msg("failed to record the routing status of an event, acknowledging it anyway")
	}
}

// discardRouting drops the collector of a mutation event the consumer NAKed
// instead of settling.
func (wr *SignalRouter) discardRouting(msg jetstream.Msg) {
	if wr.jetstreamClient == nil {
		return
	}

	if topic, parseErr := events.Parse(msg.Subject()); parseErr != nil || topic.Type() != events.TopicTypeMutationEvent {
		return
	}

	var event events.MutationEventMessage
	if json.Unmarshal(msg.Data(), &event) != nil {
		return
	}

	if tenant, tx, eventID, ok := routingIdentity(&event); ok {
		wr.routing.discard(RoutingStatusKey(tenant, tx, eventID))
	}
}

func hasTarget(targets []RoutingTarget, kind RoutingTargetKind) bool {
	for _, t := range targets {
		if t.Kind == kind {
			return true
		}
	}

	return false
}

// writeRoutingStatus stores status under key.
func (wr *SignalRouter) writeRoutingStatus(ctx context.Context, key string, status *RoutingStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal routing status: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routingWriteTimeout)
	defer cancel()

	kv, err := wr.routingBucket(ctx)
	if err != nil {
		return err
	}

	if _, err := kv.Put(ctx, key, data); err != nil {
		// The handle may be dead (the bucket was deleted or recreated). Drop it,
		// so that the next write looks the bucket up again instead of failing
		// until the process restarts.
		wr.routing.dropBucket(kv)

		return fmt.Errorf("put routing status: %w", err)
	}

	return nil
}

// routingBucket returns the routing status bucket, creating it on first use
// with the event stream's replica count and a TTL equal to its MaxAge. It is
// created here rather than at Start so that a problem with the bucket can
// never keep the router from starting: the status is best-effort.
func (wr *SignalRouter) routingBucket(ctx context.Context) (jetstream.KeyValue, error) { //nolint:ireturn // jetstream API type
	wr.routing.mu.Lock()
	defer wr.routing.mu.Unlock()

	if wr.routing.kv != nil {
		return wr.routing.kv, nil
	}

	kv, err := EnsureRoutingStatusBucket(ctx, wr.jetstreamClient, wr.streamName)
	if err != nil {
		return nil, err
	}

	wr.routing.kv = kv

	return kv, nil
}

// EnsureRoutingStatusBucket creates or updates the routing status bucket of
// stream: entries expire after routingStatusTTL, and the bucket has as many
// replicas as the event stream.
func EnsureRoutingStatusBucket(ctx context.Context, js jetstream.JetStream, streamName string) (jetstream.KeyValue, error) { //nolint:ireturn // jetstream API type
	stream, err := js.Stream(ctx, events.MutationEventTopic{StreamName: streamName}.GetStreamName())
	if err != nil {
		return nil, fmt.Errorf("look up event stream: %w", err)
	}

	info, err := stream.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("read event stream info: %w", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   RoutingStatusBucket(streamName),
		TTL:      routingStatusTTL,
		History:  1,
		Replicas: info.Config.Replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("create routing status bucket: %w", err)
	}

	return kv, nil
}

// settlement reports how the consumer settles an event after its handler
// returned err on delivery number delivered, and whether it settles it at all:
// a transient failure before the last delivery is NAKed for redelivery instead,
// and nothing is recorded for it. It mirrors the consumer's own switch.
func settlement(err error, delivered uint64, maxDeliver int) (RoutingOutcome, bool) {
	switch {
	case err == nil, isPermanent(err):
		return RoutingOutcomeDone, true
	case delivered >= uint64(maxDeliver): //nolint:gosec // validated positive
		return RoutingOutcomeGaveUp, true
	default:
		return "", false
	}
}

// RoutingStatuses returns the routing statuses of the events of a transaction
// that belong to tenantIDs, oldest first (by stream sequence). It is what the
// transactionRouting query serves.
//
// Tenant isolation: the lookup only ever builds key filters from the caller's
// own tenant IDs, <tenant>.<transaction>.*, both typed uuid.UUID so they cannot
// carry a wildcard, and it drops any entry whose stored tenant or transaction
// disagrees with the filter. No tenants means no entries.
//
// A missing bucket (no event has been settled yet) is an empty answer, not an
// error. Without a JetStream client the answer is empty too.
func (wr *SignalRouter) RoutingStatuses(ctx context.Context, tenantIDs []uuid.UUID, transactionID uuid.UUID) ([]RoutingStatus, error) {
	if wr.jetstreamClient == nil || len(tenantIDs) == 0 || transactionID == uuid.Nil {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, routingReadTimeout)
	defer cancel()

	kv, err := wr.jetstreamClient.KeyValue(ctx, RoutingStatusBucket(wr.streamName))
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("open routing status bucket: %w", err)
	}

	var out []RoutingStatus

	for _, tenantID := range tenantIDs {
		statuses, err := readRoutingStatuses(ctx, kv, tenantID, transactionID)
		if err != nil {
			return nil, err
		}

		out = append(out, statuses...)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })

	return out, nil
}

// readRoutingStatuses lists one tenant's entries of a transaction.
func readRoutingStatuses(ctx context.Context, kv jetstream.KeyValue, tenantID, transactionID uuid.UUID) ([]RoutingStatus, error) {
	lister, err := kv.ListKeysFiltered(ctx, tenantID.String()+"."+transactionID.String()+".*")
	if err != nil {
		return nil, fmt.Errorf("list routing status keys: %w", err)
	}

	defer func() {
		if stopErr := lister.Stop(); stopErr != nil {
			log.ForContext(ctx).Debug().Err(stopErr).Msg("failed to stop the routing status key lister")
		}
	}()

	var keys []string
	for key := range lister.Keys() {
		keys = append(keys, key)
	}

	var out []RoutingStatus

	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue // expired or deleted since it was listed
		}

		if err != nil {
			return nil, fmt.Errorf("read routing status %s: %w", key, err)
		}

		var status RoutingStatus
		if json.Unmarshal(entry.Value(), &status) != nil {
			continue
		}

		// The key is what the lookup filtered on; do not trust a value that
		// says otherwise.
		if status.TenantID != tenantID || status.TransactionID != transactionID {
			continue
		}

		out = append(out, status)
	}

	return out, nil
}
