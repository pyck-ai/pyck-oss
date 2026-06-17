package events_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/txid"
)

// =============================================================================
// REGISTER AND DELIVER TESTS
// =============================================================================

func TestReplyRegistry_RegisterAndDeliver(t *testing.T) {
	t.Parallel()

	t.Run("successful delivery", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		transactionID := uuid.New()
		timeout := 5 * time.Second

		// Register waiter
		ch := registry.Register(transactionID, timeout)
		require.NotNil(t, ch)
		assert.Equal(t, 1, registry.Len())

		// Deliver workflows
		workflows := []*events.WorkflowDetails{
			{Type: "test-workflow", ID: "wf-1", RunID: "run-1"},
			{Type: "test-workflow", ID: "wf-2", RunID: "run-2"},
		}

		delivered := registry.Deliver(transactionID, workflows)
		assert.True(t, delivered)

		// Receive workflows
		select {
		case received := <-ch:
			require.Len(t, received, 2)
			assert.Equal(t, "wf-1", received[0].ID)
			assert.Equal(t, "wf-2", received[1].ID)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for workflows")
		}

		// Channel should be closed
		_, ok := <-ch
		assert.False(t, ok)

		// Entry should be removed
		assert.Equal(t, 0, registry.Len())
	})

	t.Run("deliver empty workflows", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		transactionID := uuid.New()

		ch := registry.Register(transactionID, 5*time.Second)

		delivered := registry.Deliver(transactionID, nil)
		assert.True(t, delivered)

		select {
		case received := <-ch:
			assert.Nil(t, received)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for workflows")
		}
	})

	t.Run("deliver to non-existent entry returns false", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)

		delivered := registry.Deliver(uuid.New(), []*events.WorkflowDetails{
			{Type: "test", ID: "wf-1", RunID: "run-1"},
		})
		assert.False(t, delivered)
	})

	t.Run("double delivery prevented", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		transactionID := uuid.New()

		ch := registry.Register(transactionID, 5*time.Second)

		// First delivery succeeds
		workflows := []*events.WorkflowDetails{{Type: "test", ID: "wf-1", RunID: "run-1"}}
		assert.True(t, registry.Deliver(transactionID, workflows))

		// Second delivery fails (entry already removed)
		assert.False(t, registry.Deliver(transactionID, workflows))

		// Receive first delivery
		select {
		case received := <-ch:
			require.Len(t, received, 1)
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	})
}

// =============================================================================
// EXPIRATION AND CLEANUP TESTS
// =============================================================================

func TestReplyRegistry_Expiration(t *testing.T) {
	t.Parallel()

	t.Run("expired entries are cleaned up", func(t *testing.T) {
		t.Parallel()

		// Use short intervals for faster testing
		registry := events.NewReplyRegistry(50 * time.Millisecond)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry.Start(ctx)
		defer registry.Stop()

		// Register with very short timeout
		ch := registry.Register(uuid.New(), 10*time.Millisecond)
		assert.Equal(t, 1, registry.Len())

		// Poll for cleanup rather than sleeping a fixed amount: the cleanup
		// goroutine ticks every 50ms, but under heavy parallel test load the
		// exact tick timing is unpredictable, so wait for the entry to actually
		// be removed (with generous slack) instead of assuming a tick count.
		require.Eventually(t, func() bool {
			return registry.Len() == 0
		}, 2*time.Second, 5*time.Millisecond, "expired entry should be cleaned up")

		// Channel should be closed (nil received)
		select {
		case received, ok := <-ch:
			if ok {
				assert.Nil(t, received)
			}
		case <-time.After(time.Second):
			t.Fatal("channel should be closed")
		}
	})

	t.Run("non-expired entries are kept", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(50 * time.Millisecond)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry.Start(ctx)
		defer registry.Stop()

		// Register with long timeout
		_ = registry.Register(uuid.New(), 10*time.Second)
		assert.Equal(t, 1, registry.Len())

		// Wait for cleanup to run
		time.Sleep(100 * time.Millisecond)

		// Entry should still exist
		assert.Equal(t, 1, registry.Len())
	})
}

// =============================================================================
// STOP TESTS
// =============================================================================

func TestReplyRegistry_Stop(t *testing.T) {
	t.Parallel()

	t.Run("stop closes pending channels", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry.Start(ctx)

		// Register multiple entries
		ch1 := registry.Register(uuid.New(), 10*time.Second)
		ch2 := registry.Register(uuid.New(), 10*time.Second)
		assert.Equal(t, 2, registry.Len())

		// Stop the registry
		registry.Stop()

		// All channels should be closed
		select {
		case _, ok := <-ch1:
			assert.False(t, ok, "channel 1 should be closed")
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for channel 1")
		}

		select {
		case _, ok := <-ch2:
			assert.False(t, ok, "channel 2 should be closed")
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for channel 2")
		}

		// All entries should be removed
		assert.Equal(t, 0, registry.Len())
	})

	t.Run("stop is idempotent", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry.Start(ctx)

		// Multiple stops should not panic
		registry.Stop()
		registry.Stop()
		registry.Stop()
	})

	t.Run("context cancellation stops cleanup loop", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(10 * time.Millisecond)
		ctx, cancel := context.WithCancel(context.Background())

		registry.Start(ctx)

		// Cancel context
		cancel()

		// Give time for goroutine to exit
		time.Sleep(50 * time.Millisecond)

		// Should be able to stop without hanging
		done := make(chan struct{})
		go func() {
			registry.Stop()
			close(done)
		}()

		select {
		case <-done:
			// Success
		case <-time.After(time.Second):
			t.Fatal("Stop() hung after context cancellation")
		}
	})
}

// =============================================================================
// CONCURRENT ACCESS TESTS
// =============================================================================

func TestReplyRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	t.Run("concurrent register and deliver", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry.Start(ctx)
		defer registry.Stop()

		const numGoroutines = 100
		var wg sync.WaitGroup
		wg.Add(numGoroutines * 2) // Half register, half deliver

		ids := make([]uuid.UUID, numGoroutines)
		for i := range ids {
			ids[i] = uuid.New()
		}

		// Start goroutines that register
		channels := make([]<-chan []*events.WorkflowDetails, numGoroutines)
		for i := range numGoroutines {
			go func() {
				defer wg.Done()
				channels[i] = registry.Register(ids[i], 5*time.Second)
			}()
		}

		// Wait a bit for registrations
		time.Sleep(10 * time.Millisecond)

		// Start goroutines that deliver
		for i := range numGoroutines {
			go func() {
				defer wg.Done()
				registry.Deliver(ids[i], []*events.WorkflowDetails{
					{Type: "test", ID: ids[i].String(), RunID: "run"},
				})
			}()
		}

		wg.Wait()

		// All entries should eventually be removed (delivered or expired)
		time.Sleep(50 * time.Millisecond)
		// Note: Some may remain if deliver happened before register completed
	})
}

// =============================================================================
// WITH REPLY TESTS
// =============================================================================

func TestReplyRegistry_WithReply(t *testing.T) {
	t.Parallel()

	t.Run("WithReply sets up context and registers", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)

		// Forge a tx context (the gqltx middleware would install this in
		// production at BeginTx).
		ctx := txid.With(context.Background(), txid.New())

		// Call WithReply
		newCtx, ch, err := registry.WithReply(ctx, 5*time.Second)
		require.NoError(t, err)
		require.NotNil(t, ch)
		require.NotNil(t, newCtx)

		// Context should have ExpectReply set
		assert.True(t, events.ExpectsReply(newCtx))

		// Entry should be registered
		assert.Equal(t, 1, registry.Len())
	})

	t.Run("WithReply fails without transaction context", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)

		// Context without tx (gqltx middleware did not run)
		ctx := context.Background()

		_, ch, err := registry.WithReply(ctx, 5*time.Second)
		require.Error(t, err)
		assert.Nil(t, ch)
		assert.Equal(t, 0, registry.Len())
	})
}

// =============================================================================
// LEN TESTS
// =============================================================================

func TestReplyRegistry_Len(t *testing.T) {
	t.Parallel()

	registry := events.NewReplyRegistry(time.Minute)

	assert.Equal(t, 0, registry.Len())

	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()

	registry.Register(id1, time.Minute)
	assert.Equal(t, 1, registry.Len())

	registry.Register(id2, time.Minute)
	assert.Equal(t, 2, registry.Len())

	registry.Register(id3, time.Minute)
	assert.Equal(t, 3, registry.Len())

	registry.Deliver(id2, nil)
	assert.Equal(t, 2, registry.Len())
}

// =============================================================================
// CROSS-POD DELIVERY TESTS (transport mode)
// =============================================================================

// fakeBus simulates a shared NATS bus across pods: every PublishReply fans out
// to all handlers subscribed to that subject, regardless of which registry
// instance subscribed. This models two pyck-inventory replicas sharing one NATS.
// It also tracks live subscriptions so tests can assert there are no leaks.
type fakeBus struct {
	mu   sync.Mutex
	subs map[string]map[int]func([]byte)
	seq  int
}

func newFakeBus() *fakeBus {
	return &fakeBus{subs: make(map[string]map[int]func([]byte))}
}

func (b *fakeBus) PublishReply(subject string, data []byte) error {
	b.mu.Lock()
	handlers := make([]func([]byte), 0, len(b.subs[subject]))
	for _, h := range b.subs[subject] {
		handlers = append(handlers, h)
	}
	b.mu.Unlock()
	for _, h := range handlers {
		h(data)
	}
	return nil
}

func (b *fakeBus) SubscribeReply(subject string, handler func([]byte)) (func() error, error) {
	b.mu.Lock()
	if b.subs[subject] == nil {
		b.subs[subject] = make(map[int]func([]byte))
	}
	id := b.seq
	b.seq++
	b.subs[subject][id] = handler
	b.mu.Unlock()

	return func() error {
		b.mu.Lock()
		delete(b.subs[subject], id)
		if len(b.subs[subject]) == 0 {
			delete(b.subs, subject)
		}
		b.mu.Unlock()
		return nil
	}, nil
}

// activeSubs returns the number of live subscriptions across all subjects.
func (b *fakeBus) activeSubs() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.subs {
		n += len(m)
	}
	return n
}

// failingTransport models NATS being unavailable: subscriptions always fail.
type failingTransport struct{}

func (failingTransport) PublishReply(string, []byte) error { return nil }

func (failingTransport) SubscribeReply(string, func([]byte)) (func() error, error) {
	return nil, errReplyTransportDown
}

var errReplyTransportDown = errors.New("nats unavailable")

// TestReplyRegistry_InMemoryMissesCrossPod documents the bug this fix targets:
// with the old in-memory-only registry, two pods have two separate maps, so a
// reply delivered on the pod that did NOT register the waiter is lost and the
// waiter only unblocks via timeout. This is the "5-second floor" from #1124.
func TestReplyRegistry_InMemoryMissesCrossPod(t *testing.T) {
	t.Parallel()

	// No transport => the old behavior: each registry is an isolated map.
	podA := events.NewReplyRegistry(time.Minute)
	podB := events.NewReplyRegistry(time.Minute)

	transactionID := uuid.New()
	workflows := []*events.WorkflowDetails{{ID: "wf-1"}}

	// Resolver waits on pod A.
	ch := podA.Register(transactionID, time.Minute)

	// Pod B's outbox handler delivers — but the waiter lives in pod A's map.
	delivered := podB.Deliver(transactionID, workflows)
	assert.False(t, delivered, "pod B has no waiter: the reply is lost cross-pod")

	// Pod A's waiter never receives it; it would sit until its timeout fires.
	select {
	case <-ch:
		t.Fatal("unexpected delivery: in-memory mode cannot cross pods")
	case <-time.After(100 * time.Millisecond):
		// Expected: nothing arrives. This is exactly the production stall.
	}
}

func TestReplyRegistry_CrossPodDelivery(t *testing.T) {
	t.Parallel()

	t.Run("reply delivered to waiter on a different pod", func(t *testing.T) {
		t.Parallel()

		bus := newFakeBus()
		// Two registries sharing one bus = two pods sharing one NATS.
		podA := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")
		podB := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")

		transactionID := uuid.New()
		workflows := []*events.WorkflowDetails{{Type: "t", ID: "wf-1", RunID: "run-1"}}

		// Resolver runs on pod A and waits.
		ch := podA.Register(transactionID, 5*time.Second)

		// Pod B's outbox handler processed the row and delivers.
		require.True(t, podB.Deliver(transactionID, workflows))

		select {
		case got := <-ch:
			require.Len(t, got, 1)
			assert.Equal(t, "wf-1", got[0].ID)
		case <-time.After(time.Second):
			t.Fatal("waiter on pod A did not receive the reply published by pod B")
		}

		// Waiter consumed; the subscription must be released.
		assert.Equal(t, 0, podA.Len())
	})

	t.Run("subject is namespaced per transaction", func(t *testing.T) {
		t.Parallel()

		bus := newFakeBus()
		podA := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")
		podB := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")

		waitID, otherID := uuid.New(), uuid.New()
		ch := podA.Register(waitID, 5*time.Second)

		// Deliver for a different transaction must not wake this waiter.
		podB.Deliver(otherID, []*events.WorkflowDetails{{ID: "wf-other"}})

		select {
		case <-ch:
			t.Fatal("waiter woke for the wrong transaction_id")
		case <-time.After(100 * time.Millisecond):
		}

		// Correct transaction reaches it.
		require.True(t, podB.Deliver(waitID, []*events.WorkflowDetails{{ID: "wf-mine"}}))
		got := <-ch
		require.Len(t, got, 1)
		assert.Equal(t, "wf-mine", got[0].ID)
	})
}

// TestReplyRegistry_TransportIdempotentDelivery proves that the N-outbox-rows-
// per-transaction case (each row triggers a Deliver) wakes the waiter exactly
// once and leaves no dangling subscription.
func TestReplyRegistry_TransportIdempotentDelivery(t *testing.T) {
	t.Parallel()

	bus := newFakeBus()
	podA := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")
	podB := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")

	transactionID := uuid.New()
	ch := podA.Register(transactionID, time.Minute)
	require.Equal(t, 1, bus.activeSubs())

	// One transaction emitted three outbox rows -> three deliveries.
	for range 3 {
		require.True(t, podB.Deliver(transactionID, []*events.WorkflowDetails{{ID: "wf-1"}}))
	}

	// Exactly one value, then the channel is closed (no double delivery).
	got := <-ch
	require.Len(t, got, 1)
	_, open := <-ch
	assert.False(t, open, "channel must be closed after the single delivery")

	assert.Equal(t, 0, podA.Len(), "entry removed after delivery")
	assert.Equal(t, 0, bus.activeSubs(), "subscription released after delivery")
}

// TestReplyRegistry_TransportReleasesOnTimeout proves a waiter that never
// receives a reply is unblocked by its timeout AND its NATS subscription is
// released by cleanup — no goroutine/subscription leak.
func TestReplyRegistry_TransportReleasesOnTimeout(t *testing.T) {
	t.Parallel()

	bus := newFakeBus()
	registry := events.NewReplyRegistryWithTransport(20*time.Millisecond, bus, "pyck")
	registry.Start(context.Background())
	defer registry.Stop()

	ch := registry.Register(uuid.New(), 10*time.Millisecond)
	require.Equal(t, 1, bus.activeSubs())

	// Nothing is delivered: the waiter must be released by its timeout.
	select {
	case _, open := <-ch:
		assert.False(t, open, "timed-out waiter receives a closed channel")
	case <-time.After(time.Second):
		t.Fatal("waiter was not released after its timeout")
	}

	require.Eventually(t, func() bool { return bus.activeSubs() == 0 },
		time.Second, 10*time.Millisecond, "cleanup must release the subscription")
}

// TestReplyRegistry_TransportSubscribeFailureDegrades proves that if NATS is
// unavailable at Register time, the resolver does not panic or hang forever —
// it falls back to the timeout path and still completes.
func TestReplyRegistry_TransportSubscribeFailureDegrades(t *testing.T) {
	t.Parallel()

	registry := events.NewReplyRegistryWithTransport(20*time.Millisecond, failingTransport{}, "pyck")
	registry.Start(context.Background())
	defer registry.Stop()

	ch := registry.Register(uuid.New(), 10*time.Millisecond)

	select {
	case _, open := <-ch:
		assert.False(t, open, "waiter still released via timeout when subscribe failed")
	case <-time.After(time.Second):
		t.Fatal("waiter hung after subscribe failure")
	}
}

// TestReplyRegistry_TransportConcurrentRouting proves correctness under load:
// many resolvers wait on pod A while pod B delivers concurrently, and every
// reply lands on its own transaction's waiter with no cross-talk or leak.
// Run with -race to catch data races.
func TestReplyRegistry_TransportConcurrentRouting(t *testing.T) {
	t.Parallel()

	bus := newFakeBus()
	podA := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")
	podB := events.NewReplyRegistryWithTransport(time.Minute, bus, "pyck")

	const n = 50
	ids := make([]uuid.UUID, n)
	chans := make([]<-chan []*events.WorkflowDetails, n)
	for i := range n {
		ids[i] = uuid.New()
		chans[i] = podA.Register(ids[i], 5*time.Second)
	}

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			podB.Deliver(ids[i], []*events.WorkflowDetails{{ID: ids[i].String()}})
		}(i)
	}
	wg.Wait()

	for i := range n {
		select {
		case got := <-chans[i]:
			require.Len(t, got, 1)
			assert.Equal(t, ids[i].String(), got[0].ID, "reply routed to the wrong waiter")
		case <-time.After(time.Second):
			t.Fatalf("waiter %d never received its reply", i)
		}
	}

	assert.Equal(t, 0, bus.activeSubs(), "all subscriptions released")
}
