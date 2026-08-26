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
	revocationConsumerInactive = 10 * time.Minute
)

// DisabledFunc is invoked when a tenant transitions to soft-deleted.
// [ZitadelAuthProvider.OnTenantDisabled] satisfies this type directly.
//
// Restores are NOT signalled: the next request for a restored tenant misses
// the (already-empty) cache, re-introspects, and Zitadel returns active=true
// because the org has been reactivated. No subscriber-side action needed.
type DisabledFunc func(tenantID uuid.UUID)

// SubscribeRevocations creates a JetStream consumer on the management tenant
// CRUD topic and dispatches soft-delete transitions to onDisabled. The
// returned ConsumeContext should be Stop'd on shutdown.
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
func SubscribeRevocations(
	ctx context.Context,
	js jetstream.JetStream,
	streamName, serviceName string,
	onDisabled DisabledFunc,
) (jetstream.ConsumeContext, error) {
	// Build the subscription subjects through the canonical topic builder so
	// any future change to the topic format updates this subscriber alongside
	// every other event consumer. Zero-valued UUID fields render as "*"
	// wildcards, which matches any tenant / any entity row.
	subjects := []string{
		topic.MutationEventTopic{
			StreamName:    streamName,
			ServiceName:   topic.ManagementService,
			SchemaName:    topic.TenantSchema,
			OperationName: topic.OpUpdate,
		}.String(),
		topic.MutationEventTopic{
			StreamName:    streamName,
			ServiceName:   topic.ManagementService,
			SchemaName:    topic.TenantSchema,
			OperationName: topic.OpDelete,
		}.String(),
	}

	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Description:       serviceName + " revocation listener",
		FilterSubjects:    subjects,
		DeliverPolicy:     jetstream.DeliverNewPolicy,
		InactiveThreshold: revocationConsumerInactive,
	})
	if err != nil {
		return nil, fmt.Errorf("create revocation consumer for %q: %w", serviceName, err)
	}

	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		handleRevocationMessage(ctx, msg, onDisabled)
	})
	if err != nil {
		return nil, fmt.Errorf("start revocation consume for %q: %w", serviceName, err)
	}
	return cc, nil
}

func handleRevocationMessage(ctx context.Context, msg jetstream.Msg, onDisabled DisabledFunc) {
	logger := log.ForContext(ctx).With().
		Str("component", "authn.revocation").
		Str("subject", msg.Subject()).
		Logger()

	var event topic.MutationEventMessage
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		logger.Error().Err(err).Msg("decode mutation event")
		_ = msg.Ack()
		return
	}

	if event.Service != topic.ManagementService || !strings.EqualFold(event.Schema, topic.TenantSchema) {
		_ = msg.Ack()
		return
	}
	if event.ID == uuid.Nil {
		_ = msg.Ack()
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
		_ = msg.Ack()
		return
	}
	dataBefore, ok := event.DataBefore.(map[string]any)
	if !ok && event.DataBefore != nil {
		logger.Error().Interface("data_before", event.DataBefore).
			Msg("DataBefore is not a map; cannot evaluate revocation transition")
		_ = msg.Ack()
		return
	}

	deletedAfter := topic.IsDeletedAt(dataAfter["deleted_at"])
	deletedBefore := topic.IsDeletedAt(dataBefore["deleted_at"])

	// Only react to the disable transition: nil → set. Restores are a no-op
	// (re-introspection against a reactivated org succeeds naturally).
	if !deletedBefore && deletedAfter {
		onDisabled(event.ID)
		logger.Info().Str("tenant_id", event.ID.String()).
			Msg("tenant disabled; introspection cache evicted")
	}

	_ = msg.Ack()
}

