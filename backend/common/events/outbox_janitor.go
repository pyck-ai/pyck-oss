package events

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	outboxfields "github.com/pyck-ai/pyck/backend/common/internal/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	// DefaultOutboxPruneRetention is how long a published outbox row is kept
	// before the janitor deletes it. It equals the JetStream stream MaxAge (see
	// client.go): past that age the stream itself no longer holds the event, so
	// the outbox row has no remaining use.
	DefaultOutboxPruneRetention = 72 * time.Hour

	// DefaultOutboxPruneInterval is how often the janitor sweeps.
	DefaultOutboxPruneInterval = 5 * time.Minute

	// DefaultOutboxPruneBatchSize is the maximum rows deleted per statement, so
	// the first sweep over a large backlog never holds one long table lock.
	DefaultOutboxPruneBatchSize = 1000

	// DefaultOutboxPruneBatchPause is the pause between two full batches of one
	// sweep. The first sweep after an upgrade can have the whole published
	// history to delete; the pause spreads that over time to smooth WAL volume
	// and replica lag.
	DefaultOutboxPruneBatchPause = 100 * time.Millisecond
)

// OutboxJanitor periodically deletes published outbox rows older than the
// retention. Unpublished and dead-lettered rows are never touched: only rows
// with published_at set are eligible (dead rows have published_at NULL and leave
// the table through the DLQ drain instead). It mirrors idempotency.Janitor:
// an interval loop owned by the host service, stopped by cancelling the context.
type OutboxJanitor struct {
	db        *sql.DB
	tableName string
	interval  time.Duration
	retention time.Duration
	batchSize int
	// batchPause is how long Sweep waits between two full batches.
	batchPause time.Duration
}

// NewOutboxJanitor creates a janitor for the given (optionally schema-qualified)
// outbox table. Zero interval, retention or batchSize select the defaults. The
// pause between batches defaults to DefaultOutboxPruneBatchPause; change it with
// [OutboxJanitor.WithBatchPause].
func NewOutboxJanitor(db *sql.DB, tableName string, interval, retention time.Duration, batchSize int) *OutboxJanitor {
	if interval <= 0 {
		interval = DefaultOutboxPruneInterval
	}
	if retention <= 0 {
		retention = DefaultOutboxPruneRetention
	}
	if batchSize <= 0 {
		batchSize = DefaultOutboxPruneBatchSize
	}
	return &OutboxJanitor{
		db: db, tableName: tableName, interval: interval, retention: retention,
		batchSize: batchSize, batchPause: DefaultOutboxPruneBatchPause,
	}
}

// WithBatchPause sets the pause between two full batches of a sweep and returns
// the janitor. A non-positive d keeps the default.
func (j *OutboxJanitor) WithBatchPause(d time.Duration) *OutboxJanitor {
	if d > 0 {
		j.batchPause = d
	}
	return j
}

// Start spawns the prune goroutine and returns immediately. Cancelling ctx
// terminates it. Tests that need to observe shutdown can call [OutboxJanitor.Run].
func (j *OutboxJanitor) Start(ctx context.Context) {
	go j.Run(ctx)
}

// Run is the blocking prune loop. Cancel ctx to terminate.
func (j *OutboxJanitor) Run(ctx context.Context) {
	logger := log.ForContext(ctx)
	logger.Info().
		Dur("interval", j.interval).
		Dur("retention", j.retention).
		Str("table", j.tableName).
		Msg("outbox janitor started")

	t := time.NewTicker(j.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("outbox janitor stopping")
			return
		case <-t.C:
			n, err := j.Sweep(ctx)
			if err != nil {
				logger.Error().Err(err).Int("pruned", n).Msg("outbox janitor sweep failed")
				continue
			}
			if n > 0 {
				logger.Debug().Int("pruned", n).Msg("outbox janitor pruned published rows")
			}
		}
	}
}

// Sweep deletes published rows older than the retention in batches of at most
// batchSize rows per statement, until a batch comes back short, pausing
// batchPause between full batches. It returns the total number of rows deleted,
// including on error or when ctx is cancelled during a pause.
func (j *OutboxJanitor) Sweep(ctx context.Context) (int, error) {
	cutoff := time.Now().UTC().Add(-j.retention)

	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		query, args := pruneBatchQuery(j.tableName, cutoff, j.batchSize)
		res, err := j.db.ExecContext(ctx, query, args...)
		if err != nil {
			return total, fmt.Errorf("failed to prune published outbox rows: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("failed to read pruned row count: %w", err)
		}
		total += int(n)
		if int(n) < j.batchSize {
			return total, nil
		}
		if err := j.pause(ctx); err != nil {
			return total, err
		}
	}
}

// pause waits batchPause, returning early with the context's error if ctx is
// cancelled.
func (j *OutboxJanitor) pause(ctx context.Context) error {
	t := time.NewTimer(j.batchPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pruneBatchQuery builds the DELETE for one batch: at most limit published rows
// older than cutoff, picked by a LIMITed subselect on the primary key.
func pruneBatchQuery(tableName string, cutoff time.Time, limit int) (string, []any) {
	schema, table := parseTableName(tableName)
	t := makeTable(tableName)

	victims := entsql.Dialect(dialect.Postgres).
		Select(t.C(outboxfields.ID)).
		From(t).
		Where(entsql.And(
			entsql.NotNull(t.C(outboxfields.PublishedAt)),
			entsql.LT(t.C(outboxfields.PublishedAt), cutoff),
		)).
		Limit(limit).
		// Replicas each run a janitor; skip rows another replica is already
		// deleting rather than queueing behind (or deadlocking with) it.
		ForUpdate(entsql.WithLockAction(entsql.SkipLocked))

	del := entsql.Dialect(dialect.Postgres).
		Delete(table).
		Where(entsql.In(outboxfields.ID, victims))
	if schema != "" {
		del = del.Schema(schema)
	}
	return del.Query()
}
