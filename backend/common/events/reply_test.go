package events_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pyck-ai/pyck/backend/common/events"
)

// Removed unused otel import - use sdktrace directly for testing

// newTestTracer creates a tracer that produces valid trace IDs for testing.
func newTestTracer() *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()),
	)
}

// =============================================================================
// REGISTER AND DELIVER TESTS
// =============================================================================

func TestReplyRegistry_RegisterAndDeliver(t *testing.T) {
	t.Parallel()

	t.Run("successful delivery", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		correlationID := "test-correlation-1"
		timeout := 5 * time.Second

		// Register waiter
		ch := registry.Register(correlationID, timeout)
		require.NotNil(t, ch)
		assert.Equal(t, 1, registry.Len())

		// Deliver workflows
		workflows := []*events.WorkflowDetails{
			{Type: "test-workflow", ID: "wf-1", RunID: "run-1"},
			{Type: "test-workflow", ID: "wf-2", RunID: "run-2"},
		}

		delivered := registry.Deliver(correlationID, workflows)
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
		correlationID := "test-correlation-2"

		ch := registry.Register(correlationID, 5*time.Second)

		delivered := registry.Deliver(correlationID, nil)
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

		delivered := registry.Deliver("non-existent", []*events.WorkflowDetails{
			{Type: "test", ID: "wf-1", RunID: "run-1"},
		})
		assert.False(t, delivered)
	})

	t.Run("double delivery prevented", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)
		correlationID := "test-correlation-3"

		ch := registry.Register(correlationID, 5*time.Second)

		// First delivery succeeds
		workflows := []*events.WorkflowDetails{{Type: "test", ID: "wf-1", RunID: "run-1"}}
		assert.True(t, registry.Deliver(correlationID, workflows))

		// Second delivery fails (entry already removed)
		assert.False(t, registry.Deliver(correlationID, workflows))

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
		ch := registry.Register("expires-soon", 10*time.Millisecond)
		assert.Equal(t, 1, registry.Len())

		// Wait for cleanup to run
		time.Sleep(100 * time.Millisecond)

		// Entry should be removed
		assert.Equal(t, 0, registry.Len())

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
		_ = registry.Register("long-timeout", 10*time.Second)
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
		ch1 := registry.Register("stop-test-1", 10*time.Second)
		ch2 := registry.Register("stop-test-2", 10*time.Second)
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

		// Start goroutines that register
		channels := make([]<-chan []*events.WorkflowDetails, numGoroutines)
		for i := range numGoroutines {
			go func() {
				defer wg.Done()
				correlationID := string(rune('a'+i%26)) + string(rune('0'+i/26))
				channels[i] = registry.Register(correlationID, 5*time.Second)
			}()
		}

		// Wait a bit for registrations
		time.Sleep(10 * time.Millisecond)

		// Start goroutines that deliver
		for i := range numGoroutines {
			go func() {
				defer wg.Done()
				correlationID := string(rune('a'+i%26)) + string(rune('0'+i/26))
				registry.Deliver(correlationID, []*events.WorkflowDetails{
					{Type: "test", ID: correlationID, RunID: "run"},
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

		// Create context with a proper trace that has a valid trace ID
		tp := newTestTracer()
		defer func() { _ = tp.Shutdown(context.Background()) }()

		tracer := tp.Tracer("test")
		ctx, span := tracer.Start(context.Background(), "test-span")
		defer span.End()

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

	t.Run("WithReply fails without trace context", func(t *testing.T) {
		t.Parallel()

		registry := events.NewReplyRegistry(time.Minute)

		// Context without trace
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

	registry.Register("entry-1", time.Minute)
	assert.Equal(t, 1, registry.Len())

	registry.Register("entry-2", time.Minute)
	assert.Equal(t, 2, registry.Len())

	registry.Register("entry-3", time.Minute)
	assert.Equal(t, 3, registry.Len())

	registry.Deliver("entry-2", nil)
	assert.Equal(t, 2, registry.Len())
}
