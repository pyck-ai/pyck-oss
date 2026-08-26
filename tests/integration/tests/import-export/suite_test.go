//go:build integration

// Package importexport exercises the generic import/export mechanism
// (backend/common/importexport) end-to-end against a live, federated pyck
// stack: it registers every service's entities on one registry and drives a
// full round-trip — single-pass import of all entity types, re-import
// idempotency, export, and create-only skip/duplicate semantics.
//
// Ported from the former tests/cli module. Unlike that version (which used
// the static bootstrap api-user token against the default tenant), this
// suite provisions a fresh tenant + writer PAT per run, so the round-trip is
// isolated and repeatable — matching the other integration suites.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/import-export/...
package importexport

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestImportExport is the runner for the import/export round-trip suite.
func TestImportExport(t *testing.T) {
	suite.Run(t, new(ImportExportSuite))
}
