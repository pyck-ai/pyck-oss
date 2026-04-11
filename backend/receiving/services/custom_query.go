package services

import (
	"context"
	"fmt"

	"github.com/pyck-ai/pyck/backend/receiving/ent/gen"
)

func ExecuteCountQuery(ctx context.Context, tx *gen.Tx, query string, args ...interface{}) (int, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute count: %w", err)
	}
	defer rows.Close()

	var count int
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return 0, fmt.Errorf("failed to scan count query: %w", err)
		}
	}
	return count, nil
}
