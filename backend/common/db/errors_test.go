package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// TestPostgresError pins that the SQLSTATE and message are found through
// wrapping, from either driver, and that a non-Postgres error reports none.
func TestPostgresError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		err      error
		wantCode string
		wantMsg  string
		wantOK   bool
	}{
		{"lib/pq, wrapped", fmt.Errorf("gen: constraint failed: %w", &pq.Error{Code: "23505", Message: "duplicate key value"}), "23505", "duplicate key value", true},
		{"pgx, wrapped twice", fmt.Errorf("a: %w", fmt.Errorf("b: %w", &pgconn.PgError{Code: "23503", Message: "insert or update on table"})), "23503", "insert or update on table", true},
		{"not a Postgres error", errors.New("pq: looks like one"), "", "", false},
		{"nil", nil, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, msg, ok := db.PostgresError(tc.err)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, tc.wantMsg, msg)
		})
	}
}
