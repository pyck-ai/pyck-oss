package mocks

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/stretchr/testify/mock"
)

// MockPublisher is a mock for the EventPublisher type
type MockPublisher struct {
	mock.Mock
	callWg      sync.WaitGroup
	expectCount int32 // Atomic counter for expectations
}

func (m *MockPublisher) SendCustomEvent(ctx context.Context, msg *events.CustomEventMessage) error {
	args := m.Called(msg)
	return args.Error(0)
}

func (m *MockPublisher) SendMutationEvent(ctx context.Context, msg *events.MutationEventMessage) error {
	args := m.Called(msg)
	return args.Error(0)
}

func (m *MockPublisher) SendMutationEventWithReply(ctx context.Context, msg *events.MutationEventMessage) ([]byte, error) {
	args := m.Called(msg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockPublisher) SendTemporalSignalEvent(ctx context.Context, msg *events.MutationEventMessage) error {
	args := m.Called(msg)
	return args.Error(0)
}

func (m *MockPublisher) SendTemporalWorkflowEvent(ctx context.Context, msg *events.TemporalWorkflowStateChangeMessage) error {
	// Only call Done() if we have expectations set via ExpectTemporalWorkflowEvent
	if atomic.LoadInt32(&m.expectCount) > 0 {
		defer m.callWg.Done()
	}
	args := m.Called(ctx, msg)
	return args.Error(0)
}

// ExpectTemporalWorkflowEvent sets up an expectation and increments the wait counter
func (m *MockPublisher) ExpectTemporalWorkflowEvent() *mock.Call {
	m.callWg.Add(1)
	atomic.AddInt32(&m.expectCount, 1)
	return m.On("SendTemporalWorkflowEvent", mock.Anything, mock.Anything)
}

// WaitForCalls waits for all expected calls to complete
func (m *MockPublisher) WaitForCalls() {
	m.callWg.Wait()
}

func (m *MockPublisher) SendUpdateEvent(ctx context.Context, msg *events.UpdateEventMessage) error {
	args := m.Called(msg)
	return args.Error(0)
}

func (m *MockPublisher) SendWorkflowEvent(ctx context.Context, msg *events.WorkflowEventMessage) error {
	args := m.Called(msg)
	return args.Error(0)
}

func (m *MockPublisher) PublishRaw(ctx context.Context, topic string, payload []byte, msgID string) error {
	args := m.Called(ctx, topic, payload, msgID)
	return args.Error(0)
}

func (m *MockPublisher) RequestRaw(ctx context.Context, topic string, payload []byte, timeout time.Duration) (*events.EventReply, error) {
	args := m.Called(ctx, topic, payload, timeout)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*events.EventReply), args.Error(1)
}
