package gqltx

import (
	"context"
)

// Extractor gets a transaction from context
type TxFunc[T any] func(context.Context) T

// ForContext returns the transaction via the provided extractor or ErrMissing.
func ForContext[T any](ctx context.Context, extract TxFunc[T]) (T, error) {
	v := extract(ctx)

	var zero T
	if any(v) == nil {
		return zero, ErrNoTransaction
	}

	return v, nil
}
