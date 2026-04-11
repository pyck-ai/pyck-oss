package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/pyck-ai/pyck/backend/common/std"
)

type txConfigKey struct{}

// WithMaxRetries configures context so that WrapTx retries tx specified
// number of times when encountering retryable errors.
// Setting retries to 0 will retry indefinitely.
func WithMaxRetries(ctx context.Context, retries int) context.Context {
	return context.WithValue(ctx, txConfigKey{}, retries)
}

func NumRetriesFromContext(ctx context.Context, defaultRetries int) int {
	if v := ctx.Value(txConfigKey{}); v != nil {
		if retries, ok := v.(int); ok && retries >= 0 {
			return retries
		}
	}
	return defaultRetries
}

var retryableStates = map[string]bool{
	"40001": true, // SerializationFailureError
	"40P01": true, // DeadlockDetectedError
}

func ErrIsRetryable(err error) bool {
	if err == nil {
		return false
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return retryableStates[string(pqErr.Code)]
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return retryableStates[pgErr.Code]
	}
	return false
}

func GenerateSavePointName() string {
	return fmt.Sprintf("transaction_sp_%d", time.Now().UnixNano())
}

func GetTimeoutDuration(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if ok {
		return time.Until(deadline)
	}

	return 0
}

var maxSleepDuration = 250 * time.Millisecond

const (
	minSleepIncrease = 3
	maxSleepIncrease = 5
)

func GetSleepDuration(attempt int) time.Duration {
	increase := time.Duration(rand.IntN(maxSleepIncrease-minSleepIncrease+1) + minSleepIncrease)
	sleepDuration := time.Duration(attempt) * increase * time.Millisecond

	return std.Min(sleepDuration, maxSleepDuration)
}
