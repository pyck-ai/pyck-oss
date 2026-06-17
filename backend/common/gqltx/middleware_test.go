package gqltx_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type mockTx struct {
	committed   bool
	rolledBack  bool
	commitErr   error
	rollbackErr error
}

func (m *mockTx) Commit() error {
	m.committed = true
	return m.commitErr
}
func (m *mockTx) Rollback() error {
	m.rolledBack = true
	return m.rollbackErr
}

type mockTxClient struct {
	tx       *mockTx
	txError  error
	lastOpts *sql.TxOptions
	beginCnt int
	mu       sync.Mutex
}

func (c *mockTxClient) BeginTx(ctx context.Context, opts *sql.TxOptions) (*mockTx, error) {
	c.mu.Lock()
	c.lastOpts = opts
	c.beginCnt++
	c.mu.Unlock()
	return c.tx, c.txError
}

type contextKeyTx struct{}

func injectTx(ctx context.Context, tx *mockTx) context.Context {
	return context.WithValue(ctx, contextKeyTx{}, tx)
}

func TestMiddleware_ConstructsExtension(t *testing.T) {
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2)
	assert.Implements(t, (*graphql.HandlerExtension)(nil), mw)
}

func TestMiddleware_BinderBeginInject(t *testing.T) {
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2)
	m, ok := mw.(*gqltx.Middleware[*mockTx])
	assert.True(t, ok)
	tx, err := m.Binder.Begin(context.Background(), nil)
	assert.NoError(t, err)
	ctx := m.Binder.Inject(context.Background(), tx)
	assert.Equal(t, tx, ctx.Value(contextKeyTx{}))
}

func TestMockTx_CommitRollback(t *testing.T) {
	tx := &mockTx{}
	assert.NoError(t, tx.Commit())
	assert.True(t, tx.committed)
	assert.NoError(t, tx.Rollback())
	assert.True(t, tx.rolledBack)

	tx.commitErr = errors.New("fail commit")
	assert.Error(t, tx.Commit())
	tx.rollbackErr = errors.New("fail rollback")
	assert.Error(t, tx.Rollback())
}

// TestContextPropagation traces exactly which contexts are used at each stage
// to understand if the transaction context actually reaches the field resolvers
func TestContextPropagation(t *testing.T) {
	t.Parallel()
	type contextKey string
	const markerKey contextKey = "marker"

	// Track which contexts we see at each stage
	type contextTracker struct {
		mutationCtx        context.Context //nolint:containedctx
		responseHandlerCtx context.Context //nolint:containedctx
		mu                 sync.Mutex
	}
	tracker := &contextTracker{}

	// Create mock transaction
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	// Create a mock operation handler that simulates mutation + field resolution
	operationHandler := func(opCtx context.Context) graphql.ResponseHandler {
		tracker.mu.Lock()
		tracker.mutationCtx = opCtx
		tracker.mu.Unlock()

		t.Logf("MUTATION CONTEXT:")
		t.Logf("  Has transaction: %v", opCtx.Value(contextKeyTx{}) != nil)
		t.Logf("  Marker value: %v", opCtx.Value(markerKey))

		// Return a response handler that simulates field resolution
		return func(respCtx context.Context) *graphql.Response {
			tracker.mu.Lock()
			tracker.responseHandlerCtx = respCtx
			tracker.mu.Unlock()

			t.Logf("RESPONSE HANDLER CONTEXT (where edge resolvers execute):")
			t.Logf("  Has transaction: %v", respCtx.Value(contextKeyTx{}) != nil)
			t.Logf("  Marker value: %v", respCtx.Value(markerKey))

			return &graphql.Response{Data: []byte(`{"success":true}`)}
		}
	}

	// Create operation context for a mutation
	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
		},
	}

	// Start with a context that has a marker
	baseCtx := context.WithValue(context.Background(), markerKey, "original")
	ctx := graphql.WithOperationContext(baseCtx, opCtx)

	// Execute through middleware
	responseHandler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, operationHandler)
	response := responseHandler(ctx)

	require.NotNil(t, response)
	require.Empty(t, response.Errors)

	// Analyze the contexts
	t.Log("\n=== CONTEXT ANALYSIS ===")

	tracker.mu.Lock()
	mutationCtx := tracker.mutationCtx
	responseHandlerCtx := tracker.responseHandlerCtx
	tracker.mu.Unlock()

	assert.NotNil(t, mutationCtx, "Mutation context should be set")
	assert.NotNil(t, responseHandlerCtx, "Response handler context should be set")

	// Check if transaction is in mutation context
	hasTxInMutation := mutationCtx.Value(contextKeyTx{}) != nil
	t.Logf("Transaction in mutation context: %v", hasTxInMutation)
	assert.True(t, hasTxInMutation, "Mutation should have transaction in context")

	// THE CRITICAL TEST: Does the response handler context have the transaction?
	hasTxInResponse := responseHandlerCtx.Value(contextKeyTx{}) != nil
	t.Logf("Transaction in response handler context: %v", hasTxInResponse)

	if !hasTxInResponse {
		t.Error("❌ BUG CONFIRMED: Response handler does not have transaction in context!")
		t.Error("This means edge resolvers cannot access the transaction from context")
		t.Error("They fall back to using the entity's stored driver (which becomes invalid after commit)")
	} else {
		t.Log("✓ Transaction is properly propagated to response handler")
	}
}

// TestMutationSerializesFieldResolvers verifies that field resolvers execute
// serially (not concurrently) during mutations to prevent concurrent transaction access.
// This is the positive case that should work with the fix.
func TestMutationSerializesFieldResolvers(t *testing.T) {
	t.Parallel()
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	// Create operation context for a mutation
	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
		},
		ResolverMiddleware: func(ctx context.Context, next graphql.Resolver) (interface{}, error) {
			return next(ctx)
		},
	}

	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	// Call MutateOperationContext to set up serialization
	gqlErr := mw.(*gqltx.Middleware[*mockTx]).MutateOperationContext(ctx, opCtx)
	require.Nil(t, gqlErr)

	// Track execution order and concurrency
	var (
		executionOrder   []int
		currentlyRunning int
		maxConcurrent    int
		mu               sync.Mutex
	)

	// Simulate 3 concurrent field resolvers (like item, from, to edges)
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Add(1)
		resolverID := i
		go func() {
			defer wg.Done()

			// Call the wrapped resolver middleware
			_, err := opCtx.ResolverMiddleware(ctx, func(ctx context.Context) (interface{}, error) {
				mu.Lock()
				currentlyRunning++
				if currentlyRunning > maxConcurrent {
					maxConcurrent = currentlyRunning
				}
				executionOrder = append(executionOrder, resolverID)
				mu.Unlock()

				// Simulate some work
				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				currentlyRunning--
				mu.Unlock()

				return struct{}{}, nil
			})
			assert.NoError(t, err)
		}()
	}

	wg.Wait()

	// Verify serialization: max 1 resolver running at a time
	assert.Equal(t, 1, maxConcurrent, "Field resolvers should execute serially (max 1 concurrent)")
	assert.Len(t, executionOrder, 3, "All 3 resolvers should have executed")
	t.Logf("Execution order: %v", executionOrder)
	t.Log("✓ Field resolvers executed serially during mutation")
}

// TestQuerySerializesFieldResolvers verifies that after Step 8.2 queries
// also get the per-tx mutex applied. Queries now share a single read-only
// *ent.Tx (and the underlying reader-pool *sql.Conn) across all field
// resolvers; concurrent statements on that connection would corrupt it
// the same way they would on a writer tx (FINDINGS §3.1).
func TestQuerySerializesFieldResolvers(t *testing.T) {
	t.Parallel()
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	// Track the original middleware
	originalMiddleware := func(ctx context.Context, next graphql.Resolver) (interface{}, error) {
		return next(ctx)
	}

	// Create operation context for a QUERY (not mutation)
	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Query, // This is a query, not a mutation
		},
		ResolverMiddleware: originalMiddleware,
	}

	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	// Call MutateOperationContext: should now wrap the resolver middleware
	// for queries the same way it does for mutations.
	gqlErr := mw.(*gqltx.Middleware[*mockTx]).MutateOperationContext(ctx, opCtx)
	require.Nil(t, gqlErr)

	var (
		currentlyRunning int
		maxConcurrent    int
		mu               sync.Mutex
	)

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_, err := opCtx.ResolverMiddleware(ctx, func(ctx context.Context) (interface{}, error) {
				mu.Lock()
				currentlyRunning++
				if currentlyRunning > maxConcurrent {
					maxConcurrent = currentlyRunning
				}
				mu.Unlock()

				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				currentlyRunning--
				mu.Unlock()

				return struct{}{}, nil
			})
			assert.NoError(t, err)
		}()
	}

	wg.Wait()

	// Queries must now serialize: max 1 resolver running at a time, exactly
	// like the mutation case. The per-tx mutex protects the shared *sql.Conn.
	assert.Equal(t, 1, maxConcurrent, "Query field resolvers should execute serially (max 1 concurrent)")
	t.Logf("Max concurrent query resolvers: %d", maxConcurrent)
}

// TestMutateOperationContextNilOperation verifies the middleware handles edge cases
func TestMutateOperationContextNilOperation(t *testing.T) {
	t.Parallel()
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	opCtx := &graphql.OperationContext{
		Operation: nil, // Nil operation
		ResolverMiddleware: func(ctx context.Context, next graphql.Resolver) (interface{}, error) {
			return next(ctx)
		},
	}

	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	gqlErr := mw.(*gqltx.Middleware[*mockTx]).MutateOperationContext(ctx, opCtx)
	assert.Nil(t, gqlErr, "Should handle nil operation gracefully")
}

// TestConcurrentTransactionAccess simulates the actual bug scenario:
// multiple goroutines trying to use the same transaction concurrently.
// This test verifies our fix prevents the race condition.
func TestConcurrentTransactionAccess(t *testing.T) {
	t.Parallel()
	// Track transaction access
	var (
		accessCount   int
		concurrent    int
		maxConcurrent int
		mu            sync.Mutex
	)

	// Create a mock transaction that tracks concurrent access
	tx := &mockTx{}
	client := &mockTxClient{tx: tx}

	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	// Create operation context for a mutation with serialization
	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
		},
		ResolverMiddleware: func(ctx context.Context, next graphql.Resolver) (interface{}, error) {
			return next(ctx)
		},
	}

	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	// Apply serialization
	gqlErr := mw.(*gqltx.Middleware[*mockTx]).MutateOperationContext(ctx, opCtx)
	require.Nil(t, gqlErr)

	// Simulate 5 field resolvers (like multiple edges) accessing transaction concurrently
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_, err := opCtx.ResolverMiddleware(ctx, func(ctx context.Context) (interface{}, error) {
				// Simulate transaction access
				mu.Lock()
				accessCount++
				concurrent++
				if concurrent > maxConcurrent {
					maxConcurrent = concurrent
				}
				mu.Unlock()

				// Simulate query execution (this would be QueryFrom(), QueryTo(), etc.)
				time.Sleep(5 * time.Millisecond)

				mu.Lock()
				concurrent--
				mu.Unlock()

				return struct{}{}, nil
			})
			assert.NoError(t, err)
		}()
	}

	wg.Wait()

	assert.Equal(t, 5, accessCount, "All 5 resolvers should have accessed the transaction")
	assert.Equal(t, 1, maxConcurrent, "Maximum 1 resolver should access transaction at a time")
	t.Logf("Total accesses: %d, Max concurrent: %d", accessCount, maxConcurrent)
	t.Log("✓ Concurrent transaction access is properly serialized")
}
