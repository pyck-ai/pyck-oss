// Package database provides bootstrap helpers for PostgreSQL connectivity
// and advisory locking.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/backend/common/guards"
	"github.com/pyck-ai/pyck/backend/common/locking"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// ErrLockBusy is returned when another instance already holds the bootstrap lock.
var ErrLockBusy = errors.New("another instance is performing bootstrap")

const (
	dbTimeout       = 1 * time.Minute
	dbRetryInterval = 10 * time.Second
)

// AcquireLock obtains a database advisory lock to ensure only one instance
// performs the bootstrap. It waits for the database to become available first.
// The dbDriver parameter selects the locking strategy (e.g. dialect.Postgres, dialect.SQLite).
// The returned function must be called to release the lock.
func AcquireLock(ctx context.Context, db *sql.DB, serviceName string, dbDriver string) (func() error, error) {
	noop := func() error { return nil }

	if err := waitForDatabase(ctx, db); err != nil {
		return noop, fmt.Errorf("dependencies did not become ready in time: %w", err)
	}

	lockService, err := locking.New(db, dbDriver)
	if err != nil {
		return noop, fmt.Errorf("failed to create lock service: %w", err)
	}
	lockID := locking.GenerateLockID(serviceName)
	acquired, err := lockService.TryLock(ctx, lockID)
	if err != nil {
		return noop, fmt.Errorf("failed to acquire bootstrap lock: %w", err)
	}
	if !acquired {
		return noop, fmt.Errorf("%w with lock ID %d", ErrLockBusy, lockID)
	}

	return func() error {
		return lockService.Unlock(ctx, lockID)
	}, nil
}

// waitForDatabase waits for the PostgreSQL database to become reachable.
func waitForDatabase(ctx context.Context, db *sql.DB) error {
	return guards.New().
		Add(guards.Check{
			Name:          "database",
			Timeout:       dbTimeout,
			RetryInterval: dbRetryInterval,
			CheckFunc: func(ctx context.Context) (bool, error) {
				logger := log.ForContext(ctx)
				logger.Debug().Msg("Checking database connectivity")
				if err := db.PingContext(ctx); err != nil {
					logger.Warn().Err(err).
						Dur("timeout", dbTimeout).
						Dur("retry-interval", dbRetryInterval).
						Msg("Database is not ready yet")
					return false, nil
				}
				return true, nil
			},
		}).
		Wait(ctx)
}
