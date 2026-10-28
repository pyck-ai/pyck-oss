package locking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PostgresLocks holds the database connection for managing PostgreSQL advisory locks.
type PostgresLocks struct {
	db *sql.DB
}

// NewPostgresLocks returns the advisory locks of the PostgreSQL database db.
func NewPostgresLocks(db *sql.DB) *PostgresLocks {
	return &PostgresLocks{db: db}
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

// WithXactLock runs fn while holding the transaction-level advisory lock
// lockID. It waits for a connection and the lock until ctx ends; once fn runs,
// the lock is held until fn returns. The lock's transaction is rolled back
// then, so the lock is released on every path, and by the server if the
// connection drops. fn does not run inside that transaction.
func (l *PostgresLocks) WithXactLock(ctx context.Context, lockID LockID, fn func(context.Context) error) (err error) {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get advisory lock connection: %w", err)
	}
	defer func() {
		if cErr := conn.Close(); cErr != nil {
			err = errors.Join(err, fmt.Errorf("close advisory lock connection: %w", cErr))
		}
	}()

	// database/sql rolls a transaction back when its context ends, which would
	// release the lock while fn runs.
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return fmt.Errorf("begin advisory lock transaction: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("release advisory lock: %w", rbErr))
		}
	}()

	// The overflow is intentional: the key keeps the lock ID's bit pattern.
	key := int64(lockID) //nolint:gosec // G115, see above
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
		return fmt.Errorf("take advisory lock: %w", err)
	}

	return fn(ctx)
}
