package locking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"

	"entgo.io/ent/dialect"
)

var (
	// ErrLockNotHeld is returned when attempting to unlock a lock that is not held.
	ErrLockNotHeld = errors.New("lock is not held")

	// ErrUnsupportedDialect is returned when the database driver is not supported.
	// NOTE: there is a duplicate in the validator package. We need to unify these
	// errors in a common package when we re-structure the common code.
	ErrUnsupportedDialect = errors.New("unsupported SQL dialect")
)

type (
	// LockID is a database-agnostic advisory lock identifier.
	// Each implementation handles the conversion to its native representation
	// (e.g. int64 for PostgreSQL BIGINT, INTEGER for SQLite).
	LockID uint64

	// Locker is the interface for managing advisory locks.
	Locker interface {
		// TryLock attempts to acquire a lock with the given ID.
		// Returns true if the lock was acquired, false if it is held by another session.
		TryLock(ctx context.Context, lockID LockID) (bool, error)

		// Unlock releases a previously acquired lock.
		Unlock(ctx context.Context, lockID LockID) error
	}
)

// New creates a dialect-aware Locker backed by the given database connection.
// It uses the driver string (from entgo.io/ent/dialect) to select the implementation:
//   - dialect.Postgres: PostgreSQL advisory locks
//   - dialect.SQLite or "": table-based locking (SQLite, and unset dialect in tests)
//
//nolint:ireturn // Factory function intentionally returns interface for dialect abstraction.
func New(db *sql.DB, driver string) (Locker, error) {
	switch driver {
	case dialect.Postgres:
		return &PostgresLocks{db: db}, nil
	case dialect.SQLite, "":
		return &SQLiteLocks{db: db}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDialect, driver)
	}
}

// GenerateLockID computes a deterministic lock ID based on the service name.
// It uses FNV-1a hash of "pyck.ai/<service-name>" to produce a uint64.
func GenerateLockID(serviceName string) LockID {
	h := fnv.New64a()
	h.Write([]byte("pyck.ai/"))
	h.Write([]byte(serviceName))
	return LockID(h.Sum64())
}
