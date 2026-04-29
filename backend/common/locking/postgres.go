package locking

import (
	"context"
	"database/sql"
	"fmt"
)

// PostgresLocks holds the database connection for managing PostgreSQL advisory locks.
type PostgresLocks struct {
	db *sql.DB
}

// TryLock attempts to acquire a session-level advisory lock using the provided lock ID.
// It uses `pg_try_advisory_lock(key)` which returns true immediately if the lock is acquired,
// or false if it is unavailable (held by another session).
//
// This is session-scoped, meaning the lock is automatically released if the connection closes (crash).
// It returns true if lock was acquired, false if not.
func (l *PostgresLocks) TryLock(ctx context.Context, lockID LockID) (bool, error) {
	var success bool
	//nolint:gosec // G115: overflow is intentional — the bit pattern is preserved for the advisory lock key.
	key := int64(lockID)
	err := l.db.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&success)
	if err != nil {
		return false, fmt.Errorf("failed to execute pg_try_advisory_lock: %w", err)
	}
	return success, nil
}

// Unlock releases a session-level advisory lock previously acquired.
// It uses `pg_advisory_unlock(key)` which returns true if the lock was held and released,
// or false if the session did not hold the lock.
//
// It returns true if the lock was successfully released.
func (l *PostgresLocks) Unlock(ctx context.Context, lockID LockID) error {
	var success bool
	//nolint:gosec // G115: overflow is intentional — the bit pattern is preserved for the advisory lock key.
	key := int64(lockID)
	err := l.db.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", key).Scan(&success)
	if err != nil {
		return fmt.Errorf("failed to execute pg_advisory_unlock: %w", err)
	}
	if !success {
		return fmt.Errorf("%w: %d", ErrLockNotHeld, lockID)
	}
	return nil
}
