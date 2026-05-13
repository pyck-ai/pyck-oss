package events

import (
	"context"
	"database/sql"
	"reflect"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	outboxfields "github.com/pyck-ai/pyck/backend/common/internal/ent/mixin"
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

// GroupByCorrelation exports the internal groupByCorrelation function for testing.
var GroupByCorrelation = groupByCorrelation

// InjectIntoMsg exposes the internal injectIntoMsg helper for testing the
// NATS baggage carrier round-trip.
var InjectIntoMsg = injectIntoMsg

// BuildMessageID exports the OutboxHandler.buildMessageID logic for testing.
// Creates a test handler and calls the method.
func BuildMessageID(entry OutboxRow) string {
	h := &OutboxHandler{config: OutboxHandlerConfig{}}
	return h.buildMessageID(entry)
}

// ProcessEntryForTest exposes the processEntry logic for unit testing.
// It processes a single outbox entry with the provided dependencies.
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
			Publisher:           publisher,
			ReplyRegistry:       registry,
			OutboxMarkPublished: markPublished,
			OutboxMarkFailed:    markFailed,
			ReplyTimeout:        replyTimeout,
			StreamName:          "pyck",
		},
	}
	return h.processEntry(ctx, tx, entry)
}

// ProcessCorrelationGroupForTest exposes the processCorrelationGroup logic for unit testing.
func ProcessCorrelationGroupForTest(
	ctx context.Context,
	tx *sql.Tx,
	correlationID string,
	entries []OutboxRow,
	publisher Publisher,
	registry *ReplyRegistry,
	markPublished OutboxMarkPublishedFunc,
	markFailed OutboxMarkFailedFunc,
	markDead OutboxMarkCorrelationDeadFunc,
	maxRetries int,
	replyTimeout time.Duration,
) {
	h := &OutboxHandler{
		config: OutboxHandlerConfig{
			Publisher:                 publisher,
			ReplyRegistry:             registry,
			OutboxMarkPublished:       markPublished,
			OutboxMarkFailed:          markFailed,
			OutboxMarkCorrelationDead: markDead,
			MaxRetries:                maxRetries,
			ReplyTimeout:              replyTimeout,
			StreamName:                "pyck",
		},
	}
	h.processCorrelationGroup(ctx, tx, correlationID, entries)
}

// NewOutboxRowForTest creates an OutboxRow for testing.
func NewOutboxRowForTest(id uuid.UUID, correlationID, topic string, payload []byte, withReply bool, retryCount int, entityType string) OutboxRow {
	return OutboxRow{
		ID:            id,
		CorrelationID: correlationID,
		Topic:         topic,
		Payload:       payload,
		WithReply:     withReply,
		RetryCount:    retryCount,
		EntityType:    &entityType,
	}
}

// MarkFailedSQL returns the SQL query generated by NewOutboxMarkFailed without executing it.
func MarkFailedSQL(tableName string, id uuid.UUID, errMsg string) (string, []any) {
	schema, table := parseTableName(tableName)
	update := entsql.Dialect(dialect.Postgres).
		Update(table).
		Set(outboxfields.RetryCount, entsql.Raw(outboxfields.RetryCount+" + 1")).
		Set(outboxfields.LastError, errMsg).
		Set(outboxfields.NextRetryAt, entsql.Raw(
			"NOW() + (LEAST(POWER(2, "+outboxfields.RetryCount+"), 3600)::integer * INTERVAL '1 second')",
		)).
		Where(entsql.EQ(outboxfields.ID, id))
	if schema != "" {
		update = update.Schema(schema)
	}
	return update.Query()
}

// MarkPublishedSQL returns the SQL query generated by NewOutboxMarkPublished without executing it.
func MarkPublishedSQL(tableName string, id uuid.UUID) (string, []any) {
	schema, table := parseTableName(tableName)
	update := entsql.Dialect(dialect.Postgres).
		Update(table).
		Set(outboxfields.PublishedAt, entsql.Raw("NOW()")).
		SetNull(outboxfields.LastError).
		SetNull(outboxfields.NextRetryAt).
		Where(entsql.EQ(outboxfields.ID, id))
	if schema != "" {
		update = update.Schema(schema)
	}
	return update.Query()
}

// MarkCorrelationDeadSQL returns the SQL query generated by NewOutboxMarkCorrelationDead without executing it.
func MarkCorrelationDeadSQL(tableName string, correlationID string) (string, []any) {
	schema, table := parseTableName(tableName)
	del := entsql.Dialect(dialect.Postgres).
		Delete(table).
		Where(entsql.And(
			entsql.EQ(outboxfields.CorrelationID, correlationID),
			entsql.IsNull(outboxfields.PublishedAt),
		))
	if schema != "" {
		del = del.Schema(schema)
	}
	return del.Query()
}

// SelectorSQL returns the step 1 SQL query generated by NewOutboxSelector without executing it.
func SelectorSQL(tableName string, batchSize, maxRetries int) (string, []any) {
	t := makeTable(tableName)
	cidSelector := entsql.Dialect(dialect.Postgres).
		Select(t.C(outboxfields.CorrelationID)).
		From(t).
		Where(entsql.And(
			entsql.IsNull(t.C(outboxfields.PublishedAt)),
			entsql.IsNull(t.C(outboxfields.DeadAt)),
			entsql.LT(t.C(outboxfields.RetryCount), maxRetries),
			entsql.Or(
				entsql.IsNull(t.C(outboxfields.NextRetryAt)),
				entsql.LTE(t.C(outboxfields.NextRetryAt), entsql.Raw("NOW()")),
			),
		)).
		GroupBy(t.C(outboxfields.CorrelationID)).
		OrderExpr(entsql.Expr("MIN(" + t.C(outboxfields.CreatedAt) + ")")).
		Limit(batchSize)
	return cidSelector.Query()
}
