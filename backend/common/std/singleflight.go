package std

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/pyck-ai/pyck/backend/common/log"
)

var (
	// ErrSharedCallType reports a singleflight result of an unexpected type.
	ErrSharedCallType = errors.New("shared call returned an unexpected type")
	// ErrSharedCallPanic reports a panic in fn, recovered so it reaches every
	// caller as an error instead of crashing the process. The stack is logged,
	// not carried in the error, which may travel to a client.
	ErrSharedCallPanic = errors.New("shared call panicked")
)

// SharedCall runs fn once per key for all concurrent callers. The call runs
// detached from the caller's cancellation, bounded by timeout, so a caller
// that goes away cannot fail the others sharing the call; each caller still
// stops waiting on its own context, and a caller that is already cancelled
// starts nothing. A panic in fn is returned as ErrSharedCallPanic: with
// DoChan, singleflight would otherwise re-panic on a goroutine nobody can
// recover. Errors are returned as-is, so caching an outcome is up to fn.
func SharedCall[T any](ctx context.Context, group *singleflight.Group, key string, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	shared := group.DoChan(key, func() (val any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.ForContext(ctx).Error().Interface("panic", r).Bytes("stack", debug.Stack()).Str("key", key).Msg("shared call panicked")
				err = fmt.Errorf("%w: %v", ErrSharedCallPanic, r)
			}
		}()
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		return fn(callCtx)
	})
	select {
	case result := <-shared:
		if result.Err != nil {
			return zero, result.Err
		}
		v, ok := result.Val.(T)
		if !ok {
			return zero, ErrSharedCallType
		}
		return v, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
