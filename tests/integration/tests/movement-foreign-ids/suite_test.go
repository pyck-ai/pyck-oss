//go:build integration

// Package movementforeignids_test checks that an inventory movement never
// stores an id of another tenant.
//
// The tenant privacy filter scopes the movement row itself, but the ids a
// movement references were stored without an ownership check: the item of an
// item movement (the Postgres create path runs inventory.create_item_movement_proc,
// which checked FROM and TO but never the item), the item of a collection
// position, and the target of a repository movement whose parent is static.
// Tenant A could therefore write movements, and after execution stock and
// transaction rows, pointing at tenant B's item, or re-parent its repository
// under B's.
//
// Each test asserts three things: the call is refused, the same call with A's
// own ids still succeeds, and B's rows are unchanged.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestMovementForeignIDs ./tests/movement-foreign-ids/
package movementforeignids_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// MovementForeignIDSuite embeds tests.Base for ctx, config, the Zitadel
// connection and fresh-tenant cleanup.
type MovementForeignIDSuite struct {
	tests.Base
}

// TestMovementForeignIDs is the runner.
func TestMovementForeignIDs(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(MovementForeignIDSuite))
}
