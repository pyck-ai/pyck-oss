package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
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

	outboxEventsDeadLettered = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbox_events_dead_lettered_total",
			Help: "Total number of dead outbox events republished to the DLQ stream and removed from the outbox",
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

	// BatchSize is the maximum transaction groups to process per batch (default: 100).
	BatchSize int

	// ReplyTimeout is the timeout for NATS request/reply (default: 10s).
	ReplyTimeout time.Duration

	// MaxRetries is the maximum retry count before marking transaction group as dead (default: 10).
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
	// Use NewOutboxSelector to create one with proper transaction ordering.
	OutboxSelector OutboxSelectFunc

	// OutboxMarkPublished marks an entry as successfully published.
	// Use NewOutboxMarkPublished to create one.
	OutboxMarkPublished OutboxMarkPublishedFunc

	// OutboxMarkFailed marks an entry as failed for retry.
	// Use NewOutboxMarkFailed to create one.
	OutboxMarkFailed OutboxMarkFailedFunc

	// OutboxMarkTransactionDead marks all remaining entries in a transaction group as dead.
	// Use NewOutboxMarkTransactionDead to create one.
	OutboxMarkTransactionDead OutboxMarkTransactionDeadFunc

	// OutboxClaim leases fetched entries (in the same transaction as the select)
	// so no other poller picks them up while they are being published.
	// Use NewOutboxClaim to create one. Optional: if nil, no claim is taken.
	OutboxClaim OutboxClaimFunc

	// ClaimLease is how far into the future fetched entries are leased before
	// publishing. It must exceed the worst-case time to publish a batch
	// (≈ ReplyTimeout plus slack) so that a row is never re-selected mid-publish.
	// A poller that dies mid-publish simply lets the lease expire and the rows
	// are retried. Defaults to defaultClaimLease when unset.
	ClaimLease time.Duration

	// OutboxSelectDead selects dead-lettered entries to drain to the DLQ stream.
	// Use NewOutboxSelectDead. Optional: if nil, no DLQ drain runs.
	OutboxSelectDead OutboxSelectDeadFunc

	// OutboxDelete removes an entry once the DLQ stream has accepted it.
	// Use NewOutboxDelete. Required when OutboxSelectDead is set.
	OutboxDelete OutboxDeleteFunc

	// DLQDrainInterval is how often dead-lettered rows are republished to the DLQ
	// stream and deleted from the outbox. Runs on its own slow ticker, decoupled
	// from the hot poll loop. Defaults to defaultDLQDrainInterval when unset.
	DLQDrainInterval time.Duration
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

	// Start the DLQ drain (if configured)
	if h.config.OutboxSelectDead != nil {
		h.wg.Add(1)
		go h.drainDeadLettersPeriodically(ctx)
	}

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
//
// The interval is jittered around PollInterval (see nextPollDelay) so that
// multiple replicas polling the same table do not synchronize into a
// thundering herd. There is intentionally no empty-result backoff: timely
// delivery currently relies on a consistently short poll interval, so the
// delay stays centered on PollInterval whether or not the last poll found work.
func (h *OutboxHandler) pollPeriodically(ctx context.Context) {
	defer h.wg.Done()

	logger := log.ForContext(ctx)
	timer := time.NewTimer(h.nextPollDelay())
	defer timer.Stop()

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
		case <-timer.C:
			if err := h.processOutbox(ctx); err != nil {
				logger.Error().Err(err).Msg("periodic outbox poll failed")
			}
			timer.Reset(h.nextPollDelay())
		}
	}
}

// nextPollDelay returns the configured poll interval with additive jitter to
// desynchronize replicas. The result is uniformly distributed in
// [0.75*PollInterval, 1.25*PollInterval), keeping the mean at PollInterval
// (no backoff) while spreading concurrent pollers apart.
func (h *OutboxHandler) nextPollDelay() time.Duration {
	base := h.config.PollInterval
	// base < 2 covers both a non-positive interval and the sub-2ns case where
	// the jitter window (base/2) collapses to zero — rand.Int64N panics for n<=0.
	if base < 2 {
		return base
	}
	// Jitter window is 50% of the base, centered on the base. A weak RNG is
	// fine here — this only desynchronizes pollers, it is not security-relevant.
	jitter := time.Duration(rand.Int64N(int64(base / 2))) //nolint:gosec // non-crypto jitter
	return base - base/4 + jitter
}

// outboxMarkKind identifies the DB write to apply for an entry after publishing.
type outboxMarkKind int

const (
	markKindPublished outboxMarkKind = iota
	markKindFailed
	markKindDead
)

// defaultClaimLease is used when OutboxHandlerConfig.ClaimLease is unset. It must
// comfortably exceed the worst-case time to publish one batch.
var defaultClaimLease = 30 * time.Second

// defaultDLQDrainInterval is used when OutboxHandlerConfig.DLQDrainInterval is unset.
var defaultDLQDrainInterval = 5 * time.Second

// outboxMark records a DB write to apply after the NATS publish phase, so all
// writes for a batch can be committed together in a single transaction. Metrics
// are emitted from the mark after the persist commit, so counters track
// persisted outcomes rather than mere publish attempts (which can roll back).
type outboxMark struct {
	kind          outboxMarkKind
	id            uuid.UUID // entry ID (markKindPublished / markKindFailed)
	transactionID uuid.UUID // transaction group ID (markKindDead)
	entityType    string    // for Prometheus labels
	errMsg        string    // publish error (markKindFailed) or dead reason (markKindDead)
	droppedCount  int       // entries dead-lettered by this mark (markKindDead)
}

// processOutbox processes pending outbox entries in three phases so the DB
// transaction is never held open across the (slow, network-bound) NATS publish:
//
//  1. Fetch — claim a batch of pending entries in a short transaction, then
//     commit immediately to release the FOR UPDATE SKIP LOCKED row locks.
//  2. Publish — publish each entry to NATS with NO open DB transaction. Entries
//     are grouped by transaction ID and published in order; if one fails, the
//     rest of its group is skipped. An entry that has exhausted its retries
//     dead-letters its whole group.
//  3. Persist — apply all resulting marks (published / failed / dead) in a
//     single transaction, committed once.
//
// Phase 1 leases the fetched rows (OutboxClaim) before releasing the locks, so
// no other poller — the NOTIFY goroutine, the poll timer, or another replica —
// can select the same rows while they are mid-publish. This matters most for
// with-reply entries: they go through core NATS request/reply (no JetStream, no
// msgID dedup), so a concurrent republish would issue a second request and start
// duplicate workflows. Fire-and-forget republishes are additionally idempotent
// via the deterministic msgID + JetStream dedup, and all marks are idempotent.
func (h *OutboxHandler) processOutbox(ctx context.Context) error {
	logger := log.ForContext(ctx)

	// Phase 1: fetch a batch and release the locks before publishing.
	entries, err := h.fetchPendingEntries(ctx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	logger.Debug().Int("count", len(entries)).Msg("processing outbox entries")

	// Phase 2: publish to NATS with no open DB transaction.
	marks := h.publishEntries(ctx, entries)

	// Phase 3: persist all marks in a single committed transaction.
	return h.persistMarks(ctx, marks)
}

// fetchPendingEntries selects a batch of pending outbox entries and leases them
// (both within one short-lived transaction), then commits before returning. The
// lease prevents any other poller from re-selecting the rows while they are
// mid-publish; the commit releases the FOR UPDATE SKIP LOCKED locks so the
// transaction is not held open across the NATS publish.
func (h *OutboxHandler) fetchPendingEntries(ctx context.Context) ([]OutboxRow, error) {
	logger := log.ForContext(ctx)

	tx, err := h.config.DB.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to begin select transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			logger.Error().Err(err).Msg("failed to rollback select transaction")
		}
	}()

	entries, err := h.config.OutboxSelector(ctx, tx, h.config.BatchSize, h.config.MaxRetries)
	if err != nil {
		return nil, fmt.Errorf("failed to select outbox entries: %w", err)
	}

	// Lease the fetched rows so no concurrent poller publishes them too.
	if err := h.claimEntries(ctx, tx, entries); err != nil {
		return nil, err
	}

	// Commit to persist the lease and release the row locks before any publish.
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit select transaction: %w", err)
	}

	return entries, nil
}

// claimEntries leases the given entries until now + ClaimLease, within the
// supplied (select) transaction, so the lease is committed atomically with the
// lock release. A no-op when no claim function is configured (e.g. unit tests).
func (h *OutboxHandler) claimEntries(ctx context.Context, tx *sql.Tx, entries []OutboxRow) error {
	if h.config.OutboxClaim == nil || len(entries) == 0 {
		return nil
	}

	lease := h.config.ClaimLease
	if lease <= 0 {
		lease = defaultClaimLease
	}

	ids := make([]uuid.UUID, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}

	if err := h.config.OutboxClaim(ctx, tx, ids, time.Now().UTC().Add(lease)); err != nil {
		return fmt.Errorf("failed to claim outbox entries: %w", err)
	}

	return nil
}

// groupByTransaction groups entries by transaction ID, preserving order within each group.
func groupByTransaction(entries []OutboxRow) map[uuid.UUID][]OutboxRow {
	groups := make(map[uuid.UUID][]OutboxRow)
	for _, entry := range entries {
		groups[entry.TransactionID] = append(groups[entry.TransactionID], entry)
	}
	return groups
}

// publishEntries publishes all entries to NATS, grouped by transaction ID, and
// returns the marks to persist. No DB transaction is held during publishing.
func (h *OutboxHandler) publishEntries(ctx context.Context, entries []OutboxRow) []outboxMark {
	marks := make([]outboxMark, 0, len(entries))
	for transactionID, groupEntries := range groupByTransaction(entries) {
		marks = append(marks, h.publishTransactionGroup(ctx, transactionID, groupEntries)...)
	}
	return marks
}

// publishTransactionGroup publishes all entries in a transaction group in order
// and returns the marks to persist. If an entry has exhausted its retries the
// whole group is dead-lettered. If a publish fails, the remaining entries in the
// group are skipped (preserving ordered, all-or-nothing delivery per tx).
func (h *OutboxHandler) publishTransactionGroup(ctx context.Context, transactionID uuid.UUID, entries []OutboxRow) []outboxMark {
	logger := log.ForContext(ctx)
	txIDStr := transactionID.String()

	marks := make([]outboxMark, 0, len(entries))
	for _, entry := range entries {
		// Dead-letter the whole group once any entry has exhausted its retries.
		if entry.RetryCount >= h.config.MaxRetries {
			reason := fmt.Sprintf("entry %s exceeded max retries (%d)", entry.ID.String(), h.config.MaxRetries)
			logger.Error().
				Str("transaction_id", txIDStr).
				Str("entry_id", entry.ID.String()).
				Int("retry_count", entry.RetryCount).
				Msg("transaction group exceeded max retries, dead-lettering")

			// Only the remaining (not-yet-published) entries are dropped; any
			// earlier entry in this group already produced a markKindPublished
			// (a failed one would have returned above), so counting len(entries)
			// here would double-count those against outboxEventsPublished.
			return append(marks, outboxMark{
				kind:          markKindDead,
				transactionID: transactionID,
				entityType:    entry.GetEntityType(),
				errMsg:        reason,
				droppedCount:  len(entries) - len(marks),
			})
		}

		mark := h.publishEntry(ctx, entry)
		marks = append(marks, mark)
		if mark.kind == markKindFailed {
			logger.Error().
				Str("entry_id", entry.ID.String()).
				Str("transaction_id", txIDStr).
				Str("error", mark.errMsg).
				Msg("failed to publish outbox entry, skipping remaining entries in transaction group")
			return marks // Stop processing this transaction group on failure
		}
	}
	return marks
}

// publishEntry publishes a single outbox entry (handling both with-reply and
// fire-and-forget modes) and returns the mark to persist. A publish failure is
// recorded as a markKindFailed rather than returned as an error; the caller
// decides retry vs. dead-letter based on retry count. No DB write happens here.
func (h *OutboxHandler) publishEntry(ctx context.Context, entry OutboxRow) outboxMark {
	logger := log.ForContext(ctx)

	// Build NATS message ID for idempotent publishing.
	msgID := h.buildMessageID(entry)

	workflows, err := h.publish(ctx, entry, msgID)
	if err != nil {
		return outboxMark{kind: markKindFailed, id: entry.ID, entityType: entry.GetEntityType(), errMsg: err.Error()}
	}

	// Deliver workflows to the waiting resolver (if any). This is in-memory /
	// transport delivery, independent of the DB writes applied in phase 3.
	if entry.WithReply && h.config.ReplyRegistry != nil {
		if !h.config.ReplyRegistry.Deliver(entry.TransactionID, workflows) {
			logger.Debug().
				Str("transaction_id", entry.TransactionID.String()).
				Msg("no waiter for reply (timeout or fire-and-forget mode)")
		}
	}

	return outboxMark{kind: markKindPublished, id: entry.ID, entityType: entry.GetEntityType()}
}

// buildMessageID creates a NATS message ID for idempotent publishing.
//
// Format: <service_name>-<transaction_id>-<entry_id>
//
// transaction_id is generated by gqltx at BeginTx and persisted in the
// outbox row, so it is stable across outbox-handler restarts (a republish
// after a crash between PublishRaw and markEntryPublished produces the
// same msgID and is suppressed by JetStream dedup). entry_id is the
// outbox row's UUID v7, distinct per row even within the same tx.
func (h *OutboxHandler) buildMessageID(entry OutboxRow) string {
	return fmt.Sprintf("%s-%s-%s",
		h.config.ServiceName,
		entry.TransactionID.String(),
		entry.ID.String(),
	)
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
				Str("transaction_id", entry.TransactionID.String()).
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

	// Build a deterministic msgID so JetStream dedup engages on the
	// chase publish path. Without it, a republish of the same outbox row
	// (e.g. after an outbox-handler restart between PublishRaw and
	// markEntryPublished) would deliver a second copy to subscribers.
	msgID := h.buildMessageID(entry)
	if err := h.config.Publisher.PublishRaw(ctx, topic.String(), entry.Payload, msgID); err != nil {
		logger.Warn().
			Err(err).
			Str("topic", topic.String()).
			Msg("failed to publish fire-and-forget after reply")
	}
}

// persistMarks applies all post-publish marks in a single transaction,
// committed once. The DB transaction is opened only after every NATS publish has
// completed, so it is never held open across a publish. The marks are idempotent
// (mark-published / mark-failed by entry ID, dead-letter by transaction ID), so
// a rollback after a partial failure simply re-publishes the affected rows on
// the next poll, where JetStream dedup suppresses the duplicates.
func (h *OutboxHandler) persistMarks(ctx context.Context, marks []outboxMark) error {
	if len(marks) == 0 {
		return nil
	}

	logger := log.ForContext(ctx)

	tx, err := h.config.DB.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("failed to begin mark transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			logger.Error().Err(err).Msg("failed to rollback mark transaction")
		}
	}()

	for _, mark := range marks {
		switch mark.kind {
		case markKindPublished:
			if err := h.config.OutboxMarkPublished(ctx, tx, mark.id); err != nil {
				return fmt.Errorf("failed to mark entry published: %w", err)
			}
		case markKindFailed:
			if err := h.config.OutboxMarkFailed(ctx, tx, mark.id, mark.errMsg); err != nil {
				return fmt.Errorf("failed to mark entry failed: %w", err)
			}
		case markKindDead:
			if err := h.config.OutboxMarkTransactionDead(ctx, tx, mark.transactionID, mark.errMsg); err != nil {
				return fmt.Errorf("failed to dead-letter transaction group: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit mark transaction: %w", err)
	}

	// Emit metrics only after the marks are durably committed, so the counters
	// track persisted outcomes. If the commit above fails the rows are
	// re-published (and re-counted) on a later poll, rather than being counted
	// here for an outcome that rolled back.
	h.recordMarkMetrics(marks)

	return nil
}

// recordMarkMetrics increments the publish/retry/drop counters for a batch of
// successfully persisted marks.
func (h *OutboxHandler) recordMarkMetrics(marks []outboxMark) {
	for _, mark := range marks {
		switch mark.kind {
		case markKindPublished:
			outboxEventsPublished.WithLabelValues(h.config.ServiceName, mark.entityType).Inc()
		case markKindFailed:
			outboxEventsRetried.WithLabelValues(h.config.ServiceName, mark.entityType).Inc()
		case markKindDead:
			outboxEventsDropped.WithLabelValues(h.config.ServiceName, mark.entityType).Add(float64(mark.droppedCount))
		}
	}
}

// drainDeadLettersPeriodically republishes dead-lettered rows to the DLQ stream
// and deletes them from the outbox, on its own ticker, decoupled from the hot
// poll loop. It polls unconditionally — the dead-row scan is an index scan over
// a tiny partial index (dead_at IS NOT NULL AND published_at IS NULL), and dead
// rows are rare — so it holds no in-memory state and recovery is bounded: a
// replica that dies leaves its dead rows in the table, and the next tick on any
// surviving replica drains them within one DLQDrainInterval.
func (h *OutboxHandler) drainDeadLettersPeriodically(ctx context.Context) {
	defer h.wg.Done()

	logger := log.ForContext(ctx)

	interval := h.config.DLQDrainInterval
	if interval <= 0 {
		interval = defaultDLQDrainInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stopCh:
			return
		case <-ticker.C:
			if err := h.drainDeadLetters(ctx); err != nil {
				logger.Error().Err(err).Msg("dead-letter drain failed")
			}
		}
	}
}

// drainDeadLetters republishes a batch of dead-lettered rows to the DLQ stream
// and deletes each accepted row, all in one transaction: the rows are locked
// with FOR UPDATE SKIP LOCKED in the select, so exactly one replica owns each
// row through its publish, delete, and commit — no two replicas DLQ the same row
// (the dlq- msgID dedup only covers the crash-before-commit sliver). A row whose
// payload is unparseable is left in place (logged); a transient publish failure
// leaves its row for the next tick.
//
// Unlike the hot path, the DLQ publish is fire-and-forget JetStream (~ms to
// ack), so holding the row lock across the publish is cheap. The lock is held
// across the whole batch's publishes, so during a stream outage a batch holds
// the transaction for roughly BatchSize × ack-latency — fine here (dead rows are
// rare and there's nothing useful to do during an outage), but keep BatchSize
// modest.
func (h *OutboxHandler) drainDeadLetters(ctx context.Context) error {
	logger := log.ForContext(ctx)

	tx, err := h.config.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("failed to begin dead-letter drain transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			logger.Error().Err(err).Msg("failed to rollback dead-letter drain transaction")
		}
	}()

	rows, err := h.config.OutboxSelectDead(ctx, tx, h.config.BatchSize)
	if err != nil {
		return fmt.Errorf("failed to select dead-letter rows: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	acked := h.publishDeadLetters(ctx, rows)
	if len(acked) > 0 {
		ids := make([]uuid.UUID, len(acked))
		for i, row := range acked {
			ids[i] = row.ID
		}
		if err := h.config.OutboxDelete(ctx, tx, ids); err != nil {
			// Rollback; the rows stay dead-lettered and are re-published
			// (deduped) and re-deleted on the next tick.
			return fmt.Errorf("failed to delete drained dead-letter rows: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit dead-letter drain transaction: %w", err)
	}

	// Count only after the delete is durably committed.
	for _, row := range acked {
		outboxEventsDeadLettered.WithLabelValues(h.config.ServiceName, row.GetEntityType()).Inc()
	}
	if len(acked) > 0 {
		logger.Debug().Int("count", len(acked)).Msg("drained dead-letter rows to DLQ stream")
	}

	return nil
}

// publishDeadLetters republishes each dead row to its DLQ topic and returns the
// rows the stream accepted (to be deleted within the same transaction). A row
// with an unparseable payload (logged) or a transient publish failure is left
// out, so it is retried on the next tick. No DB access — separated for testing.
func (h *OutboxHandler) publishDeadLetters(ctx context.Context, rows []OutboxRow) []OutboxRow {
	logger := log.ForContext(ctx)

	acked := make([]OutboxRow, 0, len(rows))
	for _, row := range rows {
		topic, ok := h.buildDeadLetterTopic(ctx, row)
		if !ok {
			continue // unparseable payload — logged in buildDeadLetterTopic
		}
		if err := h.config.Publisher.PublishRaw(ctx, topic, row.Payload, h.deadLetterMsgID(row)); err != nil {
			logger.Warn().Err(err).Str("entry_id", row.ID.String()).Msg("failed to publish dead-letter to DLQ, will retry")
			continue
		}
		acked = append(acked, row)
	}
	return acked
}

// deadLetterMsgID derives the JetStream dedup ID for a dead-letter publish. It
// namespaces the row's deterministic msgID under a "dlq-" prefix so that drain
// retries of the same row are deduped, without colliding with the dedup entry
// from the row's original (CRUD-subject) publish attempts in the same stream.
func (h *OutboxHandler) deadLetterMsgID(row OutboxRow) string {
	return "dlq-" + h.buildMessageID(row)
}

// buildDeadLetterTopic derives the DLQ topic for a dead-lettered row from its
// payload, mirroring publishFireAndForgetAfterReply. Returns false (and logs) if
// the payload cannot be parsed.
func (h *OutboxHandler) buildDeadLetterTopic(ctx context.Context, row OutboxRow) (string, bool) {
	var payload MutationEventMessage
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		log.ForContext(ctx).Warn().
			Err(err).
			Str("entry_id", row.ID.String()).
			Msg("failed to parse payload for dead-letter topic; skipping")
		return "", false
	}

	topic := DeadLetterEventTopic{
		StreamName:    h.config.StreamName,
		TenantID:      payload.TenantID,
		ServiceName:   payload.Service,
		SchemaName:    payload.Schema,
		EntityID:      payload.ID,
		OperationName: payload.Operation,
	}
	return topic.String(), true
}

// getConnString returns the PostgreSQL connection string for LISTEN/NOTIFY.
func (h *OutboxHandler) getConnString() string {
	return h.config.ConnString
}
