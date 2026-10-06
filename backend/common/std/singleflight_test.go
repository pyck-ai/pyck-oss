package std_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"

	"github.com/pyck-ai/pyck/backend/common/std"
)

func TestSharedCall(t *testing.T) {
	t.Parallel()

	t.Run("the shared call outlives the caller that started it", func(t *testing.T) {
		t.Parallel()

		var group singleflight.Group
		callCtx := make(chan context.Context, 1)
		release := make(chan struct{})
		fn := func(ctx context.Context) (string, error) {
			callCtx <- ctx
			select {
			case <-release:
				return "value", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		ownerCtx, cancelOwner := context.WithCancel(t.Context())
		ownerErr := make(chan error, 1)
		go func() {
			_, err := std.SharedCall(ownerCtx, &group, "k", time.Minute, fn)
			ownerErr <- err
		}()
		ctx := <-callCtx

		cancelOwner()
		require.ErrorIs(t, <-ownerErr, context.Canceled, "the owner stops waiting on its own context")
		require.NoError(t, ctx.Err(), "the shared call is not cancelled with its owner")

		close(release)
	})

	t.Run("the shared call is bounded by the timeout", func(t *testing.T) {
		t.Parallel()

		var group singleflight.Group
		_, err := std.SharedCall(t.Context(), &group, "k", 10*time.Millisecond, func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("a cancelled caller starts nothing", func(t *testing.T) {
		t.Parallel()

		var group singleflight.Group
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		called := false
		_, err := std.SharedCall(ctx, &group, "k", time.Minute, func(context.Context) (int, error) {
			called = true
			return 1, nil
		})
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, called)
	})

	t.Run("a panic in fn is returned as an error and leaves the group usable", func(t *testing.T) {
		t.Parallel()

		var group singleflight.Group
		_, err := std.SharedCall(t.Context(), &group, "k", time.Minute, func(context.Context) (int, error) {
			panic("boom")
		})
		require.ErrorIs(t, err, std.ErrSharedCallPanic)
		require.ErrorContains(t, err, "boom")
		require.NotContains(t, err.Error(), "goroutine ", "the stack is logged, not returned")

		// The group is left clean: the key is callable again.
		v, err := std.SharedCall(t.Context(), &group, "k", time.Minute, func(context.Context) (int, error) {
			return 1, nil
		})
		require.NoError(t, err)
		require.Equal(t, 1, v)
	})

	t.Run("returns fn's error as-is", func(t *testing.T) {
		t.Parallel()

		var group singleflight.Group
		want := errors.New("boom")
		_, err := std.SharedCall(t.Context(), &group, "k", time.Minute, func(context.Context) (int, error) {
			return 0, want
		})
		require.ErrorIs(t, err, want)
	})
}
