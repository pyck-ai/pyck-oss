package admission_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/admission"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
)

const (
	// limitedField is the expensive anonymous mutation the limiter is
	// configured for in these tests; freeField is its unlimited neighbour.
	limitedField = "registerTenant"
	freeField    = "createThing"
)

// mutationCtx builds the operation context of a single-field mutation,
// which is what the limiter reads to find the operation's cap.
func mutationCtx(field string) context.Context {
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation:    ast.Mutation,
			Name:         "Mut",
			SelectionSet: ast.SelectionSet{&ast.Field{Name: field}},
		},
	})
}

// counter is a synthetic downstream: it records how many operations were
// let through and answers each of them with a fixed payload.
type counter struct{ calls atomic.Int64 }

func (c *counter) next(context.Context) graphql.ResponseHandler {
	c.calls.Add(1)
	return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
}

// admit runs one operation through the limiter and returns its response
// handler. Not calling the handler is how these tests hold a slot: the
// limiter releases it when the response handler runs, so an unread handler
// models an execution still in flight.
func admit(t *testing.T, l *admission.Limiter, field string, next graphql.OperationHandler) graphql.ResponseHandler {
	t.Helper()
	ctx := mutationCtx(field)
	h := l.InterceptOperation(ctx, next)
	require.NotNil(t, h)
	return h
}

// assertRefused reads a response handler and asserts it carries the
// refusal, addressed to the top-level field the client asked for.
func assertRefused(t *testing.T, h graphql.ResponseHandler) {
	t.Helper()
	const field = limitedField
	resp := h(mutationCtx(field))
	require.NotNil(t, resp)
	require.Len(t, resp.Errors, 1, "a refusal is one error, not a partial result")
	assert.Contains(t, resp.Errors[0].Message, admission.ErrTooManyInFlight.Error())
	assert.Contains(t, resp.Errors[0].Message, field, "the refusal names the operation that was refused")
	assert.Equal(t, ast.Path{ast.PathName(field)}, resp.Errors[0].Path,
		"the error must name the field the client asked for, or the federated gateway swallows it")
	assert.Nil(t, resp.Data, "a refused operation returns no data")
}

// assertAdmitted reads a response handler and asserts it carries the
// downstream's answer rather than a refusal.
func assertAdmitted(t *testing.T, field string, h graphql.ResponseHandler) {
	t.Helper()
	resp := h(mutationCtx(field))
	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
	assert.JSONEq(t, `{"ok":true}`, string(resp.Data))
}

func TestLimiter_IsAGqlgenOperationInterceptor(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	assert.Implements(t, (*graphql.HandlerExtension)(nil), l)
	assert.Implements(t, (*graphql.OperationInterceptor)(nil), l)
	assert.Equal(t, "Admission", l.ExtensionName())
	assert.NoError(t, l.Validate(nil))
}

// Executions up to the cap run: the limiter bounds concurrency, it does not
// serialize the operation.
func TestLimiter_AdmitsUpToTheCap(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 3})
	c := &counter{}

	handlers := make([]graphql.ResponseHandler, 0, 3)
	for range 3 {
		handlers = append(handlers, admit(t, l, limitedField, c.next))
	}

	assert.Equal(t, int64(3), c.calls.Load(), "every execution within the cap reaches the resolver")
	for _, h := range handlers {
		assertAdmitted(t, limitedField, h)
	}
}

// The (cap+1)-th concurrent execution is refused, and refused without ever
// reaching the resolver: the point of the bound is that the refusal costs
// nothing.
func TestLimiter_RefusesBeyondTheCap(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 2})
	c := &counter{}

	held := []graphql.ResponseHandler{
		admit(t, l, limitedField, c.next),
		admit(t, l, limitedField, c.next),
	}
	require.Equal(t, int64(2), c.calls.Load())

	refused := admit(t, l, limitedField, c.next)

	assert.Equal(t, int64(2), c.calls.Load(), "the refused operation must not reach the resolver")
	assertRefused(t, refused)

	// The held executions are unaffected by the refusal.
	for _, h := range held {
		assertAdmitted(t, limitedField, h)
	}
}

// A refusal answers now. It must not queue behind the in-flight executions,
// because a caller waiting for a slot is exactly the pile-up the limiter
// exists to prevent.
func TestLimiter_RefusalDoesNotWaitForASlot(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	defer assertAdmitted(t, limitedField, inFlight)

	start := time.Now()
	refused := admit(t, l, limitedField, c.next)
	assertRefused(t, refused)

	assert.Less(t, time.Since(start), time.Second,
		"the refusal must not block on the slot held by the in-flight execution")
}

// The slot belongs to one execution: once its response has been produced
// the next caller is admitted. A limiter that leaked slots would refuse
// every later request on a replica that had once been busy.
func TestLimiter_ReleasesTheSlotWithTheResponse(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	first := admit(t, l, limitedField, c.next)
	assertRefused(t, admit(t, l, limitedField, c.next))

	assertAdmitted(t, limitedField, first) // releases the slot

	second := admit(t, l, limitedField, c.next)
	assert.Equal(t, int64(2), c.calls.Load(), "the freed slot admits the next execution")
	assertAdmitted(t, limitedField, second)
}

// A refusal must not consume a slot of its own: the caller that was refused
// never held one, so the replica's capacity is unchanged afterwards.
func TestLimiter_RefusalConsumesNoSlot(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	for range 5 {
		assertRefused(t, admit(t, l, limitedField, c.next))
	}
	assertAdmitted(t, limitedField, inFlight)

	assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))
	assert.Equal(t, int64(2), c.calls.Load(),
		"five refusals must leave the single slot exactly as they found it")
}

// gqlgen's recover middleware turns a panicking resolver into an error
// response, so the panic unwinds through this extension. The slot must
// still come back, or one panicking request permanently shrinks the
// replica's capacity.
func TestLimiter_ReleasesTheSlotOnPanic(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	panicking := func(context.Context) graphql.ResponseHandler {
		return func(context.Context) *graphql.Response { panic("resolver exploded") }
	}

	h := admit(t, l, limitedField, panicking)
	assert.Panics(t, func() { h(mutationCtx(limitedField)) })

	c := &counter{}
	assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))
	assert.Equal(t, int64(1), c.calls.Load(), "the panicking execution must have returned its slot")
}

// A panic before the response handler exists (a later interceptor, or the
// executor before it hands back a handler) never reaches the handler's
// release. gqlgen's server recovers it, so the slot must come back from the
// interceptor itself or every such panic lowers the cap by one.
func TestLimiter_ReleasesTheSlotWhenALaterInterceptorPanics(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	panicking := func(context.Context) graphql.ResponseHandler { panic("interceptor exploded") }

	assert.Panics(t, func() { l.InterceptOperation(mutationCtx(limitedField), panicking) })

	c := &counter{}
	assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))
	assert.Equal(t, int64(1), c.calls.Load(), "the panicking interceptor's operation must have returned its slot")
}

// The cap is per operation: a mutation the limiter was not configured for
// is never refused, however busy its neighbour is.
func TestLimiter_UnlistedOperationPassesThrough(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	defer assertAdmitted(t, limitedField, inFlight)

	for range 10 {
		assertAdmitted(t, freeField, admit(t, l, freeField, c.next))
	}
	assert.Equal(t, int64(11), c.calls.Load(), "an unlimited operation is never refused")
}

// Two limited operations do not share a bound: exhausting one must not
// refuse the other, or one hot mutation would take the whole service down.
func TestLimiter_CapsAreIndependentPerOperation(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1, freeField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	assertRefused(t, admit(t, l, limitedField, c.next))

	assertAdmitted(t, freeField, admit(t, l, freeField, c.next))
	assertAdmitted(t, limitedField, inFlight)
}

// A cap of zero or less means "no bound", so an operator can switch the
// limiter off through configuration without the service growing a second
// code path.
func TestLimiter_NonPositiveCapIsUnlimited(t *testing.T) {
	t.Parallel()

	for name, limit := range map[string]int{"zero": 0, "negative": -1} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l := admission.New(map[string]int{limitedField: limit})
			c := &counter{}

			for range 20 {
				assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))
			}
			assert.Equal(t, int64(20), c.calls.Load())
		})
	}
}

// An operation gqlgen could not attach a field name to (an empty or
// malformed selection set) must pass through rather than match the empty
// key of the cap map.
func TestLimiter_OperationWithoutATopLevelFieldPassesThrough(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1, "": 1})
	c := &counter{}

	ctx := graphql.WithOperationContext(t.Context(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation, Name: "Mut"},
	})
	for range 5 {
		h := l.InterceptOperation(ctx, c.next)
		require.NotNil(t, h)
		resp := h(ctx)
		require.NotNil(t, resp)
		assert.Empty(t, resp.Errors)
	}
	assert.Equal(t, int64(5), c.calls.Load(),
		"a nameless operation must not be bounded by the empty-string key")
}

// The cap holds under real concurrency: the in-flight count never exceeds
// it, and every caller gets either an answer or a refusal, never a hang.
func TestLimiter_ConcurrentCallersNeverExceedTheCap(t *testing.T) {
	t.Parallel()

	const (
		maxInFlight = 4
		callers     = 64
	)
	l := admission.New(map[string]int{limitedField: maxInFlight})

	var inFlight, peak atomic.Int64
	var mu sync.Mutex
	next := func(context.Context) graphql.ResponseHandler {
		return func(context.Context) *graphql.Response {
			n := inFlight.Add(1)
			mu.Lock()
			if n > peak.Load() {
				peak.Store(n)
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			inFlight.Add(-1)
			return &graphql.Response{Data: json.RawMessage(`{"ok":true}`)}
		}
	}

	var admitted, refused atomic.Int64
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := mutationCtx(limitedField)
			resp := l.InterceptOperation(ctx, next)(ctx)
			if len(resp.Errors) == 0 {
				admitted.Add(1)
				return
			}
			assert.ErrorContains(t, errors.New(resp.Errors[0].Message), admission.ErrTooManyInFlight.Error())
			refused.Add(1)
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(callers), admitted.Load()+refused.Load(), "every caller is answered")
	assert.LessOrEqual(t, peak.Load(), int64(maxInFlight), "the cap is never exceeded")
	assert.Positive(t, admitted.Load(), "the limiter must admit work, not only refuse it")
}

// ---------------------------------------------------------------------------
// Ordering against the transaction middleware
// ---------------------------------------------------------------------------

type recordingTx struct{ committed, rolledBack bool }

func (t *recordingTx) Commit() error   { t.committed = true; return nil }
func (t *recordingTx) Rollback() error { t.rolledBack = true; return nil }

type recordingClient struct {
	mu       sync.Mutex
	beginCnt int
}

func (c *recordingClient) BeginTx(context.Context, *sql.TxOptions) (*recordingTx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginCnt++
	return &recordingTx{}, nil
}

func (c *recordingClient) begins() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.beginCnt
}

type txKey struct{}

func injectTx(ctx context.Context, tx *recordingTx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// The whole reason the limiter is an operation interceptor registered
// before gqltx is that a refusal must cost no database connection. This
// pins the composition gqlgen builds from that registration order: the
// limiter is the outer interceptor, the transaction middleware the inner
// one, and a refused operation never reaches it.
func TestLimiter_RefusedOperationNeverReachesTheTransactionMiddleware(t *testing.T) {
	t.Parallel()

	client := &recordingClient{}
	tx := gqltx.NewMiddleware(client, injectTx, "testns", 0).(*gqltx.Middleware[*recordingTx])
	l := admission.New(map[string]int{limitedField: 1})

	resolver := func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	}
	// gqlgen invokes operation interceptors in registration order, so the
	// first-registered extension wraps the second: admission(gqltx(resolver)).
	chain := func(ctx context.Context) graphql.ResponseHandler {
		return l.InterceptOperation(ctx, func(inner context.Context) graphql.ResponseHandler {
			return tx.InterceptOperation(inner, resolver)
		})
	}

	ctx := mutationCtx(limitedField)
	inFlight := chain(ctx)
	require.Equal(t, 1, client.begins(), "the admitted operation opens its transaction as usual")

	refused := chain(ctx)
	assert.Equal(t, 1, client.begins(),
		"the refused operation must not open a transaction: admission runs before a connection is checked out")
	assertRefused(t, refused)

	resp := inFlight(ctx)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors)
}

// ---------------------------------------------------------------------------
// The document is written by the caller
// ---------------------------------------------------------------------------

// multiFieldCtx builds the operation context of a mutation selecting
// several root fields in the order given. GraphQL executes mutation root
// fields serially and a failing one does not abort the rest, so a caller
// can put anything in front of the field it actually wants.
func multiFieldCtx(fields ...string) context.Context {
	set := make(ast.SelectionSet, 0, len(fields))
	for _, f := range fields {
		set = append(set, &ast.Field{Name: f})
	}
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation, Name: "Mut", SelectionSet: set},
	})
}

// inlineFragmentCtx builds `mutation { ... on Mutation { field } }`: one
// field, wrapped in a selection a walk over direct *ast.Field entries
// cannot see at all.
func inlineFragmentCtx(field string) context.Context {
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			Name:      "Mut",
			SelectionSet: ast.SelectionSet{&ast.InlineFragment{
				TypeCondition: "Mutation",
				SelectionSet:  ast.SelectionSet{&ast.Field{Name: field}},
			}},
		},
	})
}

// run puts one operation context through the limiter and reads its answer.
func run(t *testing.T, l *admission.Limiter, ctx context.Context, next graphql.OperationHandler) *graphql.Response {
	t.Helper()
	h := l.InterceptOperation(ctx, next)
	require.NotNil(t, h)
	resp := h(ctx)
	require.NotNil(t, resp)
	return resp
}

// assertRefusalOf asserts a response is the cap's refusal naming the
// limited field.
func assertRefusalOf(t *testing.T, resp *graphql.Response) {
	t.Helper()
	const field = limitedField
	require.Len(t, resp.Errors, 1, "a refusal is one error, not a partial result")
	assert.Contains(t, resp.Errors[0].Message, admission.ErrTooManyInFlight.Error())
	assert.Contains(t, resp.Errors[0].Message, field, "the refusal names the operation that was refused")
	assert.Nil(t, resp.Data, "a refused operation returns no data")
}

// The cap must hold wherever the limited field sits in the document. A
// limiter keyed on the operation's FIRST root field is switched off by
// naming any other field before the limited one, a bypass that costs the
// caller one extra field.
func TestLimiter_RefusesTheLimitedFieldWhereverItSitsInTheDocument(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	defer assertAdmitted(t, limitedField, inFlight)

	resp := run(t, l, multiFieldCtx(freeField, limitedField), c.next)

	assert.Equal(t, int64(1), c.calls.Load(),
		"an operation naming the limited field second must not reach the resolver either")
	assertRefusalOf(t, resp)
}

// The same for a field the caller hides in an inline fragment: it is one
// root field, invisible to a walk that only looks at direct *ast.Field
// selections.
func TestLimiter_RefusesTheLimitedFieldInsideAnInlineFragment(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	defer assertAdmitted(t, limitedField, inFlight)

	resp := run(t, l, inlineFragmentCtx(limitedField), c.next)

	assert.Equal(t, int64(1), c.calls.Load(),
		"a fragment wrapper must not hide the field from its own cap")
	assertRefusalOf(t, resp)
}

// A fragment spread resolves through its definition, which is what gqlgen
// hands the interceptor after validation.
func TestLimiter_RefusesTheLimitedFieldInsideAFragmentSpread(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	defer assertAdmitted(t, limitedField, inFlight)

	spreadCtx := graphql.WithOperationContext(t.Context(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation: ast.Mutation,
			Name:      "Mut",
			SelectionSet: ast.SelectionSet{&ast.FragmentSpread{
				Name: "F",
				Definition: &ast.FragmentDefinition{
					Name:         "F",
					SelectionSet: ast.SelectionSet{&ast.Field{Name: limitedField}},
				},
			}},
		},
	})
	resp := run(t, l, spreadCtx, c.next)

	assert.Equal(t, int64(1), c.calls.Load())
	assertRefusalOf(t, resp)
}

// An operation admitted while naming several limited fields holds one slot
// per field and gives all of them back; otherwise one such request
// permanently shrinks the replica's capacity for its neighbours.
func TestLimiter_ReleasesEverySlotAMultiFieldOperationTook(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1, freeField: 1})
	c := &counter{}

	resp := run(t, l, multiFieldCtx(freeField, limitedField), c.next)
	require.Empty(t, resp.Errors, "both caps have room, so the operation is admitted")

	assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))
	assertAdmitted(t, freeField, admit(t, l, freeField, c.next))
}

// Admission is all-or-nothing: a partially acquired operation must hand
// back what it took before the refusal is written, or a refused caller
// would leak a slot of the neighbour it never executed.
func TestLimiter_RefusedMultiFieldOperationLeaksNoSlot(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1, freeField: 1})
	c := &counter{}

	inFlight := admit(t, l, limitedField, c.next)
	defer assertAdmitted(t, limitedField, inFlight)

	// freeField's slot is taken first, then limitedField's cap refuses.
	resp := run(t, l, multiFieldCtx(freeField, limitedField), c.next)
	assertRefusalOf(t, resp)

	assertAdmitted(t, freeField, admit(t, l, freeField, c.next))
	assert.Equal(t, int64(2), c.calls.Load(),
		"the slot the refused operation had already taken must be back")
}

// aliasedCtx builds `mutation { f0: field f1: field … }`: one field name
// selected n times under different aliases, which is how a caller asks for
// n executions of the same operation in one request.
func aliasedCtx(field string, n int) context.Context {
	set := make(ast.SelectionSet, 0, n)
	for i := range n {
		set = append(set, &ast.Field{Alias: fmt.Sprintf("f%d", i), Name: field})
	}
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation, Name: "Mut", SelectionSet: set},
	})
}

// The cap bounds executions across callers, so one caller must not be able
// to hold all of it: a document that names the limited field under N
// aliases takes one slot, not N. Taking one per selection would let a
// single anonymous request deny the operation to everyone else on this
// replica for as long as it runs: the denial the cap exists to prevent,
// asked for in the caller's own document.
func TestLimiter_OneOperationTakesAtMostOneSlotPerField(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 3})
	c := &counter{}

	// Not reading this handler keeps the operation in flight, holding
	// whatever it took.
	aliased := l.InterceptOperation(aliasedCtx(limitedField, 3), c.next)
	require.NotNil(t, aliased)
	require.Equal(t, int64(1), c.calls.Load(), "the operation is admitted, once")

	assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))

	resp := aliased(mutationCtx(limitedField))
	require.NotNil(t, resp)
	assert.Empty(t, resp.Errors, "the aliased document is admitted on its single slot")
}

// The slot an aliased document took comes back once, and only once: an
// operation that released one slot per alias would hand back tokens it
// never put in the channel and block on the first of them.
func TestLimiter_AliasedOperationReleasesItsSingleSlot(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	c := &counter{}

	resp := run(t, l, aliasedCtx(limitedField, 4), c.next)
	require.Empty(t, resp.Errors)

	assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))
	assert.Equal(t, int64(2), c.calls.Load(), "the cap is free again after the aliased operation answered")
}
