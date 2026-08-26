//go:build integration

// Package orgverdictcache verifies the org-active verdict cache on the
// service auth path (backend/common/authn: checkOrgActive + OrgValidator).
//
// The cache is invisible to a functional request — its observable contract
// is a bounded stale-positive window: after a tenant's Zitadel org is
// deactivated OUT OF BAND (no pyck mutation, so no NATS eviction event),
// tokens of that tenant keep being accepted for up to the verdict TTL.
// Without the cache the validator runs on every request against the same
// Zitadel projection and rejects immediately, so "org provably INACTIVE in
// the projection, yet the request is accepted" can only be produced by a
// cached verdict. The complementary property — pyck-side disable revokes
// promptly via NATS eviction + the validator — is asserted afterwards.
//
// Both properties are checked from two vantage points, because the two
// validator implementations differ: management runs an in-process closure
// against its Zitadel gRPC connection, while every other service (probed
// here via inventory) round-trips through the gateway to management's
// `organization` query.
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
