//go:build integration

// Package disabledtenantaccess verifies the liveness axis of the tenant
// middleware end to end: a disabled tenant is unreachable for EVERY caller,
// including users homed in another tenant who hold a role grant into it,
// and becomes reachable again on restore.
//
// The suite provisions tenants A and B and one user homed in A with a grant
// into B, then walks the access matrix from two vantage points — management
// (in-process tenant-table lookup) and inventory (lookup via the gateway's
// `disabledTenants` query):
//
//	while B is disabled
//	  X-Pyck-Tenant-Id: B          → 403 (explicit, fail-closed)
//	  X-Pyck-Tenant-Id: A,B        → 403 (any explicit disabled tenant)
//	  X-Pyck-Tenant-Id: all,B      → 403 (explicit part wins)
//	  X-Pyck-Tenant-Id: all        → 200, B's rows filtered out
//	  no header (defaults to all)  → 200, B's rows filtered out
//	  X-Pyck-Tenant-Id: A          → 200 (unaffected)
//	  system token, header B       → 200 (bypass; how restore reaches B)
//	after restoreTenant
//	  X-Pyck-Tenant-Id: B          → 200, and B is back in the 'all' set
//	after a second deleteTenant
//	  X-Pyck-Tenant-Id: B          → 403 again
//
// Every denial and the re-admission must land inside half the verdict TTL
// (PYCK_LIVENESS_CACHE_TTL): that proves they were driven by the
// NATS disable/restore evictions, not by a cached verdict merely expiring.
// The second disable proves eviction also hits a verdict that was re-cached
// after the restore.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/disabled-tenant-access/...
package disabledtenantaccess_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestDisabledTenantAccess is the runner; testify runs the suite's Test*
// methods in alphabetical order.
func TestDisabledTenantAccess(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DisabledTenantAccessSuite))
}
