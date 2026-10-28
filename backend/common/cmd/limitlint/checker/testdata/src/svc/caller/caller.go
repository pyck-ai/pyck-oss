package caller

import (
	"context"

	gen "svc/ent/gentest"
)

// Each scenario below either triggers the linter or must be silent.
// Expectation markers are interpreted by analysistest.

func unsafeAll(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().All(ctx) // want `unsafe \.All\(ctx\) on Item`
}

func safeWithLimit(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Limit(50).All(ctx)
}

func safeLimitBeforeWhere(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Limit(50).Where().Order().All(ctx)
}

func safeLimitAfterWhere(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Where().Limit(50).All(ctx)
}

func unsafeWhereWithoutLimit(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Where().Order().All(ctx) // want `unsafe \.All\(ctx\) on Item`
}

// Event has no LimitMixin in the testdata schema; this must NOT be flagged.
func safeEventNoLimitMixin(ctx context.Context, c *gen.Client) {
	_, _ = c.Event.Query().All(ctx)
}

// --- .IDs(ctx) coverage: same silent cap as .All(ctx) ---

func unsafeIDs(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().IDs(ctx) // want `unsafe \.IDs\(ctx\) on Item`
}

func safeIDsWithLimit(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Limit(50).IDs(ctx)
}

// The suggested remedy must not itself trip the linter: .PagedIDs is the
// paged id-only drop-in and is not a flagged terminator.
func safePagedIDs(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().PagedIDs(ctx, 50)
}

func unsafeIDsWhereWithoutLimit(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Where().Order().IDs(ctx) // want `unsafe \.IDs\(ctx\) on Item`
}

// Event has no LimitMixin; .IDs must NOT be flagged either.
func safeEventIDsNoLimitMixin(ctx context.Context, c *gen.Client) {
	_, _ = c.Event.Query().IDs(ctx)
}

// Mutation .IDs(ctx) returns the rows an update/delete targets and is not
// subject to the read-query limit interceptor — must NOT be flagged.
func safeMutationIDs(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Mutate().IDs(ctx)
}

// allowMarkerIDs exercises the //limitlint:allow opt-out for .IDs(ctx) too.
func allowMarkerIDs(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().IDs(ctx) //limitlint:allow exercising the cap deliberately
}

func safeNonEntAll(ctx context.Context) {
	// .All on a non-query type must not be flagged.
	xs := []int{1, 2, 3}
	_ = xs
	_ = ctx
}

// nonContextAll defines a custom .All method whose argument is not a
// context — analyzer must NOT flag it.
type nonEntThing struct{}

func (nonEntThing) All(name string) []string { return nil }

func safeNonContextAll() {
	_ = nonEntThing{}.All("anything")
}

// receiverIntermediateVar exercises a known limitation: the linter walks
// chained selector calls and cannot see a Limit() applied through an
// intermediate variable. It SHOULD flag this even though a human would
// recognise the Limit is present. Documented in the analyzer doc.
func flaggedDespiteVarLimit(ctx context.Context, c *gen.Client) {
	q := c.Item.Query().Limit(50)
	_, _ = q.All(ctx) // want `unsafe \.All\(ctx\) on Item`
}

// allowMarkerSameLine exercises the //limitlint:allow opt-out on the
// same line as the call.
func allowMarkerSameLine(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().All(ctx) //limitlint:allow exercising the cap deliberately
}

// allowMarkerLineAbove exercises the //limitlint:allow opt-out on the
// line immediately above the call.
func allowMarkerLineAbove(ctx context.Context, c *gen.Client) {
	//limitlint:allow exercising the cap deliberately
	_, _ = c.Item.Query().All(ctx)
}

// allowMarkerWordBoundary verifies that the marker matches only with
// proper word boundaries — substrings like "limitlint:allowed" do NOT
// suppress the diagnostic.
func allowMarkerWordBoundary(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().All(ctx) //limitlint:allowed-or-whatever // want `unsafe \.All\(ctx\) on Item`
}

// allowMarkerBlankLineGap verifies that a blank line between marker and
// call breaks the association — the call IS flagged.
func allowMarkerBlankLineGap(ctx context.Context, c *gen.Client) {
	//limitlint:allow this comment is too far from the call

	_, _ = c.Item.Query().All(ctx) // want `unsafe \.All\(ctx\) on Item`
}

// --- Select / GroupBy projections: the receiver is *ItemSelect or
// *ItemGroupBy, not *ItemQuery, but the query runs through the same
// interceptors and the cap applies all the same ---

func unsafeSelectAll(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Where().Select("id").All(ctx) // want `unsafe \.All\(ctx\) on Item`
}

func safeSelectAllWithLimitBefore(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Limit(50).Select("id").All(ctx)
}

func safeSelectAllWithLimitAfter(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Select("id").Limit(50).All(ctx)
}

// The remedy the diagnostic suggests must not itself be flagged.
func safeSelectAllPages(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Select("id").AllPages(ctx, 50)
}

func unsafeSelectStrings(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Select("name").Strings(ctx) // want `unsafe \.Strings\(ctx\) on Item`
}

func unsafeSelectInts(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Select("n").Ints(ctx) // want `unsafe \.Ints\(ctx\) on Item`
}

func unsafeSelectFloat64s(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Select("f").Float64s(ctx) // want `unsafe \.Float64s\(ctx\) on Item`
}

func unsafeSelectBools(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Select("b").Bools(ctx) // want `unsafe \.Bools\(ctx\) on Item`
}

// .Scan takes the destination as a second argument; the context is still
// the first, which is what qualifies it.
func unsafeSelectScan(ctx context.Context, c *gen.Client) {
	var v []struct{ ID int }
	_ = c.Item.Query().Select("id").Scan(ctx, &v) // want `unsafe \.Scan\(ctx\) on Item`
}

func safeSelectStringsWithLimit(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Limit(50).Select("name").Strings(ctx)
}

func unsafeGroupByStrings(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().GroupBy("name").Strings(ctx) // want `unsafe \.Strings\(ctx\) on Item`
}

func unsafeGroupByScan(ctx context.Context, c *gen.Client) {
	var v []struct{ Name string }
	_ = c.Item.Query().GroupBy("name").Scan(ctx, &v) // want `unsafe \.Scan\(ctx\) on Item`
}

// GroupBy does not promote .Limit, so the bound has to sit before it.
func safeGroupByWithLimitBefore(ctx context.Context, c *gen.Client) {
	var v []struct{ Name string }
	_ = c.Item.Query().Limit(50).GroupBy("name").Scan(ctx, &v)
}

// Event has no LimitMixin; its projections must NOT be flagged either.
func safeEventSelectNoLimitMixin(ctx context.Context, c *gen.Client) {
	_, _ = c.Event.Query().Select("name").All(ctx)
	_, _ = c.Event.Query().Select("name").Strings(ctx)
}

func allowMarkerSelectStrings(ctx context.Context, c *gen.Client) {
	_, _ = c.Item.Query().Select("name").Strings(ctx) //limitlint:allow exercising the cap deliberately
}
