package locking_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/locking"
)

// openSQLiteDB creates an in-memory SQLite database.
func openSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	return db
}

func TestNew_UnsupportedDialect(t *testing.T) {
	t.Parallel()
	_, err := locking.New(nil, "mysql")
	require.ErrorIs(t, err, locking.ErrUnsupportedDialect)
}

func TestGenerateLockID_Deterministic(t *testing.T) {
	t.Parallel()
	serviceName := "management"
	id1 := locking.GenerateLockID(serviceName)
	id2 := locking.GenerateLockID(serviceName)

	assert.Equal(t, id1, id2, "Lock ID must be deterministic")
	assert.NotZero(t, id1, "Lock ID should not be zero")

	otherService := "inventory"
	id3 := locking.GenerateLockID(otherService)
	assert.NotEqual(t, id1, id3, "Different services should have different IDs")
}

func TestSQLiteLock_Lifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	lockID := locking.GenerateLockID("test-sqlite-lifecycle-" + time.Now().String())

	db := openSQLiteDB(t)
	defer db.Close()

	l, err := locking.New(db, dialect.SQLite)
	require.NoError(t, err)

	// 1. acquire lock --> SUCCESS
	acquired, err := l.TryLock(ctx, lockID)
	require.NoError(t, err)
	assert.True(t, acquired, "Should acquire lock")

	// 2. try acquire same lock again --> FAIL
	acquired, err = l.TryLock(ctx, lockID)
	require.NoError(t, err)
	assert.False(t, acquired, "Should NOT acquire lock already held")

	// 3. release lock --> SUCCESS
	err = l.Unlock(ctx, lockID)
	require.NoError(t, err)

	// 4. unlock again --> ERROR (not held)
	err = l.Unlock(ctx, lockID)
	require.ErrorIs(t, err, locking.ErrLockNotHeld)

	// 5. re-acquire --> SUCCESS
	acquired, err = l.TryLock(ctx, lockID)
	require.NoError(t, err)
	assert.True(t, acquired, "Should re-acquire released lock")

	_ = l.Unlock(ctx, lockID)
}

func TestSQLiteLock_EmptyDriver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	lockID := locking.GenerateLockID("test-empty-driver-" + time.Now().String())

	db := openSQLiteDB(t)
	defer db.Close()

	l, err := locking.New(db, "")
	require.NoError(t, err)

	acquired, err := l.TryLock(ctx, lockID)
	require.NoError(t, err)
	assert.True(t, acquired)

	_ = l.Unlock(ctx, lockID)
}

func TestSQLiteLock_MultipleLocks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	lockA := locking.GenerateLockID("test-sqlite-multi-a-" + time.Now().String())
	lockB := locking.GenerateLockID("test-sqlite-multi-b-" + time.Now().String())

	db := openSQLiteDB(t)
	defer db.Close()

	l, err := locking.New(db, dialect.SQLite)
	require.NoError(t, err)

	// Acquire both locks
	acquired, err := l.TryLock(ctx, lockA)
	require.NoError(t, err)
	assert.True(t, acquired, "Should acquire lock A")

	acquired, err = l.TryLock(ctx, lockB)
	require.NoError(t, err)
	assert.True(t, acquired, "Should acquire lock B independently")

	// Releasing A should not affect B
	err = l.Unlock(ctx, lockA)
	require.NoError(t, err)

	acquired, err = l.TryLock(ctx, lockB)
	require.NoError(t, err)
	assert.False(t, acquired, "Lock B should still be held")

	_ = l.Unlock(ctx, lockB)
}

func TestSQLiteLock_Concurrency(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	lockID := locking.GenerateLockID("test-sqlite-concurrency-" + time.Now().String())
	workerCount := 10

	db := openSQLiteDB(t)
	defer db.Close()

	l, err := locking.New(db, dialect.SQLite)
	require.NoError(t, err)

	var successCount int
	var mu sync.Mutex
	var wg sync.WaitGroup

	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if ok, err := l.TryLock(ctx, lockID); err == nil && ok {
				mu.Lock()
				successCount++
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				_ = l.Unlock(ctx, lockID)
			}
		}()
	}

	wg.Wait()

	assert.Positive(t, successCount, "At least one worker should have acquired the lock")
}
