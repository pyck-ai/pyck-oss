package admission_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/admission"
)

// capped names the fields the fuzzed limiter is built with: one slot, two
// slots, and a field it was never configured for. Three is enough for
// every accounting shape (a full cap, a partially full one, and a
// pass-through) while keeping a failing seed readable.
var capped = map[string]int{"one": 1, "two": 2}

const uncapped = "free"

// fuzzFields decodes the driver bytes into a sequence of operations, each
// a list of top-level fields. A byte is one field; the high bit ends the
// current operation, so the fuzzer can build documents that repeat a
// field, mix capped with uncapped ones, or select nothing at all.
func fuzzFields(driver []byte) [][]string {
	names := []string{"one", "two", uncapped, "__typename"}

	ops := make([][]string, 0, len(driver))
	current := make([]string, 0, len(driver))
	for _, b := range driver {
		current = append(current, names[int(b&0x03)])
		if b&0x80 != 0 {
			ops = append(ops, current)
			current = make([]string, 0, len(driver))
		}
	}
	return append(ops, current)
}

// docCtx builds the operation context of a mutation selecting fields in
// document order, which is all the limiter reads.
func docCtx(fields []string) context.Context {
	set := make(ast.SelectionSet, 0, len(fields))
	for _, f := range fields {
		set = append(set, &ast.Field{Name: f})
	}
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: set},
	})
}

// FuzzLimiterSlotAccounting guards slot leakage: the limiter's cap is a
// buffered channel per field, and a token that is taken but never handed
// back shrinks a replica's capacity for the rest of the process. Once the
// last token of registerTenant leaks, that mutation is refused on this
// replica forever: a self-inflicted outage no restart-free operation
// clears, and one reached by nothing more than the shape of the document a
// caller sends (repeats, aliases, a mix of capped and uncapped fields).
//
// The invariants are the ones a semaphore must hold under any arrival
// order: never more concurrent executions than the cap, no slot consumed
// by a refusal, and every slot back once the responses have been produced.
//
// Run continuously with:
//
//	go test ./backend/common/admission/ -run '^$' -fuzz FuzzLimiterSlotAccounting
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzLimiterSlotAccounting(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x80},                         // one operation selecting "one"
		{0x00, 0x80},                   // "one" twice in one document
		{0x81, 0x81, 0x81},             // three operations on "two"
		{0x80, 0x80, 0x80, 0x80},       // more operations than the cap of "one"
		{0x01, 0x02, 0x83},             // every field in one document
		{0x03, 0x83},                   // introspection beside a capped field
		{0x82, 0x82, 0x82},             // only the uncapped field
		{0x00, 0x01, 0x02, 0x03, 0x80}, // one long document
		{0xff, 0x00, 0x7f, 0x01},
	}
	for _, s := range seeds {
		f.Add(s, uint8(3))
	}

	f.Fuzz(func(t *testing.T, driver []byte, holdMask uint8) {
		// Bound the work: the property is about accounting, not volume,
		// and an unbounded driver only repeats the same transitions.
		if len(driver) > 64 {
			return
		}

		limiter := admission.New(capped)
		inFlight := map[string]int{}
		var held []func()

		for i, fields := range fuzzFields(driver) {
			ctx := docCtx(fields)

			executed := false
			handler := limiter.InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
				executed = true
				return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
			})
			require.NotNil(t, handler, "the interceptor must always answer")

			if !executed {
				// 1. A refusal never reaches the resolver, and it must
				//    hand back whatever it took on the way: reading its
				//    response is the only thing left to do with it.
				resp := handler(ctx)
				require.NotNil(t, resp)
				require.Len(t, resp.Errors, 1, "a refusal is one error, not a partial result")
				require.ErrorContains(t, resp.Errors[0], admission.ErrTooManyInFlight.Error())
				continue
			}

			// 2. An admitted operation holds one slot per distinct capped
			//    field it names, so the number running at once can never
			//    pass that field's cap.
			for _, field := range distinct(fields) {
				if _, limited := capped[field]; !limited {
					continue
				}
				inFlight[field]++
				require.LessOrEqual(t, inFlight[field], capped[field],
					"BYPASS: %d concurrent executions of %q admitted past its cap of %d (operation %d, driver %x)",
					inFlight[field], field, capped[field], i, driver)
			}

			complete := func() {
				handler(ctx)
				for _, field := range distinct(fields) {
					if _, limited := capped[field]; limited {
						inFlight[field]--
					}
				}
			}

			// The driver decides whether this execution finishes now or
			// stays in flight: a limiter only leaks under interleaving.
			if holdMask&(1<<(i%8)) != 0 {
				held = append(held, complete)
				continue
			}
			complete()
		}

		for _, complete := range held {
			complete()
		}

		// 3. Every slot is back: whatever the sequence was, the limiter
		//    now admits a full cap of fresh executions of each field.
		for field, limit := range capped {
			for range limit {
				ctx := docCtx([]string{field})
				executed := false
				handler := limiter.InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
					executed = true
					return graphql.OneShot(&graphql.Response{})
				})
				require.True(t, executed,
					"LEAK: %q refuses a fresh execution after every response was produced (driver %x)",
					field, driver)
				handler(ctx)
			}
		}
	})
}

// distinct returns the field names of a document in document order, once
// each: the limiter takes one slot per distinct field, so the oracle above
// has to count the same way.
func distinct(fields []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// gqlgen's transports call a response handler until it answers nil, which
// is how graphql.OneShot terminates the websocket and incremental-delivery
// loops. A limiter that hands its slots back on every call instead of on
// the first drains a channel it no longer has a token in: the second call
// blocks forever, taking the request goroutine with it and, once another
// execution refills the channel, stealing that execution's slot too.
//
// Releasing exactly once is therefore a property of the limiter, not of
// the transports a service happens to mount today.
func TestLimiter_ResponseHandlerCalledTwiceReleasesOnce(t *testing.T) {
	t.Parallel()

	l := admission.New(map[string]int{limitedField: 1})
	ctx := mutationCtx(limitedField)

	handler := l.InterceptOperation(ctx, func(context.Context) graphql.ResponseHandler {
		return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"ok":true}`)})
	})
	require.NotNil(t, handler(ctx), "the first call carries the response")

	second := make(chan *graphql.Response, 1)
	go func() { second <- handler(ctx) }()

	select {
	case resp := <-second:
		assert.Nil(t, resp, "graphql.OneShot answers nil once it has been read")
	case <-time.After(5 * time.Second):
		t.Fatal("the second call to the response handler blocked: the limiter released a slot it no longer held")
	}

	// The slot is back exactly once, so the next execution is admitted
	// rather than refused by a capacity a double release moved elsewhere.
	c := &counter{}
	assertAdmitted(t, limitedField, admit(t, l, limitedField, c.next))
	assert.Equal(t, int64(1), c.calls.Load())
}
