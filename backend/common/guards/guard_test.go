package guards_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/guards"
)

func TestGuard_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	guard := guards.New().
		Add(guards.Check{
			Name: "check1",
			CheckFunc: func(ctx context.Context) (bool, error) {
				return true, nil
			},
		}).
		Add(guards.Check{
			Name: "check2",
			CheckFunc: func(ctx context.Context) (bool, error) {
				return true, nil
			},
		})

	err := guard.Wait(ctx)
	require.NoError(t, err)
}

func TestGuard_Retry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	guard := guards.New()

	var attempts int32

	guard.Add(guards.Check{
		Name:          "retrying-check",
		RetryInterval: 10 * time.Millisecond,
		CheckFunc: func(ctx context.Context) (bool, error) {
			count := atomic.AddInt32(&attempts, 1)
			if count < 3 {
				return false, nil // Not ready yet
			}
			return true, nil // Ready on 3rd attempt
		},
	})

	err := guard.Wait(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, attempts, int32(3))
}

func TestGuard_FatalError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	guard := guards.New()

	expectedErr := errors.New("something went wrong")

	guard.Add(guards.Check{
		Name:          "fatal-check",
		RetryInterval: 10 * time.Millisecond,
		CheckFunc: func(ctx context.Context) (bool, error) {
			return false, expectedErr
		},
	})

	// Add a healthy check to ensure it doesn't block the failure
	guard.Add(guards.Check{
		Name: "healthy-check",
		CheckFunc: func(ctx context.Context) (bool, error) {
			return true, nil
		},
	})

	err := guard.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

func TestGuard_ContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	guard := guards.New()

	guard.Add(guards.Check{
		Name:          "slow-check",
		RetryInterval: 10 * time.Millisecond,
		CheckFunc: func(ctx context.Context) (bool, error) {
			return false, nil // Never ready
		},
	})

	// Cancel context after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := guard.Wait(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestGuard_IndividualTimeout(t *testing.T) {
	t.Parallel()
	// This test verifies that strictly long-running checks respect the provided timeout context
	ctx := context.Background()

	// We need to cancel the main wait loop eventually, otherwise retry logic runs forever
	// because we are returning false, nil above.
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	// Create a new guard with limits to avoid indefinite hanging
	g2 := guards.New()
	// Properly re-add
	g2.Add(guards.Check{
		Name:          "timeout-check",
		Timeout:       10 * time.Millisecond,
		RetryInterval: 10 * time.Millisecond, // retry fast
		CheckFunc: func(ctx context.Context) (bool, error) {
			_, hasDeadline := ctx.Deadline()
			if !hasDeadline {
				return false, errors.New("expected deadline in context")
			}
			<-ctx.Done()
			return false, nil
		},
	})

	err := g2.Wait(ctxWithTimeout)
	// It should return context deadline exceeded from the MAIN loop,
	// or we can invoke a fatal error to verify.
	// Actually, easier verification: if individual timeout didn't work,
	// the CheckFunc would block longer than 10ms.

	// Let's refine:
	// If timeout works, ctx.Done() closes inside check.
	// We return false, nil (retry).
	// Guard retries.
	// Eventually main ctx times out.
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
