//go:build integration

// Package dataindex verifies the typed-slot data index across services.
//
// The feature's correctness is not contained in one service: management owns
// the x-indices block and the frozen-binding contract, picking learns about it
// through a NATS cache invalidation, its projection hook fills the slot inside
// the write transaction, and only then can a dataIndex query find the row. Each
// half passes its own unit tests while the seam between them is broken, which
// is what this suite covers.
//
// The scenario deliberately writes the order BEFORE the binding exists: that is
// the shape of the real rollout (x-indices applied through the API against a
// live stack) and the only way to exercise the backfill that repairs rows
// predating a binding. A query that finds the row afterwards proves the whole
// chain, without a restart.
//
// One case is deliberately left to the Bruno fixture and the resolver unit
// test: an explicitly empty candidate list. The generated Go client tags the
// field omitempty, so it drops [] from the request and the server cannot tell
// it from an omitted operator.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/data-index/...
package dataindex

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestDataIndex is the runner; testify runs the suite's Test* methods in
// alphabetical order.
func TestDataIndex(t *testing.T) {
	suite.Run(t, new(DataIndexSuite))
}
