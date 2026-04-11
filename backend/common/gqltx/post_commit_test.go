package gqltx_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/99designs/gqlgen/graphql"
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

func TestSharedContainer_HooksAndPatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctx = gqltx.EnsurePostCommitContainer(ctx)

	// Simulate multiple mutations registering hooks and patches concurrently.
	var hookOrder []string
	var patchOrder []string
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
			patchName := fmt.Sprintf("patch-%d", i)
			gqltx.AddResponsePatch(ctx, func(r *graphql.Response) error {
				mu.Lock()
				patchOrder = append(patchOrder, patchName)
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()

	// Run hooks first, then patches — same order as middleware.handleSuccess.
	err := gqltx.RunPostCommit(ctx)
	require.NoError(t, err)

	resp := &graphql.Response{Data: json.RawMessage(`{}`)}
	err = gqltx.RunResponsePatches(ctx, resp)
	require.NoError(t, err)

	assert.Len(t, hookOrder, 3, "all 3 hooks should have executed")
	assert.Len(t, patchOrder, 3, "all 3 patches should have executed")
}

func TestSharedContainer_PatchStopsOnFirstError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctx = gqltx.EnsurePostCommitContainer(ctx)

	var called []string
	gqltx.AddResponsePatch(ctx, func(r *graphql.Response) error {
		called = append(called, "first")
		return nil
	})
	gqltx.AddResponsePatch(ctx, func(r *graphql.Response) error {
		called = append(called, "second")
		return errors.New("patch failed")
	})
	gqltx.AddResponsePatch(ctx, func(r *graphql.Response) error {
		called = append(called, "third")
		return nil
	})

	resp := &graphql.Response{Data: json.RawMessage(`{}`)}
	err := gqltx.RunResponsePatches(ctx, resp)

	require.EqualError(t, err, "patch failed")
	assert.Equal(t, []string{"first", "second"}, called, "should stop after first error")
}

func TestSharedContainer_RejectAfterClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctx = gqltx.EnsurePostCommitContainer(ctx)

	gqltx.AddPostCommit(ctx, func() error { return nil })
	gqltx.AddResponsePatch(ctx, func(r *graphql.Response) error { return nil })

	// Close hooks.
	require.NoError(t, gqltx.RunPostCommit(ctx))
	// Close patches.
	resp := &graphql.Response{Data: json.RawMessage(`{}`)}
	require.NoError(t, gqltx.RunResponsePatches(ctx, resp))

	// Adding after close should be silently ignored (no panic).
	gqltx.AddPostCommit(ctx, func() error { return errors.New("should not run") })
	gqltx.AddResponsePatch(ctx, func(r *graphql.Response) error { return errors.New("should not run") })

	// Running again should return already-closed errors.
	require.Error(t, gqltx.RunPostCommit(ctx))
	require.Error(t, gqltx.RunResponsePatches(ctx, resp))
}
