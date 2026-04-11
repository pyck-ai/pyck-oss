package event_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/temporal/event"
)

func TestNewHandler(t *testing.T) {
	tests := []struct {
		name      string
		publisher events.Publisher
	}{
		{
			name:      "valid publisher",
			publisher: &mocks.MockPublisher{},
		},
		{
			name:      "nil publisher",
			publisher: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := event.NewHandlerWithPoolSize(t.Context(), tt.publisher, 2, 10)
			assert.NotNil(t, handler)
			defer handler.Close()
		})
	}
}

func TestHandler_Notify(t *testing.T) {
	tests := []struct {
		name         string
		event        *events.TemporalWorkflowStateChangeMessage
		setupMocks   func(*mocks.MockPublisher)
		expectCalled bool
	}{
		{
			name: "successful notification",
			event: &events.TemporalWorkflowStateChangeMessage{
				Namespace:  "test-namespace",
				RunID:      "run-123",
				Status:     "RUNNING",
				WorkflowID: "workflow-456",
			},
			setupMocks: func(mp *mocks.MockPublisher) {
				mp.ExpectTemporalWorkflowEvent().Return(nil).Run(func(args mock.Arguments) {
					msg := args.Get(1).(*events.TemporalWorkflowStateChangeMessage)
					assert.Equal(t, "test-namespace", msg.Namespace)
					assert.Equal(t, "run-123", msg.RunID)
					assert.Equal(t, "RUNNING", msg.Status)
					assert.Equal(t, "workflow-456", msg.WorkflowID)
				})
			},
			expectCalled: true,
		},
		{
			name: "successful notification with all fields",
			event: &events.TemporalWorkflowStateChangeMessage{
				Namespace:  "test-namespace",
				RunID:      "run-123",
				Status:     "COMPLETED",
				WorkflowID: "workflow-456",
			},
			setupMocks: func(mp *mocks.MockPublisher) {
				mp.ExpectTemporalWorkflowEvent().Return(nil).Run(func(args mock.Arguments) {
					msg := args.Get(1).(*events.TemporalWorkflowStateChangeMessage)
					assert.Equal(t, "test-namespace", msg.Namespace)
					assert.Equal(t, "COMPLETED", msg.Status)
				})
			},
			expectCalled: true,
		},
		{
			name: "publisher error",
			event: &events.TemporalWorkflowStateChangeMessage{
				Namespace:  "test-namespace",
				RunID:      "run-123",
				Status:     "FAILED",
				WorkflowID: "workflow-456",
			},
			setupMocks: func(mp *mocks.MockPublisher) {
				mp.ExpectTemporalWorkflowEvent().Return(assert.AnError)
			},
			expectCalled: true,
		},
		{
			name: "empty event",
			event: &events.TemporalWorkflowStateChangeMessage{
				Namespace:  "",
				RunID:      "",
				Status:     "",
				WorkflowID: "",
			},
			setupMocks: func(mp *mocks.MockPublisher) {
				mp.ExpectTemporalWorkflowEvent().Return(nil).Run(func(args mock.Arguments) {
					msg := args.Get(1).(*events.TemporalWorkflowStateChangeMessage)
					assert.Equal(t, "", msg.Namespace)
					assert.Equal(t, "", msg.RunID)
					assert.Equal(t, "", msg.Status)
					assert.Equal(t, "", msg.WorkflowID)
				})
			},
			expectCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockPublisher := &mocks.MockPublisher{}

			if tt.setupMocks != nil {
				tt.setupMocks(mockPublisher)
			}

			handler := event.NewHandlerWithPoolSize(t.Context(), mockPublisher, 10, 100)

			ctx := context.Background()

			// Notify is async, so we need to close the handler to wait for processing
			handler.Notify(ctx, tt.event)

			// Close waits for all queued events to be processed
			handler.Close()

			if tt.expectCalled {
				mockPublisher.WaitForCalls()
				mockPublisher.AssertExpectations(t)
			} else {
				mockPublisher.AssertNotCalled(t, "SendTemporalWorkflowEvent", mock.Anything, mock.Anything)
			}
		})
	}
}

func TestHandler_Notify_WithNilPublisher(t *testing.T) {
	handler := event.NewHandlerWithPoolSize(t.Context(), nil, 2, 10)
	defer handler.Close()

	ctx := context.Background()

	evt := &events.TemporalWorkflowStateChangeMessage{
		Namespace:  "test-namespace",
		RunID:      "run-123",
		Status:     "RUNNING",
		WorkflowID: "workflow-456",
	}

	// Should not panic with nil publisher
	assert.NotPanics(t, func() {
		handler.Notify(ctx, evt)
	})
}

func TestHandler_Notify_WithNilEvent(t *testing.T) {
	mockPublisher := &mocks.MockPublisher{}

	handler := event.NewHandlerWithPoolSize(t.Context(), mockPublisher, 2, 10)
	defer handler.Close()

	ctx := context.Background()

	// Should not panic with nil event and should not call publisher
	assert.NotPanics(t, func() {
		handler.Notify(ctx, nil)
	})

	handler.Close()

	// Should not have called the publisher
	mockPublisher.AssertNotCalled(t, "SendTemporalWorkflowEvent", mock.Anything, mock.Anything)
}

func TestHandler_Close_Multiple(t *testing.T) {
	mockPublisher := &mocks.MockPublisher{}
	handler := event.NewHandlerWithPoolSize(t.Context(), mockPublisher, 2, 10)

	// Close multiple times should be safe
	handler.Close()
	assert.NotPanics(t, func() {
		handler.Close()
		handler.Close()
	})
}

func TestHandler_QueueFull(t *testing.T) {
	mockPublisher := &mocks.MockPublisher{}

	// Create handler with tiny queue to force overflow
	handler := event.NewHandlerWithPoolSize(t.Context(), mockPublisher, 1, 1)
	defer handler.Close()

	ctx := context.Background()

	// Set up mock to block, simulating slow publisher
	// Use On() instead of Expect() because some events will be dropped
	blockChan := make(chan struct{})
	mockPublisher.On("SendTemporalWorkflowEvent", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			<-blockChan // Block until test unblocks
		}).
		Return(nil)

	// Send events to fill queue and overflow
	for range 10 {
		handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
			Namespace:  "test",
			WorkflowID: "overflow-test",
			RunID:      "run-123",
			Status:     "RUNNING",
		})
	}

	// Unblock the publisher
	close(blockChan)

	// Wait for processing
	handler.Close()

	// Should have processed some events (at least 1 worker + 1 queued)
	// but not all 10 due to overflow
	mockPublisher.AssertCalled(t, "SendTemporalWorkflowEvent", mock.Anything, mock.Anything)
	callCount := len(mockPublisher.Calls)
	assert.Greater(t, callCount, 0, "Expected at least one call to SendTemporalWorkflowEvent")
	assert.Less(t, callCount, 10, "Expected fewer than 10 calls to SendTemporalWorkflowEvent due to overflow")
}

func TestHandler_ConcurrentNotify(t *testing.T) {
	mockPublisher := &mocks.MockPublisher{}
	// Use On() instead of Expect() because some events may be dropped
	mockPublisher.On("SendTemporalWorkflowEvent", mock.Anything, mock.Anything).Return(nil)

	handler := event.NewHandlerWithPoolSize(t.Context(), mockPublisher, 10, 1000)
	defer handler.Close()

	ctx := context.Background()
	numGoroutines := 100
	eventsPerGoroutine := 10

	// Launch concurrent Notify calls
	done := make(chan struct{})
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < eventsPerGoroutine; j++ {
				handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
					Namespace:  "test",
					WorkflowID: "concurrent-test",
					RunID:      "run-123",
					Status:     "RUNNING",
				})
			}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Close and wait for all events to be processed
	handler.Close()

	// Verify publisher was called (may be less than total if queue overflowed)
	mockPublisher.AssertCalled(t, "SendTemporalWorkflowEvent", mock.Anything, mock.Anything)
}

func TestHandler_PublishTimeout(t *testing.T) {
	mockPublisher := &mocks.MockPublisher{}

	// Create handler with very short timeout
	handler := event.NewHandler(t.Context(), mockPublisher, event.EventConfig{
		EventWorkerPoolSize:       2,
		EventWorkerQueueSize:      10,
		EventWorkerPublishTimeout: 50 * time.Millisecond,
	})
	defer handler.Close()

	ctx := context.Background()

	// Set up mock to block longer than timeout
	// Use On() instead of Expect() because timeout may prevent completion
	blockChan := make(chan struct{})
	mockPublisher.On("SendTemporalWorkflowEvent", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			<-blockChan // Block indefinitely
		}).
		Return(nil)

	// Send an event that will timeout
	handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
		Namespace:  "test",
		WorkflowID: "timeout-test",
		RunID:      "run-123",
		Status:     "RUNNING",
	})

	// Wait for timeout to occur
	time.Sleep(100 * time.Millisecond)

	// Unblock the publisher (though the worker has moved on)
	close(blockChan)

	// Close should not hang despite the blocked publisher
	done := make(chan struct{})
	go func() {
		handler.Close()
		close(done)
	}()

	// Verify Close() completes in reasonable time (timeout should prevent hanging)
	select {
	case <-done:
		// Success - Close completed
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung despite timeout - workers not responding")
	}

	// Publisher was called
	mockPublisher.AssertCalled(t, "SendTemporalWorkflowEvent", mock.Anything, mock.Anything)
}

func TestHandler_PublishTimeoutDoesNotBlockQueue(t *testing.T) {
	mockPublisher := &mocks.MockPublisher{}

	// Create handler with short timeout and single worker
	handler := event.NewHandler(t.Context(), mockPublisher, event.EventConfig{
		EventWorkerPoolSize:       1,
		EventWorkerQueueSize:      10,
		EventWorkerPublishTimeout: 50 * time.Millisecond,
	})
	defer handler.Close()

	ctx := context.Background()

	// Use channels to track events processed
	slowEventStarted := make(chan struct{})
	slowEventBlock := make(chan struct{})
	fastEventProcessed := make(chan struct{})

	// Use On() instead of Expect() to avoid WaitGroup issues with timeouts
	mockPublisher.On("SendTemporalWorkflowEvent", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		msg := args.Get(1).(*events.TemporalWorkflowStateChangeMessage)
		switch msg.WorkflowID {
		case "slow-event":
			select {
			case <-slowEventStarted:
				// Already closed
			default:
				close(slowEventStarted)
			}
			<-slowEventBlock // Block until we're done
		case "fast-event":
			select {
			case <-fastEventProcessed:
				// Already closed
			default:
				close(fastEventProcessed)
			}
		}
	}).Return(nil)

	// Send slow event
	handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
		Namespace:  "test",
		WorkflowID: "slow-event",
		RunID:      "run-1",
		Status:     "RUNNING",
	})

	// Wait for worker to start processing slow event
	select {
	case <-slowEventStarted:
		// Slow event started processing
	case <-time.After(1 * time.Second):
		t.Fatal("slow event never started processing")
	}

	// Send fast event
	handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
		Namespace:  "test",
		WorkflowID: "fast-event",
		RunID:      "run-2",
		Status:     "RUNNING",
	})

	// Wait for timeout on first event (50ms timeout + some buffer)
	time.Sleep(100 * time.Millisecond)

	// Unblock slow event
	close(slowEventBlock)

	// Wait for fast event to be processed (should happen after timeout)
	select {
	case <-fastEventProcessed:
		// Success - fast event was processed
	case <-time.After(200 * time.Millisecond):
		t.Fatal("fast event was not processed after slow event timed out")
	}

	handler.Close()
}
