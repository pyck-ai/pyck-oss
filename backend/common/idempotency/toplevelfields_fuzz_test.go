package idempotency_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/pyck-ai/pyck/backend/common/idempotency"
)

// gateSchema is the smallest schema that lets a fuzzed document exercise
// every shape a caller can hide a top-level field in: two mutation fields
// (one of them the field the gates single out), a query field of the same
// name, and an object type so fragments have somewhere to nest.
var gateSchema = gqlparser.MustLoadSchema(&ast.Source{
	Name: "gate.graphql",
	Input: `
		type Query { registerTenant: Thing createThing: Thing }
		type Mutation { registerTenant: Thing createThing: Thing deleteTenant: Thing }
		type Thing { id: String name: String }
	`,
})

// FuzzTopLevelFieldNames guards gate blindness: every gate that decides
// what an operation is allowed to do (this package's single-mutation
// rule, gqltx's WithoutTransaction exemption and the admission cap)
// decides over the slice this walk returns, while gqlgen executes the set
// its own graphql.CollectFields returns. A field the executor runs that
// the walk does not report is a gate bypass: the caller writes the
// document, so hiding registerTenant inside a fragment wrapper would buy
// an anonymous caller both the transaction exemption and a free pass on
// the admission cap for a mutation that provisions a whole tenant.
//
// The invariant is one-directional on purpose: the walk may report MORE
// than the executor runs (it ignores @skip/@include, which fold a field
// away at execution time) because a gate that sees more only ever
// refuses more. It may never report less.
//
// Run continuously with:
//
//	go test ./backend/common/idempotency/ -run '^$' -fuzz FuzzTopLevelFieldNames
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzTopLevelFieldNames(f *testing.F) {
	seeds := []string{
		// real documents
		`mutation { registerTenant { id } }`,
		`mutation { registerTenant { id } __typename }`,
		`query { registerTenant { id } }`,
		`mutation Reg { createThing { id } }`,
		// break-out attempts: the field hidden from a gate that reads
		// only the direct *ast.Field entries of the selection set
		`mutation { ... on Mutation { registerTenant { id } } }`,
		`mutation { ...F } fragment F on Mutation { registerTenant { id } }`,
		`mutation { ...F ...F } fragment F on Mutation { registerTenant { id } }`,
		`mutation { ...A } fragment A on Mutation { ...B } fragment B on Mutation { registerTenant { id } }`,
		// doubling spreads: 2^N names per N levels if a fragment is
		// expanded once per spread instead of once per walk
		`mutation { ...F0 } fragment F0 on Mutation { ...F1 ...F1 } fragment F1 on Mutation { ...F2 ...F2 } fragment F2 on Mutation { registerTenant { id } }`,
		`mutation { ... on Mutation { ... on Mutation { ... on Mutation { registerTenant { id } } } } }`,
		// the same field N times, which is N executions of it
		`mutation { a: registerTenant { id } b: registerTenant { id } }`,
		`mutation { registerTenant { id } registerTenant { name } }`,
		`mutation { ...F a: registerTenant { id } } fragment F on Mutation { registerTenant { id } }`,
		// the exempt field next to another one
		`mutation { registerTenant { id } createThing { id } }`,
		`mutation { createThing { id } ... on Mutation { registerTenant { id } } }`,
		// directives, introspection and aliases of introspection
		`mutation { registerTenant { id } @skip(if: true) }`,
		`mutation { registerTenant { id } ... on Mutation @skip(if: true) { createThing { id } } }`,
		`mutation { __typename registerTenant { id } }`,
		`mutation { t: __typename registerTenant { id } }`,
		// nothing a gate can act on
		`mutation { __typename }`,
		`query { __schema { types { name } } }`,
		``,
		`{`,
		`mutation`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, query string) {
		// Bound the input: a megabyte of nested fragments measures
		// gqlparser's recursion, not this walk.
		if len(query) > 4096 {
			return
		}

		// LoadQueryWithRules with the default rule set is what
		// gqlgen's executor runs before any interceptor sees the
		// document; LoadQuery is its deprecated alias.
		doc, errs := gqlparser.LoadQueryWithRules(gateSchema, query, rules.NewDefaultRules())
		if len(errs) != 0 || doc == nil || len(doc.Operations) == 0 {
			return // gqlgen validates before any gate runs
		}
		op := doc.Operations[0]
		if len(op.VariableDefinitions) != 0 {
			return // the oracle below needs the variable values to fold @skip
		}

		names := idempotency.TopLevelFieldNames(op.SelectionSet)

		// 1. Introspection is never reported: a gate counting __typename
		//    would refuse every request from Apollo Client or Relay.
		for _, name := range names {
			require.False(t, strings.HasPrefix(name, "__"),
				"introspection field %q reported to a gate for %q", name, query)
		}

		// 2. The walk is a pure function of the document.
		require.Equal(t, names, idempotency.TopLevelFieldNames(op.SelectionSet),
			"the walk must be deterministic for %q", query)

		// 3. Accepted ⟹ the gate sees at least what gqlgen executes, per
		//    field name. graphql.CollectFields is the executor's own
		//    collection, so this is an agreement check against the
		//    implementation the gate's decision is really about, not a
		//    second copy of the walk.
		for name, runs := range executedFieldCounts(op, doc) {
			require.GreaterOrEqual(t, countName(names, name), runs,
				"BYPASS: gqlgen executes %q %d time(s) but the gates see it %d time(s) in %q",
				name, runs, countName(names, name), query)
		}
	})
}

// executedFieldCounts is the oracle: the top-level fields gqlgen's own
// collection would hand the executor, counted per field name. Introspection
// is dropped because the walk under test drops it by contract.
func executedFieldCounts(op *ast.OperationDefinition, doc *ast.QueryDocument) map[string]int {
	root := "Mutation"
	if op.Operation == ast.Query {
		root = "Query"
	}

	opCtx := &graphql.OperationContext{
		RawQuery:  "",
		Variables: map[string]any{},
		Doc:       doc,
		Operation: op,
	}

	counts := map[string]int{}
	for _, collected := range graphql.CollectFields(opCtx, op.SelectionSet, []string{root}) {
		if strings.HasPrefix(collected.Name, "__") {
			continue
		}
		counts[collected.Name]++
	}
	return counts
}

// countName counts how often a field name appears in the walk's output.
func countName(names []string, want string) int {
	n := 0
	for _, name := range names {
		if name == want {
			n++
		}
	}
	return n
}

// TestTopLevelFieldNames_DoublingFragmentSpreadsStayBounded pins the bound
// the gates depend on: the work of deciding about a document is a function
// of that document's size.
//
// A fragment that spreads the next one twice doubles the expansion per
// level, so N levels are 2^N names for a document of a few hundred bytes.
// Measured on the expand-per-spread walk this replaced: depth 20 (901
// bytes) produced 1,048,576 names in 0.46 s of one core, depth 24 (1,073
// bytes) 16,777,216 names in 5.3 s, and depth 30 is an OOM. gqlparser's
// validation bounds fragment cycles, not expansion, so the document
// reaches the gates, and this walk runs for every mutation in every
// service and for every operation reaching an admission cap, neither of
// which requires a caller to be authenticated.
//
// One field is selected, once, so exactly one name is reported.
func TestTopLevelFieldNames_DoublingFragmentSpreadsStayBounded(t *testing.T) {
	t.Parallel()

	const depth = 20

	var query strings.Builder
	query.WriteString("mutation { ...F0 }")
	for i := range depth {
		fmt.Fprintf(&query, " fragment F%d on Mutation { ...F%d ...F%d }", i, i+1, i+1)
	}
	fmt.Fprintf(&query, " fragment F%d on Mutation { registerTenant { id } }", depth)

	doc, errs := gqlparser.LoadQueryWithRules(gateSchema, query.String(), rules.NewDefaultRules())
	require.Empty(t, errs, "the bomb is a valid document: validation does not stop it")

	names := idempotency.TopLevelFieldNames(doc.Operations[0].SelectionSet)

	require.Equal(t, []string{"registerTenant"}, names,
		"a %d-byte document must not expand to 2^%d names", query.Len(), depth)
}
