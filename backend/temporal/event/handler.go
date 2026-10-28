package event

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/temporal/config"
)

// Reasons an event is dropped without being published.
const (
	dropReasonQueueFull  = "queue_full"
	dropReasonShutdown   = "shutdown"
	dropReasonNoStream   = "no_stream"
	dropReasonParkedFull = "parked_full" // EventWorkerQueueSize events already wait for a stream
)

var droppedEvents = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "temporal_state_change_events_dropped_total",
		Help: "Workflow state-change events dropped before reaching NATS, by reason",
	},
	[]string{"reason"},
)

const (
	// DefaultWorkerPoolSize is the default number of concurrent workers
	// for publishing events to NATS.
	DefaultWorkerPoolSize = 10

	// DefaultQueueSize is the default buffer size for the event queue.
	// Events are dropped with a warning if the queue is full.
	DefaultQueueSize = 1000

	// DefaultPublishTimeout is the default timeout for a single publish
	// attempt to NATS. An attempt that exceeds it is retried after a backoff.
	DefaultPublishTimeout = 100 * time.Millisecond

	// DefaultRetryInitialBackoff is the wait after the first failed attempt.
	// It doubles per attempt up to DefaultRetryMaxBackoff.
	DefaultRetryInitialBackoff = 100 * time.Millisecond

	// DefaultRetryMaxBackoff caps the wait between publish attempts.
	DefaultRetryMaxBackoff = 5 * time.Second

	// DefaultShutdownGrace is how long Close lets workers drain the queue
	// before it abandons in-flight retries and drops whatever is left.
	DefaultShutdownGrace = 5 * time.Second

	// DefaultNoStreamGiveUp is how long an event no stream captures is retried.
	DefaultNoStreamGiveUp = 5 * time.Minute
)

// EventConfig is an alias for config.EventConfig to avoid breaking existing code.
type EventConfig = config.EventWorkerConfig

// Handler publishes Temporal workflow events to NATS using a worker pool
// to avoid blocking Temporal's critical path.
//
// A failed or timed-out publish is retried with exponential backoff until it
// succeeds or the handler shuts down. A retry can store a change twice (for
// example when the first attempt timed out after the server stored it). That is
// safe: the publish carries a JetStream message ID derived from the change, so
// JetStream drops a repeat within its duplicate window, and the workflow signal
// router derives its Temporal request IDs from the change itself, so Temporal
// drops a repeat beyond it. A worker that is retrying one event stays on it;
// the other workers keep draining the queue.
//
// An event no stream captures (a namespace that outlived its tenant's stream,
// or a tenant whose stream is not there yet) waits for its next attempt off the
// worker and is queued again, so such events cannot take the whole pool. It is
// retried for EventWorkerNoStreamGiveUp, and at most EventWorkerQueueSize of
// them wait at once.
//
// Events are only ever dropped, never blocked on, when the queue is full, when
// no stream takes them, or when they are still pending once Close's grace
// period has elapsed. Each drop is logged and counted in
// temporal_state_change_events_dropped_total.
type Handler struct {
	publisher events.Publisher
	config    EventConfig

	eventChan chan *publishRequest
	wg        sync.WaitGroup
	cancel    context.CancelFunc

	// stateMu guards closed and the close of eventChan, so Notify never sends
	// on a closed channel. Notify only holds it for a non-blocking send.
	stateMu sync.RWMutex
	closed  bool
	closeMu sync.Mutex

	// parked holds the events waiting off the workers for their next attempt,
	// with the timer that queues them again. Whoever removes an entry owns it.
	parkedMu     sync.Mutex
	parked       map[*publishRequest]*time.Timer
	parkedClosed bool
}

type publishRequest struct {
	// logger is captured at Notify() time to avoid using
	// a cancelled context in the worker goroutine.
	logger log.Logger
	event  *events.TemporalWorkflowStateChangeMessage

	// Retry state, kept across a parked event's attempts.
	attempt       int
	backoff       time.Duration
	noStreamSince time.Time // first of the consecutive no-stream failures
}

// NewHandler creates a new event handler with configuration from EventConfig.
// The handler spawns config.EventWorkerPoolSize workers that process events concurrently,
// with a buffered queue of config.EventQueueSize.
//
// Configuration can be set via environment variables:
//
//   - PYCK_EVENT_WORKER_POOL_SIZE (default: 10)
//
//   - PYCK_EVENT_QUEUE_SIZE (default: 1000)
//
//   - PYCK_EVENT_PUBLISH_TIMEOUT (default: 100ms)
//
//   - PYCK_EVENT_WORKER_RETRY_INITIAL_BACKOFF (default: 100ms)
//
//   - PYCK_EVENT_WORKER_RETRY_MAX_BACKOFF (default: 5s)
//
//   - PYCK_EVENT_WORKER_SHUTDOWN_GRACE (default: 5s)
//
//   - PYCK_EVENT_WORKER_NO_STREAM_GIVE_UP (default: 5m)
//
// config.EventWorkerPublishTimeout bounds how long the worker waits for one
// publish attempt before retrying; it does not cancel the publish itself, which
// keeps running under the JetStream client's default timeout. A failed attempt
// is retried with exponential backoff. Non-positive backoff and grace values
// fall back to the defaults.
//
// Always call Close() during application shutdown to ensure graceful cleanup.
func NewHandler(ctx context.Context, publisher events.Publisher, cfg EventConfig) *Handler {
	if cfg.EventWorkerRetryInitialBackoff <= 0 {
		cfg.EventWorkerRetryInitialBackoff = DefaultRetryInitialBackoff
	}

	if cfg.EventWorkerRetryMaxBackoff < cfg.EventWorkerRetryInitialBackoff {
		cfg.EventWorkerRetryMaxBackoff = max(DefaultRetryMaxBackoff, cfg.EventWorkerRetryInitialBackoff)
	}

	if cfg.EventWorkerShutdownGrace <= 0 {
		cfg.EventWorkerShutdownGrace = DefaultShutdownGrace
	}

	if cfg.EventWorkerNoStreamGiveUp <= 0 {
		cfg.EventWorkerNoStreamGiveUp = DefaultNoStreamGiveUp
	}

	bgCtx, cancel := context.WithCancel(ctx)

	h := &Handler{
		publisher: publisher,
		config:    cfg,
		eventChan: make(chan *publishRequest, cfg.EventWorkerQueueSize),
		cancel:    cancel,
		parked:    make(map[*publishRequest]*time.Timer),
	}

	for i := 0; i < cfg.EventWorkerPoolSize; i++ {
		h.wg.Add(1)
		go h.worker(bgCtx)
	}

	return h
}

// NewHandlerWithPoolSize creates a handler with explicit worker pool and queue sizing.
// This is primarily for testing. Production code should use NewHandler with EventConfig.
// Uses DefaultPublishTimeout for the publish timeout.
//
// Always call Close() during application shutdown to ensure graceful cleanup.
func NewHandlerWithPoolSize(ctx context.Context, publisher events.Publisher, workerPoolSize, queueSize int) *Handler {
	return NewHandler(ctx, publisher, EventConfig{
		EventWorkerPoolSize:       workerPoolSize,
		EventWorkerQueueSize:      queueSize,
		EventWorkerPublishTimeout: DefaultPublishTimeout,
	})
}

// worker runs in a goroutine and processes events from the queue.
func (h *Handler) worker(bgCtx context.Context) {
	defer h.wg.Done()

	for req := range h.eventChan {
		if bgCtx.Err() != nil {
			// Shutdown grace elapsed: drain the queue without publishing.
			h.dropOnShutdown(req)

			continue
		}

		h.publishWithRetry(bgCtx, req)
	}
}

// publishWithRetry publishes one event, retrying with exponential backoff until
// it succeeds. It drops the event when bgCtx is cancelled (shutdown) or no stream
// has taken it for EventWorkerNoStreamGiveUp. After a no-stream failure the event
// is parked instead of holding the worker.
// A "duplicate message id is in process" error (another pod is storing the same
// change) is retried like any other failure but logged at debug level.
func (h *Handler) publishWithRetry(bgCtx context.Context, req *publishRequest) {
	if req.backoff == 0 {
		req.backoff = h.config.EventWorkerRetryInitialBackoff
	}

	for {
		req.attempt++
		attempt, backoff := req.attempt, req.backoff

		err := h.publishOnce(bgCtx, req)
		if err == nil {
			req.logger.Debug().
				Str("namespace", req.event.Namespace).
				Str("task-queue", req.event.TaskQueue).
				Str("workflow-type", req.event.WorkflowTypeName).
				Str("workflow-id", req.event.WorkflowID).
				Str("run-id", req.event.RunID).
				Str("status", req.event.Status).
				Int("attempt", attempt).
				Msg("published workflow event")

			return
		}

		if bgCtx.Err() != nil {
			h.dropOnShutdown(req)

			return
		}

		req.noStreamSince = noStreamClock(err, req.noStreamSince, time.Now())
		if h.giveUpNoStream(err, req.noStreamSince) {
			h.dropNoStream(req, err, attempt, time.Since(req.noStreamSince))

			return
		}

		req.backoff = min(backoff*2, h.config.EventWorkerRetryMaxBackoff)

		if errors.Is(err, jetstream.ErrNoStreamResponse) {
			req.logger.Debug().
				Err(err).
				Int("attempt", attempt).
				Dur("retry-in", backoff).
				Str("namespace", req.event.Namespace).
				Str("workflow-id", req.event.WorkflowID).
				Msg("no stream takes the workflow event yet, retrying")

			h.park(req, backoff, err)

			return
		}

		level := log.WarnLevel
		if events.IsDuplicateMsgIDInProcess(err) {
			level = log.DebugLevel
		}

		req.logger.WithLevel(level).
			Err(err).
			Int("attempt", attempt).
			Dur("retry-in", backoff).
			Str("namespace", req.event.Namespace).
			Str("workflow-id", req.event.WorkflowID).
			Str("run-id", req.event.RunID).
			Str("status", req.event.Status).
			Msg("failed to publish workflow event, retrying")

		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-bgCtx.Done():
			timer.Stop()
			h.dropOnShutdown(req)

			return
		}
	}
}

// park queues req again after backoff, off the workers. Past
// EventWorkerQueueSize parked events it drops req instead.
func (h *Handler) park(req *publishRequest, backoff time.Duration, err error) {
	h.parkedMu.Lock()
	defer h.parkedMu.Unlock()

	if h.parkedClosed {
		h.dropOnShutdown(req)

		return
	}

	if len(h.parked) >= h.config.EventWorkerQueueSize {
		h.dropParkedFull(req, err)

		return
	}

	h.parked[req] = time.AfterFunc(backoff, func() { h.unpark(req) })
}

// unpark queues a parked event again, unless Close has taken it.
func (h *Handler) unpark(req *publishRequest) {
	h.parkedMu.Lock()
	_, ok := h.parked[req]
	delete(h.parked, req)
	h.parkedMu.Unlock()

	if ok {
		h.enqueue(req)
	}
}

// noStreamClock returns when the event's current run of no-stream failures
// began, after an attempt failed with err at now. An answer from a stream (an
// API error) ends the run; a timeout leaves it as it is.
func noStreamClock(err error, since, now time.Time) time.Time {
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		return time.Time{}
	}

	if errors.Is(err, jetstream.ErrNoStreamResponse) && since.IsZero() {
		return now
	}

	return since
}

// giveUpNoStream reports whether no stream has taken the event for
// EventWorkerNoStreamGiveUp.
func (h *Handler) giveUpNoStream(err error, since time.Time) bool {
	return errors.Is(err, jetstream.ErrNoStreamResponse) &&
		!since.IsZero() &&
		time.Since(since) >= h.config.EventWorkerNoStreamGiveUp
}

// publishOnce makes a single publish attempt and waits at most
// EventWorkerPublishTimeout for it. The publisher runs in its own goroutine so
// a publisher that ignores its context cannot hold the worker past the timeout;
// that attempt may still complete later. A late success is harmless because the
// workflow signal router derives its Temporal request IDs from the change, so a
// repeated change starts and signals nothing new.
func (h *Handler) publishOnce(bgCtx context.Context, req *publishRequest) error {
	ctx, cancel := context.WithTimeout(bgCtx, h.config.EventWorkerPublishTimeout)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- h.publisher.SendTemporalWorkflowEvent(ctx, req.event)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("publish attempt timed out after %s: %w", h.config.EventWorkerPublishTimeout, ctx.Err())
	}
}

func (h *Handler) dropNoStream(req *publishRequest, err error, attempts int, retriedFor time.Duration) {
	droppedEvents.WithLabelValues(dropReasonNoStream).Inc()
	req.logger.Warn().
		Err(err).
		Int("attempts", attempts).
		Dur("retried-for", retriedFor).
		Str("namespace", req.event.Namespace).
		Str("workflow-type", req.event.WorkflowTypeName).
		Str("workflow-id", req.event.WorkflowID).
		Str("run-id", req.event.RunID).
		Str("status", req.event.Status).
		Msg("no stream takes the workflow event - event dropped")
}

func (h *Handler) dropParkedFull(req *publishRequest, err error) {
	droppedEvents.WithLabelValues(dropReasonParkedFull).Inc()
	req.logger.Warn().
		Err(err).
		Int("attempts", req.attempt).
		Int("parked", h.config.EventWorkerQueueSize).
		Str("namespace", req.event.Namespace).
		Str("workflow-type", req.event.WorkflowTypeName).
		Str("workflow-id", req.event.WorkflowID).
		Str("run-id", req.event.RunID).
		Str("status", req.event.Status).
		Msg("too many workflow events wait for a stream - event dropped")
}

func (h *Handler) dropOnShutdown(req *publishRequest) {
	droppedEvents.WithLabelValues(dropReasonShutdown).Inc()
	req.logger.Warn().
		Str("namespace", req.event.Namespace).
		Str("workflow-type", req.event.WorkflowTypeName).
		Str("workflow-id", req.event.WorkflowID).
		Str("run-id", req.event.RunID).
		Str("status", req.event.Status).
		Msg("handler shutting down - pending workflow event dropped")
}

// Notify publishes a Temporal workflow event asynchronously.
//
// Events are queued for background workers. If the queue is full,
// the event is dropped, counted and a warning is logged. This prevents
// slow NATS publishing from blocking Temporal's critical path.
//
// After Close() is called, Notify will drop all events with a warning.
//
// Nil events or nil publishers are silently ignored.
func (h *Handler) Notify(ctx context.Context, event *events.TemporalWorkflowStateChangeMessage) {
	if h.publisher == nil || event == nil {
		return
	}

	// Capture logger with context values before entering async path.
	// This prevents using a cancelled context in worker goroutines.
	logger := log.ForContext(ctx)

	h.enqueue(&publishRequest{
		logger: *logger,
		event:  event,
	})
}

// enqueue sends req to the workers without blocking, or drops it when the
// handler is closed or the queue is full.
func (h *Handler) enqueue(req *publishRequest) {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()

	if h.closed {
		droppedEvents.WithLabelValues(dropReasonShutdown).Inc()
		req.logger.Warn().
			Str("namespace", req.event.Namespace).
			Str("workflow-id", req.event.WorkflowID).
			Str("run-id", req.event.RunID).
			Msg("handler shut down - workflow event dropped")

		return
	}

	// Non-blocking send - drop if the queue is full
	select {
	case h.eventChan <- req:
		// Successfully queued
	default:
		droppedEvents.WithLabelValues(dropReasonQueueFull).Inc()
		req.logger.Warn().
			Str("namespace", req.event.Namespace).
			Str("workflow-type", req.event.WorkflowTypeName).
			Str("workflow-id", req.event.WorkflowID).
			Str("run-id", req.event.RunID).
			Str("status", req.event.Status).
			Msg("event queue full - workflow event dropped")
	}
}

// Close signals the handler to stop accepting new events and lets the workers
// drain the queue. Parked events are dropped at once: no stream took them. Events
// still unpublished after config.EventWorkerShutdownGrace are dropped (logged and
// counted) and in-flight retries are abandoned, so Close returns within roughly
// the grace period plus one publish attempt.
//
// Close is safe to call multiple times.
func (h *Handler) Close() {
	h.closeMu.Lock()
	defer h.closeMu.Unlock()

	if h.closed {
		return // Already closed
	}

	h.stateMu.Lock()
	h.closed = true
	close(h.eventChan)
	h.stateMu.Unlock()

	h.dropParked()

	drained := make(chan struct{})

	go func() {
		h.wg.Wait()
		close(drained)
	}()

	grace := time.NewTimer(h.config.EventWorkerShutdownGrace)
	defer grace.Stop()

	select {
	case <-drained:
	case <-grace.C:
		// Cancel in-flight retries; workers drop what is left.
		h.cancel()
		<-drained
	}

	h.cancel()
}

// dropParked drops every parked event, and every event parked later.
func (h *Handler) dropParked() {
	h.parkedMu.Lock()
	parked := h.parked
	h.parked = nil
	h.parkedClosed = true
	h.parkedMu.Unlock()

	for req, timer := range parked {
		timer.Stop()
		h.dropOnShutdown(req)
	}
}
