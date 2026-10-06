package gqltx_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/txid"
)

// handlePayload mirrors a generated mutation payload carrying the per-tx
// handle field, as mutation resolvers assign it explicitly:
// &model.XOutput{..., TransactionID: gqltx.TransactionID(ctx)}.
type handlePayload struct {
	ID            string    `json:"id"`
	TransactionID uuid.UUID `json:"transactionID"`
}

// TestTransactionID_NoTransaction_ReturnsNil pins the helper's failure mode:
// without gqltx on the ctx it returns uuid.Nil, which the non-null ID!
// payload field serializes to null — failing the request loudly instead of
// shipping a bogus handle.
func TestTransactionID_NoTransaction_ReturnsNil(t *testing.T) {
	t.Parallel()
	assert.Equal(t, uuid.Nil, gqltx.TransactionID(context.Background()))
}

// TestTransactionID_ResolverAssignment_MatchesAttempt proves the explicit
// assignment contract: inside the gqltx pipeline the helper returns the
// current attempt's txid, so the payload carries the handle of the
// transaction that actually commits.
func TestTransactionID_ResolverAssignment_MatchesAttempt(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	var payload *handlePayload
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(attemptCtx context.Context) graphql.ResponseHandler {
		// What every mutation resolver does now:
		payload = &handlePayload{ID: "thing-1", TransactionID: gqltx.TransactionID(attemptCtx)}
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{}`)})
	})
	resp := handler(ctx)
	require.NotNil(t, resp)
	require.Empty(t, resp.Errors)

	require.NotNil(t, payload)
	assert.NotEqual(t, uuid.Nil, payload.TransactionID, "gqltx must have provided a per-tx UUID")
}

// TestTransactionID_OCCRetry_HandleMatchesCommittedAttempt proves that under
// an OCC retry (txid regenerated per attempt) the handle assigned by the
// resolver is the committing attempt's txid — the same one stamped on that
// attempt's outbox rows — not the rolled-back attempt's.
func TestTransactionID_OCCRetry_HandleMatchesCommittedAttempt(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2)

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	retryableErr := gqlerror.WrapPath(nil, &pq.Error{Code: "40001"})

	var attempt atomic.Int64
	var attemptTxIDs []uuid.UUID
	var payload *handlePayload

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(attemptCtx context.Context) graphql.ResponseHandler {
		attemptTxIDs = append(attemptTxIDs, gqltx.TransactionID(attemptCtx))
		payload = &handlePayload{ID: "thing-1", TransactionID: gqltx.TransactionID(attemptCtx)}

		return func(context.Context) *graphql.Response {
			if attempt.Add(1) == 1 {
				return &graphql.Response{Errors: gqlerror.List{retryableErr}}
			}
			return &graphql.Response{Data: json.RawMessage(`{}`)}
		}
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	require.Empty(t, resp.Errors, "mutation must succeed after the OCC retry")

	require.Len(t, attemptTxIDs, 2, "two attempts must have run")
	assert.NotEqual(t, attemptTxIDs[0], attemptTxIDs[1], "txid must be regenerated per attempt")
	require.NotNil(t, payload)
	assert.Equal(t, attemptTxIDs[1], payload.TransactionID,
		"the handle must be the committing attempt's txid")
}

// TestTransactionID_IdempotencyCachesCompleteBody proves that with the
// handle assigned in the resolver (in-tx, before the response is cached)
// and no post-commit patching, the body cached for idempotent replays
// equals the wire body, handle included.
func TestTransactionID_IdempotencyCachesCompleteBody(t *testing.T) {
	t.Parallel()

	tenantID, userID := uuid.New(), uuid.New()
	store := &fakeStore{}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(tenantID, userID)))

	ctx := mutationCtxWithHeader(t, "k-handle")

	var txID uuid.UUID
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(attemptCtx context.Context) graphql.ResponseHandler {
		txID = gqltx.TransactionID(attemptCtx)
		payload := &handlePayload{ID: "thing-1", TransactionID: txID}

		// The payload is serialized by gqlgen after the resolver pipeline;
		// mirror that here so the response body contains the handle.
		body, err := json.Marshal(map[string]any{"doHandle": payload})
		require.NoError(t, err)
		return graphql.OneShot(&graphql.Response{Data: body})
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	require.Empty(t, resp.Errors)

	// Wire body carries the handle...
	assert.Contains(t, string(resp.Data), txID.String())

	// ...and the cached body is byte-identical to the wire body: nothing
	// mutates the response after commit, so replays return the complete
	// response including the handle.
	require.Len(t, store.markCalls, 1)
	var cached struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(store.markCalls[0].Response, &cached))
	assert.JSONEq(t, string(resp.Data), string(cached.Data),
		"cached body must equal the wire body, handle included")
}

// TestEventCount_CountsRecordedEventsPerAttempt pins the counter's scope: it
// lives on the attempt's context, so a rolled-back OCC attempt's events don't
// leak into the committing attempt's count.
func TestEventCount_CountsRecordedEventsPerAttempt(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2)

	opCtx := &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation},
	}
	ctx := graphql.WithOperationContext(context.Background(), opCtx)

	retryableErr := gqlerror.WrapPath(nil, &pq.Error{Code: "40001"})

	var attempt atomic.Int64
	var counts []int

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(attemptCtx context.Context) graphql.ResponseHandler {
		n := int(attempt.Add(1))
		for range 2 * n { // attempt 1 writes 2 events, attempt 2 writes 4
			txid.RecordEvent(attemptCtx)
		}
		counts = append(counts, gqltx.EventCount(attemptCtx))

		return func(context.Context) *graphql.Response {
			if n == 1 {
				return &graphql.Response{Errors: gqlerror.List{retryableErr}}
			}
			return &graphql.Response{Data: json.RawMessage(`{}`)}
		}
	})

	resp := handler(ctx)
	require.NotNil(t, resp)
	require.Empty(t, resp.Errors)
	assert.Equal(t, []int{2, 4}, counts, "each attempt counts only its own events")
}

func TestEventCount_NoTransaction_IsZero(t *testing.T) {
	t.Parallel()
	txid.RecordEvent(context.Background()) // must not panic
	assert.Zero(t, gqltx.EventCount(context.Background()))
}

// TestInterceptField_EventCount proves the middleware fills a mutation
// payload's eventCount field from the transaction's counter at resolution
// time, whatever the resolver put in the struct, and leaves other fields
// alone.
func TestInterceptField_EventCount(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0).(*gqltx.Middleware[*mockTx])

	txCtx := txid.With(context.Background(), txid.New())
	txid.RecordEvent(txCtx)
	txid.RecordEvent(txCtx)
	txid.RecordEvent(txCtx)

	resolve := func(op ast.Operation, field string) any {
		t.Helper()
		ctx := graphql.WithOperationContext(txCtx, &graphql.OperationContext{
			Operation: &ast.OperationDefinition{Operation: op},
		})
		ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
			Field: graphql.CollectedField{Field: &ast.Field{Name: field}},
		})
		got, err := mw.InterceptField(ctx, func(context.Context) (any, error) { return 0, nil })
		require.NoError(t, err)
		return got
	}

	assert.Equal(t, 3, resolve(ast.Mutation, "eventCount"))
	assert.Equal(t, 0, resolve(ast.Mutation, "transactionID"), "other fields pass through")
	assert.Equal(t, 0, resolve(ast.Query, "eventCount"), "only mutations are intercepted")
}
