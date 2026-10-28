// Package mocks holds liveness.TenantSource stand-ins for tests.
package mocks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/liveness"
)

// NoDisabledTenants is a liveness.TenantSource for tests that never disable
// a tenant: every tenant is live.
func NoDisabledTenants(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// Source is a liveness.TenantSource with a fixed disabled set (or a fixed
// error) that records every batch it was asked about. Not safe for
// concurrent use; mutate Disabled/Err between calls to simulate lifecycle
// changes.
type Source struct {
	Disabled map[uuid.UUID]struct{}
	Err      error
	Batches  [][]uuid.UUID
}

// Lookup is the TenantSource.
func (s *Source) Lookup(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	s.Batches = append(s.Batches, append([]uuid.UUID(nil), ids...))
	if s.Err != nil {
		return nil, s.Err
	}
	var disabled []uuid.UUID
	for _, id := range ids {
		if _, ok := s.Disabled[id]; ok {
			disabled = append(disabled, id)
		}
	}
	return disabled, nil
}

// Calls is the number of batches asked so far.
func (s *Source) Calls() int { return len(s.Batches) }

// NewTestCache returns a real liveness cache with a long TTL that is closed
// when the test ends — the standard cache for handler/resolver test
// harnesses. (Named apart from liveness.NewCache: this is a convenience
// constructor, not a mock.)
func NewTestCache(tb testing.TB) *liveness.Cache {
	tb.Helper()
	c := liveness.NewCache(time.Hour)
	tb.Cleanup(c.Close)
	return c
}

// NewWarmTestCache is NewTestCache with a live verdict already cached for
// every tenant in ids, so a request through tenant.HTTPMiddleware is served
// from the cache instead of waiting on a source lookup.
//
// A cold cache makes every request wait on the shared lookup goroutine,
// and that wait is bounded by liveness.DefaultLookupTimeout (one second).
// On a starved CI runner under the race detector the goroutine can miss that
// bound; the middleware then fails closed with 403 and a resolver test
// reads it as an empty response with no GraphQL errors, far from the cause.
// Warming is done through LookupTenants itself, repeated until it returns
// without error: a timed-out attempt still lands its verdicts in the cache
// once the detached source call finishes, so the next attempt is a hit.
// ctx bounds the whole warm-up: once it ends, the last lookup error is
// returned rather than failing the test, which may run off the test
// goroutine where tb.Fatalf must not be called.
func NewWarmTestCache(ctx context.Context, tb testing.TB, ids ...uuid.UUID) (*liveness.Cache, error) {
	tb.Helper()
	c := NewTestCache(tb)
	if err := warm(ctx, c, ids, NoDisabledTenants); err != nil {
		return nil, err
	}
	return c, nil
}

// warmRetryPause spaces the warm-up's attempts, so a source that fails at
// once is retried at a bounded rate instead of in a busy loop.
const warmRetryPause = 10 * time.Millisecond

// warm looks ids up through c until a lookup succeeds or ctx ends.
func warm(ctx context.Context, c *liveness.Cache, ids []uuid.UUID, source liveness.TenantSource) error {
	if len(ids) == 0 {
		return nil
	}
	for {
		_, err := c.LookupTenants(ctx, ids, source)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("warming the liveness cache: %w", err)
		case <-time.After(warmRetryPause):
		}
	}
}
