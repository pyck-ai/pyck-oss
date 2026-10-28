//go:build integration

// Package orgverdictcache verifies the tenant liveness verdict on the
// service auth path (backend/common/authn: checkTenantLive, cached in
// backend/common/liveness).
//
// Two properties, in one run. First, that the verdict answers from the
// tenant row and from nothing else: a tenant's Zitadel organization is
// deactivated OUT OF BAND and its tokens keep working, because pyck is the
// only writer of tenant state and no row changed. Second, that a pyck-side
// disable revokes promptly: the mutation soft-deletes the row, the CRUD
// event evicts the cached token and verdict on every service, and the next
// introspection reads the new verdict off the row.
//
// Both are checked from two vantage points, because the two source
// implementations differ: management queries its own tenant table in
// process, while every other service (probed here via inventory)
// round-trips through the gateway to management's `disabledTenants` query.
//
// Timing caveat: the tenant-reconcile sweeper treats "DB row live, org
// INACTIVE" as drift and restores the org (locally every ~5s). The
// stale-positive phase therefore re-reads the org state AFTER the probes
// and only counts acceptances observed while the org was still INACTIVE.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/org-verdict-cache/...
package orgverdictcache

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestOrgVerdictCache is the runner; testify runs the suite's Test*
// methods in alphabetical order.
func TestOrgVerdictCache(t *testing.T) {
	suite.Run(t, new(VerdictCacheSuite))
}
