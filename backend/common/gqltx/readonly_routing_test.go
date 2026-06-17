package gqltx_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
)

// TestInterceptOperation_QueryUsesReadOnlyOpts is the unit-level proof for
// Step 8.2: a GraphQL query operation begins its tx with
// {ReadOnly: true, Isolation: REPEATABLE READ}, which is the signal
// pgMultiDriver.BeginTx (Step 8.1) reads to route the tx to the reader pool.
func TestInterceptOperation_QueryUsesReadOnlyOpts(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	// Query op.
	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Query},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	respHandler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(ctx context.Context) graphql.ResponseHandler {
		return func(ctx context.Context) *graphql.Response {
			return &graphql.Response{Data: []byte(`{"ok":true}`)}
		}
	})
	resp := respHandler(ctx)

	require.NotNil(t, resp)
	require.Empty(t, resp.Errors)

	client.mu.Lock()
	defer client.mu.Unlock()

	require.Equal(t, 1, client.beginCnt, "BeginTx must be called exactly once for a query")
	require.NotNil(t, client.lastOpts, "query must pass non-nil opts so pgMultiDriver routes to the reader")
	assert.True(t, client.lastOpts.ReadOnly, "query must set ReadOnly: true")
	assert.Equal(t, sql.LevelRepeatableRead, client.lastOpts.Isolation,
		"query must use REPEATABLE READ for snapshot consistency across statements")

	// Read-only success path commits (rollback would also be valid, but we
	// commit so the connection returns to the pool cleanly).
	assert.True(t, client.tx.committed, "read-only tx should commit on success")
	assert.False(t, client.tx.rolledBack, "read-only tx should not roll back on success")
}

// TestInterceptOperation_MutationUsesNilOpts proves mutations still pass
// nil opts so the writer pool's per-service default isolation applies
// (SERIALIZABLE everywhere, READ COMMITTED in inventory after Phase 6.4).
// Mutation routing must not regress just because queries gained their
// own tx path.
func TestInterceptOperation_MutationUsesNilOpts(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	respHandler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(ctx context.Context) graphql.ResponseHandler {
		return func(ctx context.Context) *graphql.Response {
			return &graphql.Response{Data: []byte(`{"ok":true}`)}
		}
	})
	resp := respHandler(ctx)

	require.NotNil(t, resp)
	require.Empty(t, resp.Errors)

	client.mu.Lock()
	defer client.mu.Unlock()

	require.Equal(t, 1, client.beginCnt, "BeginTx must be called exactly once for a mutation")
	assert.Nil(t, client.lastOpts, "mutation must pass nil opts so the writer pool's default isolation applies")
	assert.True(t, client.tx.committed, "mutation tx should commit on success")
}

// TestInterceptOperation_QueryRollsBackOnError exercises the error path:
// when the resolver pipeline returns errors, the read-only tx is rolled
// back rather than committed, releasing the reader-pool connection.
func TestInterceptOperation_QueryRollsBackOnError(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Query},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	respHandler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(ctx context.Context) graphql.ResponseHandler {
		return func(ctx context.Context) *graphql.Response {
			return graphql.ErrorResponse(ctx, "resolver failed")
		}
	})
	resp := respHandler(ctx)

	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Errors, "error response from resolver must propagate")

	assert.True(t, client.tx.rolledBack, "read-only tx should roll back on resolver error")
	assert.False(t, client.tx.committed, "read-only tx must not commit on resolver error")
}

// TestInterceptOperation_QueryInjectsTxIntoContext verifies that the tx
// returned by the binder is exposed to the resolver pipeline via the
// inject hook, the same as for mutations. Without this the resolvers
// would fall back to the entity's stored driver and bypass the read-only
// tx (which would then be wasted).
func TestInterceptOperation_QueryInjectsTxIntoContext(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Query},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	var sawTxInResolver bool
	respHandler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(opCtx context.Context) graphql.ResponseHandler {
		// The resolver pipeline runs with the injected ctx.
		if opCtx.Value(contextKeyTx{}) == client.tx {
			sawTxInResolver = true
		}
		return func(ctx context.Context) *graphql.Response {
			return &graphql.Response{Data: []byte(`{"ok":true}`)}
		}
	})
	respHandler(ctx)

	assert.True(t, sawTxInResolver, "query resolver pipeline must see the read-only tx in context")
}

// TestInterceptOperation_SubscriptionBypassesTx documents the chosen
// behavior for subscriptions. No service ships a Subscription type today
// (verified at the time of Step 8.2). If one were added, holding a
// read-only reader-pool connection open for the lifetime of the
// subscription would silently exhaust the pool. We bypass tx setup
// entirely so the foot-gun is more visible: the resolver receives no
// gqltx tx in context, and any DB call inside it would hit the entity's
// stored driver directly. Revisit this if subscriptions are introduced.
func TestInterceptOperation_SubscriptionBypassesTx(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Subscription},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	respHandler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(ctx context.Context) graphql.ResponseHandler {
		return func(ctx context.Context) *graphql.Response {
			return &graphql.Response{Data: []byte(`{"ok":true}`)}
		}
	})
	respHandler(ctx)

	client.mu.Lock()
	defer client.mu.Unlock()
	assert.Equal(t, 0, client.beginCnt, "subscription must not begin a tx")
}
