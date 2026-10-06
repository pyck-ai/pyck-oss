package workflowsdk_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

func TestRetryOnConflict(t *testing.T) {
	t.Parallel()

	// A serialization conflict as it reaches the worker across the GraphQL
	// boundary (text).
	serErr := errors.New(`failed to commit transaction: pq: could not serialize access due to read/write dependencies among transactions`)

	t.Run("retries a transient conflict then succeeds", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := workflowsdk.RetryOnConflict(context.Background(), 5, time.Millisecond, func() error {
			calls++
			if calls <= 2 {
				return serErr
			}
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, 3, calls, "2 failures + 1 success")
	})

	t.Run("returns the error only after exhausting the attempt budget", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := workflowsdk.RetryOnConflict(context.Background(), 4, time.Millisecond, func() error {
			calls++
			return serErr
		})
		require.ErrorIs(t, err, serErr)
		require.Equal(t, 4, calls)
	})

	t.Run("does not retry a non-retryable error", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("connection refused")
		calls := 0
		err := workflowsdk.RetryOnConflict(context.Background(), 5, time.Millisecond, func() error {
			calls++
			return boom
		})
		require.ErrorIs(t, err, boom)
		require.Equal(t, 1, calls)
	})

	t.Run("stops when the context is cancelled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := workflowsdk.RetryOnConflict(ctx, 5, time.Millisecond, func() error {
			calls++
			return serErr
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, calls, "one try, then the cancelled context aborts the wait")
	})

	t.Run("retries every serialization/deadlock/duplicate variant", func(t *testing.T) {
		t.Parallel()
		for _, msg := range []string{
			"pq: could not serialize access due to read/write dependencies",
			"COULD NOT SERIALIZE", // case-insensitive
			"ERROR: 40001",
			"pq: deadlock detected",
			"ERROR: 40P01",
			// A replica losing the race to create a brand-new workflow.
			`pq: duplicate key value violates unique constraint "workflow_tenant_id_name"`,
			"ERROR: 23505",
			// The same race as the API reports it: constraint names are
			// hidden, the SQLSTATE is kept.
			"input: registerWorkflow duplicate key: record already exists (SQLSTATE 23505)",
		} {
			calls := 0
			err := workflowsdk.RetryOnConflict(context.Background(), 2, time.Millisecond, func() error {
				calls++
				return errors.New(msg)
			})
			require.Error(t, err, "the budget is exhausted, so the error surfaces")
			require.Equal(t, 2, calls, "expected %q to be retried", msg)
		}
	})
}
