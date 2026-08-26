//go:build integration

// Package tenantisolation exercises cross-tenant isolation on write paths that
// Ent's tenant privacy interceptor cannot cover on its own — specifically edge
// / M2M attaches, where attaching another tenant's row IDs bypasses that row's
// privacy policy and must be guarded explicitly in the resolver.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/tenant-isolation/...
package tenantisolation_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TenantIsolationSuite embeds tests.Base for the shared ctx/config/Zitadel
// connection and fresh-tenant cleanup.
type TenantIsolationSuite struct {
	tests.Base
}

// TestTenantIsolation is the runner for the cross-tenant write-path suite.
func TestTenantIsolation(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(TenantIsolationSuite))
}
