package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/env/config"
	outboxfields "github.com/pyck-ai/pyck/backend/common/internal/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// Sentinel errors for outbox inserter reflection operations.
var (
	ErrOutboxFieldNotFound  = errors.New("EntityEventsOutbox field not found on transaction type")
	ErrCreateMethodNotFound = errors.New("create method not found on EntityEventsOutbox client")
	ErrSetterNotFound       = errors.New("setter method not found on builder")
	ErrExecMethodNotFound   = errors.New("exec method not found on builder")
)

// OutboxRow represents a row from the outbox table.
// This is used by OutboxSelector to return entries for processing.
type OutboxRow struct {
	ID            uuid.UUID
	CorrelationID string
	Topic         string
	Payload       []byte
	WithReply     bool
	RetryCount    int
	EntityType    *string
}

// GetEntityType returns the entity type or "unknown" if nil.
func (r OutboxRow) GetEntityType() string {
	if r.EntityType != nil {
		return *r.EntityType
	}
	return "unknown"
}

// OutboxInsertFunc inserts an outbox entry with the unmarshalled payload.
// The payload is already converted from []byte to map[string]any.
type OutboxInsertFunc func(ctx context.Context, entry *OutboxEntry, payload map[string]any) error

// NewOutboxInserter creates an OutboxInserter that handles JSON unmarshalling
// and delegates the actual insert to the provided function.
// This avoids duplicating the unmarshal logic across services.
func NewOutboxInserter(insertFn OutboxInsertFunc) func(context.Context, *OutboxEntry) error {
	return func(ctx context.Context, entry *OutboxEntry) error {
		var payload map[string]any
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			log.ForContext(ctx).Error().Err(err).Msg("failed to unmarshal outbox payload")
			return err
		}
		return insertFn(ctx, entry, payload)
	}
}

// entOutboxInserter holds cached reflection information for efficient outbox insertion.
type entOutboxInserter[T any] struct {
	outboxFieldIndex []int
	createMethod     reflect.Value
	setters          []reflect.Value // SetID, SetCreatedAt, SetCorrelationID, SetNillableUserID, SetTopic, SetPayload, SetWithReply
	execMethod       reflect.Value
}

// NewEntOutboxInserter creates a standard OutboxInserter for Ent-generated outbox tables.
// This helper eliminates boilerplate by providing a pre-configured inserter that works
// with all Ent-based services following the EntityEventsOutbox schema pattern.
func NewEntOutboxInserter[T any](txFromContext func(context.Context) T) func(context.Context, *OutboxEntry) error {
	var (
		once     sync.Once
		inserter *entOutboxInserter[T]
		initErr  error
	)

	return NewOutboxInserter(func(ctx context.Context, entry *OutboxEntry, payload map[string]any) error {
		tx := txFromContext(ctx)
		rv := reflect.ValueOf(tx)
		if !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
			return ErrNoTransaction
		}

		once.Do(func() {
			inserter, initErr = buildEntOutboxInserter[T](tx)
		})
		if initErr != nil {
			return fmt.Errorf("build ent outbox inserter: %w", initErr)
		}

		return inserter.insert(ctx, tx, entry, payload)
	})
}

// buildEntOutboxInserter performs one-time reflection analysis to cache method lookups.
func buildEntOutboxInserter[T any](txSample T) (*entOutboxInserter[T], error) {
	txVal := reflect.ValueOf(txSample)
	if txVal.Kind() == reflect.Pointer {
		txVal = txVal.Elem()
	}

	txType := txVal.Type()

	// Find EntityEventsOutbox field
	outboxField, found := txType.FieldByName("EntityEventsOutbox")
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrOutboxFieldNotFound, txType.Name())
	}

	// Get the client type (pointer to *EntityEventsOutboxClient)
	clientType := outboxField.Type

	// Find Create method on the client
	createMethod, ok := clientType.MethodByName("Create")
	if !ok {
		return nil, ErrCreateMethodNotFound
	}

	// The builder type is the return type of Create()
	builderType := createMethod.Type.Out(0)

	// Cache all setter methods on the builder type
	setterNames := []string{
		"SetID",
		"SetCreatedAt",
		"SetCorrelationID",
		"SetNillableUserID",
		"SetTopic",
		"SetPayload",
		"SetWithReply",
		"SetNillableEntityType",
		"SetNillableEntityID",
		"SetNillableTenantID",
	}

	setters := make([]reflect.Value, len(setterNames))
	for i, name := range setterNames {
		method, ok := builderType.MethodByName(name)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrSetterNotFound, name)
		}
		setters[i] = method.Func
	}

	execMethod, ok := builderType.MethodByName("Exec")
	if !ok {
		return nil, ErrExecMethodNotFound
	}

	return &entOutboxInserter[T]{
		outboxFieldIndex: outboxField.Index,
		createMethod:     createMethod.Func,
		setters:          setters,
		execMethod:       execMethod.Func,
	}, nil
}

// insert performs the actual outbox insertion using cached reflection methods.
func (e *entOutboxInserter[T]) insert(ctx context.Context, tx T, entry *OutboxEntry, payload map[string]any) error {
	txVal := reflect.ValueOf(tx)
	if txVal.Kind() == reflect.Pointer {
		txVal = txVal.Elem()
	}

	// Get the EntityEventsOutbox client field
	clientField := txVal.FieldByIndex(e.outboxFieldIndex)

	// Call Create() -> returns builder
	builder := e.createMethod.Call([]reflect.Value{clientField})[0]

	// Chain all setters (each returns the builder)
	// Order matches setterNames in buildEntOutboxInserter
	args := []reflect.Value{
		reflect.ValueOf(entry.ID),
		reflect.ValueOf(entry.CreatedAt),
		reflect.ValueOf(entry.CorrelationID),
		reflect.ValueOf(entry.UserID),
		reflect.ValueOf(entry.Topic.String()),
		reflect.ValueOf(payload),
		reflect.ValueOf(entry.WithReply),
		reflect.ValueOf(entry.EntityType),
		reflect.ValueOf(entry.EntityID),
		reflect.ValueOf(entry.TenantID),
	}

	for i, setter := range e.setters {
		builder = setter.Call([]reflect.Value{builder, args[i]})[0]
	}

	// Call Exec(ctx)
	results := e.execMethod.Call([]reflect.Value{builder, reflect.ValueOf(ctx)})
	if !results[0].IsNil() {
		if err, ok := results[0].Interface().(error); ok {
			return err
		}
	}

	return nil
}

// OutboxSelectFunc selects pending outbox entries for processing.
// It receives the transaction, batch size, and max retries, and returns entries grouped by correlation ID.
// The query MUST ensure correlation ordering: all events for a correlation ID are processed together, in order.
type OutboxSelectFunc func(ctx context.Context, tx *sql.Tx, batchSize, maxRetries int) ([]OutboxRow, error)

// OutboxMarkPublishedFunc marks an outbox entry as successfully published.
type OutboxMarkPublishedFunc func(ctx context.Context, tx *sql.Tx, id uuid.UUID) error

// OutboxMarkFailedFunc marks an outbox entry as failed for retry.
type OutboxMarkFailedFunc func(ctx context.Context, tx *sql.Tx, id uuid.UUID, errMsg string) error

// OutboxMarkCorrelationDeadFunc marks all remaining entries in a correlation group as dead.
// This is called when an entry exceeds max retries, preventing the entire correlation group from processing.
type OutboxMarkCorrelationDeadFunc func(ctx context.Context, tx *sql.Tx, correlationID string, reason string) error

// parseTableName splits a schema-qualified table name into schema and table parts.
// If no schema is present, returns empty schema and the original name as table.
func parseTableName(tableName string) (schema, table string) {
	if idx := strings.LastIndex(tableName, "."); idx != -1 {
		return tableName[:idx], tableName[idx+1:]
	}
	return "", tableName
}

// makeTable creates a SelectTable from a potentially schema-qualified table name.
func makeTable(tableName string) *entsql.SelectTable {
	schema, table := parseTableName(tableName)
	t := entsql.Table(table)
	if schema != "" {
		t = t.Schema(schema)
	}
	return t
}

// NewOutboxSelector creates an OutboxSelector that uses the provided table name.
// The table name can be schema-qualified (e.g., "receiving.event_outbox").
// The query ensures correlation ordering by:
// 1. Finding the first N correlation IDs with unprocessed events (ordered by oldest event)
// 2. Locking and fetching ALL events for those correlations with FOR UPDATE SKIP LOCKED
func NewOutboxSelector(tableName string) OutboxSelectFunc {
	return func(ctx context.Context, tx *sql.Tx, batchSize, maxRetries int) ([]OutboxRow, error) {
		t := makeTable(tableName)
		now := time.Now().UTC()

		// Step 1: Find the N oldest correlation IDs with pending events (no locking here)
		// PostgreSQL doesn't allow FOR UPDATE with GROUP BY, so we identify targets first
		cidSelector := entsql.Dialect(dialect.Postgres).
			Select(t.C(outboxfields.CorrelationID)).
			From(t).
			Where(entsql.And(
				entsql.IsNull(t.C(outboxfields.PublishedAt)),
				entsql.IsNull(t.C(outboxfields.DeadAt)),
				entsql.LT(t.C(outboxfields.RetryCount), maxRetries),
				entsql.Or(
					entsql.IsNull(t.C(outboxfields.NextRetryAt)),
					entsql.LTE(t.C(outboxfields.NextRetryAt), now),
				),
			)).
			GroupBy(t.C(outboxfields.CorrelationID)).
			OrderExpr(entsql.Expr("MIN(" + t.C(outboxfields.CreatedAt) + ")")).
			Limit(batchSize)

		query, args := cidSelector.Query()
		idRows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to select correlation IDs: %w", err)
		}
		defer idRows.Close()

		var correlationIDs []any
		for idRows.Next() {
			var cid string
			if err := idRows.Scan(&cid); err != nil {
				return nil, fmt.Errorf("failed to scan correlation ID: %w", err)
			}
			correlationIDs = append(correlationIDs, cid)
		}

		if err := idRows.Err(); err != nil {
			return nil, fmt.Errorf("error during correlation ID iteration: %w", err)
		}

		if len(correlationIDs) == 0 {
			return nil, nil
		}

		// Step 2: Lock and fetch all events for those correlation IDs
		// FOR UPDATE SKIP LOCKED ensures concurrent workers don't process the same rows
		dataSelector := entsql.Dialect(dialect.Postgres).
			Select(
				t.C(outboxfields.ID),
				t.C(outboxfields.CorrelationID),
				t.C(outboxfields.Topic),
				t.C(outboxfields.Payload),
				t.C(outboxfields.WithReply),
				t.C(outboxfields.RetryCount),
				t.C(outboxfields.EntityType),
			).
			From(t).
			Where(entsql.And(
				entsql.In(t.C(outboxfields.CorrelationID), correlationIDs...),
				entsql.IsNull(t.C(outboxfields.PublishedAt)),
				entsql.IsNull(t.C(outboxfields.DeadAt)),
			)).
			OrderBy(
				entsql.Asc(t.C(outboxfields.CorrelationID)),
				entsql.Asc(t.C(outboxfields.CreatedAt)),
			).
			ForUpdate(entsql.WithLockAction(entsql.SkipLocked))

		query, args = dataSelector.Query()
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to select outbox entries: %w", err)
		}

		return scanOutboxRows(rows)
	}
}

// scanOutboxRows converts database rows to OutboxRow structs.
func scanOutboxRows(rows *sql.Rows) ([]OutboxRow, error) {
	defer rows.Close()

	var results []OutboxRow
	for rows.Next() {
		var row OutboxRow
		var entityType sql.NullString
		if err := rows.Scan(
			&row.ID,
			&row.CorrelationID,
			&row.Topic,
			&row.Payload,
			&row.WithReply,
			&row.RetryCount,
			&entityType,
		); err != nil {
			return nil, fmt.Errorf("failed to scan outbox row: %w", err)
		}
		if entityType.Valid {
			row.EntityType = &entityType.String
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error during row iteration: %w", err)
	}

	return results, nil
}

// NewOutboxMarkPublished creates an OutboxMarkPublishedFunc for the given table.
// The table name can be schema-qualified (e.g., "receiving.event_outbox").
func NewOutboxMarkPublished(tableName string) OutboxMarkPublishedFunc {
	schema, table := parseTableName(tableName)
	return func(ctx context.Context, tx *sql.Tx, id uuid.UUID) error {
		update := entsql.Dialect(dialect.Postgres).
			Update(table).
			Set(outboxfields.PublishedAt, time.Now().UTC()).
			SetNull(outboxfields.LastError).
			SetNull(outboxfields.NextRetryAt).
			Where(entsql.EQ(outboxfields.ID, id))
		if schema != "" {
			update = update.Schema(schema)
		}

		query, args := update.Query()
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
}

// NewOutboxMarkFailed creates an OutboxMarkFailedFunc for the given table.
// The table name can be schema-qualified (e.g., "receiving.event_outbox").
func NewOutboxMarkFailed(tableName string) OutboxMarkFailedFunc {
	schema, table := parseTableName(tableName)
	return func(ctx context.Context, tx *sql.Tx, id uuid.UUID, errMsg string) error {
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

		query, args := update.Query()
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
}

// NewOutboxMarkCorrelationDead creates an OutboxMarkCorrelationDeadFunc for the given table.
// The table name can be schema-qualified (e.g., "receiving.event_outbox").
// This deletes all unpublished entries in a correlation group that have exceeded max retries.
// The reason is logged before deletion for audit purposes.
func NewOutboxMarkCorrelationDead(tableName string) OutboxMarkCorrelationDeadFunc {
	schema, table := parseTableName(tableName)
	return func(ctx context.Context, tx *sql.Tx, correlationID string, reason string) error {
		log.ForContext(ctx).Warn().
			Str("correlation_id", correlationID).
			Str("reason", reason).
			Msg("deleting dead correlation group from outbox")

		del := entsql.Dialect(dialect.Postgres).
			Delete(table).
			Where(entsql.And(
				entsql.EQ(outboxfields.CorrelationID, correlationID),
				entsql.IsNull(outboxfields.PublishedAt),
			))
		if schema != "" {
			del = del.Schema(schema)
		}

		query, args := del.Query()
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
}

// OutboxTableName is the standard table name for the event outbox.
const OutboxTableName = "event_outbox"

// OutboxSystemConfig configures the complete outbox event system.
// Use this with NewOutboxSystem for simplified setup.
type OutboxSystemConfig struct {
	// ServiceName is used to derive the schema-qualified table name.
	ServiceName string

	// DB is the database connection pool.
	DB *sql.DB

	// ConnString is the PostgreSQL connection string for LISTEN/NOTIFY.
	ConnString string

	// Publisher for NATS events.
	Publisher Publisher

	// StreamName for NATS topics.
	StreamName string

	// Timing configuration
	PollInterval         time.Duration
	BatchSize            int
	ReplyTimeout         time.Duration
	MaxRetries           int
	NotifyChannel        string
	ListenerPingInterval time.Duration
	ReplyCleanupInterval time.Duration
	ListenNotifyEnabled  bool
}

// OutboxSystem manages the outbox handler and reply registry lifecycle.
type OutboxSystem struct {
	Handler  *OutboxHandler
	Registry *ReplyRegistry
}

// NewOutboxSystem creates a complete outbox event system with minimal configuration.
// The outbox table helpers are automatically derived from the service name.
func NewOutboxSystem(cfg OutboxSystemConfig) *OutboxSystem {
	// Derive table name from service name
	tableName := cfg.ServiceName + "." + OutboxTableName

	// Create reply registry
	registry := NewReplyRegistry(cfg.ReplyCleanupInterval)

	// Create handler with derived helpers
	handler := NewOutboxHandler(OutboxHandlerConfig{
		DB:                        cfg.DB,
		ConnString:                cfg.ConnString,
		Publisher:                 cfg.Publisher,
		ReplyRegistry:             registry,
		StreamName:                cfg.StreamName,
		PollInterval:              cfg.PollInterval,
		BatchSize:                 cfg.BatchSize,
		ReplyTimeout:              cfg.ReplyTimeout,
		MaxRetries:                cfg.MaxRetries,
		NotifyChannel:             cfg.NotifyChannel,
		ListenerPingInterval:      cfg.ListenerPingInterval,
		ListenNotifyEnabled:       cfg.ListenNotifyEnabled,
		ServiceName:               cfg.ServiceName,
		OutboxSelector:            NewOutboxSelector(tableName),
		OutboxMarkPublished:       NewOutboxMarkPublished(tableName),
		OutboxMarkFailed:          NewOutboxMarkFailed(tableName),
		OutboxMarkCorrelationDead: NewOutboxMarkCorrelationDead(tableName),
	})

	return &OutboxSystem{
		Handler:  handler,
		Registry: registry,
	}
}

// Start begins processing outbox entries and starts the reply registry cleanup.
func (s *OutboxSystem) Start(ctx context.Context) error {
	s.Registry.Start(ctx)
	return s.Handler.Start(ctx)
}

// Stop gracefully stops the outbox handler and reply registry.
func (s *OutboxSystem) Stop() {
	s.Handler.Stop()
	s.Registry.Stop()
}

// PostCommitFunc is the signature for scheduling functions after transaction commit.
type PostCommitFunc func(context.Context, func() error)

// EventSystemConfig configures the complete event system including hook and outbox.
type EventSystemConfig[T any] struct {
	// ServiceName is the service identifier (e.g., "receiving", "inventory").
	ServiceName string

	// StreamName for NATS topics.
	StreamName string

	// ConnString is the PostgreSQL connection string for LISTEN/NOTIFY.
	ConnString string

	// Publisher for NATS events.
	Publisher Publisher

	// PostCommit schedules functions to run after transaction commit.
	// Use gqltx.AddPostCommit.
	PostCommit PostCommitFunc

	// TxFromContext extracts the Ent transaction from context.
	// Use ent.TxFromContext.
	TxFromContext func(context.Context) T

	// DB is the database connection pool.
	DB *sql.DB

	// Outbox timing configuration.
	Outbox config.EventOutboxConfig
}

// EventSystem manages the complete event infrastructure: mutation hook and outbox processing.
type EventSystem struct {
	hook   ent.Hook
	outbox *OutboxSystem
}

// NewEventSystem creates a complete event system with mutation hook and outbox handler.
func NewEventSystem[T any](cfg EventSystemConfig[T]) *EventSystem {
	// Create mutation hook
	hook := MutationEventHook(HookConfig{
		Service:            cfg.ServiceName,
		StreamName:         cfg.StreamName,
		EntityFetcher:      BuildEntityFetcher(cfg.TxFromContext, FieldData),
		OutboxInserter:     NewEntOutboxInserter(cfg.TxFromContext),
		FieldChangeEmitter: NewFieldChangeEmitter(cfg.Publisher, cfg.PostCommit),
	})

	// Create outbox system
	outbox := NewOutboxSystem(OutboxSystemConfig{
		ServiceName:          cfg.ServiceName,
		DB:                   cfg.DB,
		ConnString:           cfg.ConnString,
		Publisher:            cfg.Publisher,
		StreamName:           cfg.StreamName,
		PollInterval:         cfg.Outbox.OutboxPollInterval,
		BatchSize:            cfg.Outbox.OutboxBatchSize,
		ReplyTimeout:         cfg.Outbox.OutboxReplyTimeout,
		MaxRetries:           cfg.Outbox.OutboxMaxRetries,
		NotifyChannel:        cfg.Outbox.OutboxNotifyChannel,
		ListenerPingInterval: cfg.Outbox.OutboxListenerPingInterval,
		ReplyCleanupInterval: cfg.Outbox.OutboxReplyCleanupInterval,
		ListenNotifyEnabled:  cfg.Outbox.OutboxListenNotifyEnabled,
	})

	return &EventSystem{
		hook:   hook,
		outbox: outbox,
	}
}

// Hook returns the Ent mutation hook for capturing entity changes.
// Use with dbClient.Use(eventSystem.Hook()).
func (e *EventSystem) Hook() ent.Hook {
	return e.hook
}

// Registry returns the reply registry for workflow reply coordination.
// Use with gqltx.NewWorkflowReplyMiddleware(eventSystem.Registry(), timeout).
func (e *EventSystem) Registry() *ReplyRegistry {
	return e.outbox.Registry
}

// Start begins processing outbox entries.
func (e *EventSystem) Start(ctx context.Context) error {
	return e.outbox.Start(ctx)
}

// Stop gracefully stops the event system.
func (e *EventSystem) Stop() {
	e.outbox.Stop()
}
