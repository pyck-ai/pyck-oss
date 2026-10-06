//go:build integration

// Package relationfilterisolation checks that a where-input relation filter
// (hasXWith, and plain hasX) can only evaluate rows the caller is allowed to
// read, and only live ones.
//
// Ent's tenant and soft-delete privacy filters scope the root table of a
// query. The EXISTS / IN sub-select that a relation filter compiles to is
// built by the generated Has<Edge>With or Has<Edge>() predicate and never
// passes through the privacy policy. Where a row of tenant A references a row
// of tenant B, the sub-select would evaluate the caller's predicates against
// B's row. Two such references are exercised: a legitimate one (a user homed
// in B who is a guest in A checks in on A's device) and an inventory item
// movement of A referencing B's item, which the API now refuses and the test
// therefore writes into the database, as a movement stored before that check
// could hold it. Soft-deleted neighbours and relation filters on edge
// connections (resolved by an edge traversal, not a root query) are covered
// too.
//
// Each test asserts both halves: as A the filter must not match B's row, and
// as A the same filter must still match A's own rows, so the fix cannot be
// "relation filters never match".
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestRelationFilterIsolation ./tests/relation-filter-isolation/
package relationfilterisolation_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// RelationFilterSuite embeds tests.Base for ctx, config, the Zitadel
// connection and fresh-tenant cleanup.
type RelationFilterSuite struct {
	tests.Base
}

// TestRelationFilterIsolation is the runner.
func TestRelationFilterIsolation(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(RelationFilterSuite))
}
