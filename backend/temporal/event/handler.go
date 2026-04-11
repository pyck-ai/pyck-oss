package event

import (
	"context"
	"sync"
	"time"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/temporal/config"
)

const (
	// DefaultWorkerPoolSize is the default number of concurrent workers
	// for publishing events to NATS.
	DefaultWorkerPoolSize = 10

	// DefaultQueueSize is the default buffer size for the event queue.
	// Events are dropped with a warning if the queue is full.
	DefaultQueueSize = 1000

	// DefaultPublishTimeout is the default timeout for publishing a single
	// event to NATS. Events that exceed this timeout are logged as errors
	// and the worker moves to the next event.
	DefaultPublishTimeout = 100 * time.Millisecond
)

// EventConfig is an alias for config.EventConfig to avoid breaking existing code.
type EventConfig = config.EventWorkerConfig

// Handler publishes Temporal workflow events to NATS using a worker pool
// to avoid blocking Temporal's critical path.
type Handler struct {
	publisher events.Publisher
	config    EventConfig

	eventChan chan *publishRequest
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	closed    bool
	closeMu   sync.Mutex
}

type publishRequest struct {
	// logger is captured at Notify() time to avoid using
	// a cancelled context in the worker goroutine.
	logger log.Logger
	event  *events.TemporalWorkflowStateChangeMessage
}

// NewHandler creates a new event handler with configuration from EventConfig.
// The handler spawns config.EventWorkerPoolSize workers that process events concurrently,
// with a buffered queue of config.EventQueueSize.
//
// Configuration can be set via environment variables:
//   - PYCK_EVENT_WORKER_POOL_SIZE (default: 10)
//   - PYCK_EVENT_QUEUE_SIZE (default: 1000)
//   - PYCK_EVENT_PUBLISH_TIMEOUT (default: 100ms)
//
// Each worker enforces config.EventPublishTimeout to prevent blocking indefinitely
// on slow NATS operations, ensuring workers remain responsive during shutdown.
//
// Always call Close() during application shutdown to ensure graceful cleanup.
func NewHandler(ctx context.Context, publisher events.Publisher, cfg EventConfig) *Handler {
	bgCtx, cancel := context.WithCancel(ctx)

	h := &Handler{
		publisher: publisher,
		config:    cfg,
		eventChan: make(chan *publishRequest, cfg.EventWorkerQueueSize),
		cancel:    cancel,
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
		h.publish(bgCtx, req)
	}
}

// publish sends a single event to NATS with timeout, logging errors.
func (h *Handler) publish(bgCtx context.Context, req *publishRequest) {
	// Create context with timeout for this publish operation
	ctx, cancel := context.WithTimeout(bgCtx, h.config.EventWorkerPublishTimeout)
	defer cancel()

	// Create a channel to receive the publish result
	done := make(chan error, 1)

	// Execute publish in goroutine to enable timeout
	go func() {
		done <- h.publisher.SendTemporalWorkflowEvent(ctx, req.event)
	}()

	// Wait for either publish completion or timeout
	select {
	case err := <-done:
		if err != nil {
			req.logger.Error().
				Err(err).
				Msg("failed to publish workflow event")
		}
	case <-ctx.Done():
		req.logger.Error().
			Dur("timeout", h.config.EventWorkerPublishTimeout).
			Str("namespace", req.event.Namespace).
			Str("task-queue", req.event.TaskQueue).
			Str("workflow-type", req.event.WorkflowTypeName).
			Str("workflow-id", req.event.WorkflowID).
			Str("run-id", req.event.RunID).
			Msg("timeout publishing workflow event")
	}

	req.logger.Debug().
		Str("namespace", req.event.Namespace).
		Str("task-queue", req.event.TaskQueue).
		Str("workflow-type", req.event.WorkflowTypeName).
		Str("workflow-id", req.event.WorkflowID).
		Str("run-id", req.event.RunID).
		Str("status", req.event.Status).
		Msg("published workflow event")
}

// Notify publishes a Temporal workflow event asynchronously.
//
// Events are queued for background workers. If the queue is full,
// the event is dropped and a warning is logged. This prevents
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

	req := &publishRequest{
		logger: *logger,
		event:  event,
	}

	// Non-blocking send - drop if queue is full or closed
	select {
	case h.eventChan <- req:
		// Successfully queued
	default:
		// Queue full or handler shutting down - drop and log
		logger.Warn().
			Str("namespace", event.Namespace).
			Str("workflow-type", event.WorkflowTypeName).
			Str("workflow-id", event.WorkflowID).
			Str("run-id", event.RunID).
			Msg("event queue full or handler shutting down - workflow event dropped")
	}
}

// Close signals the handler to stop accepting new events, drains the queue,
// and waits for all workers to exit. After Close() is called, Notify() will
// drop all events.
//
// Close blocks until all queued events have been processed and all worker
// goroutines have exited.
//
// Close is safe to call multiple times.
func (h *Handler) Close() {
	h.closeMu.Lock()
	defer h.closeMu.Unlock()

	if h.closed {
		return // Already closed
	}

	h.closed = true

	// Close channel to signal workers to exit after draining queue
	close(h.eventChan)

	// Wait for all workers to finish processing
	h.wg.Wait()

	// Cancel context after workers have finished to clean up resources
	h.cancel()
}
