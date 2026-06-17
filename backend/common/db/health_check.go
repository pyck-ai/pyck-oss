package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/log"
)

type DbHealthChecker struct {
	db        *sql.DB
	tableName string
}

func NewDbHealthChecker(db *sql.DB, tableName string) *DbHealthChecker {
	return &DbHealthChecker{
		db:        db,
		tableName: tableName,
	}
}

func (checker *DbHealthChecker) HealthCheck(ctx context.Context) error {
	_, err := checker.db.ExecContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", checker.tableName))
	if err != nil {
		return err
	}

	transactionIsolationLevel, err := checker.getTransactionIsolationLevel(ctx)
	if err != nil {
		return err
	}

	if transactionIsolationLevel != strings.ToLower(sql.LevelSerializable.String()) {
		log.ForContext(ctx).Debug().
			Str("transactionIsolationLevel", transactionIsolationLevel).
			Str("expectedTransactionIsolationLevel", sql.LevelSerializable.String()).
			Msg("Unexpected transaction isolation level")
	}
	return nil
}

func (checker *DbHealthChecker) getTransactionIsolationLevel(ctx context.Context) (string, error) {
	rows, err := checker.db.QueryContext(ctx, "SHOW TRANSACTION ISOLATION LEVEL")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var isolationLevel string
	for rows.Next() {
		if rows.Err() != nil {
			return "", rows.Err()
		}

		err = rows.Scan(&isolationLevel)
		if err != nil {
			return "", err
		}
	}

	return isolationLevel, err
}
