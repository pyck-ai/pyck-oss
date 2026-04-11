package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

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
// Thread Safety:
// ReplyRegistry is safe for concurrent use. Multiple goroutines can call
// Register and Deliver simultaneously.
type ReplyRegistry struct {
	entries sync.Map // map[string]*replyEntry

	cleanupInterval time.Duration
	stopCh          chan struct{}
	stopped         atomic.Bool
	wg              sync.WaitGroup
}

// NewReplyRegistry creates a new ReplyRegistry with the given cleanup interval.
// Call Start() to begin the background cleanup goroutine.
func NewReplyRegistry(cleanupInterval time.Duration) *ReplyRegistry {
	return &ReplyRegistry{
		cleanupInterval: cleanupInterval,
		stopCh:          make(chan struct{}),
	}
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
			if !entry.delivered.Swap(true) {
				close(entry.ch)
			}
		}
		r.entries.Delete(key)
		return true
	})
}

// Register creates a pending reply entry for the given correlation ID.
// Returns a channel that will receive the workflow details when Deliver is called,
// or be closed when the timeout expires or Stop is called.
func (r *ReplyRegistry) Register(correlationID string, timeout time.Duration) <-chan []*WorkflowDetails {
	ch := make(chan []*WorkflowDetails, 1)

	entry := &replyEntry{
		ch:        ch,
		expiresAt: time.Now().Add(timeout),
	}

	r.entries.Store(correlationID, entry)

	return ch
}

// Deliver sends workflow details to the waiting resolver for the given correlation ID.
// Returns true if the delivery was successful, false if no waiter was found
// (timeout already expired or never registered).
func (r *ReplyRegistry) Deliver(correlationID string, workflows []*WorkflowDetails) bool {
	value, ok := r.entries.LoadAndDelete(correlationID)
	if !ok {
		logger := log.DefaultLogger()
		logger.Warn().
			Str("correlation_id", correlationID).
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

			// Close channel to unblock waiter with nil result
			if !entry.delivered.Swap(true) {
				close(entry.ch)
			}

			if correlationID, ok := key.(string); ok {
				logger := log.DefaultLogger()
				logger.Debug().
					Str("correlation_id", correlationID).
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
// It extracts the correlation ID from the context, marks the context for reply, and registers
// a channel to receive workflow details.
func (r *ReplyRegistry) WithReply(ctx context.Context, timeout time.Duration) (context.Context, <-chan []*WorkflowDetails, error) {
	correlationID, err := CorrelationIDFromContext(ctx)
	if err != nil {
		return ctx, nil, err
	}

	ctx = WithExpectReply(ctx, true)
	ch := r.Register(correlationID, timeout)

	return ctx, ch, nil
}
