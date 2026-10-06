package event_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"

	"github.com/pyck-ai/pyck/backend/temporal/event"
)

var errPublish = errors.New("nats unavailable")

// fakePublisher fails the first `failures` SendTemporalWorkflowEvent calls
// (or blocks until the attempt context ends when block is set) and records
// every attempt. The embedded nil Publisher panics on any other method.
type fakePublisher struct {
	events.Publisher

	mu       sync.Mutex
	failures int
	failErr  error // returned by failing attempts; errPublish when nil
	block    bool
	attempts []*events.TemporalWorkflowStateChangeMessage
	ok       chan struct{}
}

func newFakePublisher(failures int) *fakePublisher {
	return &fakePublisher{failures: failures, ok: make(chan struct{}, 100)}
}

func (f *fakePublisher) SendTemporalWorkflowEvent(ctx context.Context, msg *events.TemporalWorkflowStateChangeMessage) error {
	f.mu.Lock()
	f.attempts = append(f.attempts, msg)
	fail := len(f.attempts) <= f.failures
	f.mu.Unlock()

	if fail {
		if f.block {
			<-ctx.Done()

			return ctx.Err()
		}

		if f.failErr != nil {
			return f.failErr
		}

		return errPublish
	}

	f.ok <- struct{}{}

	return nil
}

func (f *fakePublisher) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.attempts)
}

func fastRetryConfig() event.EventConfig {
	return event.EventConfig{
		EventWorkerPoolSize:            1,
		EventWorkerQueueSize:           10,
		EventWorkerPublishTimeout:      20 * time.Millisecond,
		EventWorkerRetryInitialBackoff: time.Millisecond,
		EventWorkerRetryMaxBackoff:     5 * time.Millisecond,
		EventWorkerShutdownGrace:       50 * time.Millisecond,
	}
}

func stateChange(status string) *events.TemporalWorkflowStateChangeMessage {
	return &events.TemporalWorkflowStateChangeMessage{
		Namespace:  "tenant-a",
		WorkflowID: "wf-1",
		RunID:      "run-1",
		Status:     status,
	}
}

func waitOK(t *testing.T, f *fakePublisher) {
	t.Helper()

	select {
	case <-f.ok:
	case <-time.After(2 * time.Second):
		t.Fatalf("event was never published after %d attempts", f.attemptCount())
	}
}

func TestHandler_RetriesFailedPublishUntilSuccess(t *testing.T) {
	t.Parallel()

	pub := newFakePublisher(3)
	h := event.NewHandler(t.Context(), pub, fastRetryConfig())
	defer h.Close()

	h.Notify(t.Context(), stateChange("RUNNING"))
	waitOK(t, pub)

	require.Equal(t, 4, pub.attemptCount(), "3 failures, then 1 success")

	// Every attempt carries the same message, so the router, which derives
	// its Temporal request IDs from the message, treats a stored repeat as
	// one change.
	pub.mu.Lock()
	defer pub.mu.Unlock()

	for _, m := range pub.attempts {
		assert.Equal(t, pub.attempts[0], m)
	}
}

func TestHandler_RetriesTimedOutPublish(t *testing.T) {
	t.Parallel()

	pub := newFakePublisher(2)
	pub.block = true
	h := event.NewHandler(t.Context(), pub, fastRetryConfig())
	defer h.Close()

	h.Notify(t.Context(), stateChange("RUNNING"))
	waitOK(t, pub)

	assert.Equal(t, 3, pub.attemptCount(), "2 timeouts, then 1 success")
}

func TestHandler_NotifyDoesNotBlockWhileRetrying(t *testing.T) {
	t.Parallel()

	cfg := fastRetryConfig()
	cfg.EventWorkerPoolSize = 2

	pub := newFakePublisher(1_000_000) // never succeeds

	h := event.NewHandler(t.Context(), pub, cfg)
	defer h.Close()

	h.Notify(t.Context(), stateChange("RUNNING"))

	// A second publisher view that succeeds is not possible with one fake, so
	// assert the retrying worker keeps attempting while Notify stays non-blocking.
	done := make(chan struct{})
	go func() {
		for range 5 {
			h.Notify(t.Context(), stateChange("COMPLETED"))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked while a worker was retrying")
	}
}

func TestHandler_CloseWithPendingEventsReturnsPromptly(t *testing.T) {
	t.Parallel()

	pub := newFakePublisher(1_000_000)
	h := event.NewHandler(t.Context(), pub, fastRetryConfig())

	for range 4 {
		h.Notify(t.Context(), stateChange("RUNNING"))
	}

	closed := make(chan struct{})
	go func() {
		h.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return while events were still being retried")
	}
}

func TestHandler_QueueFullDropsAndCounts(t *testing.T) {
	t.Parallel()

	cfg := fastRetryConfig()
	cfg.EventWorkerQueueSize = 1

	pub := newFakePublisher(1_000_000)
	pub.block = true // the single worker stays busy, so the queue fills

	before := testutil.ToFloat64(event.DroppedCounter("queue_full"))

	h := event.NewHandler(t.Context(), pub, cfg)
	defer h.Close()

	for range 6 {
		h.Notify(t.Context(), stateChange("RUNNING"))
	}

	dropped := testutil.ToFloat64(event.DroppedCounter("queue_full")) - before
	assert.GreaterOrEqual(t, dropped, float64(4), "6 notifies, at most 1 in flight + 1 queued")
}

//nolint:paralleltest // asserts an exact delta of the process-global "shutdown" dropped counter, which TestHandler_CloseWithPendingEventsReturnsPromptly also moves
func TestHandler_ShutdownDropsPendingAndCounts(t *testing.T) {
	cfg := fastRetryConfig()
	cfg.EventWorkerShutdownGrace = 20 * time.Millisecond

	pub := newFakePublisher(1_000_000) // always fails, worker keeps retrying

	before := testutil.ToFloat64(event.DroppedCounter("shutdown"))

	h := event.NewHandler(t.Context(), pub, cfg)
	for range 4 {
		h.Notify(t.Context(), stateChange("RUNNING"))
	}

	h.Close()

	dropped := testutil.ToFloat64(event.DroppedCounter("shutdown")) - before
	assert.InDelta(t, float64(4), dropped, 0, "1 in flight + 3 queued")
}

// syncBuffer is a bytes.Buffer safe for the handler worker to write while the
// test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestHandler_DuplicateInProcessRetriesWithoutWarning(t *testing.T) {
	t.Parallel()

	pub := newFakePublisher(1)
	pub.failErr = fmt.Errorf("nats: %w", &jetstream.APIError{
		Code: 409, ErrorCode: 10158, Description: "duplicate message id is in process",
	})

	var out syncBuffer

	ctx := log.Context(t.Context(), zerolog.New(&out).Level(zerolog.DebugLevel))

	h := event.NewHandler(t.Context(), pub, fastRetryConfig())
	defer h.Close()

	h.Notify(ctx, stateChange("RUNNING"))
	waitOK(t, pub)

	assert.Equal(t, 2, pub.attemptCount(), "1 duplicate-in-process failure, then 1 success")

	logs := out.String()
	assert.Contains(t, logs, `"level":"debug"`)
	assert.Contains(t, logs, "failed to publish workflow event, retrying")
	assert.NotContains(t, logs, `"level":"warn"`)
	assert.NotContains(t, logs, `"level":"error"`, "no error-level line")
}

func TestHandler_OtherPublishErrorStillWarns(t *testing.T) {
	t.Parallel()

	pub := newFakePublisher(1)

	var out syncBuffer

	ctx := log.Context(t.Context(), zerolog.New(&out).Level(zerolog.DebugLevel))

	h := event.NewHandler(t.Context(), pub, fastRetryConfig())
	defer h.Close()

	h.Notify(ctx, stateChange("RUNNING"))
	waitOK(t, pub)

	assert.Contains(t, out.String(), `"level":"warn"`)
}
