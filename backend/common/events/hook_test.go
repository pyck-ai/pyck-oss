package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/test/pgtest"
	"github.com/pyck-ai/pyck/backend/common/txid"
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

	// Mirror the production OTel global propagator wiring (see
	// backend/common/otel/provider.go) so carrier round-trip tests work.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	var terminate func()
	pgHandle, terminate = pgtest.Start("events")
	code := m.Run()
	terminate()
	os.Exit(code)
}

// pgHandle is the shared Postgres test container (nil when SKIP_PG_TESTS=1).
var pgHandle *pgtest.Handle

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

func (m *mockPublisher) SendCustomEvent(_ context.Context, _ *events.CustomEventMessage) error {
	return nil
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

func TestMutationEventHook_OutboxEntryTimestampIsUTC(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	var captured *events.OutboxEntry

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, entry *events.OutboxEntry) error {
			captured = entry
			return nil
		},
	})

	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return &testEntity{ID: entityID, TenantID: tenantID, Name: "test"}, nil
	})

	mutator := hook(next)
	m := &mockMutation{op: ent.OpCreate, typ: "TestEntity", ids: []uuid.UUID{entityID}}

	// buildOutboxEntry requires a transaction ID on ctx (the gqltx
	// middleware would install this at BeginTx in production). An OTel
	// trace context is optional now — it only feeds the trace_id
	// observability column.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()
	ctx = authn.Context(ctx, &authn.User{
		ID:       uuid.New(),
		TenantID: tenantID,
		Roles:    map[uuid.UUID]authn.Role{tenantID: authn.ROLE_WRITER},
	})
	ctx = txid.With(ctx, txid.New())

	_, err := mutator.Mutate(ctx, m)
	require.NoError(t, err)
	require.NotNil(t, captured, "outbox entry should have been captured")
	assert.Equal(t, time.UTC, captured.CreatedAt.Location(),
		"outbox entry CreatedAt should be in UTC")
}

// TestMutationEventHook_StampsTransactionIDSearchAttribute verifies that the
// per-tx UUID is stamped into the event's wf_search_attributes so workflows
// started from this event carry the pyck_transaction_id search attribute —
// the key clients use to look up the executions a mutation triggered. The
// stamp must win over any context-provided attribute of the same name.
func TestMutationEventHook_StampsTransactionIDSearchAttribute(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	var captured *events.OutboxEntry

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, entry *events.OutboxEntry) error {
			captured = entry
			return nil
		},
	})

	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return &testEntity{ID: entityID, TenantID: tenantID, Name: "test"}, nil
	})

	mutator := hook(next)
	m := &mockMutation{op: ent.OpCreate, typ: "TestEntity", ids: []uuid.UUID{entityID}}

	ctx := authn.Context(context.Background(), &authn.User{
		ID:       uuid.New(),
		TenantID: tenantID,
		Roles:    map[uuid.UUID]authn.Role{tenantID: authn.ROLE_WRITER},
	})
	// A context-provided attribute must not be able to spoof the txid.
	ctx = events.WithExtraSearchAttribute(ctx, "pyck_transaction_id", "spoofed")
	transactionID := txid.New()
	ctx = txid.With(ctx, transactionID)

	_, err := mutator.Mutate(ctx, m)
	require.NoError(t, err)
	require.NotNil(t, captured, "outbox entry should have been captured")

	var msg events.MutationEventMessage
	require.NoError(t, json.Unmarshal(captured.Payload, &msg))
	assert.Equal(t, transactionID.String(), msg.WfSearchAttributes["pyck_transaction_id"],
		"the per-tx UUID must be stamped last so nothing can clobber it")
}

// TestMutationEventHook_PayloadCarriesOutboxEntryID pins that the published
// payload's event_id is the outbox row's own ID (not the entity ID, which two
// updates of one entity share), so a consumer can tell events apart and
// recognise a redelivery of the same one.
func TestMutationEventHook_PayloadCarriesOutboxEntryID(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000021")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000022")

	var captured []*events.OutboxEntry

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, entry *events.OutboxEntry) error {
			captured = append(captured, entry)
			return nil
		},
	})

	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return &testEntity{ID: entityID, TenantID: tenantID, Name: "test"}, nil
	})

	ctx := authn.Context(context.Background(), &authn.User{
		ID:       uuid.New(),
		TenantID: tenantID,
		Roles:    map[uuid.UUID]authn.Role{tenantID: authn.ROLE_WRITER},
	})
	ctx = txid.With(ctx, txid.New())

	// Two updates of the same entity.
	for range 2 {
		m := &mockMutation{op: ent.OpUpdateOne, typ: "TestEntity", ids: []uuid.UUID{entityID}}
		_, err := hook(next).Mutate(ctx, m)
		require.NoError(t, err)
	}

	require.Len(t, captured, 2)

	seen := map[uuid.UUID]bool{}

	for _, entry := range captured {
		var msg events.MutationEventMessage
		require.NoError(t, json.Unmarshal(entry.Payload, &msg))

		assert.Equal(t, entry.ID, msg.EventID, "event_id must equal the outbox row ID")
		assert.Equal(t, entityID, msg.ID, "id stays the entity ID")
		assert.Equal(t, uuid.Version(7), msg.EventID.Version(), "event_id is a UUIDv7")

		seen[msg.EventID] = true
	}

	assert.Len(t, seen, 2, "two events of one entity get two event IDs")
}

func TestMutationEventHook_SuppressEvents_SkipsOutbox(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000012")

	outboxInserted := false
	fetcherCalled := false

	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		EntityFetcher: func(_ context.Context, _ string, _ uuid.UUID) (any, error) {
			fetcherCalled = true
			return nil, nil //nolint:nilnil // test stub
		},
		OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
			outboxInserted = true
			return nil
		},
	})

	mutated := false
	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		mutated = true
		return &testEntity{ID: entityID, TenantID: tenantID, Name: "test"}, nil
	})

	mutator := hook(next)
	m := &mockMutation{op: ent.OpUpdate, typ: "TestEntity", ids: []uuid.UUID{entityID}}

	// With the suppress feature set, the hook must run the mutation but skip the
	// before-state fetch and the outbox write entirely — no transaction ID on
	// ctx is required because no outbox entry is built.
	ctx := feature.Context(context.Background(), feature.FEATURE_SUPPRESS_EVENTS)

	value, err := mutator.Mutate(ctx, m)
	require.NoError(t, err)
	assert.True(t, mutated, "underlying mutation must still run")
	assert.False(t, outboxInserted, "outbox must not be written when update events are suppressed")
	assert.False(t, fetcherCalled, "before-state fetch must be skipped when update events are suppressed")
	_ = value
}

// TestMutationEventHook_CountsEventsPerTransaction verifies the tally behind
// the eventCount field of a mutation result: every outbox row the hook writes
// under a transaction is counted on that transaction's context, and none are
// when FEATURE_SUPPRESS_EVENTS bypasses the hook.
//
//nolint:tparallel,paralleltest // subtests share the hook's `inserted` counter and run sequentially
func TestMutationEventHook_CountsEventsPerTransaction(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000021")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000022")

	inserted := 0
	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
			inserted++
			return nil
		},
	})
	mutator := hook(ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return &testEntity{ID: entityID, TenantID: tenantID, Name: "test"}, nil
	}))
	m := &mockMutation{op: ent.OpCreate, typ: "TestEntity", ids: []uuid.UUID{entityID}}

	txCtx := func() context.Context {
		ctx := authn.Context(context.Background(), &authn.User{
			ID:       uuid.New(),
			TenantID: tenantID,
			Roles:    map[uuid.UUID]authn.Role{tenantID: authn.ROLE_WRITER},
		})
		return txid.With(ctx, txid.New())
	}

	t.Run("N events are reported as N", func(t *testing.T) {
		inserted = 0
		ctx := txCtx()
		require.Zero(t, gqltx.EventCount(ctx))

		for range 3 {
			_, err := mutator.Mutate(ctx, m)
			require.NoError(t, err)
		}

		assert.Equal(t, 3, inserted)
		assert.Equal(t, 3, gqltx.EventCount(ctx))
	})

	t.Run("a failed insert is not counted", func(t *testing.T) {
		failing := events.MutationEventHook(events.HookConfig{
			Service:    "test",
			StreamName: "test-stream",
			OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
				return errors.New("insert failed")
			},
		})(ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
			return &testEntity{ID: entityID, TenantID: tenantID, Name: "test"}, nil
		}))
		ctx := txCtx()

		_, err := failing.Mutate(ctx, m)
		require.Error(t, err)
		assert.Zero(t, gqltx.EventCount(ctx))
	})

	t.Run("suppressed events are reported as 0", func(t *testing.T) {
		inserted = 0
		ctx := feature.Context(txCtx(), feature.FEATURE_SUPPRESS_EVENTS)

		for range 3 {
			_, err := mutator.Mutate(ctx, m)
			require.NoError(t, err)
		}

		assert.Zero(t, inserted)
		assert.Zero(t, gqltx.EventCount(ctx))
	})

	t.Run("transactions count separately", func(t *testing.T) {
		a, b := txCtx(), txCtx()
		_, err := mutator.Mutate(a, m)
		require.NoError(t, err)

		assert.Equal(t, 1, gqltx.EventCount(a))
		assert.Zero(t, gqltx.EventCount(b))
	})
}

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

// systemUserCtx returns a system-user context with a tx ID and no tenant ID,
// the shape of a background activity that forgot request.Context.
func systemUserCtx(t *testing.T) context.Context {
	t.Helper()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()),
	)
	t.Cleanup(func() { assert.NoError(t, tp.Shutdown(context.Background())) })
	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	t.Cleanup(func() { span.End() })
	ctx = authn.Context(ctx, authn.SystemUser())
	return txid.With(ctx, txid.New())
}

// A mutation whose tenant ID cannot be resolved must fail loudly with
// ErrNoTenantForEvent instead of committing with no event.
func TestMutationEventHook_NoResolvableTenant_FailsLoudly(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	for _, tc := range []struct {
		name string
		op   ent.Op
		ids  []uuid.UUID
	}{
		{"UpdateOne", ent.OpUpdateOne, []uuid.UUID{entityID}},
		{"Create", ent.OpCreate, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

			// differentEntity has no TenantID field: not a tenant-scoped entity.
			next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
				return &differentEntity{ID: entityID, Code: "test"}, nil
			})

			m := &mockMutation{op: tc.op, typ: "Widget", ids: tc.ids}
			value, err := hook(next).Mutate(systemUserCtx(t), m)

			require.ErrorIs(t, err, events.ErrNoTenantForEvent,
				"the returned error makes ent roll the surrounding tx back")
			assert.Nil(t, value)
			assert.False(t, outboxInserted, "no outbox row may be written")
		})
	}
}

// The tenant ID from the context (request.Context / tenant.Context) is enough
// for an entity without a tenant_id column.
func TestMutationEventHook_TenantFromContext_Emits(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	var captured *events.OutboxEntry
	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, e *events.OutboxEntry) error {
			captured = e
			return nil
		},
	})
	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return &differentEntity{ID: entityID, Code: "test"}, nil
	})

	ctx := tenant.Context(systemUserCtx(t), tenantID)
	m := &mockMutation{op: ent.OpUpdateOne, typ: "Widget", ids: []uuid.UUID{entityID}}
	_, err := hook(next).Mutate(ctx, m)

	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, tenantID, captured.TenantID)
}

// A schema listed in SelfTenantSchemas is its own tenant: with no tenant in
// the context, the entity ID is the tenant ID.
func TestMutationEventHook_SelfTenantSchema_UsesEntityIDAsTenant(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	var captured *events.OutboxEntry
	hook := events.MutationEventHook(events.HookConfig{
		Service:           "test",
		StreamName:        "test-stream",
		SelfTenantSchemas: []string{"Tenant"},
		OutboxInserter: func(_ context.Context, e *events.OutboxEntry) error {
			captured = e
			return nil
		},
	})
	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		return &differentEntity{ID: entityID, Code: "test"}, nil
	})

	m := &mockMutation{op: ent.OpUpdateOne, typ: "Tenant", ids: []uuid.UUID{entityID}}
	_, err := hook(next).Mutate(systemUserCtx(t), m)

	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, entityID, captured.TenantID, "tenantID == entityID for a self-tenant schema")
	assert.Equal(t, &entityID, captured.EntityID)
	assert.Contains(t, captured.Topic.String(), "."+entityID.String()+".", "topic carries the entity ID")
	assert.True(t, strings.HasSuffix(captured.Topic.String(), ".update"), captured.Topic.String())

	// Self-tenant applies only to listed schemas: another schema still fails.
	other := &mockMutation{op: ent.OpUpdateOne, typ: "Widget", ids: []uuid.UUID{entityID}}
	_, err = hook(next).Mutate(systemUserCtx(t), other)
	require.ErrorIs(t, err, events.ErrNoTenantForEvent)
}

// FEATURE_SUPPRESS_EVENTS bypasses the hook entirely, so a mutation that would
// otherwise fail with ErrNoTenantForEvent still runs and writes nothing.
func TestMutationEventHook_SuppressEvents_SkipsTenantResolution(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	outboxInserted := false
	mutated := false
	hook := events.MutationEventHook(events.HookConfig{
		Service:    "test",
		StreamName: "test-stream",
		OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
			outboxInserted = true
			return nil
		},
	})
	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		mutated = true
		return &differentEntity{ID: entityID, Code: "test"}, nil
	})

	ctx := feature.Context(systemUserCtx(t), feature.FEATURE_SUPPRESS_EVENTS)
	m := &mockMutation{op: ent.OpUpdateOne, typ: "Widget", ids: []uuid.UUID{entityID}}
	_, err := hook(next).Mutate(ctx, m)

	require.NoError(t, err)
	assert.True(t, mutated, "the mutation itself still runs")
	assert.False(t, outboxInserted)
}

// Ent returns the affected row count (an int) from bulk Update()/Delete() and
// from DeleteOne, never the entity. A DeleteOne therefore has no tenant_id in
// its result and takes the tenant from the before-state.
func TestMutationEventHook_DeleteOne_ReturnsInt_TenantFromBeforeState(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	ctxTenantID := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	for _, tc := range []struct {
		name string
		ctx  func(t *testing.T) context.Context
	}{
		{"no context tenant", systemUserCtx},
		{"context tenant differs", func(t *testing.T) context.Context {
			t.Helper()
			return tenant.Context(systemUserCtx(t), ctxTenantID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var captured *events.OutboxEntry
			hook := events.MutationEventHook(events.HookConfig{
				Service:    "test",
				StreamName: "test-stream",
				EntityFetcher: func(_ context.Context, _ string, _ uuid.UUID) (any, error) {
					return &testEntity{ID: entityID, TenantID: tenantID, Name: "test"}, nil
				},
				OutboxInserter: func(_ context.Context, e *events.OutboxEntry) error {
					captured = e
					return nil
				},
			})
			next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
				return 1, nil
			})

			m := &mockMutation{op: ent.OpDeleteOne, typ: "Widget", ids: []uuid.UUID{entityID}}
			value, err := hook(next).Mutate(tc.ctx(t), m)

			require.NoError(t, err)
			assert.Equal(t, 1, value)
			require.NotNil(t, captured)
			assert.Equal(t, tenantID, captured.TenantID, "the before-state tenant wins over the context tenant")
			assert.True(t, strings.HasSuffix(captured.Topic.String(), ".delete"), captured.Topic.String())
		})
	}
}

// Rule P3: a bulk Update()/Delete() that matches rows fails before it runs.
func TestMutationEventHook_BulkMatchingRows_FailsBeforeExecuting(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	for _, tc := range []struct {
		name string
		op   ent.Op
		ids  []uuid.UUID
	}{
		{"bulk Update, one row", ent.OpUpdate, []uuid.UUID{entityID}},
		{"bulk Delete, one row", ent.OpDelete, []uuid.UUID{entityID}},
		{"bulk Update, two rows", ent.OpUpdate, []uuid.UUID{entityID, uuid.New()}},
		{"bulk Delete, two rows", ent.OpDelete, []uuid.UUID{entityID, uuid.New()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				t.Fatal("a bulk mutation matching rows must not run")
				panic("unreachable")
			})

			// Even with a resolvable tenant: the rule does not depend on it.
			ctx := tenant.Context(systemUserCtx(t), tenantID)
			m := &mockMutation{op: tc.op, typ: "Widget", ids: tc.ids}
			_, err := hook(next).Mutate(ctx, m)

			require.ErrorIs(t, err, events.ErrBulkMutationEmitsNoEvent)
			assert.False(t, outboxInserted)
		})
	}
}

// A bulk write that matches nothing changes nothing: it runs, returns its
// count, and emits no event and no error.
func TestMutationEventHook_BulkNoMatches_RunsWithoutEvent(t *testing.T) {
	t.Parallel()

	for _, op := range []ent.Op{ent.OpUpdate, ent.OpDelete} {
		t.Run(op.String(), func(t *testing.T) {
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
			ran := false
			next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
				ran = true
				return 0, nil
			})

			m := &mockMutation{op: op, typ: "Widget", ids: nil}
			value, err := hook(next).Mutate(systemUserCtx(t), m)

			require.NoError(t, err)
			assert.Equal(t, 0, value)
			assert.True(t, ran)
			assert.False(t, outboxInserted)
		})
	}
}

// Suppressed events lift the bulk rule: the write runs even when rows match.
func TestMutationEventHook_BulkMatchingRows_SuppressedRuns(t *testing.T) {
	t.Parallel()

	hook := events.MutationEventHook(events.HookConfig{Service: "test", StreamName: "test-stream"})
	ran := false
	next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
		ran = true
		return 2, nil
	})

	ctx := feature.Context(context.Background(), feature.FEATURE_SUPPRESS_EVENTS)
	m := &mockMutation{op: ent.OpDelete, typ: "Widget", ids: []uuid.UUID{uuid.New(), uuid.New()}}
	value, err := hook(next).Mutate(ctx, m)

	require.NoError(t, err)
	assert.Equal(t, 2, value)
	assert.True(t, ran)
}

// A context whose single tenant is uuid.Nil resolves nothing: the mutation
// still fails with ErrNoTenantForEvent.
func TestMutationEventHook_NilContextTenant_FailsLoudly(t *testing.T) {
	t.Parallel()

	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

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
		return 1, nil
	})

	ctx := tenant.Context(systemUserCtx(t), uuid.Nil)
	m := &mockMutation{op: ent.OpDeleteOne, typ: "Widget", ids: []uuid.UUID{entityID}}
	_, err := hook(next).Mutate(ctx, m)

	require.ErrorIs(t, err, events.ErrNoTenantForEvent)
	assert.False(t, outboxInserted)
}

// IdempotencyKey rows are infrastructure: create, update and delete run
// through the hook without a transaction ID, a tenant or an outbox row.
func TestMutationEventHook_IdempotencyKey_NeverEmits(t *testing.T) {
	t.Parallel()

	keyID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	for _, op := range []ent.Op{ent.OpCreate, ent.OpUpdate, ent.OpUpdateOne, ent.OpDelete, ent.OpDeleteOne} {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()

			outboxInserted := false
			fetcherCalled := false
			hook := events.MutationEventHook(events.HookConfig{
				Service:    "test",
				StreamName: "test-stream",
				EntityFetcher: func(_ context.Context, _ string, _ uuid.UUID) (any, error) {
					fetcherCalled = true
					return nil, nil //nolint:nilnil // test stub
				},
				OutboxInserter: func(_ context.Context, _ *events.OutboxEntry) error {
					outboxInserted = true
					return nil
				},
			})
			ran := false
			next := ent.MutateFunc(func(_ context.Context, _ ent.Mutation) (ent.Value, error) {
				ran = true
				return 1, nil
			})

			// Matching ids on a bulk op: the bulk rule must not apply either.
			m := &mockMutation{op: op, typ: "IdempotencyKey", ids: []uuid.UUID{keyID}}
			_, err := hook(next).Mutate(context.Background(), m)

			require.NoError(t, err)
			assert.True(t, ran)
			assert.False(t, outboxInserted, "no outbox row for an IdempotencyKey write")
			assert.False(t, fetcherCalled)
		})
	}
}
