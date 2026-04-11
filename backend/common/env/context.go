package env

import (
	"context"
	"reflect"
)

type contextKey struct {
	field string
}

func contextKeyFor[T any]() contextKey {
	return contextKey{
		field: reflect.TypeFor[T]().String(),
	}
}

func Context[T any](ctx context.Context, config *T) context.Context {
	return context.WithValue(ctx, contextKeyFor[T](), config)
}

func FromContext[T any](ctx context.Context) T {
	val, ok := ctx.Value(contextKeyFor[T]()).(*T)
	if !ok {
		panic("failed to retrieve config from context")
	}

	return *val
}
