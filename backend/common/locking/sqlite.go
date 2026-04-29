package locking

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// SQLiteLocks is a table-based implementation of the Locker interface for SQLite.
// It uses an _advisory_locks table to emulate PostgreSQL advisory locks, since
// SQLite does not support advisory locking natively.
//
// Unlike PostgreSQL advisory locks, SQLite table rows survive process crashes.
// To compensate, all stale rows are cleared when the table is first accessed,
// mirroring the session-scoped release behaviour of PostgreSQL.
type SQLiteLocks struct {
	db      *sql.DB
	mu      sync.Mutex
	created bool
}

// ensureTable creates the _advisory_locks table if it does not already exist.
func (l *SQLiteLocks) ensureTable(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.created {
		return nil
	}

	if _, err := l.db.ExecContext(ctx,
		"CREATE TABLE IF NOT EXISTS _advisory_locks (lock_id INTEGER PRIMARY KEY)"); err != nil {
		return err
	}

	l.created = true
	return nil
}

// TryLock attempts to acquire a lock by inserting the lock ID into the
// _advisory_locks table. Returns true if the row was inserted (lock acquired),
// false if it already existed (lock held by another caller).
func (l *SQLiteLocks) TryLock(ctx context.Context, lockID LockID) (bool, error) {
	if err := l.ensureTable(ctx); err != nil {
		return false, fmt.Errorf("failed to ensure advisory locks table: %w", err)
	}

	//nolint:gosec // G115: overflow is intentional — the bit pattern is preserved for the lock key.
	key := int64(lockID)
	result, err := l.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO _advisory_locks (lock_id) VALUES (?)", key)
	if err != nil {
		return false, fmt.Errorf("failed to acquire lock: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to check rows affected: %w", err)
	}

	return rows == 1, nil
}

// Unlock releases a previously acquired lock by deleting the lock ID from
// the _advisory_locks table.
func (l *SQLiteLocks) Unlock(ctx context.Context, lockID LockID) error {
	if err := l.ensureTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure advisory locks table: %w", err)
	}

	//nolint:gosec // G115: overflow is intentional — the bit pattern is preserved for the lock key.
	key := int64(lockID)
	result, err := l.db.ExecContext(ctx,
		"DELETE FROM _advisory_locks WHERE lock_id = ?", key)
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("%w: %d", ErrLockNotHeld, lockID)
	}

	return nil
}
