//go:build integration

// Package constrainterrordisclosure_test checks that a database constraint
// violation reaches a client through the gateway as a generic message, never
// as the raw Postgres error that names the table and the constraint.
//
// The caller is an ordinary writer of a fresh tenant. Each case triggers one
// violation class the API can hit: a unique violation (the same SKU twice) and
// a foreign-key violation (an item id that does not exist) on both item
// movement paths. The unique message must keep "23505" and "duplicate key":
// worker SDKs retry a lost registration race by matching them, and workers
// treat "duplicate key" on a create as "it already exists".
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestConstraintErrorDisclosure ./tests/constraint-error-disclosure/
package constrainterrordisclosure_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// ConstraintErrorSuite embeds tests.Base for ctx, config, the Zitadel
// connection and fresh-tenant cleanup.
type ConstraintErrorSuite struct {
	tests.Base
}

// TestConstraintErrorDisclosure is the runner.
func TestConstraintErrorDisclosure(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(ConstraintErrorSuite))
}
