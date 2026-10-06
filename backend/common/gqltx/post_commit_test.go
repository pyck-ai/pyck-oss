package gqltx_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
)

func TestAddPostCommit_ExecutesHooks(t *testing.T) {
	ctx := context.Background()
	ctx = gqltx.EnsurePostCommitContainer(ctx)

	var called []string
	gqltx.AddPostCommit(ctx, func() error { called = append(called, "first"); return nil })
	gqltx.AddPostCommit(ctx, func() error { called = append(called, "second"); return errors.New("fail") })
	gqltx.AddPostCommit(ctx, func() error { called = append(called, "third"); return nil })

	err := gqltx.RunPostCommit(ctx)
	assert.Equal(t, []string{"first", "second", "third"}, called)
	assert.EqualError(t, err, "fail")
}

func TestAddPostCommit_NilFn(t *testing.T) {
	ctx := context.Background()
	ctx = gqltx.EnsurePostCommitContainer(ctx)
	// Should not panic or add anything
	gqltx.AddPostCommit(ctx, nil)
	// Run should not fail
	err := gqltx.RunPostCommit(ctx)
	assert.NoError(t, err)
}

func TestSharedContainer_ConcurrentHooks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctx = gqltx.EnsurePostCommitContainer(ctx)

	// Simulate multiple mutations registering hooks concurrently.
	var hookOrder []string
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("hook-%d", i)
			gqltx.AddPostCommit(ctx, func() error {
				mu.Lock()
				hookOrder = append(hookOrder, name)
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()

	err := gqltx.RunPostCommit(ctx)
	require.NoError(t, err)

	assert.Len(t, hookOrder, 3, "all 3 hooks should have executed")
}

func TestSharedContainer_RejectAfterClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctx = gqltx.EnsurePostCommitContainer(ctx)

	gqltx.AddPostCommit(ctx, func() error { return nil })

	// Close hooks.
	require.NoError(t, gqltx.RunPostCommit(ctx))

	// Adding after close should be silently ignored (no panic).
	gqltx.AddPostCommit(ctx, func() error { return errors.New("should not run") })

	// Running again should return already-closed errors.
	require.Error(t, gqltx.RunPostCommit(ctx))
}
