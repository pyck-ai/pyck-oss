package events_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"entgo.io/ent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
)

// testEntity is a test struct for field comparison tests (without special Data handling).
type testEntity struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Name      string
	Age       int
	Active    bool
	CreatedAt time.Time
}

// testEntityWithData is a separate test struct for Data field tests.
// The Data field receives special JSON map comparison treatment.
type testEntityWithData struct {
	ID   uuid.UUID
	Data map[string]any
}

// differentEntity is used for type mismatch tests.
type differentEntity struct {
	ID   uuid.UUID
	Code string
}

func TestMain(m *testing.M) {
	// Pre-register test types in the typeInfoCache.
	// testEntity: no special data field handling
	events.RegisterTestType(testEntity{}, "")
	// testEntityWithData: Data field gets special JSON map comparison
	events.RegisterTestType(testEntityWithData{}, "Data")
	// differentEntity: for type mismatch tests
	events.RegisterTestType(differentEntity{}, "")

	os.Exit(m.Run())
}

// expectedUpdateEvent holds expected values for an update event.
type expectedUpdateEvent struct {
	attribute string
	oldValue  any
	newValue  any
}

// mockPublisher implements events.Publisher for testing.
type mockPublisher struct {
	events               []*events.UpdateEventMessage
	sendUpdateEventErr   error
	sendUpdateEventErrAt int // Fail at this call index (-1 = never)
	callCount            int
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{
		events:               make([]*events.UpdateEventMessage, 0),
		sendUpdateEventErrAt: -1,
	}
}

func (m *mockPublisher) SendUpdateEvent(_ context.Context, msg *events.UpdateEventMessage) error {
	if m.sendUpdateEventErrAt >= 0 && m.callCount >= m.sendUpdateEventErrAt {
		return m.sendUpdateEventErr
	}
	if m.sendUpdateEventErr != nil && m.sendUpdateEventErrAt < 0 {
		return m.sendUpdateEventErr
	}
	m.events = append(m.events, msg)
	m.callCount++
	return nil
}

// Implement other Publisher interface methods as no-ops.
func (m *mockPublisher) SendMutationEvent(_ context.Context, _ *events.MutationEventMessage) error {
	return nil
}

func (m *mockPublisher) PublishRaw(_ context.Context, _ string, _ []byte, _ string) error {
	return nil
}

func (m *mockPublisher) RequestRaw(_ context.Context, _ string, _ []byte, _ time.Duration) (*events.EventReply, error) {
	return nil, nil //nolint:nilnil // Mock method intentionally returns nil,nil (no data, no error)
}

func (m *mockPublisher) SendCustomEvent(_ context.Context, _ *events.CustomEventMessage) error {
	return nil
}

func (m *mockPublisher) SendMutationEventWithReply(_ context.Context, _ *events.MutationEventMessage) ([]byte, error) {
	return nil, nil
}

func (m *mockPublisher) SendTemporalWorkflowEvent(_ context.Context, _ *events.TemporalWorkflowStateChangeMessage) error {
	return nil
}

func (m *mockPublisher) SendWorkflowEvent(_ context.Context, _ *events.WorkflowEventMessage) error {
	return nil
}

// mockMutation implements ent.Mutation and the IDsProvider interface for testing the hook.
// Embeds ent.Mutation so only the methods actually exercised by the hook need overriding.
type mockMutation struct {
	ent.Mutation // provides no-op defaults; panics if unexpected methods are called
	op           ent.Op
	typ          string
	ids          []uuid.UUID
}

func (m *mockMutation) Op() ent.Op                               { return m.op }
func (m *mockMutation) Type() string                             { return m.typ }
func (m *mockMutation) Field(string) (ent.Value, bool)           { return nil, false }
func (m *mockMutation) IDs(context.Context) ([]uuid.UUID, error) { return m.ids, nil }

func TestMutationEventHook_BulkUpdateZeroMatches_SkipsEventEmission(t *testing.T) {
	t.Parallel()

	outboxInserted := false

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
			outboxInserted = true
			return nil
		},
	})

	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return 0, nil // Bulk update returns affected count
	})

	mutator := hook(next)
	m := &mockMutation{op: ent.OpUpdate, typ: "Item", ids: nil}

	value, err := mutator.Mutate(context.Background(), m)

	require.NoError(t, err)
	assert.Equal(t, 0, value)
	assert.False(t, outboxInserted, "outbox should not be inserted for zero-match bulk update")
}

func TestMutationEventHook_BulkDeleteZeroMatches_SkipsEventEmission(t *testing.T) {
	t.Parallel()

	outboxInserted := false

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
			outboxInserted = true
			return nil
		},
	})

	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return 0, nil // Bulk delete returns affected count
	})

	mutator := hook(next)
	m := &mockMutation{op: ent.OpDelete, typ: "Item", ids: nil}

	value, err := mutator.Mutate(context.Background(), m)

	require.NoError(t, err)
	assert.Equal(t, 0, value)
	assert.False(t, outboxInserted, "outbox should not be inserted for zero-match bulk delete")
}

func TestMutationEventHook_UpdateOneZeroMatches_ReturnsError(t *testing.T) {
	t.Parallel()

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
	})

	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		t.Fatal("next should not be called when entity ID extraction fails for UpdateOne")
		panic("unreachable")
	})

	mutator := hook(next)
	// OpUpdateOne with zero IDs should still fail — it must match exactly one entity.
	m := &mockMutation{op: ent.OpUpdateOne, typ: "Item", ids: nil}

	_, err := mutator.Mutate(context.Background(), m)

	require.Error(t, err)
	assert.ErrorIs(t, err, events.ErrExtractEntityID)
}

func TestMutationEventHook_DeleteOneZeroMatches_ReturnsError(t *testing.T) {
	t.Parallel()

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
	})

	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		t.Fatal("next should not be called when entity ID extraction fails for DeleteOne")
		panic("unreachable")
	})

	mutator := hook(next)
	// OpDeleteOne with zero IDs should still fail — it must match exactly one entity.
	m := &mockMutation{op: ent.OpDeleteOne, typ: "Item", ids: nil}

	_, err := mutator.Mutate(context.Background(), m)

	require.Error(t, err)
	assert.ErrorIs(t, err, events.ErrExtractEntityID)
}

func TestSendFieldChangeEvents(t *testing.T) {
	t.Parallel()

	testID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	testTenantID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	uuid1 := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	uuid2 := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	time1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	time2 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	baseEventMsg := events.MutationEventMessage{
		Service:  "test-service",
		Type:     "test-serviceTestEntity",
		Schema:   "TestEntity",
		ID:       testID,
		TenantID: testTenantID,
	}

	tests := []struct {
		name        string
		before      any
		after       any
		setupMock   func(*mockPublisher)
		wantEvents  []expectedUpdateEvent
		wantErr     bool
		errContains string
	}{
		// Category 1: No Changes
		{
			name: "identical entities - no events",
			before: &testEntity{
				ID:       uuid1,
				TenantID: testTenantID,
				Name:     "same",
				Age:      30,
			},
			after: &testEntity{
				ID:       uuid1,
				TenantID: testTenantID,
				Name:     "same",
				Age:      30,
			},
			wantEvents: []expectedUpdateEvent{},
		},

		// Category 2: Simple Field Changes
		{
			name:   "string field changed",
			before: &testEntity{Name: "old name"},
			after:  &testEntity{Name: "new name"},
			wantEvents: []expectedUpdateEvent{
				{attribute: "name", oldValue: "old name", newValue: "new name"},
			},
		},
		{
			name:   "integer field changed",
			before: &testEntity{Age: 30},
			after:  &testEntity{Age: 31},
			wantEvents: []expectedUpdateEvent{
				{attribute: "age", oldValue: 30, newValue: 31},
			},
		},
		{
			name:   "boolean field changed",
			before: &testEntity{Active: false},
			after:  &testEntity{Active: true},
			wantEvents: []expectedUpdateEvent{
				{attribute: "active", oldValue: false, newValue: true},
			},
		},
		{
			name:   "time field changed",
			before: &testEntity{CreatedAt: time1},
			after:  &testEntity{CreatedAt: time2},
			wantEvents: []expectedUpdateEvent{
				{attribute: "createdat", oldValue: time1, newValue: time2},
			},
		},
		{
			name:   "uuid field changed",
			before: &testEntity{ID: uuid1},
			after:  &testEntity{ID: uuid2},
			wantEvents: []expectedUpdateEvent{
				{attribute: "id", oldValue: uuid1, newValue: uuid2},
			},
		},

		// Category 3: Multiple Field Changes
		{
			name: "two fields changed",
			before: &testEntity{
				Name: "old",
				Age:  30,
			},
			after: &testEntity{
				Name: "new",
				Age:  35,
			},
			wantEvents: []expectedUpdateEvent{
				{attribute: "name", oldValue: "old", newValue: "new"},
				{attribute: "age", oldValue: 30, newValue: 35},
			},
		},
		{
			name: "all fields changed",
			before: &testEntity{
				Name:   "old",
				Age:    30,
				Active: false,
			},
			after: &testEntity{
				Name:   "new",
				Age:    35,
				Active: true,
			},
			wantEvents: []expectedUpdateEvent{
				{attribute: "name", oldValue: "old", newValue: "new"},
				{attribute: "age", oldValue: 30, newValue: 35},
				{attribute: "active", oldValue: false, newValue: true},
			},
		},

		// Category 4: Map Field Changes (Data Field)
		// Uses testEntityWithData to avoid typeInfoCache conflicts with other tests
		{
			name: "map value changed",
			before: &testEntityWithData{
				Data: map[string]any{
					"field1": "value1",
					"field2": 42,
				},
			},
			after: &testEntityWithData{
				Data: map[string]any{
					"field1": "value1_changed",
					"field2": 42,
				},
			},
			wantEvents: []expectedUpdateEvent{
				{
					attribute: "data",
					oldValue: map[string]any{
						"field1": "value1",
					},
					newValue: map[string]any{
						"field1": "value1_changed",
					},
				},
			},
		},
		{
			name: "map key added",
			before: &testEntityWithData{
				Data: map[string]any{
					"field1": "value1",
				},
			},
			after: &testEntityWithData{
				Data: map[string]any{
					"field1": "value1",
					"field2": "new_value",
				},
			},
			wantEvents: []expectedUpdateEvent{
				{
					attribute: "data",
					oldValue: map[string]any{
						"field2": nil,
					},
					newValue: map[string]any{
						"field2": "new_value",
					},
				},
			},
		},
		{
			name: "map key removed",
			before: &testEntityWithData{
				Data: map[string]any{
					"field1": "value1",
					"field2": "value2",
				},
			},
			after: &testEntityWithData{
				Data: map[string]any{
					"field1": "value1",
				},
			},
			wantEvents: []expectedUpdateEvent{
				{
					attribute: "data",
					oldValue: map[string]any{
						"field2": "value2",
					},
					newValue: map[string]any{
						"field2": nil,
					},
				},
			},
		},
		{
			name: "map unchanged",
			before: &testEntityWithData{
				Data: map[string]any{"field1": "value1"},
			},
			after: &testEntityWithData{
				Data: map[string]any{"field1": "value1"},
			},
			wantEvents: []expectedUpdateEvent{},
		},

		// Category 5: Edge Cases
		{
			name:       "nil before entity",
			before:     (*testEntity)(nil),
			after:      &testEntity{Name: "new"},
			wantEvents: []expectedUpdateEvent{},
		},
		{
			name:       "nil after entity",
			before:     &testEntity{Name: "old"},
			after:      (*testEntity)(nil),
			wantEvents: []expectedUpdateEvent{},
		},
		{
			name:       "both nil",
			before:     (*testEntity)(nil),
			after:      (*testEntity)(nil),
			wantEvents: []expectedUpdateEvent{},
		},
		{
			name:        "non-struct type",
			before:      "string",
			after:       "string",
			wantErr:     true,
			errContains: "struct",
		},
		{
			name:        "type mismatch",
			before:      &testEntity{},
			after:       &differentEntity{},
			wantErr:     true,
			errContains: "same type",
		},

		// Category 6: Publisher Errors
		{
			name:   "publisher error",
			before: &testEntity{Name: "old"},
			after:  &testEntity{Name: "new"},
			setupMock: func(m *mockPublisher) {
				m.sendUpdateEventErr = errors.New("publish failed")
			},
			wantErr:     true,
			errContains: "publish failed",
		},
		{
			name:   "publisher error on second event",
			before: &testEntity{Name: "old", Age: 30},
			after:  &testEntity{Name: "new", Age: 31},
			setupMock: func(m *mockPublisher) {
				m.sendUpdateEventErr = errors.New("publish failed")
				m.sendUpdateEventErrAt = 1 // Fail after first success
			},
			wantErr: true,
		},

		// Category 7: Field Name Handling
		{
			name:   "field name is lowercased",
			before: &testEntity{Name: "old"},
			after:  &testEntity{Name: "new"},
			wantEvents: []expectedUpdateEvent{
				{attribute: "name", oldValue: "old", newValue: "new"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			mockPub := newMockPublisher()

			if tt.setupMock != nil {
				tt.setupMock(mockPub)
			}

			err := events.SendFieldChangeEvents(ctx, mockPub, baseEventMsg, tt.before, tt.after)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Len(t, mockPub.events, len(tt.wantEvents), "event count mismatch")

			// Build map by attribute for order-independent assertion
			gotByAttr := make(map[string]*events.UpdateEventMessage)
			for _, evt := range mockPub.events {
				gotByAttr[evt.Attribute] = evt
			}

			for _, want := range tt.wantEvents {
				got, exists := gotByAttr[want.attribute]
				require.True(t, exists, "missing event for attribute: %s", want.attribute)
				data, ok := got.Data.(events.UpdateAttributeDetails)
				require.True(t, ok, "Data should be UpdateAttributeDetails for attribute: %s", want.attribute)
				assert.Equal(t, want.oldValue, data.OldValue, "oldValue mismatch for %s", want.attribute)
				assert.Equal(t, want.newValue, data.NewValue, "newValue mismatch for %s", want.attribute)
			}

			// Verify base event fields
			for _, evt := range mockPub.events {
				assert.Equal(t, baseEventMsg.Service, evt.Service)
				assert.Equal(t, baseEventMsg.Schema, evt.Schema)
				assert.Equal(t, baseEventMsg.ID, evt.ID)
				assert.Equal(t, baseEventMsg.TenantID, evt.TenantID)
			}
		})
	}
}

func TestSendFieldChangeEventsAsync_SyncMode(t *testing.T) {
	t.Parallel()

	ctx := feature.Context(context.Background(), feature.FEATURE_SYNC_UPDATES)
	mockPub := newMockPublisher()
	mockPub.sendUpdateEventErr = errors.New("intentional test error")

	eventMsg := events.MutationEventMessage{
		Service:  "test-service",
		Type:     "test-serviceTestEntity",
		Schema:   "TestEntity",
		ID:       uuid.New(),
		TenantID: uuid.New(),
	}

	before := &testEntity{Name: "old"}
	after := &testEntity{Name: "new"}

	// Should return error in sync mode
	err := events.SendFieldChangeEventsAsync(ctx, mockPub, eventMsg, before, after)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "intentional test error")
}

func TestSendFieldChangeEventsAsync_SyncModeSuccess(t *testing.T) {
	t.Parallel()

	ctx := feature.Context(context.Background(), feature.FEATURE_SYNC_UPDATES)
	mockPub := newMockPublisher()

	eventMsg := events.MutationEventMessage{
		Service:  "test-service",
		Type:     "test-serviceTestEntity",
		Schema:   "TestEntity",
		ID:       uuid.New(),
		TenantID: uuid.New(),
	}

	before := &testEntity{Name: "old"}
	after := &testEntity{Name: "new"}

	// Should return nil in sync mode when successful
	err := events.SendFieldChangeEventsAsync(ctx, mockPub, eventMsg, before, after)

	require.NoError(t, err)
	assert.Len(t, mockPub.events, 1)
}

func TestSendFieldChangeEventsAsync_AsyncModeReturnsNil(t *testing.T) {
	t.Parallel()

	ctx := context.Background() // No FEATURE_SYNC_UPDATES
	mockPub := newMockPublisher()
	mockPub.sendUpdateEventErr = errors.New("intentional test error")

	eventMsg := events.MutationEventMessage{
		Service:  "test-service",
		Type:     "test-serviceTestEntity",
		Schema:   "TestEntity",
		ID:       uuid.New(),
		TenantID: uuid.New(),
	}

	before := &testEntity{Name: "old"}
	after := &testEntity{Name: "new"}

	// Should return nil immediately in async mode (doesn't block)
	err := events.SendFieldChangeEventsAsync(ctx, mockPub, eventMsg, before, after)
	require.NoError(t, err) // Returns nil, error logged asynchronously

	// Wait for goroutine to complete
	time.Sleep(100 * time.Millisecond)
}

func TestGetUpdatedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		before  any
		after   any
		wantLen int
		wantErr bool
	}{
		{
			name:    "no changes",
			before:  &testEntity{Name: "same", Age: 30},
			after:   &testEntity{Name: "same", Age: 30},
			wantLen: 0,
		},
		{
			name:    "one change",
			before:  &testEntity{Name: "old"},
			after:   &testEntity{Name: "new"},
			wantLen: 1,
		},
		{
			name:    "multiple changes",
			before:  &testEntity{Name: "old", Age: 30, Active: false},
			after:   &testEntity{Name: "new", Age: 35, Active: true},
			wantLen: 3,
		},
		{
			name:    "map data field changes",
			before:  &testEntityWithData{Data: map[string]any{"key": "old"}},
			after:   &testEntityWithData{Data: map[string]any{"key": "new"}},
			wantLen: 1,
		},
		{
			name:    "nil before",
			before:  (*testEntity)(nil),
			after:   &testEntity{Name: "new"},
			wantLen: 0,
		},
		{
			name:    "nil after",
			before:  &testEntity{Name: "old"},
			after:   (*testEntity)(nil),
			wantLen: 0,
		},
		{
			name:    "non-struct",
			before:  42,
			after:   42,
			wantErr: true,
		},
		{
			name:    "type mismatch",
			before:  &testEntity{},
			after:   &differentEntity{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resultLen, err := events.GetUpdatedFields(tt.before, tt.after)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantLen, resultLen)
		})
	}
}

func TestGetChangedMapValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		oldMap   map[string]any
		newMap   map[string]any
		wantKeys []string
	}{
		{
			name:     "value changed",
			oldMap:   map[string]any{"key": "old"},
			newMap:   map[string]any{"key": "new"},
			wantKeys: []string{"key"},
		},
		{
			name:     "key added",
			oldMap:   map[string]any{},
			newMap:   map[string]any{"key": "new"},
			wantKeys: []string{"key"},
		},
		{
			name:     "key removed",
			oldMap:   map[string]any{"key": "old"},
			newMap:   map[string]any{},
			wantKeys: []string{"key"},
		},
		{
			name:     "no changes",
			oldMap:   map[string]any{"key": "same"},
			newMap:   map[string]any{"key": "same"},
			wantKeys: []string{},
		},
		{
			name:     "multiple changes",
			oldMap:   map[string]any{"a": 1, "b": 2, "c": 3},
			newMap:   map[string]any{"a": 1, "b": 99, "d": 4},
			wantKeys: []string{"b", "c", "d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := events.GetChangedMapValues(tt.oldMap, tt.newMap)

			assert.Len(t, result, len(tt.wantKeys))
			for _, key := range tt.wantKeys {
				_, exists := result[key]
				assert.True(t, exists, "missing key: %s", key)
			}
		})
	}
}

func TestMutationEventHook_SystemUserNoTenant_SkipsOutbox(t *testing.T) {
	t.Parallel()

	outboxInserted := false
	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
			outboxInserted = true
			return nil
		},
	})

	// next returns a differentEntity (no TenantID field) — simulates an entity
	// without TenantMixin (like the Tenant schema itself).
	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return &differentEntity{ID: entityID, Code: "test"}, nil
	})

	mutator := hook(next)
	m := &mockMutation{op: ent.OpUpdateOne, typ: "Tenant", ids: []uuid.UUID{entityID}}

	// System user context without tenant — exactly the scenario that caused the panic.
	// OTel trace context is required by buildOutboxEntry for correlation ID.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()
	ctx = authn.Context(ctx, authn.SystemUser())

	value, err := mutator.Mutate(ctx, m)

	require.NoError(t, err)
	assert.NotNil(t, value)
	assert.False(t, outboxInserted, "outbox should not be inserted when no tenant is determinable")
}
