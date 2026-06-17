package idempotency_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("boom"), false},
		{"lib/pq 23505", &pq.Error{Code: "23505"}, true},
		{"lib/pq 23503 (foreign key)", &pq.Error{Code: "23503"}, false},
		{"lib/pq 23514 (check)", &pq.Error{Code: "23514"}, false},
		{"pgx 23505", &pgconn.PgError{Code: "23505"}, true},
		{"pgx 23503", &pgconn.PgError{Code: "23503"}, false},
		{"wrapped lib/pq 23505", fmt.Errorf("insert failed: %w", &pq.Error{Code: "23505"}), true},
		{"wrapped pgx 23505", fmt.Errorf("insert failed: %w", &pgconn.PgError{Code: "23505"}), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := idempotency.IsUniqueViolation(c.err); got != c.want {
				t.Fatalf("IsUniqueViolation(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
