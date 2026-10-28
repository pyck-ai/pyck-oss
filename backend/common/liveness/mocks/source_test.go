package mocks //nolint:testpackage // tests the unexported warm with sources that fail

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errSourceDown = errors.New("source down")

// A source that keeps failing ends the warm-up with an error once ctx
// ends, so the caller decides how to fail; the attempts are spaced, not a
// busy loop.
func TestWarmReturnsAnErrorOnceCtxEnds(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	failing := func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
		calls.Add(1)
		return nil, errSourceDown
	}
	const budget = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	err := warm(ctx, NewTestCache(t), []uuid.UUID{uuid.New()}, failing)

	require.ErrorContains(t, err, "warming the liveness cache")
	// The last attempt may lose its wait to ctx instead of seeing the
	// source's error.
	assert.True(t, errors.Is(err, errSourceDown) || errors.Is(err, context.DeadlineExceeded), "got %v", err)
	assert.LessOrEqual(t, calls.Load(), int64(budget/warmRetryPause)+1, "attempts are spaced by warmRetryPause")
}

// A failed attempt is retried until a lookup succeeds.
func TestWarmRetriesUntilALookupSucceeds(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	failsOnce := func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
		if calls.Add(1) == 1 {
			return nil, errSourceDown
		}
		return nil, nil
	}
	id := uuid.New()
	c := NewTestCache(t)

	require.NoError(t, warm(t.Context(), c, []uuid.UUID{id}, failsOnce))

	live, err := c.LookupTenants(t.Context(), []uuid.UUID{id}, failing(t))
	require.NoError(t, err)
	assert.True(t, live[id], "the verdict is served from the cache")
	assert.Equal(t, int64(2), calls.Load())
}

// failing is a source a cache hit never reaches.
func failing(t *testing.T) func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
	t.Helper()
	return func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
		t.Error("source asked although the cache is warm")
		return nil, errSourceDown
	}
}
