//go:build integration

// Package authroles exercises the per-service role gate end-to-end across every
// gated service (inventory, picking, receiving, file, main-data; management and
// workflow are intentionally ungated), plus the management assignRoles /
// removeRoles / serviceRoles / userServiceRoles endpoints.
//
// Coverage is organised as:
//   - ar_gate_test.go: the gate's allow/deny matrix over all gated services —
//     missing role, present role, wrong service's role, system bypass,
//     unauthenticated fall-through, and the ungated management control.
//   - ar_gate_loopholes_test.go: ways a user might try to slip past — no roles
//     at all, a service role with no ladder role, and cross-tenant /
//     "all"-expansion scoping.
//   - ar_assign_test.go, ar_remove_test.go, ar_catalog_test.go: the management
//     role API, including additive/idempotent semantics, surgical removal,
//     validation (ladder/unknown/empty keys, unknown tenant/user) and
//     authentication + admin-only authz.
//
// The gate cases call a gated service DIRECTLY (config.<Service>URL) rather than
// via the gateway, because the gate denies with an HTTP 403 and the gateway
// (Apollo Router) would fold a subgraph 403 into a GraphQL error, hiding the
// status. The endpoint cases go through the gateway like every other management
// call.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/auth-roles/...
package authroles

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestAuthRoles is the runner for the per-service role suite.
func TestAuthRoles(t *testing.T) {
	suite.Run(t, new(AuthRolesSuite))
}
