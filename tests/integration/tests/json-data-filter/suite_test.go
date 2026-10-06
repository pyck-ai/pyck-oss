//go:build integration

// Package jsondatafilter_test checks, against a live stack and for every
// service with a JSON data column, that a data filter the resolver cannot
// apply is refused. The *WhereInput data filters are positional lists
// (Data: [path, value], DataIn: [path, value…], DataContains: [path, value])
// plus DataHasKey: path; a list of the wrong length, or an empty key, used to
// add no predicate at all, so the query returned the tenant's whole table.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/json-data-filter/...
package jsondatafilter_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestJSONDataFilter runs the suite.
func TestJSONDataFilter(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(DataFilterSuite))
}
