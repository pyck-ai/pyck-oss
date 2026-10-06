package gqlserver_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/gqlserver"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// TestPresentErrorHidesConstraintViolations pins that a class 23 SQLSTATE
// reaches the client as a generic message with its code and without the
// table or constraint name, and that every other error is left as is.
func TestPresentErrorHidesConstraintViolations(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			"unique violation, lib/pq behind ent",
			fmt.Errorf("gen: constraint failed: %w", &pq.Error{Code: "23505", Message: `duplicate key value violates unique constraint "item_tenant_id_sku"`, Constraint: "item_tenant_id_sku"}),
			"duplicate key: record already exists (SQLSTATE 23505)",
		},
		{
			"missing parent, pgx from a stored procedure",
			fmt.Errorf("failed creating item movement: %w", &pgconn.PgError{Code: "23503", Message: `insert or update on table "item_movements" violates foreign key constraint "item_movements_items_itemMovementItems"`}),
			"referenced record does not exist (SQLSTATE 23503)",
		},
		{
			"parent still referenced",
			&pq.Error{Code: "23503", Message: `update or delete on table "items" violates foreign key constraint "stocks_items_stock" on table "stocks"`},
			"record is still referenced by other records (SQLSTATE 23503)",
		},
		{
			"other constraint class",
			&pq.Error{Code: "23502", Message: `null value in column "sku" of relation "items" violates not-null constraint`},
			"request violates a data constraint (SQLSTATE 23502)",
		},
		{
			"serialization failure is not a constraint violation",
			&pq.Error{Code: "40001", Message: "could not serialize access due to read/write dependencies"},
			"pq: could not serialize access due to read/write dependencies (40001)",
		},
		{
			"plain error",
			errors.New("invalid itemIDs"),
			"invalid itemIDs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := gqlserver.PresentError(t.Context(), tc.err)
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Message)
		})
	}
}

// TestPresentErrorKeepsCauseForRetry pins that the presented error still
// wraps the Postgres error, which the transaction middleware unwraps to
// decide on a retry.
func TestPresentErrorKeepsCauseForRetry(t *testing.T) {
	t.Parallel()

	cause := &pq.Error{Code: "23505", Message: "duplicate key value"}
	got := gqlserver.PresentError(t.Context(), fmt.Errorf("wrapped: %w", cause))

	var pqErr *pq.Error
	require.ErrorAs(t, got, &pqErr)
	assert.Same(t, cause, pqErr)
	code, _, ok := db.PostgresError(got.Unwrap())
	assert.True(t, ok)
	assert.Equal(t, "23505", code)
}

// TestPresentErrorLogsRawError pins that the detail hidden from the client
// still reaches the server log: the raw message with its constraint name and
// the SQLSTATE, at warn level. Errors that are not hidden are not logged
// here; the transaction middleware already logs every response error.
func TestPresentErrorLogsRawError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	ctx := log.Context(t.Context(), zerolog.New(&buf))

	require.NotNil(t, gqlserver.PresentError(ctx, fmt.Errorf("gen: constraint failed: %w",
		&pq.Error{Code: "23505", Message: `duplicate key value violates unique constraint "item_tenant_id_sku"`})))
	logged := buf.String()
	assert.Contains(t, logged, "item_tenant_id_sku", "the constraint name must stay visible to operators")
	assert.Contains(t, logged, `"sqlstate":"23505"`)
	assert.Contains(t, logged, `"level":"warn"`)

	buf.Reset()
	require.NotNil(t, gqlserver.PresentError(ctx, errors.New("invalid itemIDs")))
	require.NotNil(t, gqlserver.PresentError(ctx, &pq.Error{Code: "40001", Message: "could not serialize access"}))
	assert.Empty(t, buf.String(), "only hidden constraint violations are logged by the presenter")
}

// TestPresentedErrorsStillDriveRetry pins the hand-over to the transaction
// middleware, which decides on a retry from the presented response errors:
// a serialization failure or deadlock stays retryable after presenting, and
// a constraint violation stays non-retryable although its message changed.
func TestPresentedErrorsStillDriveRetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		err       error
		retryable bool
	}{
		{"serialization failure", fmt.Errorf("commit: %w", &pq.Error{Code: "40001", Message: "could not serialize access"}), true},
		{"deadlock from a stored procedure", fmt.Errorf("proc: %w", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}), true},
		{"unique violation", fmt.Errorf("gen: constraint failed: %w", &pq.Error{Code: "23505", Message: "duplicate key value"}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			presented := gqlserver.PresentError(t.Context(), tc.err)
			assert.Equal(t, tc.retryable, gqltx.ErrIsRetryable(gqlerror.List{presented}))
		})
	}
}
