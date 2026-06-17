package idempotency_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

type pruneCountingStore struct {
	pruneCalls atomic.Int32
	lastCutoff atomic.Pointer[time.Time]
	deleted    int
	err        error
}

func (s *pruneCountingStore) Lookup(context.Context, string, uuid.UUID, uuid.UUID) (*idempotency.Record, error) {
	return nil, idempotency.ErrNotFound
}

func (s *pruneCountingStore) LookupForResolve(context.Context, string, uuid.UUID, uuid.UUID) (*idempotency.Record, error) {
	return nil, idempotency.ErrNotFound
}

func (s *pruneCountingStore) InsertInFlight(context.Context, idempotency.Record) error { return nil }

func (s *pruneCountingStore) MarkCommitted(context.Context, string, uuid.UUID, uuid.UUID, []byte) error {
	return nil
}

func (s *pruneCountingStore) Prune(_ context.Context, cutoff time.Time) (int, error) {
	s.pruneCalls.Add(1)
	c := cutoff
	s.lastCutoff.Store(&c)
	return s.deleted, s.err
}

func TestJanitor_RunsPruneOnInterval(t *testing.T) {
	t.Parallel()

	store := &pruneCountingStore{deleted: 3}
	j := idempotency.NewJanitor(store, 10*time.Millisecond, time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		j.Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return store.pruneCalls.Load() >= 2
	}, time.Second, 5*time.Millisecond, "janitor should have pruned at least twice")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("janitor did not stop after context cancel")
	}

	cutoff := store.lastCutoff.Load()
	require.NotNil(t, cutoff)
	assert.WithinDuration(t, time.Now().UTC().Add(-time.Hour), *cutoff, 5*time.Second)
}

func TestJanitor_PruneErrorDoesNotKillLoop(t *testing.T) {
	t.Parallel()

	store := &pruneCountingStore{err: errors.New("nope")}
	j := idempotency.NewJanitor(store, 10*time.Millisecond, time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go j.Run(ctx)

	require.Eventually(t, func() bool {
		return store.pruneCalls.Load() >= 3
	}, time.Second, 5*time.Millisecond, "janitor should keep ticking after prune errors")
}
