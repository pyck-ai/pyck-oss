package gqltx_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
)

func TestErrIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		errs     gqlerror.List
		expected bool
	}{
		{
			name:     "empty error list",
			errs:     gqlerror.List{},
			expected: false,
		},
		{
			name:     "nil error list",
			errs:     nil,
			expected: false,
		},
		{
			name:     "single nil error",
			errs:     gqlerror.List{nil},
			expected: false,
		},
		{
			name: "single retryable postgres serialization error (pq)",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, &pq.Error{Code: "40001"}),
			},
			expected: true,
		},
		{
			name: "single retryable postgres deadlock error (pq)",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, &pq.Error{Code: "40P01"}),
			},
			expected: true,
		},
		{
			name: "single retryable postgres serialization error (pgx)",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, &pgconn.PgError{Code: "40001"}),
			},
			expected: true,
		},
		{
			name: "single retryable postgres deadlock error (pgx)",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, &pgconn.PgError{Code: "40P01"}),
			},
			expected: true,
		},
		{
			name: "single non-retryable postgres unique violation",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, &pq.Error{Code: "23505"}),
			},
			expected: false,
		},
		{
			name: "single non-retryable postgres syntax error",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, &pq.Error{Code: "42601"}),
			},
			expected: false,
		},
		{
			name: "single non-retryable generic error",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, errors.New("generic error")),
			},
			expected: false,
		},
		{
			name: "multiple non-retryable errors",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, errors.New("error 1")),
				gqlerror.WrapPath(nil, &pq.Error{Code: "23505"}),
			},
			expected: false,
		},
		{
			name: "mixed errors with one retryable",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, errors.New("generic error")),
				gqlerror.WrapPath(nil, &pq.Error{Code: "40001"}),
				gqlerror.WrapPath(nil, errors.New("another error")),
			},
			expected: true,
		},
		{
			name: "mixed errors with multiple retryable",
			errs: gqlerror.List{
				gqlerror.WrapPath(nil, &pq.Error{Code: "40001"}),
				gqlerror.WrapPath(nil, &pgconn.PgError{Code: "40P01"}),
				gqlerror.WrapPath(nil, errors.New("generic error")),
			},
			expected: true,
		},
		{
			name: "error with nil cause",
			errs: gqlerror.List{
				&gqlerror.Error{
					Message: "test error",
					Path:    nil,
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gqltx.ErrIsRetryable(tt.errs)
			assert.Equal(t, tt.expected, result, "ErrIsRetryable(%v) = %v, want %v", tt.errs, result, tt.expected)
		})
	}
}

func TestErrIsRetryable_PostgresErrors_pq(t *testing.T) {
	// Test retryable postgres error codes with pq driver
	retryableCodes := []string{
		"40001", // serialization_failure
		"40P01", // deadlock_detected
	}

	for _, code := range retryableCodes {
		t.Run("retryable_pq_code_"+code, func(t *testing.T) {
			err := &pq.Error{Code: pq.ErrorCode(code)}
			errs := gqlerror.List{
				gqlerror.WrapPath(nil, err),
			}
			result := gqltx.ErrIsRetryable(errs)
			assert.True(t, result, "Postgres error code %s should be retryable", code)
		})
	}

	// Test some non-retryable postgres codes with pq driver
	nonRetryableCodes := []string{
		"23505", // unique_violation
		"42601", // syntax_error
		"42P01", // undefined_table
		"42703", // undefined_column
		"08000", // connection_exception (not in current retryable list)
	}

	for _, code := range nonRetryableCodes {
		t.Run("non_retryable_pq_code_"+code, func(t *testing.T) {
			err := &pq.Error{Code: pq.ErrorCode(code)}
			errs := gqlerror.List{
				gqlerror.WrapPath(nil, err),
			}
			result := gqltx.ErrIsRetryable(errs)
			assert.False(t, result, "Postgres error code %s should not be retryable", code)
		})
	}
}

func TestErrIsRetryable_PostgresErrors_pgx(t *testing.T) {
	// Test retryable postgres error codes with pgx driver
	retryableCodes := []string{
		"40001", // serialization_failure
		"40P01", // deadlock_detected
	}

	for _, code := range retryableCodes {
		t.Run("retryable_pgx_code_"+code, func(t *testing.T) {
			err := &pgconn.PgError{Code: code}
			errs := gqlerror.List{
				gqlerror.WrapPath(nil, err),
			}
			result := gqltx.ErrIsRetryable(errs)
			assert.True(t, result, "Postgres error code %s should be retryable", code)
		})
	}

	// Test some non-retryable postgres codes with pgx driver
	nonRetryableCodes := []string{
		"23505", // unique_violation
		"42601", // syntax_error
		"42P01", // undefined_table
		"42703", // undefined_column
		"08000", // connection_exception (not in current retryable list)
	}

	for _, code := range nonRetryableCodes {
		t.Run("non_retryable_pgx_code_"+code, func(t *testing.T) {
			err := &pgconn.PgError{Code: code}
			errs := gqlerror.List{
				gqlerror.WrapPath(nil, err),
			}
			result := gqltx.ErrIsRetryable(errs)
			assert.False(t, result, "Postgres error code %s should not be retryable", code)
		})
	}
}

func TestErrIsRetryable_ComplexScenarios(t *testing.T) {
	t.Run("first error retryable", func(t *testing.T) {
		errs := gqlerror.List{
			gqlerror.WrapPath(nil, &pq.Error{Code: "40001"}),
			gqlerror.WrapPath(nil, errors.New("generic")),
			gqlerror.WrapPath(nil, errors.New("another")),
		}
		result := gqltx.ErrIsRetryable(errs)
		assert.True(t, result)
	})

	t.Run("middle error retryable", func(t *testing.T) {
		errs := gqlerror.List{
			gqlerror.WrapPath(nil, errors.New("generic")),
			gqlerror.WrapPath(nil, &pgconn.PgError{Code: "40P01"}),
			gqlerror.WrapPath(nil, errors.New("another")),
		}
		result := gqltx.ErrIsRetryable(errs)
		assert.True(t, result)
	})

	t.Run("last error retryable", func(t *testing.T) {
		errs := gqlerror.List{
			gqlerror.WrapPath(nil, errors.New("generic")),
			gqlerror.WrapPath(nil, errors.New("another")),
			gqlerror.WrapPath(nil, &pq.Error{Code: "40001"}),
		}
		result := gqltx.ErrIsRetryable(errs)
		assert.True(t, result)
	})

	t.Run("mixed nil and non-nil errors with retryable", func(t *testing.T) {
		errs := gqlerror.List{
			nil,
			gqlerror.WrapPath(nil, errors.New("generic")),
			nil,
			gqlerror.WrapPath(nil, &pq.Error{Code: "40001"}),
			nil,
		}
		result := gqltx.ErrIsRetryable(errs)
		assert.True(t, result)
	})

	t.Run("only nil errors", func(t *testing.T) {
		errs := gqlerror.List{nil, nil, nil}
		result := gqltx.ErrIsRetryable(errs)
		assert.False(t, result)
	})

	t.Run("wrapped retryable error", func(t *testing.T) {
		wrappedErr := errors.Join(&pq.Error{Code: "40001"}, errors.New("wrapped"))
		errs := gqlerror.List{
			gqlerror.WrapPath(nil, wrappedErr),
		}
		result := gqltx.ErrIsRetryable(errs)
		assert.True(t, result)
	})
}

func TestErrNoTransaction(t *testing.T) {
	t.Run("error message", func(t *testing.T) {
		assert.Equal(t, "no transaction found in context", gqltx.ErrNoTransaction.Error())
	})

	t.Run("error is unique", func(t *testing.T) {
		other := errors.New("no transaction found in context")
		assert.False(t, errors.Is(other, gqltx.ErrNoTransaction))
		assert.True(t, errors.Is(gqltx.ErrNoTransaction, gqltx.ErrNoTransaction))
	})
}
