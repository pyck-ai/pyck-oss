package gqltx

import (
	"context"
	"sync"

	"github.com/99designs/gqlgen/graphql"
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

// postCommitContainer stores post-commit hooks and response patches for a single request/tx.
// Safe for concurrent use: resolvers may register hooks in parallel.
type postCommitContainer struct {
	mu            sync.Mutex
	hooks         []func() error
	closed        bool // set to true by RunPostCommit to block further hook registrations
	patches       []func(*graphql.Response) error
	patchesClosed bool // set to true by RunResponsePatches to block further patch registrations
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
		hooks:   make([]func() error, 0, 2),
		patches: make([]func(*graphql.Response) error, 0, 2),
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

// AddResponsePatch registers a function that can modify the serialized GraphQL response.
// Patches run after post-commit hooks, so they can incorporate data that only becomes
// available after commit (e.g., workflow IDs from async signal replies).
// No-op when fn is nil or the container is missing/closed.
func AddResponsePatch(ctx context.Context, fn func(*graphql.Response) error) {
	if fn == nil {
		return
	}

	c, ok := getPostCommitContainer(ctx)
	if !ok {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.patchesClosed {
		return
	}
	c.patches = append(c.patches, fn)
}

// HasResponsePatches reports whether the post-commit container exists in ctx.
func HasResponsePatches(ctx context.Context) bool {
	_, ok := getPostCommitContainer(ctx)
	return ok
}

// RunResponsePatches executes all registered patches against the response.
// Returns immediately on the first error encountered. Marks the container closed.
func RunResponsePatches(ctx context.Context, r *graphql.Response) error {
	c, ok := getPostCommitContainer(ctx)
	if !ok {
		return ErrNoPostCommitContainer
	}

	c.mu.Lock()
	if c.patchesClosed {
		c.mu.Unlock()
		return ErrResponsePatchAlreadyClosed
	}
	patches := make([]func(*graphql.Response) error, len(c.patches))
	copy(patches, c.patches)
	c.patches = nil // allow GC
	c.patchesClosed = true
	c.mu.Unlock()

	for _, p := range patches {
		if err := p(r); err != nil {
			return err
		}
	}

	return nil
}
