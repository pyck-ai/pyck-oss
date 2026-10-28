package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	tenantEventsConsumerInactive = 10 * time.Minute
)

// TenantEvent is a tenant lifecycle transition observed on the management
// tenant CRUD topic.
type TenantEvent struct {
	TenantID uuid.UUID
	// Disabling is true only for the disable transition (deleted_at
	// nil → set); false for restores and every other row event. It flags
	// the transition, not the tenant's state — an update to an
	// already-disabled tenant carries Disabling=false.
	Disabling bool
}

// TenantEventFunc is invoked for every tenant lifecycle transition. Use
// [TenantLifecycleHandler] to build the standard one.
type TenantEventFunc func(TenantEvent)

// SubscribeTenantEvents creates a JetStream consumer on the management tenant
// CRUD topic and dispatches every tenant row event (create, update, soft
// delete) to onEvent, flagging the disable transition (deleted_at nil → set).
// Every event is dispatched — not only the two lifecycle transitions —
// because an unrelated update costs the consumer no more than one extra
// verdict lookup, while a missed liveness change would be served for a full
// TTL. The returned
// ConsumeContext should be Stop'd on shutdown.
//
// Subscribes to both 'update' and 'delete' because the outbox hook
// reclassifies SetDeletedAt mutations as soft deletes (operation = 'delete');
// see backend/management/events/tenants/trigger.go for the same pattern.
//
// The consumer is ephemeral: leaving Name unset has JetStream generate a
// unique consumer per replica, sidestepping the queue-group semantics that
// would otherwise have a shared name deliver each event to only one
// replica. InactiveThreshold reaps consumers of crashed or scaled-down
// replicas; DeliverNewPolicy skips replay of the stream's retention window
// against a process-local cache that starts empty.
//
//nolint:ireturn // jetstream.ConsumeContext is the cancel handle the caller needs.
func SubscribeTenantEvents(
	ctx context.Context,
	js jetstream.JetStream,
	streamName, serviceName string,
	onEvent TenantEventFunc,
) (jetstream.ConsumeContext, error) {
	// Build the subscription subjects through the canonical topic builder so
	// any future change to the topic format updates this subscriber alongside
	// every other event consumer. Zero-valued UUID fields render as "*"
	// wildcards, which matches any tenant / any entity row.
	subjects := make([]string, 0, 3)
	for _, op := range []string{topic.OpCreate, topic.OpUpdate, topic.OpDelete} {
		subjects = append(subjects, topic.MutationEventTopic{
			StreamName:    streamName,
			ServiceName:   topic.ManagementService,
			SchemaName:    topic.TenantSchema,
			OperationName: op,
		}.String())
	}

	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Description:       serviceName + " tenant-events listener",
		FilterSubjects:    subjects,
		DeliverPolicy:     jetstream.DeliverNewPolicy,
		InactiveThreshold: tenantEventsConsumerInactive,
	})
	if err != nil {
		return nil, fmt.Errorf("create tenant-events consumer for %q: %w", serviceName, err)
	}

	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		handleTenantEvent(ctx, msg, onEvent)
	})
	if err != nil {
		return nil, fmt.Errorf("start tenant-events consume for %q: %w", serviceName, err)
	}
	return cc, nil
}

func handleTenantEvent(ctx context.Context, msg jetstream.Msg, onEvent TenantEventFunc) {
	logger := log.ForContext(ctx).With().
		Str("component", "authn.tenant-events").
		Str("subject", msg.Subject()).
		Logger()
	// Every path acks: retry cannot fix a malformed payload, and the
	// dispatched work is idempotent evictions.
	defer ack(msg, logger)

	var event topic.MutationEventMessage
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		logger.Error().Err(err).Msg("decode mutation event")
		return
	}

	if event.Service != topic.ManagementService || !strings.EqualFold(event.Schema, topic.TenantSchema) {
		return
	}
	if event.ID == uuid.Nil {
		return
	}

	// DeliverNewPolicy + ephemeral consumer = no replay. A silently
	// dropped malformed payload is lost forever, so a structural
	// mismatch must be Error-logged. Ack rather than Nak — retry can't
	// fix a shape mismatch.
	dataAfter, ok := event.DataAfter.(map[string]any)
	if !ok && event.DataAfter != nil {
		logger.Error().Interface("data_after", event.DataAfter).
			Msg("DataAfter is not a map; cannot evaluate revocation transition")
		return
	}
	dataBefore, ok := event.DataBefore.(map[string]any)
	if !ok && event.DataBefore != nil {
		logger.Error().Interface("data_before", event.DataBefore).
			Msg("DataBefore is not a map; cannot evaluate revocation transition")
		return
	}

	deletedAfter := topic.IsDeletedAt(dataAfter["deleted_at"])
	deletedBefore := topic.IsDeletedAt(dataBefore["deleted_at"])

	disabling := !deletedBefore && deletedAfter
	onEvent(TenantEvent{TenantID: event.ID, Disabling: disabling})
	if disabling || (deletedBefore && !deletedAfter) {
		logger.Info().
			Str("tenant_id", event.ID.String()).
			Bool("disabling", disabling).
			Msg("tenant lifecycle transition applied to caches")
	}
}

// ack acknowledges msg and logs (rather than drops) an Ack failure: the
// consumer is ephemeral with DeliverNew, so a failed Ack only risks one
// redelivery of an idempotent event — worth a Warn, not a retry.
func ack(msg jetstream.Msg, logger log.Logger) {
	if err := msg.Ack(); err != nil {
		logger.Warn().Err(err).Msg("ack tenant lifecycle event")
	}
}
