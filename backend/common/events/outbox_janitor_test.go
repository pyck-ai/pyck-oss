package events_test

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/test/pgtest"
)

const (
	janitorSchema = "janitor"

	// janitorOutboxDDL is the slice of the outbox table the janitor reads.
	janitorOutboxDDL = `CREATE TABLE janitor.event_outbox (
	id uuid PRIMARY KEY,
	created_at timestamptz NOT NULL DEFAULT now(),
	published_at timestamptz NULL,
	dead_at timestamptz NULL
);`
)

func openJanitorDB(t *testing.T) *sql.DB {
	t.Helper()
	pgtest.Require(t, pgHandle)

	migrations := fstest.MapFS{
		"migrations/1_outbox.up.sql": {Data: []byte(janitorOutboxDDL)},
	}
	dsn := pgtest.CreateMigratedDB(t, pgHandle, janitorSchema, migrations)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	return db
}

func insertOutboxRow(t *testing.T, db *sql.DB, publishedAt, deadAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO janitor.event_outbox (id, published_at, dead_at) VALUES ($1, $2, $3)`,
		id, publishedAt, deadAt)
	require.NoError(t, err)
	return id
}

func outboxRowExists(t *testing.T, db *sql.DB, id uuid.UUID) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM janitor.event_outbox WHERE id = $1`, id).Scan(&n))
	return n == 1
}

func TestOutboxJanitor_SweepDeletesOnlyOldPublishedRows(t *testing.T) {
	t.Parallel()

	db := openJanitorDB(t)
	old := time.Now().Add(-4 * 24 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)

	publishedOld := insertOutboxRow(t, db, &old, nil)
	publishedRecent := insertOutboxRow(t, db, &recent, nil)
	unpublishedOld := insertOutboxRow(t, db, nil, nil)
	deadOld := insertOutboxRow(t, db, nil, &old)

	j := events.NewOutboxJanitor(db, "janitor.event_outbox", time.Minute, 72*time.Hour, 1000)
	n, err := j.Sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, n)
	assert.False(t, outboxRowExists(t, db, publishedOld), "old published row should be pruned")
	assert.True(t, outboxRowExists(t, db, publishedRecent), "recent published row must stay")
	assert.True(t, outboxRowExists(t, db, unpublishedOld), "unpublished row must stay")
	assert.True(t, outboxRowExists(t, db, deadOld), "dead row must stay")
}

func TestOutboxJanitor_SweepLoopsUntilShortBatch(t *testing.T) {
	t.Parallel()

	db := openJanitorDB(t)
	old := time.Now().Add(-4 * 24 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)

	const oldRows = 7
	for range oldRows {
		insertOutboxRow(t, db, &old, nil)
	}
	keep := insertOutboxRow(t, db, &recent, nil)

	// Batch size 3 < 7 rows: needs 3 statements (3+3+1) to finish.
	j := events.NewOutboxJanitor(db, "janitor.event_outbox", time.Minute, 72*time.Hour, 3)
	n, err := j.Sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, oldRows, n)
	var left int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM janitor.event_outbox`).Scan(&left))
	assert.Equal(t, 1, left)
	assert.True(t, outboxRowExists(t, db, keep))
}

func TestOutboxJanitor_SweepPausesBetweenBatchesAndStopsOnCancel(t *testing.T) {
	t.Parallel()

	db := openJanitorDB(t)
	old := time.Now().Add(-4 * 24 * time.Hour)
	for range 7 {
		insertOutboxRow(t, db, &old, nil)
	}

	// Batch size 3 < 7 rows with an hour-long pause: the first batch is
	// deleted, then the pause is cut short by the context deadline.
	j := events.NewOutboxJanitor(db, "janitor.event_outbox", time.Minute, 72*time.Hour, 3).
		WithBatchPause(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	n, err := j.Sweep(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 3, n, "only the first batch runs before the pause")
	assert.Less(t, time.Since(start), 10*time.Second, "the pause must end with the context")
}

func TestOutboxJanitor_RunSweepsOnIntervalAndStopsOnCancel(t *testing.T) {
	t.Parallel()

	db := openJanitorDB(t)
	old := time.Now().Add(-4 * 24 * time.Hour)
	id := insertOutboxRow(t, db, &old, nil)

	j := events.NewOutboxJanitor(db, "janitor.event_outbox", 10*time.Millisecond, 72*time.Hour, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()

	assert.Eventually(t, func() bool { return !outboxRowExists(t, db, id) },
		5*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("janitor did not stop after context cancel")
	}
}
