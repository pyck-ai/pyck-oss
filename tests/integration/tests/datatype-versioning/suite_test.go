//go:build integration

// Package datatypeversioning exercises the append-only, version-pinned
// DataType model end-to-end through the federated gateway: version
// assignment and immutability, slug resolution, soft-delete promotion,
// entity pinning by dataTypeID (including every mismatch and validation
// failure mode), tenant isolation, and fuzzed data payloads against both
// permissive and strictly-typed schemas.
//
// Every test provisions its own fresh tenant, so the suite is isolated and
// repeatable; nothing touches a shared or default tenant.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/datatype-versioning/...
package datatypeversioning

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestDataTypeVersioning is the runner for the DataType versioning suite.
func TestDataTypeVersioning(t *testing.T) {
	suite.Run(t, new(VersioningSuite))
}
