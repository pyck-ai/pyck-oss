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
	groupBackoffSchema = "groupbackoff"
	groupBackoffTable  = groupBackoffSchema + ".event_outbox"

	// groupBackoffOutboxDDL is the slice of the outbox table the selector,
	// claim and mark-failed queries touch.
	groupBackoffOutboxDDL = `CREATE TABLE groupbackoff.event_outbox (
	id uuid PRIMARY KEY,
	transaction_id uuid NOT NULL,
	topic varchar NOT NULL,
	payload jsonb NOT NULL,
	entity_type varchar NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	published_at timestamptz NULL,
	dead_at timestamptz NULL,
	retry_count integer NOT NULL DEFAULT 0,
	last_error varchar NULL,
	next_retry_at timestamptz NULL
);`
)

func openGroupBackoffDB(t *testing.T) *sql.DB {
	t.Helper()
	pgtest.Require(t, pgHandle)

	migrations := fstest.MapFS{
		"migrations/1_outbox.up.sql": {Data: []byte(groupBackoffOutboxDDL)},
	}
	dsn := pgtest.CreateMigratedDB(t, pgHandle, groupBackoffSchema, migrations)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	return db
}

// insertGroupRow inserts an open outbox row with an explicit created_at so the
// in-group order is deterministic.
func insertGroupRow(t *testing.T, db *sql.DB, txID uuid.UUID, createdAt time.Time, retryCount int, nextRetryAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO groupbackoff.event_outbox
		   (id, transaction_id, topic, payload, created_at, retry_count, next_retry_at)
		 VALUES ($1, $2, 'topic', '{}', $3, $4, $5)`,
		id, txID, createdAt, retryCount, nextRetryAt)
	require.NoError(t, err)
	return id
}

func selectGroupRows(t *testing.T, db *sql.DB, maxRetries int) []uuid.UUID {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { assert.NoError(t, tx.Rollback()) }()

	rows, err := events.NewOutboxSelector(groupBackoffTable)(context.Background(), tx, 100, maxRetries)
	require.NoError(t, err)
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func inTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	fn(tx)
	require.NoError(t, tx.Commit())
}

// A failed E1 is in backoff while its skipped successor E2 only carries the
// claim lease. When the lease expires the group must stay unselected until E1's
// backoff elapses, then both rows come back in created_at order.
func TestOutboxSelector_GroupStaysHeldWhileAnyRowInBackoff(t *testing.T) {
	t.Parallel()

	db := openGroupBackoffDB(t)
	const maxRetries = 15
	txID := uuid.New()
	base := time.Now().Add(-time.Hour)
	e1 := insertGroupRow(t, db, txID, base, 0, nil)
	e2 := insertGroupRow(t, db, txID, base.Add(time.Second), 0, nil)

	// Poller selects and leases both rows.
	assert.Equal(t, []uuid.UUID{e1, e2}, selectGroupRows(t, db, maxRetries))
	inTx(t, db, func(tx *sql.Tx) {
		require.NoError(t, events.NewOutboxClaim(groupBackoffTable)(
			context.Background(), tx, []uuid.UUID{e1, e2}, time.Now().UTC().Add(30*time.Second)))
	})

	// While the lease stands, another poller must not pick the group up.
	assert.Empty(t, selectGroupRows(t, db, maxRetries), "leased group must not be re-selected")

	// E1's publish fails: backoff on E1 (2^0 s = 1s from now); E2 is skipped and
	// keeps only its lease.
	inTx(t, db, func(tx *sql.Tx) {
		require.NoError(t, events.NewOutboxMarkFailed(groupBackoffTable)(context.Background(), tx, e1, "boom"))
	})
	// Push E1's backoff well into the future and expire E2's lease.
	_, err := db.ExecContext(context.Background(),
		`UPDATE groupbackoff.event_outbox SET next_retry_at = now() + interval '1 hour' WHERE id = $1`, e1)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`UPDATE groupbackoff.event_outbox SET next_retry_at = now() - interval '1 second' WHERE id = $1`, e2)
	require.NoError(t, err)

	assert.Empty(t, selectGroupRows(t, db, maxRetries),
		"group must not be re-selected while E1 is still in backoff, even though E2's lease expired")

	// E1's backoff elapses: the group is selected again, both rows in order.
	_, err = db.ExecContext(context.Background(),
		`UPDATE groupbackoff.event_outbox SET next_retry_at = now() - interval '1 second' WHERE id = $1`, e1)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{e1, e2}, selectGroupRows(t, db, maxRetries))
}

// A group with an exhausted row is selected for dead-lettering even while a
// sibling row sits in backoff or under a lease.
func TestOutboxSelector_ExhaustedGroupStillSelectedForDeadLetter(t *testing.T) {
	t.Parallel()

	db := openGroupBackoffDB(t)
	const maxRetries = 3
	txID := uuid.New()
	base := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	e1 := insertGroupRow(t, db, txID, base, maxRetries, &future)
	e2 := insertGroupRow(t, db, txID, base.Add(time.Second), 0, &future)

	assert.Equal(t, []uuid.UUID{e1, e2}, selectGroupRows(t, db, maxRetries))
}

// Groups whose rows are all eligible are selected as before, and a held group
// does not hide a different, eligible group.
func TestOutboxSelector_EligibleGroupSelectedNextToHeldGroup(t *testing.T) {
	t.Parallel()

	db := openGroupBackoffDB(t)
	const maxRetries = 15
	base := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Minute)

	held := uuid.New()
	insertGroupRow(t, db, held, base, 1, &future)
	insertGroupRow(t, db, held, base.Add(time.Second), 0, &past)

	eligible := uuid.New()
	a := insertGroupRow(t, db, eligible, base.Add(time.Minute), 0, nil)
	b := insertGroupRow(t, db, eligible, base.Add(time.Minute+time.Second), 2, &past)

	assert.Equal(t, []uuid.UUID{a, b}, selectGroupRows(t, db, maxRetries))
}
