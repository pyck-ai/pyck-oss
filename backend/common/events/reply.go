package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// WorkflowDetails contains information about a started workflow.
// This type mirrors common/workflow.WorkflowDetails to avoid import cycles.
// The outbox handler and signal router use this type for reply payloads.
type WorkflowDetails struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	RunID string `json:"runID"`
}

// replyEntry holds a pending reply channel and its expiration time.
type replyEntry struct {
	ch        chan []*WorkflowDetails
	expiresAt time.Time
	delivered atomic.Bool
	// unsub releases the cross-pod NATS subscription for this entry. nil in
	// in-memory mode (no transport).
	unsub func() error
}

// ReplyRegistry coordinates request/reply semantics between resolvers and the outbox handler.
//
// Flow:
//  1. Resolver calls Register() before executing mutation, gets a channel to wait on
//  2. Mutation executes, Ent hook writes to outbox (within same transaction)
//  3. Transaction commits
//  4. Outbox handler processes entry, publishes to NATS, receives workflow IDs
//  5. Outbox handler calls Deliver() with workflow IDs
//  6. Resolver receives workflow IDs on channel (or times out)
//
// Keying: entries are keyed by the per-tx transaction_id (UUID v7) generated
// by gqltx at BeginTx. Each OCC retry gets a distinct transaction_id, so the
// rolled-back attempt's waiter cannot accidentally absorb the successful
// attempt's reply.
//
// Delivery: with a ReplyTransport configured the registry is multi-pod safe —
// Register subscribes to a per-tx core NATS subject and Deliver publishes to
// it, so the reply reaches the waiting pod regardless of which pod's outbox
// handler processed the row. Without a transport (tests, single pod) it falls
// back to purely in-process delivery via the local map.
//
// Thread Safety: ReplyRegistry is safe for concurrent use. Multiple
// goroutines can call Register and Deliver simultaneously.
type ReplyRegistry struct {
	entries sync.Map // map[uuid.UUID]*replyEntry

	transport  ReplyTransport // nil => in-process delivery only
	streamName string

	cleanupInterval time.Duration
	stopCh          chan struct{}
	stopped         atomic.Bool
	wg              sync.WaitGroup
}

// NewReplyRegistry creates a new ReplyRegistry with the given cleanup interval.
// It delivers replies in-process only (single pod / tests). Use
// NewReplyRegistryWithTransport for multi-pod deployments.
// Call Start() to begin the background cleanup goroutine.
func NewReplyRegistry(cleanupInterval time.Duration) *ReplyRegistry {
	return &ReplyRegistry{
		cleanupInterval: cleanupInterval,
		stopCh:          make(chan struct{}),
	}
}

// NewReplyRegistryWithTransport creates a ReplyRegistry that delivers replies
// across pods via the given ReplyTransport (core NATS pub/sub). streamName
// namespaces the reply subjects per stream. This is the production constructor
// for horizontally-scaled services.
func NewReplyRegistryWithTransport(cleanupInterval time.Duration, transport ReplyTransport, streamName string) *ReplyRegistry {
	return &ReplyRegistry{
		transport:       transport,
		streamName:      streamName,
		cleanupInterval: cleanupInterval,
		stopCh:          make(chan struct{}),
	}
}

// replySubject builds the core NATS subject for a transaction's workflow reply.
// It is intentionally outside the JetStream stream's "<stream>.>" capture so the
// message is ephemeral core NATS (not persisted), mirroring request/reply inboxes.
func replySubject(streamName string, transactionID uuid.UUID) string {
	return fmt.Sprintf("reply.workflows.%s.%s", streamName, transactionID.String())
}

// Start begins the background cleanup goroutine that removes expired entries.
// The cleanup runs periodically at the configured interval.
// Call Stop() to terminate the cleanup goroutine.
func (r *ReplyRegistry) Start(ctx context.Context) {
	r.wg.Add(1)
	go r.cleanupLoop(ctx)
}

// Stop terminates the cleanup goroutine and closes all pending channels.
// Pending waiters will receive nil (empty workflow list).
// Safe to call multiple times.
func (r *ReplyRegistry) Stop() {
	if r.stopped.Swap(true) {
		return // Already stopped
	}

	close(r.stopCh)
	r.wg.Wait()

	// Close all remaining channels to unblock any waiters
	r.entries.Range(func(key, value any) bool {
		if entry, ok := value.(*replyEntry); ok {
			if entry.unsub != nil {
				_ = entry.unsub()
			}
			if !entry.delivered.Swap(true) {
				close(entry.ch)
			}
		}
		r.entries.Delete(key)
		return true
	})
}

// Register creates a pending reply entry for the given transaction ID.
// Returns a channel that will receive the workflow details when the reply is
// delivered, or be closed when the timeout expires or Stop is called.
//
// With a transport configured, Register also subscribes to the transaction's
// core NATS reply subject so a reply published by any pod's outbox handler is
// routed here and forwarded onto the channel.
func (r *ReplyRegistry) Register(transactionID uuid.UUID, timeout time.Duration) <-chan []*WorkflowDetails {
	ch := make(chan []*WorkflowDetails, 1)

	entry := &replyEntry{
		ch:        ch,
		expiresAt: time.Now().Add(timeout),
	}

	if r.transport != nil {
		entry.unsub = r.subscribeForReply(transactionID)
	}

	r.entries.Store(transactionID, entry)

	return ch
}

// subscribeForReply subscribes to the transaction's cross-pod reply subject and
// returns the unsubscribe func, or nil if subscription failed (in which case
// the registry degrades to in-process delivery for that entry).
func (r *ReplyRegistry) subscribeForReply(transactionID uuid.UUID) func() error {
	logger := log.DefaultLogger()

	unsub, err := r.transport.SubscribeReply(replySubject(r.streamName, transactionID), func(data []byte) {
		var workflows []*WorkflowDetails
		if len(data) > 0 {
			if err := json.Unmarshal(data, &workflows); err != nil {
				logger.Warn().
					Err(err).
					Str("transaction_id", transactionID.String()).
					Msg("failed to unmarshal cross-pod workflow reply")
				return
			}
		}
		r.deliverLocal(transactionID, workflows)
	})
	if err != nil {
		// In transport mode all delivery (including same-pod) goes through NATS,
		// so a failed subscription means this waiter can only be served by its
		// timeout. The resolver still completes correctly — it just won't carry
		// the workflow IDs. Log loudly so the NATS fault is visible.
		logger.Error().
			Err(err).
			Str("transaction_id", transactionID.String()).
			Msg("failed to subscribe for cross-pod reply; waiter will fall back to timeout")
		return nil
	}
	return unsub
}

// Deliver routes workflow details to the resolver waiting on the given
// transaction ID. With a transport configured it publishes to the transaction's
// core NATS reply subject (the waiting pod receives it via its subscription);
// otherwise it delivers in-process. Returns true on successful publish/delivery.
func (r *ReplyRegistry) Deliver(transactionID uuid.UUID, workflows []*WorkflowDetails) bool {
	if r.transport != nil {
		logger := log.DefaultLogger()
		data, err := json.Marshal(workflows)
		if err != nil {
			logger.Error().
				Err(err).
				Str("transaction_id", transactionID.String()).
				Msg("failed to marshal workflow reply for publish")
			return false
		}
		if err := r.transport.PublishReply(replySubject(r.streamName, transactionID), data); err != nil {
			logger.Warn().
				Err(err).
				Str("transaction_id", transactionID.String()).
				Msg("failed to publish cross-pod workflow reply")
			return false
		}
		return true
	}

	return r.deliverLocal(transactionID, workflows)
}

// deliverLocal hands workflow details to the in-process waiter for the given
// transaction ID. It is the terminal step on the pod that registered the
// waiter: invoked directly in in-memory mode, or from the NATS subscription
// callback in transport mode. Returns false if no live waiter exists.
func (r *ReplyRegistry) deliverLocal(transactionID uuid.UUID, workflows []*WorkflowDetails) bool {
	value, ok := r.entries.LoadAndDelete(transactionID)
	if !ok {
		logger := log.DefaultLogger()
		logger.Debug().
			Str("transaction_id", transactionID.String()).
			Int("workflow_count", len(workflows)).
			Msg("reply delivery failed: no waiter found (timeout expired or not registered)")
		return false
	}

	entry, ok := value.(*replyEntry)
	if !ok {
		return false
	}

	// Prevent double delivery
	if entry.delivered.Swap(true) {
		return false
	}

	if entry.unsub != nil {
		_ = entry.unsub()
	}

	// Send workflows to channel (non-blocking due to buffer)
	entry.ch <- workflows
	close(entry.ch)

	return true
}

// cleanupLoop periodically removes expired entries.
func (r *ReplyRegistry) cleanupLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.removeExpired()
		}
	}
}

// removeExpired removes all entries that have exceeded their timeout.
func (r *ReplyRegistry) removeExpired() {
	now := time.Now()

	r.entries.Range(func(key, value any) bool {
		entry, ok := value.(*replyEntry)
		if !ok {
			r.entries.Delete(key)
			return true
		}

		if now.After(entry.expiresAt) {
			r.entries.Delete(key)

			// Release the cross-pod subscription, if any.
			if entry.unsub != nil {
				_ = entry.unsub()
			}

			// Close channel to unblock waiter with nil result
			if !entry.delivered.Swap(true) {
				close(entry.ch)
			}

			if transactionID, ok := key.(uuid.UUID); ok {
				logger := log.DefaultLogger()
				logger.Debug().
					Str("transaction_id", transactionID.String()).
					Msg("reply entry expired and removed")
			}
		}

		return true
	})
}

// Len returns the number of pending entries. Useful for testing and metrics.
func (r *ReplyRegistry) Len() int {
	count := 0
	r.entries.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// WithReply sets up the context for request/reply and registers for the reply in one call.
// It extracts the transaction ID from the context, marks the context for reply, and registers
// a channel to receive workflow details.
func (r *ReplyRegistry) WithReply(ctx context.Context, timeout time.Duration) (context.Context, <-chan []*WorkflowDetails, error) {
	transactionID, err := TransactionIDFromContext(ctx)
	if err != nil {
		return ctx, nil, err
	}

	ctx = WithExpectReply(ctx, true)
	ch := r.Register(transactionID, timeout)

	return ctx, ch, nil
}
