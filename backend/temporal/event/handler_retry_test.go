package event_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/test/natstest"

	"github.com/pyck-ai/pyck/backend/temporal/event"
)

var errPublish = errors.New("nats unavailable")

const noStreamDropped = "no stream takes the workflow event - event dropped"

// fakePublisher fails the first `failures` SendTemporalWorkflowEvent calls
// (or blocks until the attempt context ends when block is set) and records
// every attempt. The embedded nil Publisher panics on any other method.
type fakePublisher struct {
	events.Publisher

	mu       sync.Mutex
	failures int
	failErr  error   // returned by failing attempts; errPublish when nil
	failErrs []error // when set, failing attempt n returns failErrs[n % len]
	block    bool
	noStream string // a namespace no stream takes, whatever failures says
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
	noStream := f.noStream != "" && msg.Namespace == f.noStream
	f.mu.Unlock()

	if noStream {
		return jetstream.ErrNoStreamResponse
	}

	if fail {
		if f.block {
			<-ctx.Done()

			return ctx.Err()
		}

		if len(f.failErrs) > 0 {
			f.mu.Lock()
			n := len(f.attempts) - 1
			f.mu.Unlock()

			return f.failErrs[n%len(f.failErrs)]
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

func TestHandler_NoStreamDropsAfterGiveUp(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cfg := fastRetryConfig()
		cfg.EventWorkerNoStreamGiveUp = time.Second

		pub := newFakePublisher(1_000_000)
		pub.failErr = fmt.Errorf("publish: %w", jetstream.ErrNoStreamResponse)

		// Other parallel tests drop no-stream events too, so the counter only
		// has to have moved; the log line pins this handler's drop.
		before := testutil.ToFloat64(event.DroppedCounter("no_stream"))

		var out syncBuffer

		ctx := log.Context(t.Context(), zerolog.New(&out).Level(zerolog.WarnLevel))

		h := event.NewHandler(t.Context(), pub, cfg)
		defer h.Close()

		h.Notify(ctx, stateChange("RUNNING"))

		time.Sleep(2 * cfg.EventWorkerNoStreamGiveUp)
		synctest.Wait()

		require.Contains(t, out.String(), noStreamDropped, "the event was never dropped")
		assert.Greater(t, testutil.ToFloat64(event.DroppedCounter("no_stream")), before, "the drop is counted")

		// The worker is free again: a later event is attempted.
		attempts := pub.attemptCount()
		h.Notify(t.Context(), stateChange("COMPLETED"))
		synctest.Wait()

		assert.Greater(t, pub.attemptCount(), attempts)
	})
}

func TestHandler_NoStreamDropsDespiteInterleavedTimeouts(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cfg := fastRetryConfig()
		cfg.EventWorkerNoStreamGiveUp = time.Second

		// A timed-out attempt says nothing about the stream: it must not
		// restart the give-up clock.
		pub := newFakePublisher(1_000_000)
		pub.failErrs = []error{
			jetstream.ErrNoStreamResponse,
			fmt.Errorf("publish attempt timed out: %w", context.DeadlineExceeded),
			jetstream.ErrNoStreamResponse,
			errPublish,
		}

		var out syncBuffer

		ctx := log.Context(t.Context(), zerolog.New(&out).Level(zerolog.WarnLevel))

		h := event.NewHandler(t.Context(), pub, cfg)
		defer h.Close()

		h.Notify(ctx, stateChange("RUNNING"))

		time.Sleep(2 * cfg.EventWorkerNoStreamGiveUp)
		synctest.Wait()

		assert.Contains(t, out.String(), noStreamDropped, "the event was never dropped")
	})
}

// TestHandler_NoStreamDropsThroughTheRealPublisher publishes through
// events.EventPublisher to a server without streams, at the default attempt
// timeout: the no-stream error must come back within it, or the handler only
// ever sees timeouts and never gives up.
func TestHandler_NoStreamDropsThroughTheRealPublisher(t *testing.T) {
	t.Parallel()

	srv := natstest.Start(t, nil)
	nc := natstest.Connect(t, srv, natstest.ServiceUser, nil)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	cfg := fastRetryConfig()
	cfg.EventWorkerPublishTimeout = event.DefaultPublishTimeout
	cfg.EventWorkerNoStreamGiveUp = 300 * time.Millisecond

	var out syncBuffer

	ctx := log.Context(t.Context(), zerolog.New(&out).Level(zerolog.WarnLevel))

	h := event.NewHandler(t.Context(), events.NewEventPublisher(js, nc, "pyck"), cfg)
	defer h.Close()

	h.Notify(ctx, stateChange("RUNNING"))

	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), noStreamDropped)
	}, 5*time.Second, 10*time.Millisecond, "the event was never dropped")
	assert.NotContains(t, out.String(), "publish attempt timed out", "the no-stream error must beat the attempt timeout")
}

func TestHandler_NoStreamRetriesUntilTheStreamAppears(t *testing.T) {
	t.Parallel()

	cfg := fastRetryConfig()
	cfg.EventWorkerNoStreamGiveUp = time.Minute

	pub := newFakePublisher(5)
	pub.failErr = jetstream.ErrNoStreamResponse

	h := event.NewHandler(t.Context(), pub, cfg)
	defer h.Close()

	h.Notify(t.Context(), stateChange("RUNNING"))
	waitOK(t, pub)

	assert.Equal(t, 6, pub.attemptCount(), "5 attempts without a stream, then 1 success")
}

// Events of a namespace without a stream wait off the workers, so they cannot
// starve the other namespaces' events.
func TestHandler_NoStreamDoesNotHoldTheWorkers(t *testing.T) {
	t.Parallel()

	cfg := fastRetryConfig()
	cfg.EventWorkerNoStreamGiveUp = time.Minute
	cfg.EventWorkerRetryMaxBackoff = 100 * time.Millisecond

	pub := newFakePublisher(0)
	pub.noStream = "gone"

	h := event.NewHandler(t.Context(), pub, cfg)
	defer h.Close()

	for range 5 {
		msg := stateChange("RUNNING")
		msg.Namespace = "gone"
		h.Notify(t.Context(), msg)
	}

	h.Notify(t.Context(), stateChange("RUNNING"))
	waitOK(t, pub)
}

func TestHandler_CloseDropsParkedEvents(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cfg := fastRetryConfig()
		cfg.EventWorkerNoStreamGiveUp = time.Minute
		cfg.EventWorkerRetryInitialBackoff = time.Minute
		cfg.EventWorkerRetryMaxBackoff = time.Minute

		pub := newFakePublisher(0)
		pub.noStream = "tenant-a"

		var out syncBuffer

		ctx := log.Context(t.Context(), zerolog.New(&out).Level(zerolog.WarnLevel))

		h := event.NewHandler(t.Context(), pub, cfg)
		h.Notify(ctx, stateChange("RUNNING"))
		synctest.Wait()

		require.Equal(t, 1, pub.attemptCount())

		start := time.Now()
		h.Close()

		assert.Zero(t, time.Since(start), "Close waited for a parked event")
		assert.Contains(t, out.String(), "pending workflow event dropped")
	})
}

// Past EventWorkerQueueSize parked events a new one is dropped as overflow,
// which an operator must tell apart from a stream that never came.
func TestHandler_ParkedFullDropsAsOverflow(t *testing.T) {
	t.Parallel()

	cfg := fastRetryConfig()
	cfg.EventWorkerQueueSize = 2
	cfg.EventWorkerNoStreamGiveUp = time.Minute
	cfg.EventWorkerRetryInitialBackoff = time.Minute
	cfg.EventWorkerRetryMaxBackoff = time.Minute

	pub := newFakePublisher(0)
	pub.noStream = "gone"

	var out syncBuffer

	ctx := log.Context(t.Context(), zerolog.New(&out).Level(zerolog.WarnLevel))

	beforeFull := testutil.ToFloat64(event.DroppedCounter("parked_full"))

	h := event.NewHandler(t.Context(), pub, cfg)
	defer h.Close()

	for i := range 3 {
		msg := stateChange("RUNNING")
		msg.Namespace = "gone"
		msg.WorkflowID = fmt.Sprintf("wf-%d", i)
		h.Notify(ctx, msg)
		require.Eventually(t, func() bool { return pub.attemptCount() == i+1 }, 2*time.Second, time.Millisecond)
	}

	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "too many workflow events wait for a stream")
	}, 2*time.Second, 5*time.Millisecond, "the third event was not dropped as overflow")
	assert.InDelta(t, 1.0, testutil.ToFloat64(event.DroppedCounter("parked_full"))-beforeFull, 0)
	assert.NotContains(t, out.String(), noStreamDropped, "an overflow is not a give-up")
}
