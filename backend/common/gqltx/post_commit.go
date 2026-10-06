package gqltx

import (
	"context"
	"sync"
)

// AddPostCommit registers a hook to run if the surrounding transaction succeeds.
// Panics if the middleware did not seed the container. No-op when fn is nil.
func AddPostCommit(ctx context.Context, fn func() error) {
	if fn == nil {
		return
	}

	c, ok := getPostCommitContainer(ctx)
	if !ok {
		// Return if container missing (middleware not run)
		// Optionally, could log or track this error
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		// Optionally, could log or track this error
		return
	}
	c.hooks = append(c.hooks, fn)
}

// postCommitKey is the private context key for the post-commit container.
type postCommitKey struct{}

// postCommitContainer stores post-commit hooks for a single request/tx.
// Safe for concurrent use: resolvers may register hooks in parallel.
type postCommitContainer struct {
	mu     sync.Mutex
	hooks  []func() error
	closed bool // set to true by RunPostCommit to block further hook registrations
}

// getPostCommitContainer retrieves the container from ctx if present.
func getPostCommitContainer(ctx context.Context) (*postCommitContainer, bool) {
	if v := ctx.Value(postCommitKey{}); v != nil {
		if c, ok := v.(*postCommitContainer); ok {
			return c, true
		}
	}
	return nil, false
}

// EnsurePostCommitContainer seeds a new container into ctx if missing.
// This is intended to be called by the Tx middleware ONLY.
func EnsurePostCommitContainer(ctx context.Context) context.Context {
	if _, ok := getPostCommitContainer(ctx); ok {
		return ctx
	}
	c := &postCommitContainer{
		hooks: make([]func() error, 0, 2),
	}
	return context.WithValue(ctx, postCommitKey{}, c)
}

// WithFreshPostCommitContainer always installs a new, empty container on ctx,
// shadowing any existing one. This is the variant the Tx middleware MUST use
// per attempt: on OCC retries the failing attempt's ctx (with its registered
// hooks) is reused, and EnsurePostCommitContainer would short-circuit, causing
// hooks from a rolled-back attempt to fire after the successful retry's commit.
func WithFreshPostCommitContainer(ctx context.Context) context.Context {
	c := &postCommitContainer{
		hooks: make([]func() error, 0, 2),
	}
	return context.WithValue(ctx, postCommitKey{}, c)
}

// RunPostCommit executes all registered hooks and returns the first error encountered.
// Returns error if container is missing or already closed.
// Best-effort: runs all hooks even if one fails. Marks the container closed.
func RunPostCommit(ctx context.Context) error {
	c, ok := getPostCommitContainer(ctx)
	if !ok {
		return ErrNoPostCommitContainer
	}

	// Snapshot & close under lock.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrPostCommitAlreadyClosed
	}
	hooks := make([]func() error, len(c.hooks))
	copy(hooks, c.hooks)
	c.hooks = nil // allow GC
	c.closed = true
	c.mu.Unlock()

	var firstErr error
	for _, h := range hooks {
		if err := h(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}
