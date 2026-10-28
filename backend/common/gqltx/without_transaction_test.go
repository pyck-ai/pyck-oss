package gqltx_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/idempotency"
	"github.com/pyck-ai/pyck/backend/common/txid"
)

// exemptField and pooledField are the two top-level mutation fields these
// tests contrast: the first is registered with WithoutTransaction, the
// second is not, so every assertion about the exemption is paired with the
// unchanged behaviour of a neighbour operation on the same middleware.
const (
	exemptField = "registerTenant"
	pooledField = "createThing"
)

// mutationCtxForField builds the operation context of a single-field
// mutation, which is what TopLevelFieldName reads to decide whether the
// operation is exempt.
func mutationCtxForField(field string) context.Context {
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation:    ast.Mutation,
			Name:         "Mut",
			SelectionSet: ast.SelectionSet{&ast.Field{Name: field}},
		},
		Variables: map[string]any{"x": 1},
	})
}

// okResponse is the successful payload the synthetic resolvers return.
func okResponse() *graphql.Response {
	return &graphql.Response{Data: json.RawMessage(`{"ok":true}`)}
}

// A mutation named by WithoutTransaction must never check a connection out
// of the writer pool: the whole point of the exemption is that a resolver
// which waits on its own committed work does not pin one for the wait.
func TestWithoutTransaction_ListedMutationOpensNoTransaction(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2, gqltx.WithoutTransaction(exemptField))

	ctx := mutationCtxForField(exemptField)
	var sawTx bool
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		sawTx = c.Value(contextKeyTx{}) != nil
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.Zero(t, client.beginCnt, "no transaction may be opened for an exempt mutation")
	assert.False(t, client.tx.committed, "there is nothing to commit")
	assert.False(t, client.tx.rolledBack, "there is nothing to roll back")
	assert.False(t, sawTx,
		"no transaction may be injected: a resolver that found one would write into a tx nobody commits")
}

// The exemption removes the transaction, not the post-commit machinery:
// resolvers and the outbox hook register post-commit hooks
// unconditionally, so they must still run, and run on the same context
// the resolver saw.
func TestWithoutTransaction_RunsPostCommitHooks(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := mutationCtxForField(exemptField)
	hookRan := false
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		gqltx.AddPostCommit(c, func() error { hookRan = true; return nil })
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.True(t, hookRan, "post-commit hooks must still run without a transaction")
}

// Every operation gets its own post-commit container and its own
// transaction id: a container shared with a previous request would run that
// request's hooks again, and resolvers read the id unconditionally.
func TestWithoutTransaction_GivesEachOperationAFreshContainerAndTxID(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	// A hook registered on the incoming context belongs to an earlier
	// container; the operation's fresh one must not inherit it.
	outerCtx := gqltx.EnsurePostCommitContainer(mutationCtxForField(exemptField))
	strayRan := false
	gqltx.AddPostCommit(outerCtx, func() error { strayRan = true; return nil })

	seen := make([]uuid.UUID, 0, 2)
	run := func() {
		handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(outerCtx, func(c context.Context) graphql.ResponseHandler {
			id, ok := txid.FromContext(c)
			require.True(t, ok, "the resolver context must carry a transaction id")
			seen = append(seen, id)
			return graphql.OneShot(okResponse())
		})
		require.NotNil(t, handler(outerCtx))
	}
	run()
	run()

	assert.False(t, strayRan, "the operation must not run hooks from the container it inherited")
	require.Len(t, seen, 2)
	assert.NotEqual(t, uuid.Nil, seen[0])
	assert.NotEqual(t, seen[0], seen[1], "each operation gets its own transaction id")
}

// An error in the response is returned as it is. There is nothing to roll
// back (the resolver committed its own work) and re-running it would
// repeat the side effects, so neither the retry loop nor a rollback may be
// reachable on this path.
//
// The hooks still run, which is where this path parts company with the
// transactional one: there a rollback undid the write a hook announces,
// here the write stands, so dropping the hook would lose a notification
// about durable work while the response says the operation failed.
func TestWithoutTransaction_ErrorResponseIsNotRetriedOrRolledBack(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 5, gqltx.WithoutTransaction(exemptField))

	ctx := mutationCtxForField(exemptField)
	attempts := 0
	hookRan := false
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		attempts++
		gqltx.AddPostCommit(c, func() error { hookRan = true; return nil })
		return graphql.OneShot(&graphql.Response{
			Errors: gqlerror.List{{Message: "registration failed", Path: ast.Path{ast.PathName(exemptField)}}},
		})
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "registration failed", resp.Errors[0].Message, "the resolver's error reaches the client verbatim")
	assert.Equal(t, 1, attempts, "a failed exempt mutation is never re-run: its side effects already happened")
	assert.False(t, client.tx.rolledBack)
	assert.Zero(t, client.beginCnt)
	assert.True(t, hookRan, "the resolver's own commits stand, so their post-commit hook still owes its side effect")
}

// A failing post-commit hook is a diagnostic, not a client-visible failure:
// the work it follows is already committed, so the response must survive it
// unchanged.
func TestWithoutTransaction_PostCommitFailureDoesNotFailTheResponse(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := mutationCtxForField(exemptField)
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		gqltx.AddPostCommit(c, func() error { return errors.New("outbox drainer refused") })
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors, "a hook failure must not turn a committed mutation into a client error")
	assert.Equal(t, okResponse().Data, resp.Data, "the committed mutation's answer reaches the client unchanged")
}

// The exemption is per operation: a mutation that is not on the list keeps
// the writer transaction, so listing one operation cannot silently disable
// the transaction for the rest of the schema.
func TestWithoutTransaction_UnlistedMutationStillGetsATransaction(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := mutationCtxForField(pooledField)
	var injected *mockTx
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		injected, _ = c.Value(contextKeyTx{}).(*mockTx)
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.Equal(t, 1, client.beginCnt, "an unlisted mutation still opens the writer transaction")
	assert.True(t, client.tx.committed)
	assert.Same(t, client.tx, injected, "the transaction is injected as before")
}

// WithoutTransaction takes a list and accumulates across calls, so a
// service can register the option once per concern without the last call
// dropping the earlier names.
func TestWithoutTransaction_AccumulatesAcrossOptions(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithoutTransaction(exemptField, "secondOp"),
		gqltx.WithoutTransaction("thirdOp"))

	// One middleware, one counter: asserted in a plain loop rather than in
	// subtests, which would have to share both.
	for _, field := range []string{exemptField, "secondOp", "thirdOp"} {
		ctx := mutationCtxForField(field)
		handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
			return graphql.OneShot(okResponse())
		})
		require.NotNil(t, handler(ctx))
		assert.Zero(t, client.beginCnt, "%s must stay exempt", field)
	}
}

// An exempt mutation commits outside the middleware's control, so a lease
// taken here could neither roll back with it nor cache a response that
// matches what was committed. The Idempotency-Key is therefore ignored, not
// honoured: the store must not be touched at all.
func TestWithoutTransaction_IdempotencyKeyIsIgnored(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(uuid.New(), uuid.New())),
		gqltx.WithoutTransaction(exemptField))

	ctx := mutationCtxForField(exemptField)
	oc := graphql.GetOperationContext(ctx)
	oc.Headers = map[string][]string{idempotency.HeaderName: {"key-1"}}

	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})
	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.Empty(t, store.lookupCalls, "an exempt mutation must not consult the idempotency store")
	assert.Empty(t, store.insertCalls, "no lease may be taken for work this middleware does not commit")
	assert.Empty(t, store.markCalls, "no response may be cached under a key whose work was not transactional")
	assert.Zero(t, client.beginCnt)
}

// A cached idempotent response would be the one way an exempt mutation
// could answer without running: it must not happen either, or a second
// registration attempt would get the first one's answer without a row.
func TestWithoutTransaction_IdempotencyCacheHitDoesNotShortCircuit(t *testing.T) {
	t.Parallel()

	store := &fakeStore{record: &idempotency.Record{
		Status:   idempotency.StatusCommitted,
		Response: []byte(`{"cached":true}`),
	}}
	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0,
		gqltx.WithIdempotency(store, staticAuth(uuid.New(), uuid.New())),
		gqltx.WithoutTransaction(exemptField))

	ctx := mutationCtxForField(exemptField)
	graphql.GetOperationContext(ctx).Headers = map[string][]string{idempotency.HeaderName: {"key-1"}}

	ran := false
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		ran = true
		return graphql.OneShot(okResponse())
	})
	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.True(t, ran, "the resolver must run: there is no cached answer for a non-transactional mutation")
	assert.JSONEq(t, `{"ok":true}`, string(resp.Data))
	assert.Empty(t, store.lookupCalls)
}

// The exemption names mutations. A query whose top-level field happens to
// carry the same name must keep its read-only routing, which is the only
// thing that sends it to the reader pool.
func TestWithoutTransaction_QueryWithTheSameFieldNameKeepsReadOnlyRouting(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := graphql.WithOperationContext(t.Context(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation:    ast.Query,
			Name:         "Q",
			SelectionSet: ast.SelectionSet{&ast.Field{Name: exemptField}},
		},
	})
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})
	require.NotNil(t, handler(ctx))

	require.Equal(t, 1, client.beginCnt, "a query still opens its read transaction")
	require.NotNil(t, client.lastOpts)
	assert.True(t, client.lastOpts.ReadOnly, "the query must still be routed to the reader pool")
}

// Without the option nothing changes: the default is a transaction for
// every mutation, so a service that never calls WithoutTransaction cannot
// end up with an exempt operation by accident.
func TestWithoutTransaction_DefaultIsATransactionForEveryMutation(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0)

	ctx := mutationCtxForField(exemptField)
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})
	require.NotNil(t, handler(ctx))

	assert.Equal(t, 1, client.beginCnt, "the exemption must be opt-in")
	assert.True(t, client.tx.committed)
}

// ---------------------------------------------------------------------------
// The document decides which fields run, and the client writes the document
// ---------------------------------------------------------------------------

// multiFieldMutationCtx builds a mutation selecting several root fields in
// the order given. Mutation root fields execute serially and a failing one
// does not abort the rest, so a caller can put any field in front of the
// one it is after.
func multiFieldMutationCtx(fields ...string) context.Context {
	set := make(ast.SelectionSet, 0, len(fields))
	for _, f := range fields {
		set = append(set, &ast.Field{Name: f})
	}
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation, Name: "Mut", SelectionSet: set},
		Variables: map[string]any{"x": 1},
	})
}

// inlineFragmentMutationCtx builds `mutation { ... on Mutation { field } }`:
// a single root field wrapped in a selection that a walk over the
// SelectionSet's direct *ast.Field entries does not see at all.
func inlineFragmentMutationCtx(field string) context.Context {
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			Name:      "Mut",
			SelectionSet: ast.SelectionSet{&ast.InlineFragment{
				TypeCondition: "Mutation",
				SelectionSet:  ast.SelectionSet{&ast.Field{Name: field}},
			}},
		},
		Variables: map[string]any{"x": 1},
	})
}

// aliasedMutationCtx builds `mutation { f0: field f1: field … }`: one field
// name selected n times under different aliases, which is how a client asks
// for n executions of the same mutation in one request.
func aliasedMutationCtx(field string, n int) context.Context {
	set := make(ast.SelectionSet, 0, n)
	for i := range n {
		set = append(set, &ast.Field{Alias: fmt.Sprintf("f%d", i), Name: field})
	}
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation, Name: "Mut", SelectionSet: set},
		Variables: map[string]any{"x": 1},
	})
}

// An exempt field named second in the document must not drag the operation
// onto the transactional path: the exempt resolver commits its own work and
// then waits for it, so a transaction opened around it pins a writer
// connection for the whole wait, which is the hold the exemption exists to
// prevent, handed back to the caller by one extra field in the document.
func TestWithoutTransaction_ExemptFieldNamedSecondOpensNoTransaction(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2, gqltx.WithoutTransaction(exemptField))

	ctx := multiFieldMutationCtx(pooledField, exemptField)
	reached := false
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		reached = true
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Zero(t, client.beginCnt,
		"an operation naming the exempt field must not open a writer transaction, whatever else it names")
	assert.False(t, reached, "a document the middleware cannot serve either way is refused, not executed")
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, gqltx.ErrMixedTransactionExemption.Error())
	assert.Equal(t, ast.Path{ast.PathName(pooledField)}, resp.Errors[0].Path,
		"the refusal names a field the client asked for, or the federated gateway swallows it")
}

// The same document with the exempt field first is refused for the same
// reason: the other field's resolver would run without the transaction it
// expects. Order must not change the verdict.
func TestWithoutTransaction_ExemptFieldNamedFirstAlongsideAnotherIsRefused(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := multiFieldMutationCtx(exemptField, pooledField)
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Zero(t, client.beginCnt)
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, gqltx.ErrMixedTransactionExemption.Error())
}

// A single exempt field stays exempt however it is wrapped: the exemption
// is decided over the flattened selection set, not over the first
// *ast.Field the operation happens to carry.
func TestWithoutTransaction_ExemptFieldInsideAnInlineFragmentStaysExempt(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := inlineFragmentMutationCtx(exemptField)
	var hookRan bool
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(c context.Context) graphql.ResponseHandler {
		gqltx.AddPostCommit(c, func() error { hookRan = true; return nil })
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.Zero(t, client.beginCnt, "a fragment wrapper must not put the exempt resolver back into a transaction")
	assert.True(t, hookRan, "the exempt path still owes its post-commit hooks")
}

// A pooled mutation wrapped the same way keeps its transaction: the
// flattening must not lose the field, or every fragment-wrapped mutation
// would run without one.
func TestWithoutTransaction_PooledFieldInsideAnInlineFragmentKeepsItsTransaction(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := inlineFragmentMutationCtx(pooledField)
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.Equal(t, 1, client.beginCnt, "an unlisted mutation still opens the writer transaction")
	assert.True(t, client.tx.committed)
}

// A document that names only unlisted fields is unaffected by the
// all-or-nothing rule: multi-field mutations without an exempt field keep
// their single transaction.
func TestWithoutTransaction_MultipleUnlistedFieldsShareOneTransaction(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := multiFieldMutationCtx(pooledField, "secondPooled")
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.Equal(t, 1, client.beginCnt)
}

// Introspection fields are not mutations: Apollo Client and Relay inject
// __typename at the operation root, and an exempt mutation sent by such a
// client must not be refused as "mixed" for carrying one.
func TestWithoutTransaction_IntrospectionFieldDoesNotBreakTheExemption(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := multiFieldMutationCtx("__typename", exemptField)
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.Zero(t, client.beginCnt)
}

// Aliases select the same field several times, so a document naming the
// exempt field n times is n executions of a resolver whose whole reason for
// being exempt is that it waits, and the document is written by the
// caller. Served, one anonymous request would run n registrations serially,
// each holding the operation's admission slot for its full wait; the cap
// that is meant to bound concurrent registrations would bound one request.
// The exemption is for the only field of its operation, aliases included.
func TestWithoutTransaction_AliasedDuplicatesOfTheExemptFieldAreRefused(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 2, gqltx.WithoutTransaction(exemptField))

	ctx := aliasedMutationCtx(exemptField, 8)
	reached := false
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		reached = true
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.False(t, reached, "a document the middleware cannot serve once is not served n times")
	assert.Zero(t, client.beginCnt, "the refusal must cost no writer connection")
	assert.False(t, client.tx.rolledBack)
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, gqltx.ErrMixedTransactionExemption.Error())
	assert.Equal(t, ast.Path{ast.PathName(exemptField)}, resp.Errors[0].Path,
		"the refusal names a field the client asked for, or the federated gateway swallows it")
}

// The single exempt field keeps its exemption: the repeat check must key on
// how often the field is selected, not on the field being exempt at all.
func TestWithoutTransaction_SingleExemptFieldIsStillExempt(t *testing.T) {
	t.Parallel()

	client := &mockTxClient{tx: &mockTx{}}
	mw := gqltx.NewMiddleware(client, injectTx, "testns", 0, gqltx.WithoutTransaction(exemptField))

	ctx := aliasedMutationCtx(exemptField, 1)
	handler := mw.(*gqltx.Middleware[*mockTx]).InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(okResponse())
	})

	resp := handler(ctx)

	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors, "one aliased exempt field is one field")
	assert.Zero(t, client.beginCnt)
}
