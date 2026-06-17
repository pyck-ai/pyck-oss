package events

import (
	"context"
	"database/sql"
	"reflect"
	"time"

	"github.com/google/uuid"
)

// Export internal functions for testing in events_test package.
// This follows the standard Go pattern for white-box testing.

// RegisterTestType pre-registers a type in the typeInfoCache for testing.
// dataField specifies which field should receive special JSON map comparison treatment.
func RegisterTestType(value any, dataField string) {
	t := reflect.TypeOf(value)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	info := buildTypeInfo(t, dataField)
	typeInfoCache.Store(t, info)
}

// SendFieldChangeEvents wraps the internal sendFieldChangeEvents function.
var SendFieldChangeEvents = sendFieldChangeEvents

// SendFieldChangeEventsAsync wraps the internal sendFieldChangeEventsAsync function.
var SendFieldChangeEventsAsync = sendFieldChangeEventsAsync

// GetChangedMapValues wraps the internal getChangedMapValues function.
var GetChangedMapValues = getChangedMapValues

// GetUpdatedFields wraps the internal getUpdatedFields function.
// Returns the number of changed fields (length of the result map) for testing.
func GetUpdatedFields(oldObject, newObject any) (int, error) {
	result, err := getUpdatedFields(oldObject, newObject)
	if err != nil {
		return 0, err
	}
	return len(result), nil
}

// Ensure Publisher interface is implemented by test mock.
var _ Publisher = (*testPublisher)(nil)

type testPublisher struct{}

func (testPublisher) SendMutationEvent(context.Context, *MutationEventMessage) error { return nil }
func (testPublisher) SendUpdateEvent(context.Context, *UpdateEventMessage) error     { return nil }
func (testPublisher) SendCustomEvent(context.Context, *CustomEventMessage) error     { return nil }
func (testPublisher) SendMutationEventWithReply(context.Context, *MutationEventMessage) ([]byte, error) {
	return nil, nil
}

func (testPublisher) SendTemporalWorkflowEvent(context.Context, *TemporalWorkflowStateChangeMessage) error {
	return nil
}

func (testPublisher) SendWorkflowEvent(context.Context, *WorkflowEventMessage) error { return nil }
func (testPublisher) PublishRaw(context.Context, string, []byte, string) error       { return nil }

func (testPublisher) RequestRaw(context.Context, string, []byte, time.Duration) (*EventReply, error) {
	return nil, nil //nolint:nilnil // Test mock returns no data, no error
}

// GroupByTransaction exports the internal groupByTransaction function for testing.
var GroupByTransaction = groupByTransaction

// NextPollDelayForTest exposes OutboxHandler.nextPollDelay for testing the poll
// interval jitter.
func NextPollDelayForTest(pollInterval time.Duration) time.Duration {
	h := &OutboxHandler{config: OutboxHandlerConfig{PollInterval: pollInterval}}
	return h.nextPollDelay()
}

// InjectIntoMsg exposes the internal injectIntoMsg helper for testing the
// NATS baggage carrier round-trip.
var InjectIntoMsg = injectIntoMsg

// BuildMessageID exports the OutboxHandler.buildMessageID logic for testing.
// Creates a test handler with the given service name and calls the method.
func BuildMessageID(serviceName string, entry OutboxRow) string {
	h := &OutboxHandler{config: OutboxHandlerConfig{ServiceName: serviceName}}
	return h.buildMessageID(entry)
}

// applyMarkWithFuncs applies a single post-publish mark via the provided mark
// functions, mirroring persistMarks but without needing a real *sql.DB so unit
// tests can pass a nil tx and mock functions.
func applyMarkWithFuncs(
	ctx context.Context,
	tx *sql.Tx,
	mark outboxMark,
	markPublished OutboxMarkPublishedFunc,
	markFailed OutboxMarkFailedFunc,
	markDead OutboxMarkTransactionDeadFunc,
) error {
	switch mark.kind {
	case markKindPublished:
		return markPublished(ctx, tx, mark.id)
	case markKindFailed:
		return markFailed(ctx, tx, mark.id, mark.errMsg)
	case markKindDead:
		if markDead != nil {
			return markDead(ctx, tx, mark.transactionID, mark.errMsg)
		}
	}
	return nil
}

// ProcessEntryForTest exposes the publish-then-persist logic for a single outbox
// entry for unit testing. It publishes the entry (no DB transaction held) and
// then applies the resulting mark via the provided mark functions, returning any
// error from the persist step.
func ProcessEntryForTest(
	ctx context.Context,
	tx *sql.Tx,
	entry OutboxRow,
	publisher Publisher,
	registry *ReplyRegistry,
	markPublished OutboxMarkPublishedFunc,
	markFailed OutboxMarkFailedFunc,
	replyTimeout time.Duration,
) error {
	h := &OutboxHandler{
		config: OutboxHandlerConfig{
			Publisher:     publisher,
			ReplyRegistry: registry,
			ReplyTimeout:  replyTimeout,
			StreamName:    "pyck",
		},
	}
	mark := h.publishEntry(ctx, entry)
	return applyMarkWithFuncs(ctx, tx, mark, markPublished, markFailed, nil)
}

// ProcessTransactionGroupForTest exposes the publish-then-persist logic for a
// transaction group for unit testing. It publishes the group (no DB transaction
// held) and applies the resulting marks via the provided mark functions in
// order, stopping at the first persist error (mirroring persistMarks).
func ProcessTransactionGroupForTest(
	ctx context.Context,
	tx *sql.Tx,
	transactionID uuid.UUID,
	entries []OutboxRow,
	publisher Publisher,
	registry *ReplyRegistry,
	markPublished OutboxMarkPublishedFunc,
	markFailed OutboxMarkFailedFunc,
	markDead OutboxMarkTransactionDeadFunc,
	maxRetries int,
	replyTimeout time.Duration,
) {
	h := &OutboxHandler{
		config: OutboxHandlerConfig{
			Publisher:     publisher,
			ReplyRegistry: registry,
			MaxRetries:    maxRetries,
			ReplyTimeout:  replyTimeout,
			StreamName:    "pyck",
		},
	}
	marks := h.publishTransactionGroup(ctx, transactionID, entries)
	for _, mark := range marks {
		if err := applyMarkWithFuncs(ctx, tx, mark, markPublished, markFailed, markDead); err != nil {
			return
		}
	}
}

// PublishGroupCounts holds the per-kind outcome of publishing a transaction
// group, for asserting metric/dead-letter accounting in tests.
type PublishGroupCounts struct {
	Published int
	Failed    int
	Dead      int
	Dropped   int // total entries dead-lettered (summed droppedCount)
}

// PublishTransactionGroupCountsForTest publishes a transaction group (no DB) and
// returns the counts derived from the resulting marks.
func PublishTransactionGroupCountsForTest(
	ctx context.Context,
	transactionID uuid.UUID,
	entries []OutboxRow,
	publisher Publisher,
	registry *ReplyRegistry,
	maxRetries int,
	replyTimeout time.Duration,
) PublishGroupCounts {
	h := &OutboxHandler{
		config: OutboxHandlerConfig{
			Publisher:     publisher,
			ReplyRegistry: registry,
			MaxRetries:    maxRetries,
			ReplyTimeout:  replyTimeout,
			StreamName:    "pyck",
		},
	}
	var counts PublishGroupCounts
	for _, mark := range h.publishTransactionGroup(ctx, transactionID, entries) {
		switch mark.kind {
		case markKindPublished:
			counts.Published++
		case markKindFailed:
			counts.Failed++
		case markKindDead:
			counts.Dead++
			counts.Dropped += mark.droppedCount
		}
	}
	return counts
}

// NewOutboxRowForTest creates an OutboxRow for testing.
func NewOutboxRowForTest(id, transactionID uuid.UUID, topic string, payload []byte, withReply bool, retryCount int, entityType string) OutboxRow {
	return OutboxRow{
		ID:            id,
		TransactionID: transactionID,
		Topic:         topic,
		Payload:       payload,
		WithReply:     withReply,
		RetryCount:    retryCount,
		EntityType:    &entityType,
	}
}

// The SQL export helpers below delegate to the same query builders the
// production OutboxMark*/OutboxSelector closures use, so the white-box SQL
// assertions exercise the exact production query and cannot drift from it.
// A fixed timestamp is passed where production passes time.Now().UTC(); the
// value is bound as a parameter, so it never appears in the query string.

var fixedSQLTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// MarkFailedSQL returns the SQL query generated by NewOutboxMarkFailed without executing it.
func MarkFailedSQL(tableName string, id uuid.UUID, errMsg string) (string, []any) {
	return markFailedQuery(tableName, id, errMsg)
}

// MarkPublishedSQL returns the SQL query generated by NewOutboxMarkPublished without executing it.
func MarkPublishedSQL(tableName string, id uuid.UUID) (string, []any) {
	return markPublishedQuery(tableName, id, fixedSQLTime)
}

// MarkTransactionDeadSQL returns the SQL query generated by NewOutboxMarkTransactionDead without executing it.
func MarkTransactionDeadSQL(tableName string, transactionID uuid.UUID, reason string) (string, []any) {
	return markTransactionDeadQuery(tableName, transactionID, reason, fixedSQLTime)
}

// ClaimSQL returns the SQL query generated by NewOutboxClaim without executing it.
func ClaimSQL(tableName string, ids []uuid.UUID) (string, []any) {
	return claimQuery(tableName, ids, fixedSQLTime)
}

// SelectorSQL returns the step 1 SQL query generated by NewOutboxSelector without executing it.
func SelectorSQL(tableName string, batchSize, maxRetries int) (string, []any) {
	return selectTransactionIDsQuery(tableName, batchSize, maxRetries, fixedSQLTime)
}

// SelectDeadSQL returns the SQL query generated by NewOutboxSelectDead without executing it.
func SelectDeadSQL(tableName string, batchSize int) (string, []any) {
	return selectDeadRowsQuery(tableName, batchSize)
}

// DeleteByIDsSQL returns the SQL query generated by NewOutboxDelete without executing it.
func DeleteByIDsSQL(tableName string, ids []uuid.UUID) (string, []any) {
	return deleteByIDsQuery(tableName, ids)
}

// PublishDeadLettersForTest runs the DLQ publish-decision loop (no DB) and
// returns the IDs of the rows the publisher accepted.
func PublishDeadLettersForTest(ctx context.Context, serviceName, streamName string, rows []OutboxRow, publisher Publisher) []uuid.UUID {
	h := &OutboxHandler{
		config: OutboxHandlerConfig{
			ServiceName: serviceName,
			StreamName:  streamName,
			Publisher:   publisher,
		},
	}
	acked := h.publishDeadLetters(ctx, rows)
	ids := make([]uuid.UUID, len(acked))
	for i, row := range acked {
		ids[i] = row.ID
	}
	return ids
}
