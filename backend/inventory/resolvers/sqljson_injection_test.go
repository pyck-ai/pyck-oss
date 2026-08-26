package resolvers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testresolver "github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// This suite is the regression guard for the SQL-injection defect in issue
// #1382: the GraphQL *WhereInput JSON-data filters (Data / DataHasKey / DataIn
// / DataContains) and the order-by jsonPath passed the client-supplied JSON
// path straight into ent's sqljson.DotPath. On Postgres ent emits each path
// segment into raw SQL wrapped in single quotes WITHOUT escaping, so a single
// quote in the path broke out of the string literal and injected SQL into the
// WHERE / ORDER BY clause — neutralising the tenant-isolation filter and
// enabling blind, cross-tenant data extraction.
//
// backend/common/sqljsonpath.Validate now rejects any path whose segments fall
// outside the safe key alphabet, applied at every DotPath call site. Each case
// below feeds a break-out payload to one sink and asserts three things:
//
//  1. the request is rejected with an "invalid JSON path" error,
//  2. the injected pg_sleep never executes (the query returns in ~ms, proving
//     no injected SQL reached Postgres), and
//  3. the equivalent benign path still returns the seeded row (the fix does not
//     break legitimate JSON filtering / ordering).
//
// It MUST run on Postgres: the SQLite dialect used by the default setup()
// double-quotes path segments, so the break-out never reproduced there — which
// is exactly why the pre-existing SQLite filter/order tests missed this. Before
// the fix, every case here executed the injected pg_sleep instead of erroring.

const (
	// injectionSleep is how long the injected pg_sleep would block if the
	// injection took hold.
	injectionSleep = 2 * time.Second
	// injectionThreshold sits well above a benign query (single-digit ms) and
	// well below injectionSleep, so a rejected (fast) query and a successful
	// injection (slow) are never confused.
	injectionThreshold = 1200 * time.Millisecond
)

// sleepSubquery is a scalar boolean sub-select that would block for
// injectionSleep had the surrounding path broken out of its string literal.
func sleepSubquery() string {
	return fmt.Sprintf("(SELECT true FROM pg_sleep(%d))", int(injectionSleep.Seconds()))
}

func TestSQLInjection_JSONPathRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// where is the GraphQL where-filter literal carrying the injected path.
		where string
		// benignWhere is the same filter with a legitimate path that matches the
		// seeded item (validData has type=custom).
		benignWhere string
		// jsonPath / benignJSONPath drive the order-by sink instead of a filter.
		jsonPath       string
		benignJSONPath string
	}{
		{
			// Sink: sqljson.ValueEQ -> "data" ->> '<path>' = $1
			name:        "Data/ValueEQ",
			where:       fmt.Sprintf(`{ Data: ["x' = 'x' OR %s OR 'z", "v"] }`, sleepSubquery()),
			benignWhere: `{ Data: ["type", "custom"] }`,
		},
		{
			// Sink: sqljson.HasKey -> "data" -> '<path>' IS NOT NULL
			name:        "DataHasKey/HasKey",
			where:       fmt.Sprintf(`{ DataHasKey: "x' IS NOT NULL OR %s OR 'z" }`, sleepSubquery()),
			benignWhere: `{ DataHasKey: "type" }`,
		},
		{
			// Sink: sqljson.ValueIn -> "data" ->> '<path>' IN ($1, $2)
			name:        "DataIn/ValueIn",
			where:       fmt.Sprintf(`{ DataIn: ["x' = 'x' OR %s OR 'z", "v1", "v2"] }`, sleepSubquery()),
			benignWhere: `{ DataIn: ["type", "custom", "other"] }`,
		},
		{
			// Sink: sqljson.ValueContains -> ("data" ->> '<path>')::jsonb @> $1
			name:        "DataContains/ValueContains",
			where:       fmt.Sprintf(`{ DataContains: ["x')::jsonb @> ('\"x\"')::jsonb OR %s OR ('{}", "v"] }`, sleepSubquery()),
			benignWhere: `{ DataContains: ["type", "custom"] }`,
		},
		{
			// Sink: sqljson.OrderValue -> ORDER BY "data" ->> '<path>'
			name:           "OrderBy/jsonPath",
			jsonPath:       fmt.Sprintf("x' || (SELECT ''::text FROM pg_sleep(%d)) || 'z", int(injectionSleep.Seconds())),
			benignJSONPath: "sum",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			te := setup(t)
			t.Cleanup(func() { te.Close(t) })
			ctx := te.ctx(userA)
			te.newItem(ctx, userA).Data(validData).Create()

			var tpl testresolver.Template
			var injectArgs, benignArgs any
			if tc.jsonPath != "" {
				tpl = queryItemsJSONOrder
				injectArgs = map[string]any{"JSONPath": tc.jsonPath}
				benignArgs = map[string]any{"JSONPath": tc.benignJSONPath}
			} else {
				tpl = queryItemsWithFilter
				injectArgs = map[string]any{"Where": tc.where}
				benignArgs = map[string]any{"Where": tc.benignWhere}
			}

			// The injected path is rejected before any SQL is built...
			elapsed, errs := execTimed(te, ctx, tpl, injectArgs)
			require.NotEmpty(t, errs, "injection must be rejected with an error")
			assert.Contains(t, errs[0].Message, "invalid JSON path",
				"rejection must come from the JSON-path validator")

			// ...so the injected pg_sleep never ran.
			assert.Less(t, elapsed, injectionThreshold,
				"injected pg_sleep executed — path validation was bypassed")

			// The benign path still works: the seeded item is returned.
			benign := execOK[queryItemsData](te, ctx, tpl, benignArgs)
			assert.Equal(t, 1, benign.InventoryItems.TotalCount,
				"legitimate JSON path must still match")
		})
	}
}

// execTimed renders and executes tpl, returning how long the request took and
// any GraphQL errors, without failing the test on those errors.
func execTimed(te *testEnv, ctx context.Context, tpl testresolver.Template, args any) (time.Duration, []testresolver.GQLError) {
	te.t.Helper()
	start := time.Now()
	res := testresolver.Exec[json.RawMessage, *ent.Client](te.TestEnvironment, ctx, tpl, args)
	return time.Since(start), res.Errors
}
