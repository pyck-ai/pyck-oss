package gqltx_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
)

// TestPostCommitHook_DoubleFireOnRetry exercises the OCC retry path that
// hellmann issue #61 fingers as a duplicate-publish source.
//
// Scenario:
//   - Attempt 1: resolver runs, registers ONE post-commit hook via
//     gqltx.AddPostCommit, then surfaces a retryable Postgres error
//     (40001 / serialization failure — same retry class as ErrOCCConflict).
//   - Middleware rolls back the tx and re-enters handleMutationWithTx.
//   - Attempt 2: same resolver runs again, registers ONE more post-commit
//     hook, succeeds.
//   - Middleware commits and calls RunPostCommit.
//
// Correct behavior: exactly one hook fires (the second attempt's). The first
// attempt's hook was tied to a rolled-back tx and must be discarded.
//
// Buggy behavior (the bug under test): both hooks fire because
// EnsurePostCommitContainer short-circuits on retry (sees the container
// from attempt 1 still on attemptCtx) and the old hook list is preserved.
func TestPostCommitHook_DoubleFireOnRetry(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware[*mockTx, *mockTxClient](client, injectTx, "occprobe", 2)
	m, ok := mw.(*gqltx.Middleware[*mockTx])
	require.True(t, ok)

	// retryableErr satisfies gqltx.ErrIsRetryable via db.ErrIsRetryable
	// matching pq.Error code 40001 (serialization failure).
	retryableErr := gqlerror.WrapPath(nil, &pq.Error{Code: "40001"})

	var (
		hookFireCount atomic.Int64
		attempt       atomic.Int64
	)

	op := func(ctx context.Context) graphql.ResponseHandler {
		// Each call to the operation handler represents one tx attempt.
		// gqltx.AddPostCommit is invoked by the "resolver" (this closure)
		// on every attempt, mirroring how MutationEventHook works in
		// every service's Ent client.
		gqltx.AddPostCommit(ctx, func() error {
			hookFireCount.Add(1)
			return nil
		})

		// Return a response handler. The handler's value is the response
		// the middleware inspects to decide retry vs. commit.
		return func(callCtx context.Context) *graphql.Response {
			n := attempt.Add(1)
			if n == 1 {
				// First attempt: surface the retryable error.
				return &graphql.Response{
					Errors: gqlerror.List{retryableErr},
				}
			}
			// Subsequent attempts: success.
			return &graphql.Response{}
		}
	}

	// Build a Mutation OperationContext so InterceptOperation routes to
	// handleMutationWithTx (where the retry pipeline lives).
	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	respHandler := m.InterceptOperation(ctx, op)
	require.NotNil(t, respHandler)

	resp := respHandler(ctx)
	require.NotNil(t, resp)
	require.Empty(t, resp.Errors, "final response must succeed after retry")

	// Sanity: middleware actually retried.
	require.EqualValues(t, 2, attempt.Load(), "operation handler must have been called twice (one failed attempt + one retry)")
	require.GreaterOrEqual(t, client.beginCnt, 2, "tx must have been begun at least twice (per attempt)")

	// THE ASSERTION UNDER TEST.
	//
	// Expected: 1 — only the successful retry's hook fires.
	// Buggy:    2 — both hooks fire because the post-commit container
	//               from attempt 1 is preserved on the reused attemptCtx.
	require.EqualValues(t, 1, hookFireCount.Load(),
		"post-commit hook must fire exactly once per logical mutation; "+
			"got %d, which means the rolled-back attempt's hook leaked into the retry (issue #61 mechanism)",
		hookFireCount.Load())
}

// TestHandleFailure_PersistentRetryableError_IsBounded pins the fix on
// branch u/jan/handlefailure-attempt-ctx: a resolver that ALWAYS returns a
// retryable Postgres error must stop after maxRetries instead of looping
// forever.
//
// Before the fix, handleFailure recursed with the original ctx and each
// handleMutationWithTx frame reset its local attempts counter to 0, so
// *attempts never exceeded 1 and the `> maxRetries` guard never tripped —
// an unbounded retry loop. The fix decrements the budget through the ctx
// (db.WithMaxRetries(ctx, maxRetries-1)) so the chain terminates.
//
// With maxRetries=2 the operation handler must run exactly 3 times
// (1 initial attempt + 2 retries) and then surface the retryable error.
//
// Detecting the bug without hanging: against the UNFIXED code the retry
// chain never terminates on its own, which would hang the test until the
// `go test` timeout. To fail fast instead, the resolver flips to a
// NON-retryable error once it has been called `tripwire` times (well past
// the legitimate bound). That forces handleFailure to stop recursing, so
// the test always returns — and the final attempt count then exposes
// whether the budget bounded the chain (== maxRetries+1) or ran away
// (>= tripwire).
func TestHandleFailure_PersistentRetryableError_IsBounded(t *testing.T) {
	t.Parallel()

	const (
		maxRetries = 2
		// tripwire sits far above the legitimate maxRetries+1 so a correctly
		// bounded chain never reaches it; an unbounded chain hits it and is
		// broken out of, turning a hang into a fast, legible failure.
		tripwire = maxRetries + 8
	)

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware[*mockTx, *mockTxClient](client, injectTx, "boundprobe", maxRetries)
	m, ok := mw.(*gqltx.Middleware[*mockTx])
	require.True(t, ok)

	// Persistent serialization failure — never clears.
	retryableErr := gqlerror.WrapPath(nil, &pq.Error{Code: "40001"})
	// 23505 (unique_violation) is non-retryable: returning it makes
	// handleFailure stop recursing, breaking a runaway loop.
	loopBreakerErr := gqlerror.WrapPath(nil, &pq.Error{Code: "23505"})

	var attempt atomic.Int64
	op := func(ctx context.Context) graphql.ResponseHandler {
		return func(callCtx context.Context) *graphql.Response {
			if attempt.Add(1) > tripwire {
				// Unbounded recursion guard: stop the loop so the test fails
				// fast rather than hanging until the go test timeout.
				return &graphql.Response{Errors: gqlerror.List{loopBreakerErr}}
			}
			return &graphql.Response{Errors: gqlerror.List{retryableErr}}
		}
	}

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	resp := m.InterceptOperation(ctx, op)(ctx)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Errors, "exhausted retries must surface an error, not loop")

	got := attempt.Load()
	// Tripwire fired → the chain never bounded itself (the bug).
	require.Less(t, got, int64(tripwire),
		"retry loop is unbounded: the resolver was called %d times and only the "+
			"tripwire stopped it, which means the ctx-threaded retry budget is not "+
			"decrementing across handleMutationWithTx frames", got)
	// 1 initial attempt + maxRetries retries = 3 total, then stop.
	require.EqualValues(t, maxRetries+1, got,
		"operation must run exactly maxRetries+1 times; a different count means the "+
			"ctx-threaded budget is not bounding the recursion")
}
