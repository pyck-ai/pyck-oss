package events_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
)

// =============================================================================
// DLQ TOPIC TESTS
// =============================================================================

func TestDeadLetterEventTopic_String(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	entityID := uuid.New()
	topic := events.DeadLetterEventTopic{
		StreamName:    "pyck",
		TenantID:      tenantID,
		ServiceName:   "inventory",
		SchemaName:    "Stock",
		EntityID:      entityID,
		OperationName: "update",
	}

	s := topic.String()

	// Mirrors the CRUD topic schema but with the "dlq" segment instead of "crud".
	assert.True(t, strings.HasPrefix(s, "pyck."), "topic should start with the stream name: %s", s)
	assert.Contains(t, s, ".dlq.", "topic should carry the dlq segment: %s", s)
	assert.NotContains(t, s, ".crud.", "topic must not use the crud segment: %s", s)
	assert.Contains(t, s, "inventory")
	assert.Contains(t, s, "stock")
	assert.Contains(t, s, strings.ToLower(tenantID.String()))
	assert.Contains(t, s, strings.ToLower(entityID.String()))
	assert.Equal(t, events.TopicTypeDeadLetterEvent, topic.Type())
}

// =============================================================================
// DLQ SQL TESTS
// =============================================================================

func TestSelectDeadSQL(t *testing.T) {
	t.Parallel()

	query, _ := events.SelectDeadSQL("inventory.event_outbox", 100)

	assert.True(t, strings.HasPrefix(query, "SELECT"), "expected SELECT, got: %s", query)
	// Dead-but-unpublished rows only.
	assert.Contains(t, query, "dead_at")
	assert.Contains(t, query, "IS NOT NULL")
	assert.Contains(t, query, "published_at")
	assert.Contains(t, query, "LIMIT")
	assert.Contains(t, query, `"inventory"`)
	// Rows must be locked so exactly one replica drains each.
	assert.Contains(t, query, "FOR UPDATE")
	assert.Contains(t, query, "SKIP LOCKED")
}

func TestDeleteByIDsSQL(t *testing.T) {
	t.Parallel()

	ids := []uuid.UUID{uuid.New(), uuid.New()}
	query, args := events.DeleteByIDsSQL("inventory.event_outbox", ids)

	assert.True(t, strings.HasPrefix(query, "DELETE"), "expected DELETE, got: %s", query)
	assert.Contains(t, query, "id")
	assert.Contains(t, query, `"inventory"`)

	for _, id := range ids {
		found := false
		for _, a := range args {
			if got, ok := a.(uuid.UUID); ok && got == id {
				found = true
				break
			}
		}
		assert.True(t, found, "expected id %s in delete args", id)
	}
}

// =============================================================================
// DLQ DRAIN PUBLISH TESTS
// =============================================================================

func deadRow(t *testing.T, tenantID uuid.UUID) events.OutboxRow {
	t.Helper()
	payload, err := json.Marshal(events.MutationEventMessage{
		Service:   "inventory",
		Schema:    "Stock",
		Operation: "update",
		ID:        uuid.New(),
		TenantID:  tenantID,
	})
	require.NoError(t, err)
	return events.NewOutboxRowForTest(uuid.New(), uuid.New(), "orig.topic", payload, false, 10, "Stock")
}

func TestPublishDeadLetters_AcksOnPublishSuccess(t *testing.T) {
	t.Parallel()

	publisher := &outboxMockPublisher{}
	tenantID := uuid.New()
	rows := []events.OutboxRow{deadRow(t, tenantID), deadRow(t, tenantID)}

	acked := events.PublishDeadLettersForTest(context.Background(), "inventory", "pyck", rows, publisher)

	require.Len(t, acked, 2, "both rows should be acked when the DLQ publish succeeds")

	// Each must have been published to a dlq topic with a DLQ-namespaced
	// deterministic msgID (distinct from the original CRUD-subject publish so it
	// cannot be deduped away by a prior publish in the same stream).
	calls := publisher.getPublishCalls()
	require.Len(t, calls, 2)
	for _, c := range calls {
		assert.Contains(t, c.Topic, ".dlq.", "must publish to the DLQ topic: %s", c.Topic)
		assert.True(t, strings.HasPrefix(c.MsgID, "dlq-"), "DLQ publish msgID must be dlq-namespaced: %s", c.MsgID)
	}
}

func TestPublishDeadLetters_LeavesRowOnPublishFailure(t *testing.T) {
	t.Parallel()

	publisher := &outboxMockPublisher{publishRawErr: assert.AnError}
	rows := []events.OutboxRow{deadRow(t, uuid.New())}

	acked := events.PublishDeadLettersForTest(context.Background(), "inventory", "pyck", rows, publisher)

	assert.Empty(t, acked, "a row whose DLQ publish fails must not be acked (so it is retried)")
}

func TestPublishDeadLetters_SkipsUnparseablePayload(t *testing.T) {
	t.Parallel()

	publisher := &outboxMockPublisher{}
	bad := events.NewOutboxRowForTest(uuid.New(), uuid.New(), "orig.topic", []byte("not json"), false, 10, "Stock")

	acked := events.PublishDeadLettersForTest(context.Background(), "inventory", "pyck", []events.OutboxRow{bad}, publisher)

	assert.Empty(t, acked, "a row with an unparseable payload must be skipped")
	assert.Empty(t, publisher.getPublishCalls(), "no publish should be attempted for an unparseable payload")
}
