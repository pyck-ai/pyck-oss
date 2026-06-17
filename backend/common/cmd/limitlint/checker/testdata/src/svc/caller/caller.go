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
