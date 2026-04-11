package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
)

// =============================================================================
// MOCK PUBLISHER FOR OUTBOX HANDLER TESTS
// =============================================================================

type outboxMockPublisher struct {
	mu            sync.Mutex
	publishCalls  []publishCall
	requestCalls  []requestCall
	publishErr    error
	requestReply  *events.EventReply
	requestErr    error
	publishRawErr error
}

type publishCall struct {
	Topic   string
	Payload []byte
	MsgID   string
}

type requestCall struct {
	Topic   string
	Payload []byte
	Timeout time.Duration
}

func (m *outboxMockPublisher) SendMutationEvent(context.Context, *events.MutationEventMessage) error {
	return nil
}

func (m *outboxMockPublisher) SendMutationEventWithReply(context.Context, *events.MutationEventMessage) ([]byte, error) {
	return nil, nil
}

func (m *outboxMockPublisher) SendUpdateEvent(context.Context, *events.UpdateEventMessage) error {
	return nil
}

func (m *outboxMockPublisher) SendCustomEvent(context.Context, *events.CustomEventMessage) error {
	return nil
}

func (m *outboxMockPublisher) SendTemporalWorkflowEvent(context.Context, *events.TemporalWorkflowStateChangeMessage) error {
	return nil
}

func (m *outboxMockPublisher) SendWorkflowEvent(context.Context, *events.WorkflowEventMessage) error {
	return nil
}

func (m *outboxMockPublisher) PublishRaw(_ context.Context, topic string, payload []byte, msgID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.publishCalls = append(m.publishCalls, publishCall{
		Topic:   topic,
		Payload: payload,
		MsgID:   msgID,
	})

	if m.publishRawErr != nil {
		return m.publishRawErr
	}
	return m.publishErr
}

func (m *outboxMockPublisher) RequestRaw(_ context.Context, topic string, payload []byte, timeout time.Duration) (*events.EventReply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requestCalls = append(m.requestCalls, requestCall{
		Topic:   topic,
		Payload: payload,
		Timeout: timeout,
	})

	if m.requestErr != nil {
		return nil, m.requestErr
	}

	if m.requestReply != nil {
		return m.requestReply, nil
	}

	return &events.EventReply{Success: true}, nil
}

func (m *outboxMockPublisher) getPublishCalls() []publishCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]publishCall{}, m.publishCalls...)
}

func (m *outboxMockPublisher) getRequestCalls() []requestCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]requestCall{}, m.requestCalls...)
}

var _ events.Publisher = (*outboxMockPublisher)(nil)

// =============================================================================
// MOCK OUTBOX FUNCTIONS
// =============================================================================

type mockOutboxFunctions struct {
	mu               sync.Mutex
	selectEntries    []events.OutboxRow //nolint:unused // Available for future tests
	selectErr        error              //nolint:unused // Available for future tests
	markPublishedIDs []uuid.UUID
	markPublishedErr error
	markFailedCalls  []markFailedCall
	markFailedErr    error
	markDeadCalls    []markDeadCall //nolint:unused // Used by markCorrelationDead
	markDeadErr      error          //nolint:unused // Used by markCorrelationDead
}

type markFailedCall struct {
	ID     uuid.UUID
	ErrMsg string
}

type markDeadCall struct {
	CorrelationID string
	Reason        string
}

//nolint:unused // Available for future integration tests
func (m *mockOutboxFunctions) selector(_ context.Context, _ *sql.Tx, _, _ int) ([]events.OutboxRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.selectErr != nil {
		return nil, m.selectErr
	}
	return m.selectEntries, nil
}

func (m *mockOutboxFunctions) markPublished(_ context.Context, _ *sql.Tx, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.markPublishedIDs = append(m.markPublishedIDs, id)
	return m.markPublishedErr
}

func (m *mockOutboxFunctions) markFailed(_ context.Context, _ *sql.Tx, id uuid.UUID, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.markFailedCalls = append(m.markFailedCalls, markFailedCall{ID: id, ErrMsg: errMsg})
	return m.markFailedErr
}

//nolint:unused // Available for future integration tests
func (m *mockOutboxFunctions) markCorrelationDead(_ context.Context, _ *sql.Tx, correlationID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.markDeadCalls = append(m.markDeadCalls, markDeadCall{CorrelationID: correlationID, Reason: reason})
	return m.markDeadErr
}

// =============================================================================
// GROUP BY CORRELATION TESTS
// =============================================================================

func TestGroupByCorrelation(t *testing.T) {
	t.Parallel()

	t.Run("groups entries by correlation ID", func(t *testing.T) {
		t.Parallel()

		entries := []events.OutboxRow{
			{ID: uuid.New(), CorrelationID: "corr-1"},
			{ID: uuid.New(), CorrelationID: "corr-2"},
			{ID: uuid.New(), CorrelationID: "corr-1"},
			{ID: uuid.New(), CorrelationID: "corr-3"},
			{ID: uuid.New(), CorrelationID: "corr-2"},
		}

		groups := events.GroupByCorrelation(entries)

		assert.Len(t, groups, 3)
		assert.Len(t, groups["corr-1"], 2)
		assert.Len(t, groups["corr-2"], 2)
		assert.Len(t, groups["corr-3"], 1)
	})

	t.Run("empty entries returns empty map", func(t *testing.T) {
		t.Parallel()

		groups := events.GroupByCorrelation(nil)
		assert.Empty(t, groups)

		groups = events.GroupByCorrelation([]events.OutboxRow{})
		assert.Empty(t, groups)
	})

	t.Run("preserves order within groups", func(t *testing.T) {
		t.Parallel()

		id1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
		id2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
		id3 := uuid.MustParse("00000000-0000-0000-0000-000000000003")

		entries := []events.OutboxRow{
			{ID: id1, CorrelationID: "corr-1"},
			{ID: id2, CorrelationID: "corr-1"},
			{ID: id3, CorrelationID: "corr-1"},
		}

		groups := events.GroupByCorrelation(entries)

		require.Len(t, groups["corr-1"], 3)
		assert.Equal(t, id1, groups["corr-1"][0].ID)
		assert.Equal(t, id2, groups["corr-1"][1].ID)
		assert.Equal(t, id3, groups["corr-1"][2].ID)
	})
}

// =============================================================================
// BUILD MESSAGE ID TESTS
// =============================================================================

func TestBuildMessageID(t *testing.T) {
	t.Parallel()

	t.Run("builds correct message ID format", func(t *testing.T) {
		t.Parallel()

		entityID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
		payload := events.MutationEventMessage{
			Schema: "Order",
			ID:     entityID,
		}
		payloadBytes, err := json.Marshal(payload)
		require.NoError(t, err)

		entry := events.OutboxRow{
			CorrelationID: "trace-123",
			Payload:       payloadBytes,
		}

		msgID := events.BuildMessageID(entry)
		assert.Equal(t, "trace-123-Order-11111111-1111-1111-1111-111111111111", msgID)
	})

	t.Run("falls back to correlation ID on invalid payload", func(t *testing.T) {
		t.Parallel()

		entry := events.OutboxRow{
			CorrelationID: "trace-456",
			Payload:       []byte("invalid json"),
		}

		msgID := events.BuildMessageID(entry)
		assert.Equal(t, "trace-456", msgID)
	})
}

// =============================================================================
// OUTBOX HANDLER INTEGRATION TESTS
// =============================================================================

func TestOutboxHandler_ProcessEntry(t *testing.T) {
	t.Parallel()

	t.Run("processes fire-and-forget entry successfully", func(t *testing.T) {
		t.Parallel()

		publisher := &outboxMockPublisher{}
		outboxFuncs := &mockOutboxFunctions{}
		registry := events.NewReplyRegistry(time.Minute)

		entityID := uuid.New()
		payload := events.MutationEventMessage{
			Service:   "test",
			Schema:    "Order",
			Operation: "create",
			ID:        entityID,
			TenantID:  uuid.New(),
		}
		payloadBytes, _ := json.Marshal(payload)

		entry := events.OutboxRow{
			ID:            uuid.New(),
			CorrelationID: "corr-1",
			Topic:         "pyck.test.order.create",
			Payload:       payloadBytes,
			WithReply:     false,
			RetryCount:    0,
		}

		err := events.ProcessEntryForTest(
			context.Background(),
			nil, // tx not used by mock
			entry,
			publisher,
			registry,
			outboxFuncs.markPublished,
			outboxFuncs.markFailed,
			10*time.Second,
		)
		require.NoError(t, err)

		// Should have published
		calls := publisher.getPublishCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, "pyck.test.order.create", calls[0].Topic)

		// Should be marked as published
		assert.Len(t, outboxFuncs.markPublishedIDs, 1)
		assert.Equal(t, entry.ID, outboxFuncs.markPublishedIDs[0])
	})

	t.Run("processes with-reply entry and delivers workflows", func(t *testing.T) {
		t.Parallel()

		workflows := []*events.WorkflowDetails{
			{Type: "TestWorkflow", ID: "wf-123", RunID: "run-abc"},
		}
		workflowsJSON, _ := json.Marshal(workflows)

		publisher := &outboxMockPublisher{
			requestReply: &events.EventReply{
				Success: true,
				Data:    workflowsJSON,
			},
		}
		outboxFuncs := &mockOutboxFunctions{}
		registry := events.NewReplyRegistry(time.Minute)
		ctx := context.Background()
		registry.Start(ctx)
		defer registry.Stop()

		// Pre-register for reply
		correlationID := "corr-with-reply"
		replyCh := registry.Register(correlationID, 5*time.Second)

		entityID := uuid.New()
		payload := events.MutationEventMessage{
			Service:   "test",
			Schema:    "Order",
			Operation: "create",
			ID:        entityID,
			TenantID:  uuid.New(),
		}
		payloadBytes, _ := json.Marshal(payload)

		entry := events.OutboxRow{
			ID:            uuid.New(),
			CorrelationID: correlationID,
			Topic:         "request.reply.pyck.test.order.create",
			Payload:       payloadBytes,
			WithReply:     true,
			RetryCount:    0,
		}

		err := events.ProcessEntryForTest(
			context.Background(),
			nil,
			entry,
			publisher,
			registry,
			outboxFuncs.markPublished,
			outboxFuncs.markFailed,
			10*time.Second,
		)
		require.NoError(t, err)

		// Should have called RequestRaw
		requestCalls := publisher.getRequestCalls()
		require.Len(t, requestCalls, 1)
		assert.Equal(t, "request.reply.pyck.test.order.create", requestCalls[0].Topic)

		// Should have delivered to registry
		select {
		case received := <-replyCh:
			require.Len(t, received, 1)
			assert.Equal(t, "wf-123", received[0].ID)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for workflows")
		}
	})

	t.Run("marks entry failed on publish error", func(t *testing.T) {
		t.Parallel()

		publisher := &outboxMockPublisher{
			publishRawErr: errors.New("NATS connection failed"),
		}
		outboxFuncs := &mockOutboxFunctions{}
		registry := events.NewReplyRegistry(time.Minute)

		entry := events.OutboxRow{
			ID:            uuid.New(),
			CorrelationID: "corr-fail",
			Topic:         "pyck.test.order.create",
			Payload:       []byte(`{}`),
			WithReply:     false,
			RetryCount:    0,
		}

		// Note: ProcessEntry returns nil when markFailed succeeds,
		// even if publish failed. The error is recorded in the outbox.
		err := events.ProcessEntryForTest(
			context.Background(),
			nil,
			entry,
			publisher,
			registry,
			outboxFuncs.markPublished,
			outboxFuncs.markFailed,
			10*time.Second,
		)
		// When markFailed succeeds, processEntry returns nil
		require.NoError(t, err)

		// Should be marked as failed
		require.Len(t, outboxFuncs.markFailedCalls, 1)
		assert.Equal(t, entry.ID, outboxFuncs.markFailedCalls[0].ID)
		assert.Contains(t, outboxFuncs.markFailedCalls[0].ErrMsg, "NATS connection failed")

		// Should NOT be marked as published
		assert.Empty(t, outboxFuncs.markPublishedIDs)
	})

	t.Run("returns error when markFailed fails", func(t *testing.T) {
		t.Parallel()

		publisher := &outboxMockPublisher{
			publishRawErr: errors.New("NATS connection failed"),
		}
		outboxFuncs := &mockOutboxFunctions{
			markFailedErr: errors.New("database error"),
		}
		registry := events.NewReplyRegistry(time.Minute)

		entry := events.OutboxRow{
			ID:            uuid.New(),
			CorrelationID: "corr-fail-db",
			Topic:         "pyck.test.order.create",
			Payload:       []byte(`{}`),
			WithReply:     false,
			RetryCount:    0,
		}

		err := events.ProcessEntryForTest(
			context.Background(),
			nil,
			entry,
			publisher,
			registry,
			outboxFuncs.markPublished,
			outboxFuncs.markFailed,
			10*time.Second,
		)
		// When markFailed fails, processEntry returns the error
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database error")
	})
}

// =============================================================================
// CORRELATION GROUP PROCESSING TESTS
// =============================================================================

func TestOutboxHandler_ProcessCorrelationGroup(t *testing.T) {
	t.Parallel()

	t.Run("processes all entries in order", func(t *testing.T) {
		t.Parallel()

		var processedOrder []uuid.UUID
		var mu sync.Mutex

		publisher := &outboxMockPublisher{}
		markPublished := func(_ context.Context, _ *sql.Tx, id uuid.UUID) error {
			mu.Lock()
			processedOrder = append(processedOrder, id)
			mu.Unlock()
			return nil
		}
		markFailed := func(_ context.Context, _ *sql.Tx, _ uuid.UUID, _ string) error {
			return nil
		}
		markDead := func(_ context.Context, _ *sql.Tx, _, _ string) error {
			return nil
		}
		registry := events.NewReplyRegistry(time.Minute)

		id1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
		id2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
		id3 := uuid.MustParse("00000000-0000-0000-0000-000000000003")

		entries := []events.OutboxRow{
			{ID: id1, CorrelationID: "corr-1", Topic: "t1", Payload: []byte(`{}`)},
			{ID: id2, CorrelationID: "corr-1", Topic: "t2", Payload: []byte(`{}`)},
			{ID: id3, CorrelationID: "corr-1", Topic: "t3", Payload: []byte(`{}`)},
		}

		events.ProcessCorrelationGroupForTest(
			context.Background(),
			nil,
			"corr-1",
			entries,
			publisher,
			registry,
			markPublished,
			markFailed,
			markDead,
			10,
			10*time.Second,
		)

		mu.Lock()
		defer mu.Unlock()

		require.Len(t, processedOrder, 3)
		assert.Equal(t, id1, processedOrder[0])
		assert.Equal(t, id2, processedOrder[1])
		assert.Equal(t, id3, processedOrder[2])
	})

	t.Run("stops on entry failure", func(t *testing.T) {
		t.Parallel()

		var processedCount atomic.Int32
		publisher := &outboxMockPublisher{}

		// Fail on second entry
		markPublished := func(_ context.Context, _ *sql.Tx, id uuid.UUID) error {
			count := processedCount.Add(1)
			if count == 2 {
				return errors.New("db error")
			}
			return nil
		}
		markFailed := func(_ context.Context, _ *sql.Tx, _ uuid.UUID, _ string) error {
			return nil
		}
		markDead := func(_ context.Context, _ *sql.Tx, _, _ string) error {
			return nil
		}
		registry := events.NewReplyRegistry(time.Minute)

		entries := []events.OutboxRow{
			{ID: uuid.New(), CorrelationID: "corr-1", Topic: "t1", Payload: []byte(`{}`)},
			{ID: uuid.New(), CorrelationID: "corr-1", Topic: "t2", Payload: []byte(`{}`)},
			{ID: uuid.New(), CorrelationID: "corr-1", Topic: "t3", Payload: []byte(`{}`)},
		}

		events.ProcessCorrelationGroupForTest(
			context.Background(),
			nil,
			"corr-1",
			entries,
			publisher,
			registry,
			markPublished,
			markFailed,
			markDead,
			10,
			10*time.Second,
		)

		// Should have only processed 2 (first success, second fails)
		assert.Equal(t, int32(2), processedCount.Load())
	})

	t.Run("marks correlation dead when max retries exceeded", func(t *testing.T) {
		t.Parallel()

		publisher := &outboxMockPublisher{}
		var deadCalls []markDeadCall
		var mu sync.Mutex

		markPublished := func(_ context.Context, _ *sql.Tx, _ uuid.UUID) error {
			return nil
		}
		markFailed := func(_ context.Context, _ *sql.Tx, _ uuid.UUID, _ string) error {
			return nil
		}
		markDead := func(_ context.Context, _ *sql.Tx, correlationID, reason string) error {
			mu.Lock()
			deadCalls = append(deadCalls, markDeadCall{CorrelationID: correlationID, Reason: reason})
			mu.Unlock()
			return nil
		}
		registry := events.NewReplyRegistry(time.Minute)

		// Entry with retry count >= max retries
		entries := []events.OutboxRow{
			{ID: uuid.New(), CorrelationID: "corr-dead", Topic: "t1", Payload: []byte(`{}`), RetryCount: 10},
		}

		events.ProcessCorrelationGroupForTest(
			context.Background(),
			nil,
			"corr-dead",
			entries,
			publisher,
			registry,
			markPublished,
			markFailed,
			markDead,
			10, // maxRetries
			10*time.Second,
		)

		mu.Lock()
		defer mu.Unlock()

		require.Len(t, deadCalls, 1)
		assert.Equal(t, "corr-dead", deadCalls[0].CorrelationID)
		assert.Contains(t, deadCalls[0].Reason, "exceeded max retries")
	})
}

// =============================================================================
// OUTBOX HANDLER LIFECYCLE TESTS
// =============================================================================

func TestOutboxHandler_Lifecycle(t *testing.T) {
	t.Parallel()

	t.Run("stop is idempotent", func(t *testing.T) {
		t.Parallel()

		handler := events.NewOutboxHandler(events.OutboxHandlerConfig{
			PollInterval:         time.Minute,
			NotifyChannel:        "test_channel",
			ListenerPingInterval: time.Minute,
		})

		// Multiple stops should not panic
		handler.Stop()
		handler.Stop()
		handler.Stop()
	})
}
