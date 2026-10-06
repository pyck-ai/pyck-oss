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
//
// TransactionID is the canonical dedup key used by the outbox handler to
// build the NATS message ID.
type OutboxRow struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	Topic         string
	Payload       []byte
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

// nilIfEmpty returns nil for an empty string, &s otherwise. Used to feed
// Ent's SetNillable* setters which expect a *string and skip writing the
// column when nil.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	setters          []reflect.Value // SetID, SetCreatedAt, SetTransactionID, SetNillableTraceID, SetNillableRequestID, SetNillableUserID, SetTopic, SetPayload, SetNillableEntityType, SetNillableEntityID, SetTenantID
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
		"SetTransactionID",
		"SetNillableTraceID",
		"SetNillableRequestID",
		"SetNillableUserID",
		"SetTopic",
		"SetPayload",
		"SetNillableEntityType",
		"SetNillableEntityID",
		"SetTenantID",
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
	// Order matches setterNames in buildEntOutboxInserter.
	// Nillable string setters (TraceID, RequestID) take *string; passing
	// nil for an empty value skips writing the column.
	traceID := nilIfEmpty(entry.TraceID)
	requestID := nilIfEmpty(entry.RequestID)
	args := []reflect.Value{
		reflect.ValueOf(entry.ID),
		reflect.ValueOf(entry.CreatedAt),
		reflect.ValueOf(entry.TransactionID),
		reflect.ValueOf(traceID),
		reflect.ValueOf(requestID),
		reflect.ValueOf(entry.UserID),
		reflect.ValueOf(entry.Topic.String()),
		reflect.ValueOf(payload),
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
// It receives the transaction, batch size, and max retries, and returns entries grouped by transaction ID.
// The query MUST ensure transaction ordering: all events for a transaction ID are processed together, in order.
type OutboxSelectFunc func(ctx context.Context, tx *sql.Tx, batchSize, maxRetries int) ([]OutboxRow, error)

// OutboxMarkPublishedFunc marks an outbox entry as successfully published.
type OutboxMarkPublishedFunc func(ctx context.Context, tx *sql.Tx, id uuid.UUID) error

// OutboxMarkFailedFunc marks an outbox entry as failed for retry.
type OutboxMarkFailedFunc func(ctx context.Context, tx *sql.Tx, id uuid.UUID, errMsg string) error

// OutboxMarkTransactionDeadFunc marks all remaining entries in a transaction group as dead.
// This is called when an entry exceeds max retries, preventing the entire transaction group from processing.
type OutboxMarkTransactionDeadFunc func(ctx context.Context, tx *sql.Tx, transactionID uuid.UUID, reason string) error

// OutboxClaimFunc leases the given entries until leaseUntil so that no other
// poller selects them while they are being published. See NewOutboxClaim.
type OutboxClaimFunc func(ctx context.Context, tx *sql.Tx, ids []uuid.UUID, leaseUntil time.Time) error

// OutboxSelectDeadFunc selects dead-lettered entries (dead_at set, not yet
// published) so they can be drained to the DLQ stream. See NewOutboxSelectDead.
type OutboxSelectDeadFunc func(ctx context.Context, tx *sql.Tx, batchSize int) ([]OutboxRow, error)

// OutboxDeleteFunc deletes the given entries by ID, used to remove a row once it
// has been accepted by the DLQ stream. See NewOutboxDelete.
type OutboxDeleteFunc func(ctx context.Context, tx *sql.Tx, ids []uuid.UUID) error

// The query builders below are the single source of truth for the outbox SQL.
// Both the production OutboxSelectFunc/OutboxMark* closures and the test export
// wrappers call these, so a predicate change cannot silently drift the two
// apart (the white-box SQL assertions exercise the exact production query).

// selectTransactionIDsQuery builds step 1 of the selector: the N oldest
// transaction IDs that still have work. Over the unpublished, non-dead rows of
// each group, a group is selected when EITHER
//   - a row has exhausted its retries (retry_count >= maxRetries), so the
//     group is dead-lettered. This is essential: without it a poisoned row at
//     the cap would no longer match the retry predicate and its group would
//     linger forever as pending instead of being dead-lettered; OR
//   - no row is held back, i.e. none has retry_count below the cap and a
//     next_retry_at still in the future.
//
// The second branch is what keeps backoff per group, not per row. Step 2
// fetches every open row of a selected group, and a group publishes in
// created_at order and stops at its first failure. A failed row E1 has its
// backoff in next_retry_at while its skipped successor E2 only carries the
// short claim lease. If the group were selected as soon as E2's lease expired,
// step 2 would return E1 too and republish it before its backoff elapsed, so a
// multi-row group would retry every lease period instead of every 2^n seconds.
// A live poller's lease is a future next_retry_at as well, so the same branch
// keeps other pollers off a group that is mid-publish. Once nothing is held
// back, every open row is eligible, which is the all-eligible case.
func selectTransactionIDsQuery(tableName string, batchSize, maxRetries int, now time.Time) (string, []any) {
	t := makeTable(tableName)
	// Aggregate predicates over the group. They are built with the builder
	// (Ident/Arg) so identifiers are quoted and placeholders numbered like the
	// rest of the query.
	exhausted := entsql.P(func(b *entsql.Builder) {
		b.WriteString("MAX(").Ident(t.C(outboxfields.RetryCount)).WriteString(") >= ").Arg(maxRetries)
	})
	noneHeldBack := entsql.P(func(b *entsql.Builder) {
		b.WriteString("COUNT(*) FILTER (WHERE ").
			Ident(t.C(outboxfields.RetryCount)).WriteString(" < ").Arg(maxRetries).
			WriteString(" AND ").
			Ident(t.C(outboxfields.NextRetryAt)).WriteString(" > ").Arg(now).
			WriteString(") = 0")
	})
	return entsql.Dialect(dialect.Postgres).
		Select(t.C(outboxfields.TransactionID)).
		From(t).
		Where(entsql.And(
			entsql.IsNull(t.C(outboxfields.PublishedAt)),
			entsql.IsNull(t.C(outboxfields.DeadAt)),
		)).
		GroupBy(t.C(outboxfields.TransactionID)).
		Having(entsql.Or(exhausted, noneHeldBack)).
		OrderExpr(entsql.Expr("MIN(" + t.C(outboxfields.CreatedAt) + ")")).
		Limit(batchSize).
		Query()
}

// markPublishedQuery builds the UPDATE that records a successful publish and
// clears the retry/backoff bookkeeping.
func markPublishedQuery(tableName string, id uuid.UUID, publishedAt time.Time) (string, []any) {
	schema, table := parseTableName(tableName)
	update := entsql.Dialect(dialect.Postgres).
		Update(table).
		Set(outboxfields.PublishedAt, publishedAt).
		SetNull(outboxfields.LastError).
		SetNull(outboxfields.NextRetryAt).
		Where(entsql.EQ(outboxfields.ID, id))
	if schema != "" {
		update = update.Schema(schema)
	}
	return update.Query()
}

// markFailedQuery builds the UPDATE that records a publish failure and schedules
// the next retry with capped exponential backoff (NOW() + 2^retry_count, max 1h).
func markFailedQuery(tableName string, id uuid.UUID, errMsg string) (string, []any) {
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

// markTransactionDeadQuery builds the UPDATE that dead-letters every unpublished,
// not-already-dead row in a transaction group by setting dead_at (with the reason
// recorded in last_error). Dead rows are retained for audit and excluded from
// selection rather than deleted.
func markTransactionDeadQuery(tableName string, transactionID uuid.UUID, reason string, deadAt time.Time) (string, []any) {
	schema, table := parseTableName(tableName)
	update := entsql.Dialect(dialect.Postgres).
		Update(table).
		Set(outboxfields.DeadAt, deadAt).
		Set(outboxfields.LastError, reason).
		Where(entsql.And(
			entsql.EQ(outboxfields.TransactionID, transactionID),
			entsql.IsNull(outboxfields.PublishedAt),
			entsql.IsNull(outboxfields.DeadAt),
		))
	if schema != "" {
		update = update.Schema(schema)
	}
	return update.Query()
}

// claimQuery builds the UPDATE that leases the given entries until leaseUntil by
// pushing next_retry_at into the future. The lease reuses the next_retry_at
// column (no schema change): a leased row looks "not yet eligible" to the
// selector, so no other poller picks it up while it is mid-publish. The publish
// outcome (markPublished clears next_retry_at, markFailed overwrites it with the
// backoff time) supersedes the lease; only a poller that dies mid-publish leaves
// the lease standing, and the row is retried once it expires.
func claimQuery(tableName string, ids []uuid.UUID, leaseUntil time.Time) (string, []any) {
	schema, table := parseTableName(tableName)
	idArgs := make([]any, len(ids))
	for i, id := range ids {
		idArgs[i] = id
	}
	update := entsql.Dialect(dialect.Postgres).
		Update(table).
		Set(outboxfields.NextRetryAt, leaseUntil).
		Where(entsql.And(
			entsql.In(outboxfields.ID, idArgs...),
			entsql.IsNull(outboxfields.PublishedAt),
			entsql.IsNull(outboxfields.DeadAt),
		))
	if schema != "" {
		update = update.Schema(schema)
	}
	return update.Query()
}

// selectDeadRowsQuery builds the SELECT for dead-lettered rows pending DLQ
// delivery: dead_at IS NOT NULL AND published_at IS NULL, oldest first.
func selectDeadRowsQuery(tableName string, batchSize int) (string, []any) {
	t := makeTable(tableName)
	return entsql.Dialect(dialect.Postgres).
		Select(
			t.C(outboxfields.ID),
			t.C(outboxfields.TransactionID),
			t.C(outboxfields.Topic),
			t.C(outboxfields.Payload),
			t.C(outboxfields.RetryCount),
			t.C(outboxfields.EntityType),
		).
		From(t).
		Where(entsql.And(
			entsql.NotNull(t.C(outboxfields.DeadAt)),
			entsql.IsNull(t.C(outboxfields.PublishedAt)),
		)).
		OrderBy(entsql.Asc(t.C(outboxfields.CreatedAt))).
		Limit(batchSize).
		// Lock each dead row so exactly one replica owns it through publish +
		// delete + commit, so no two replicas DLQ the same row.
		ForUpdate(entsql.WithLockAction(entsql.SkipLocked)).
		Query()
}

// deleteByIDsQuery builds the DELETE that removes the given rows by ID.
func deleteByIDsQuery(tableName string, ids []uuid.UUID) (string, []any) {
	schema, table := parseTableName(tableName)
	idArgs := make([]any, len(ids))
	for i, id := range ids {
		idArgs[i] = id
	}
	del := entsql.Dialect(dialect.Postgres).
		Delete(table).
		Where(entsql.In(outboxfields.ID, idArgs...))
	if schema != "" {
		del = del.Schema(schema)
	}
	return del.Query()
}

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
// The query ensures transaction ordering by:
// 1. Finding the first N transaction IDs with unprocessed events (ordered by oldest event)
// 2. Locking and fetching ALL events for those transactions with FOR UPDATE SKIP LOCKED
func NewOutboxSelector(tableName string) OutboxSelectFunc {
	return func(ctx context.Context, tx *sql.Tx, batchSize, maxRetries int) ([]OutboxRow, error) {
		t := makeTable(tableName)

		// Step 1: Find the N oldest transaction IDs with pending events (no locking
		// here). PostgreSQL doesn't allow FOR UPDATE with GROUP BY, so we identify
		// targets first. See selectTransactionIDsQuery for the selection predicate.
		query, args := selectTransactionIDsQuery(tableName, batchSize, maxRetries, time.Now().UTC())
		idRows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to select transaction IDs: %w", err)
		}
		defer idRows.Close()

		var transactionIDs []any
		for idRows.Next() {
			var txID uuid.UUID
			if err := idRows.Scan(&txID); err != nil {
				return nil, fmt.Errorf("failed to scan transaction ID: %w", err)
			}
			transactionIDs = append(transactionIDs, txID)
		}

		if err := idRows.Err(); err != nil {
			return nil, fmt.Errorf("error during transaction ID iteration: %w", err)
		}

		if len(transactionIDs) == 0 {
			return nil, nil
		}

		// Step 2: Lock and fetch all events for those transaction IDs
		// FOR UPDATE SKIP LOCKED ensures concurrent workers don't process the same rows
		dataSelector := entsql.Dialect(dialect.Postgres).
			Select(
				t.C(outboxfields.ID),
				t.C(outboxfields.TransactionID),
				t.C(outboxfields.Topic),
				t.C(outboxfields.Payload),
				t.C(outboxfields.RetryCount),
				t.C(outboxfields.EntityType),
			).
			From(t).
			Where(entsql.And(
				entsql.In(t.C(outboxfields.TransactionID), transactionIDs...),
				entsql.IsNull(t.C(outboxfields.PublishedAt)),
				entsql.IsNull(t.C(outboxfields.DeadAt)),
			)).
			OrderBy(
				entsql.Asc(t.C(outboxfields.TransactionID)),
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
			&row.TransactionID,
			&row.Topic,
			&row.Payload,
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
	return func(ctx context.Context, tx *sql.Tx, id uuid.UUID) error {
		query, args := markPublishedQuery(tableName, id, time.Now().UTC())
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
}

// NewOutboxMarkFailed creates an OutboxMarkFailedFunc for the given table.
// The table name can be schema-qualified (e.g., "receiving.event_outbox").
func NewOutboxMarkFailed(tableName string) OutboxMarkFailedFunc {
	return func(ctx context.Context, tx *sql.Tx, id uuid.UUID, errMsg string) error {
		query, args := markFailedQuery(tableName, id, errMsg)
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
}

// NewOutboxMarkTransactionDead creates an OutboxMarkTransactionDeadFunc for the given table.
// The table name can be schema-qualified (e.g., "receiving.event_outbox").
// This dead-letters all unpublished entries in a transaction group that have
// exceeded max retries by setting dead_at, rather than deleting them. Setting
// dead_at both excludes the rows from the normal publish selector (dead_at IS
// NULL filter) and enqueues them for the DLQ drain: the handler's dead-letter
// drain (see drainDeadLetters) republishes each dead row to a dedicated DLQ
// stream — deduped by a DLQ-namespaced deterministic msgID — and deletes it once
// the stream accepts it, so dead rows are drained out of the outbox rather than
// retained forever. The reason is recorded in last_error for audit.
func NewOutboxMarkTransactionDead(tableName string) OutboxMarkTransactionDeadFunc {
	return func(ctx context.Context, tx *sql.Tx, transactionID uuid.UUID, reason string) error {
		log.ForContext(ctx).Warn().
			Str("transaction_id", transactionID.String()).
			Str("reason", reason).
			Msg("dead-lettering transaction group in outbox")

		query, args := markTransactionDeadQuery(tableName, transactionID, reason, time.Now().UTC())
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
}

// NewOutboxClaim creates an OutboxClaimFunc for the given table. It leases the
// given entries until leaseUntil so a concurrent poller (the NOTIFY-triggered
// goroutine, the poll timer, or another replica) cannot select the same rows
// while they are being published. That avoids redundant concurrent publishes
// and keeps retry counts and publish marks accurate. It does not make a
// republish impossible: JetStream msgID dedup only covers the stream's
// 2 minute Duplicates window (StreamDuplicateWindow), so consumers
// deduplicate by the event ID in the payload.
func NewOutboxClaim(tableName string) OutboxClaimFunc {
	return func(ctx context.Context, tx *sql.Tx, ids []uuid.UUID, leaseUntil time.Time) error {
		if len(ids) == 0 {
			return nil
		}
		query, args := claimQuery(tableName, ids, leaseUntil)
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
}

// NewOutboxSelectDead creates an OutboxSelectDeadFunc for the given table. It
// returns dead-lettered rows (dead_at set, not yet published) so they can be
// drained to the DLQ stream and removed from the outbox.
func NewOutboxSelectDead(tableName string) OutboxSelectDeadFunc {
	return func(ctx context.Context, tx *sql.Tx, batchSize int) ([]OutboxRow, error) {
		query, args := selectDeadRowsQuery(tableName, batchSize)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to select dead outbox entries: %w", err)
		}
		return scanOutboxRows(rows)
	}
}

// NewOutboxDelete creates an OutboxDeleteFunc for the given table. It deletes the
// given rows by ID — used to remove a dead-lettered row once the DLQ stream has
// accepted it.
func NewOutboxDelete(tableName string) OutboxDeleteFunc {
	return func(ctx context.Context, tx *sql.Tx, ids []uuid.UUID) error {
		if len(ids) == 0 {
			return nil
		}
		query, args := deleteByIDsQuery(tableName, ids)
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
	MaxRetries           int
	NotifyChannel        string
	ListenerPingInterval time.Duration
	ListenNotifyEnabled  bool
	ClaimLease           time.Duration
	DLQDrainInterval     time.Duration

	// Published-row pruning (see OutboxJanitor). Zero values select the
	// DefaultOutboxPrune* defaults.
	PruneInterval  time.Duration
	PruneRetention time.Duration
	PruneBatchSize int

	// PruneBatchPause is the janitor's pause between full batches of one sweep
	// (default: 100ms).
	PruneBatchPause time.Duration
}

// OutboxSystem manages the outbox handler and published-row janitor lifecycle.
type OutboxSystem struct {
	Handler *OutboxHandler
	Janitor *OutboxJanitor
}

// NewOutboxSystem creates a complete outbox event system with minimal configuration.
// The outbox table helpers are automatically derived from the service name.
func NewOutboxSystem(cfg OutboxSystemConfig) *OutboxSystem {
	// Derive table name from service name
	tableName := cfg.ServiceName + "." + OutboxTableName

	// Create handler with derived helpers
	handler := NewOutboxHandler(OutboxHandlerConfig{
		DB:                        cfg.DB,
		ConnString:                cfg.ConnString,
		Publisher:                 cfg.Publisher,
		StreamName:                cfg.StreamName,
		PollInterval:              cfg.PollInterval,
		BatchSize:                 cfg.BatchSize,
		MaxRetries:                cfg.MaxRetries,
		NotifyChannel:             cfg.NotifyChannel,
		ListenerPingInterval:      cfg.ListenerPingInterval,
		ListenNotifyEnabled:       cfg.ListenNotifyEnabled,
		ServiceName:               cfg.ServiceName,
		ClaimLease:                cfg.ClaimLease,
		DLQDrainInterval:          cfg.DLQDrainInterval,
		OutboxSelector:            NewOutboxSelector(tableName),
		OutboxMarkPublished:       NewOutboxMarkPublished(tableName),
		OutboxMarkFailed:          NewOutboxMarkFailed(tableName),
		OutboxMarkTransactionDead: NewOutboxMarkTransactionDead(tableName),
		OutboxClaim:               NewOutboxClaim(tableName),
		OutboxSelectDead:          NewOutboxSelectDead(tableName),
		OutboxDelete:              NewOutboxDelete(tableName),
	})

	return &OutboxSystem{
		Handler: handler,
		Janitor: NewOutboxJanitor(cfg.DB, tableName,
			cfg.PruneInterval, cfg.PruneRetention, cfg.PruneBatchSize).
			WithBatchPause(cfg.PruneBatchPause),
	}
}

// Start begins processing outbox entries and starts the published-row janitor
// (which stops when ctx is cancelled).
func (s *OutboxSystem) Start(ctx context.Context) error {
	s.Janitor.Start(ctx)
	return s.Handler.Start(ctx)
}

// Stop gracefully stops the outbox handler.
func (s *OutboxSystem) Stop() {
	s.Handler.Stop()
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

	// SelfTenantSchemas lists schemas whose entity ID doubles as their tenant
	// ID (see HookConfig.SelfTenantSchemas), e.g. management's "Tenant".
	SelfTenantSchemas []string
}

// EventSystem manages the complete event infrastructure: mutation hook and outbox processing.
type EventSystem struct {
	hook       ent.Hook
	hookConfig HookConfig
	outbox     *OutboxSystem
}

// NewEventSystem creates a complete event system with mutation hook and outbox handler.
func NewEventSystem[T any](cfg EventSystemConfig[T]) *EventSystem {
	hookConfig := HookConfig{
		Service:            cfg.ServiceName,
		StreamName:         cfg.StreamName,
		EntityFetcher:      BuildEntityFetcher(cfg.TxFromContext, FieldData),
		OutboxInserter:     NewEntOutboxInserter(cfg.TxFromContext),
		FieldChangeEmitter: NewFieldChangeEmitter(cfg.Publisher, cfg.PostCommit),
		SelfTenantSchemas:  cfg.SelfTenantSchemas,
	}

	// Create mutation hook
	hook := MutationEventHook(hookConfig)

	// Create outbox system
	outbox := NewOutboxSystem(OutboxSystemConfig{
		ServiceName:          cfg.ServiceName,
		DB:                   cfg.DB,
		ConnString:           cfg.ConnString,
		Publisher:            cfg.Publisher,
		StreamName:           cfg.StreamName,
		PollInterval:         cfg.Outbox.OutboxPollInterval,
		BatchSize:            cfg.Outbox.OutboxBatchSize,
		MaxRetries:           cfg.Outbox.OutboxMaxRetries,
		NotifyChannel:        cfg.Outbox.OutboxNotifyChannel,
		ListenerPingInterval: cfg.Outbox.OutboxListenerPingInterval,
		ListenNotifyEnabled:  cfg.Outbox.OutboxListenNotifyEnabled,
		ClaimLease:           cfg.Outbox.OutboxClaimLease,
		DLQDrainInterval:     cfg.Outbox.OutboxDLQDrainInterval,
		PruneInterval:        cfg.Outbox.OutboxPruneInterval,
		PruneRetention:       cfg.Outbox.OutboxPruneRetention,
		PruneBatchSize:       cfg.Outbox.OutboxPruneBatchSize,
		PruneBatchPause:      cfg.Outbox.OutboxPruneBatchPause,
	})

	return &EventSystem{
		hook:       hook,
		hookConfig: hookConfig,
		outbox:     outbox,
	}
}

// Hook returns the Ent mutation hook for capturing entity changes.
// Use with dbClient.Use(eventSystem.Hook()).
func (e *EventSystem) Hook() ent.Hook {
	return e.hook
}

// Start begins processing outbox entries.
func (e *EventSystem) Start(ctx context.Context) error {
	return e.outbox.Start(ctx)
}

// Stop gracefully stops the event system.
func (e *EventSystem) Stop() {
	e.outbox.Stop()
}

// EmitEvent manually emits an outbox event for a mutation that bypassed the
// Ent mutation hook (e.g. a stored procedure that INSERTs directly via
// tx.QueryContext). Callers must invoke this from within the same DB
// transaction as the bypassing write so the outbox row is committed
// atomically with the entity row.
//
// schema is the Ent type name (e.g. "ItemMovement"), op is one of OpCreate /
// OpUpdate / OpDelete, and value is the loaded entity (typically reloaded via
// tx.<Type>.Get after the bypass write).
func (e *EventSystem) EmitEvent(ctx context.Context, schema, op string, entityID uuid.UUID, value ent.Value, beforeData any) error {
	return emitOutboxEvent(ctx, e.hookConfig, schema, op, entityID, value, beforeData)
}
