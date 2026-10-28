package locking_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/pyck-ai/pyck/backend/common/locking"
	"github.com/pyck-ai/pyck/backend/common/test/pgtest"
)

// pgHandle is the shared Postgres test container (nil when SKIP_PG_TESTS=1).
var pgHandle *pgtest.Handle

func TestMain(m *testing.M) {
	var terminate func()

	pgHandle, terminate = pgtest.Start("locking")
	code := m.Run()

	terminate()
	os.Exit(code)
}

// TestPostgresLocks_WithXactLock runs callers of one lock against a real
// Postgres: none overlaps another, the lock outlives a failing fn's error, and
// a cancelled wait gives up without running fn.
//
//nolint:tparallel,paralleltest // the subtests take the same lock and must not overlap
func TestPostgresLocks_WithXactLock(t *testing.T) {
	t.Parallel()
	pgtest.Require(t, pgHandle)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	db, err := sql.Open("pgx", pgHandle.AdminDSN())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	locks := locking.NewPostgresLocks(db)
	lockID := locking.GenerateLockID("xact-lock-test")

	t.Run("serializes callers", func(t *testing.T) {
		var inside, overlaps atomic.Int32

		group, groupCtx := errgroup.WithContext(ctx)
		for range 8 {
			group.Go(func() error {
				return locks.WithXactLock(groupCtx, lockID, func(context.Context) error {
					if inside.Add(1) > 1 {
						overlaps.Add(1)
					}
					time.Sleep(20 * time.Millisecond)
					inside.Add(-1)

					return nil
				})
			})
		}

		require.NoError(t, group.Wait())
		assert.Zero(t, overlaps.Load(), "two callers held the lock at once")
	})

	t.Run("releases the lock when fn fails", func(t *testing.T) {
		errFn := errors.New("fn failed")

		err := locks.WithXactLock(ctx, lockID, func(context.Context) error { return errFn })
		require.ErrorIs(t, err, errFn)

		ran := false
		require.NoError(t, locks.WithXactLock(ctx, lockID, func(context.Context) error {
			ran = true
			return nil
		}))
		assert.True(t, ran)
	})

	t.Run("gives up waiting when the context ends", func(t *testing.T) {
		held := make(chan struct{})
		release := make(chan struct{})

		holder := make(chan error, 1)
		go func() {
			holder <- locks.WithXactLock(ctx, lockID, func(context.Context) error {
				close(held)
				<-release

				return nil
			})
		}()
		<-held

		waitCtx, waitCancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer waitCancel()

		ran := false
		err := locks.WithXactLock(waitCtx, lockID, func(context.Context) error {
			ran = true
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, ran, "fn ran without the lock")

		close(release)
		require.NoError(t, <-holder)
	})

	t.Run("gives up waiting for a connection when the context ends", func(t *testing.T) {
		single, err := sql.Open("pgx", pgHandle.AdminDSN())
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, single.Close()) })
		single.SetMaxOpenConns(1)

		busy, err := single.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, busy.Close()) })

		waitCtx, waitCancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer waitCancel()

		ran := false
		err = locking.NewPostgresLocks(single).WithXactLock(waitCtx, lockID, func(context.Context) error {
			ran = true
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, ran)
	})

	t.Run("holds the lock until fn returns when the context ends", func(t *testing.T) {
		holderCtx, holderCancel := context.WithCancel(ctx)
		defer holderCancel()

		held := make(chan struct{})
		release := make(chan struct{})

		holder := make(chan error, 1)
		go func() {
			holder <- locks.WithXactLock(holderCtx, lockID, func(context.Context) error {
				close(held)
				<-release

				return nil
			})
		}()
		<-held
		holderCancel()

		waitCtx, waitCancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer waitCancel()

		ran := false
		err := locks.WithXactLock(waitCtx, lockID, func(context.Context) error {
			ran = true
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, ran, "the lock was released while fn still ran")

		close(release)
		require.NoError(t, <-holder)
	})
}
