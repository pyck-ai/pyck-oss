package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// TestErrIsRetryable_OCCConflict pins the Phase 6.3 contract: any error
// that wraps db.ErrOCCConflict is classified retryable, so the inventory
// stocks ledger's optimistic-concurrency-control sentinel re-runs the
// outer transaction without inventory needing to know about the retry
// middleware's internals.
func TestErrIsRetryable_OCCConflict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "bare sentinel is retryable",
			err:  db.ErrOCCConflict,
			want: true,
		},
		{
			name: "fmt-wrapped sentinel is retryable",
			err:  fmt.Errorf("failed inserting stocks: %w", db.ErrOCCConflict),
			want: true,
		},
		{
			name: "doubly-wrapped sentinel is retryable",
			err:  fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", db.ErrOCCConflict)),
			want: true,
		},
		{
			name: "unrelated sentinel is not retryable",
			err:  errors.New("unrelated"),
			want: false,
		},
		{
			name: "nil is not retryable",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := db.ErrIsRetryable(tc.err); got != tc.want {
				t.Errorf("ErrIsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestErrIsRetryable_PostgresStates guards against regressions in the
// pre-existing retryable classifications: 40001 (serialization failure)
// and 40P01 (deadlock detected) on both pgx and lib/pq driver error
// types. Phase 6.3 added the OCC sentinel match alongside these; the
// existing matches must keep working for transition safety while
// inventory's READ COMMITTED + OCC migration lands and other services
// continue to rely on SSI's serialization-failure retries.
func TestErrIsRetryable_PostgresStates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "pgx 40001 serialization failure is retryable",
			err:  &pgconn.PgError{Code: "40001"},
			want: true,
		},
		{
			name: "pgx 40P01 deadlock detected is retryable",
			err:  &pgconn.PgError{Code: "40P01"},
			want: true,
		},
		{
			name: "lib/pq 40001 serialization failure is retryable",
			err:  &pq.Error{Code: "40001"},
			want: true,
		},
		{
			name: "lib/pq 40P01 deadlock detected is retryable",
			err:  &pq.Error{Code: "40P01"},
			want: true,
		},
		{
			name: "pgx 23505 unique violation is not retryable on its own",
			err:  &pgconn.PgError{Code: "23505"},
			want: false,
		},
		{
			name: "wrapped pgx 40001 is retryable through errors.As",
			err:  fmt.Errorf("tx failed: %w", &pgconn.PgError{Code: "40001"}),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := db.ErrIsRetryable(tc.err); got != tc.want {
				t.Errorf("ErrIsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
