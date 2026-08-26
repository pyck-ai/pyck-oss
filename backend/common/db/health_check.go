package db

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// DbHealthChecker reports whether the database is reachable. It is meant to
// be wired to the /health/ready endpoint, using a connection that is NOT
// shared with request traffic (see HealthDB on the multi driver): a service
// that cannot reach Postgres cannot serve traffic and should be pulled from
// Service endpoints, but it must NOT be restarted — restarting every
// replica during a database outage turns a recoverable outage into a
// restart storm. That is why this backs readiness, not liveness.
type DbHealthChecker struct {
	db *sql.DB

	// isolationOnce gates the informational transaction-isolation check so
	// it runs on the first probe only instead of on every kubelet hit.
	isolationOnce sync.Once
}

// NewDbHealthChecker wraps db for use as a health-check component. The
// caller keeps ownership of db and is responsible for closing it.
func NewDbHealthChecker(db *sql.DB) *DbHealthChecker {
	return &DbHealthChecker{db: db}
}

// HealthCheck performs a constant-cost round trip to the database. The
// query result is irrelevant; only the ability to complete the round trip
// within the probe's deadline (ctx) matters.
func (checker *DbHealthChecker) HealthCheck(ctx context.Context) error {
	if _, err := checker.db.ExecContext(ctx, "SELECT 1"); err != nil {
		return err
	}

	checker.isolationOnce.Do(func() {
		checker.logUnexpectedIsolationLevel(ctx)
	})
	return nil
}

// logUnexpectedIsolationLevel emits a debug log when the connection's
// default transaction isolation is not SERIALIZABLE (the level all write
// pools are expected to run at). It is informational only and must never
// fail the probe: a misconfigured isolation level is a correctness concern
// for mutations, not a sign the process should be restarted.
func (checker *DbHealthChecker) logUnexpectedIsolationLevel(ctx context.Context) {
	isolationLevel, err := checker.getTransactionIsolationLevel(ctx)
	if err != nil {
		log.ForContext(ctx).Debug().
			Err(err).
			Msg("Could not determine transaction isolation level")
		return
	}

	if isolationLevel != strings.ToLower(sql.LevelSerializable.String()) {
		log.ForContext(ctx).Debug().
			Str("transactionIsolationLevel", isolationLevel).
			Str("expectedTransactionIsolationLevel", sql.LevelSerializable.String()).
			Msg("Unexpected transaction isolation level")
	}
}

func (checker *DbHealthChecker) getTransactionIsolationLevel(ctx context.Context) (string, error) {
	rows, err := checker.db.QueryContext(ctx, "SHOW TRANSACTION ISOLATION LEVEL")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var isolationLevel string
	for rows.Next() {
		if err = rows.Scan(&isolationLevel); err != nil {
			return "", err
		}
	}

	return isolationLevel, rows.Err()
}
