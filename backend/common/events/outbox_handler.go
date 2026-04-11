package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/pyck-ai/pyck/backend/common/log"
)

var (
	outboxEventsPublished = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbox_events_published_total",
			Help: "Total number of outbox events successfully published",
		},
		[]string{"service", "entity_type"},
	)

	outboxEventsRetried = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbox_events_retried_total",
			Help: "Total number of outbox event publish retries",
		},
		[]string{"service", "entity_type"},
	)

	outboxEventsDropped = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbox_events_dropped_total",
			Help: "Total number of outbox events dropped (dead letter)",
		},
		[]string{"service", "entity_type"},
	)
)

// OutboxHandlerConfig configures the OutboxHandler behavior.
// Use EventOutboxConfig from common/env/config to load these values from environment variables.
type OutboxHandlerConfig struct {
	// DB is the database connection pool.
	DB *sql.DB

	// ConnString is the PostgreSQL connection string for LISTEN/NOTIFY.
	// This is required because LISTEN needs a dedicated connection, not a pooled one.
	ConnString string

	// Publisher is used for publishing events to NATS.
	// Use NewEventPublisher to create one.
	Publisher Publisher

	// ReplyRegistry is used to deliver workflow IDs back to waiting resolvers.
	ReplyRegistry *ReplyRegistry

	// StreamName for NATS topics (default: "pyck").
	StreamName string

	// PollInterval is the interval between polling checks (default: 5s).
	PollInterval time.Duration

	// BatchSize is the maximum correlation groups to process per batch (default: 100).
	BatchSize int

	// ReplyTimeout is the timeout for NATS request/reply (default: 10s).
	ReplyTimeout time.Duration

	// MaxRetries is the maximum retry count before marking correlation group as dead (default: 10).
	MaxRetries int

	// NotifyChannel is the PostgreSQL NOTIFY channel name (default: "outbox_events").
	NotifyChannel string

	// ListenerPingInterval is the interval for pinging the LISTEN connection to keep it alive (default: 90s).
	ListenerPingInterval time.Duration

	// ListenNotifyEnabled controls whether PostgreSQL LISTEN/NOTIFY is used for low-latency processing.
	// When false, only periodic polling is used. Default: true.
	ListenNotifyEnabled bool

	// ServiceName is the service identifier for Prometheus metric labels.
	ServiceName string

	// OutboxSelector selects pending outbox entries for processing.
	// Use NewOutboxSelector to create one with proper correlation ordering.
	OutboxSelector OutboxSelectFunc

	// OutboxMarkPublished marks an entry as successfully published.
	// Use NewOutboxMarkPublished to create one.
	OutboxMarkPublished OutboxMarkPublishedFunc

	// OutboxMarkFailed marks an entry as failed for retry.
	// Use NewOutboxMarkFailed to create one.
	OutboxMarkFailed OutboxMarkFailedFunc

	// OutboxMarkCorrelationDead marks all remaining entries in a correlation group as dead.
	// Use NewOutboxMarkCorrelationDead to create one.
	OutboxMarkCorrelationDead OutboxMarkCorrelationDeadFunc
}

// OutboxHandler processes outbox entries and publishes them to NATS.
//
// It uses a hybrid approach for reliability:
//   - LISTEN/NOTIFY for low-latency processing (~ms after commit)
//   - Periodic polling for crash recovery (missed notifications)
//
// Flow:
//  1. Service writes outbox entry in mutation transaction
//  2. PostgreSQL trigger sends NOTIFY on insert
//  3. OutboxHandler receives NOTIFY, processes entry
//  4. Publishes to NATS (request/reply or fire-and-forget)
//  5. Marks entry as published (sets published_at)
//  6. For with_reply entries, delivers workflow IDs to ReplyRegistry
type OutboxHandler struct {
	config OutboxHandlerConfig

	stopCh  chan struct{}
	stopped atomic.Bool
	wg      sync.WaitGroup
}

// NewOutboxHandler creates a new OutboxHandler with the given configuration.
// Configuration values should be loaded from environment via EventOutboxConfig (common/env/config).
func NewOutboxHandler(config OutboxHandlerConfig) *OutboxHandler {
	if config.StreamName == "" {
		config.StreamName = DefaultStreamName
	}

	return &OutboxHandler{
		config: config,
		stopCh: make(chan struct{}),
	}
}

// Start begins processing outbox entries.
// Spawns two goroutines: one for LISTEN/NOTIFY, one for periodic polling.
func (h *OutboxHandler) Start(ctx context.Context) error {
	// Start NOTIFY listener (if enabled)
	if h.config.ListenNotifyEnabled {
		h.wg.Add(1)
		go h.listenForNotifications(ctx)
	}

	// Start periodic poller
	h.wg.Add(1)
	go h.pollPeriodically(ctx)

	return nil
}

// Stop stops the handler and waits for goroutines to finish.
func (h *OutboxHandler) Stop() {
	if h.stopped.Swap(true) {
		return // Already stopped
	}

	close(h.stopCh)
	h.wg.Wait()
}

// listenForNotifications listens for PostgreSQL NOTIFY events.
func (h *OutboxHandler) listenForNotifications(ctx context.Context) {
	defer h.wg.Done()

	logger := log.ForContext(ctx)

	// Create a separate connection for LISTEN (can't use pooled connection)
	listener := pq.NewListener(
		h.getConnString(),
		10*time.Second,
		time.Minute,
		func(ev pq.ListenerEventType, err error) {
			if err != nil {
				logger.Error().Err(err).Msg("outbox listener error")
			}
		},
	)
	defer listener.Close()

	if err := listener.Listen(h.config.NotifyChannel); err != nil {
		logger.Error().Err(err).Str("channel", h.config.NotifyChannel).Msg("failed to listen on channel")
		return
	}

	logger.Info().Str("channel", h.config.NotifyChannel).Msg("outbox handler listening for notifications")

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stopCh:
			return
		case notification := <-listener.Notify:
			if notification == nil {
				// Connection lost, listener will reconnect
				continue
			}

			logger.Debug().
				Str("channel", notification.Channel).
				Str("payload", notification.Extra).
				Msg("received outbox notification")

			// Process outbox entries
			if err := h.processOutbox(ctx); err != nil {
				logger.Error().Err(err).Msg("failed to process outbox after notification")
			}
		case <-time.After(h.config.ListenerPingInterval):
			// Ping to keep connection alive
			if err := listener.Ping(); err != nil {
				logger.Warn().Err(err).Msg("listener ping failed")
			}
		}
	}
}

// pollPeriodically polls the outbox table at regular intervals.
// This is a fallback for crash recovery and missed notifications.
func (h *OutboxHandler) pollPeriodically(ctx context.Context) {
	defer h.wg.Done()

	logger := log.ForContext(ctx)
	ticker := time.NewTicker(h.config.PollInterval)
	defer ticker.Stop()

	// Initial poll on startup
	if err := h.processOutbox(ctx); err != nil {
		logger.Error().Err(err).Msg("initial outbox poll failed")
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stopCh:
			return
		case <-ticker.C:
			if err := h.processOutbox(ctx); err != nil {
				logger.Error().Err(err).Msg("periodic outbox poll failed")
			}
		}
	}
}

// processOutbox processes pending outbox entries.
// Entries are grouped by correlation ID and processed in order within each group.
// If any entry in a correlation group fails, remaining entries in that group are skipped.
// If an entry exceeds max retries, the entire correlation group is marked as dead.
func (h *OutboxHandler) processOutbox(ctx context.Context) error {
	logger := log.ForContext(ctx)

	// Start transaction with READ COMMITTED isolation.
	tx, err := h.config.DB.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			logger.Error().Err(err).Msg("failed to rollback transaction")
		}
	}()

	// Select entries using callback (ensures correlation ordering)
	entries, err := h.config.OutboxSelector(ctx, tx, h.config.BatchSize, h.config.MaxRetries)
	if err != nil {
		return fmt.Errorf("failed to select outbox entries: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	logger.Debug().Int("count", len(entries)).Msg("processing outbox entries")

	// Group entries by correlation ID for ordered processing
	correlationGroups := groupByCorrelation(entries)

	// Process each correlation group
	for correlationID, groupEntries := range correlationGroups {
		h.processCorrelationGroup(ctx, tx, correlationID, groupEntries)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// groupByCorrelation groups entries by correlation ID, preserving order within each group.
func groupByCorrelation(entries []OutboxRow) map[string][]OutboxRow {
	groups := make(map[string][]OutboxRow)
	for _, entry := range entries {
		groups[entry.CorrelationID] = append(groups[entry.CorrelationID], entry)
	}
	return groups
}

// processCorrelationGroup processes all entries in a correlation group in order.
// If any entry fails, remaining entries in the group are skipped.
// If an entry exceeds max retries, the entire group is marked as dead.
func (h *OutboxHandler) processCorrelationGroup(ctx context.Context, tx *sql.Tx, correlationID string, entries []OutboxRow) {
	logger := log.ForContext(ctx)

	for _, entry := range entries {
		// Check if entry has exceeded max retries
		if entry.RetryCount >= h.config.MaxRetries {
			reason := fmt.Sprintf("entry %s exceeded max retries (%d)", entry.ID.String(), h.config.MaxRetries)
			logger.Error().
				Str("correlation_id", correlationID).
				Str("entry_id", entry.ID.String()).
				Int("retry_count", entry.RetryCount).
				Msg("correlation group exceeded max retries, deleting from outbox")

			outboxEventsDropped.WithLabelValues(h.config.ServiceName, entry.GetEntityType()).Add(float64(len(entries)))

			if err := h.config.OutboxMarkCorrelationDead(ctx, tx, correlationID, reason); err != nil {
				logger.Error().Err(err).Str("correlation_id", correlationID).Msg("failed to delete dead correlation group")
			}
			return // Stop processing this correlation group
		}

		// Process the entry
		if err := h.processEntry(ctx, tx, entry); err != nil {
			logger.Error().
				Err(err).
				Str("entry_id", entry.ID.String()).
				Str("correlation_id", correlationID).
				Msg("failed to process outbox entry, skipping remaining entries in correlation group")
			return // Stop processing this correlation group on failure
		}
	}
}

// processEntry processes a single outbox entry.
func (h *OutboxHandler) processEntry(ctx context.Context, tx *sql.Tx, entry OutboxRow) error {
	logger := log.ForContext(ctx)

	// Build NATS message ID for idempotent publishing
	msgID := h.buildMessageID(entry)

	// Publish the entry (handles both with-reply and fire-and-forget)
	workflows, err := h.publish(ctx, entry, msgID)
	if err != nil {
		return h.markEntryFailed(ctx, tx, entry.ID, entry.GetEntityType(), err)
	}

	// Mark as published
	if err := h.markEntryPublished(ctx, tx, entry.ID); err != nil {
		return err
	}

	outboxEventsPublished.WithLabelValues(h.config.ServiceName, entry.GetEntityType()).Inc()

	// Deliver workflows to waiting resolver (if any)
	if entry.WithReply && h.config.ReplyRegistry != nil {
		if !h.config.ReplyRegistry.Deliver(entry.CorrelationID, workflows) {
			logger.Debug().
				Str("correlation_id", entry.CorrelationID).
				Msg("no waiter for reply (timeout or fire-and-forget mode)")
		}
	}

	return nil
}

// buildMessageID creates a NATS message ID for idempotent publishing.
func (h *OutboxHandler) buildMessageID(entry OutboxRow) string {
	// Parse payload to extract entity type and ID
	var payload MutationEventMessage
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		// Fallback to just correlation ID
		return entry.CorrelationID
	}

	return fmt.Sprintf("%s-%s-%s", entry.CorrelationID, payload.Schema, payload.ID.String())
}

// publish publishes an outbox entry, handling both with-reply and fire-and-forget modes.
// For with-reply entries, it also publishes to the fire-and-forget topic after success.
func (h *OutboxHandler) publish(ctx context.Context, entry OutboxRow, msgID string) ([]*WorkflowDetails, error) {
	if entry.WithReply {
		return h.publishWithReply(ctx, entry)
	}

	return nil, h.config.Publisher.PublishRaw(ctx, entry.Topic, entry.Payload, msgID)
}

// publishWithReply publishes an event and waits for a reply with workflow IDs.
// After a successful reply, also publishes to the fire-and-forget topic for other subscribers.
func (h *OutboxHandler) publishWithReply(ctx context.Context, entry OutboxRow) ([]*WorkflowDetails, error) {
	logger := log.ForContext(ctx)

	reply, err := h.config.Publisher.RequestRaw(ctx, entry.Topic, entry.Payload, h.config.ReplyTimeout)
	if err != nil {
		return nil, fmt.Errorf("NATS request failed: %w", err)
	}

	if !reply.Success {
		return nil, fmt.Errorf("%w: %s", ErrReplyFailed, reply.Error)
	}

	// Parse workflow details from reply data
	var workflows []*WorkflowDetails
	if len(reply.Data) > 0 {
		if err := json.Unmarshal(reply.Data, &workflows); err != nil {
			logger.Warn().
				Err(err).
				Str("correlation_id", entry.CorrelationID).
				Msg("failed to parse workflow details from reply")
			// Don't fail - the request succeeded
		}
	}

	// Also publish to fire-and-forget topic for other subscribers
	h.publishFireAndForgetAfterReply(ctx, entry)

	return workflows, nil
}

// publishFireAndForgetAfterReply publishes to the fire-and-forget topic after a successful reply.
// This allows other subscribers to receive the event.
func (h *OutboxHandler) publishFireAndForgetAfterReply(ctx context.Context, entry OutboxRow) {
	logger := log.ForContext(ctx)

	// Parse payload to get details for building the fire-and-forget topic
	var payload MutationEventMessage
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		logger.Warn().Err(err).Msg("failed to parse payload for fire-and-forget publish")
		return
	}

	// Build fire-and-forget topic (different from request/reply topic)
	topic := MutationEventTopic{
		StreamName:    h.config.StreamName,
		TenantID:      payload.TenantID,
		ServiceName:   payload.Service,
		SchemaName:    payload.Schema,
		EntityID:      payload.ID,
		OperationName: payload.Operation,
	}

	if err := h.config.Publisher.PublishRaw(ctx, topic.String(), entry.Payload, ""); err != nil {
		logger.Warn().
			Err(err).
			Str("topic", topic.String()).
			Msg("failed to publish fire-and-forget after reply")
	}
}

// markEntryPublished marks an outbox entry as successfully published.
func (h *OutboxHandler) markEntryPublished(ctx context.Context, tx *sql.Tx, id uuid.UUID) error {
	if err := h.config.OutboxMarkPublished(ctx, tx, id); err != nil {
		return fmt.Errorf("failed to mark entry published: %w", err)
	}

	return nil
}

// markEntryFailed records a publish failure for retry.
func (h *OutboxHandler) markEntryFailed(ctx context.Context, tx *sql.Tx, id uuid.UUID, entityType string, publishErr error) error {
	if err := h.config.OutboxMarkFailed(ctx, tx, id, publishErr.Error()); err != nil {
		return fmt.Errorf("failed to mark entry failed: %w", err)
	}

	outboxEventsRetried.WithLabelValues(h.config.ServiceName, entityType).Inc()

	return nil
}

// getConnString returns the PostgreSQL connection string for LISTEN/NOTIFY.
func (h *OutboxHandler) getConnString() string {
	return h.config.ConnString
}
