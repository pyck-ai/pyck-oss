//go:build integration

// Package temporalisolation proves the two halves of pyck's Temporal
// multi-tenancy story hold at the same time: connections are SHARED,
// namespaces are NOT.
//
// Connection sharing (backend/common/workflow's DefaultClientFactory): the
// workflow service holds a single root gRPC connection to the internal
// frontend (:7236) and derives one namespace-scoped client per tenant from
// it, memoized in the per-namespace cache. The suite drives the service
// across two tenant namespaces through the gateway and asserts the service
// process still holds exactly one established connection to Temporal.
//
// Namespace isolation, on both frontends:
//
//   - Through the service (internal frontend): the per-namespace cache must
//     bind each tenant's derived client to that tenant's namespace — tenant
//     A's workflowExecutions query returns A's executions and can never
//     surface B's, even though both ride the same TCP connection.
//   - On the authorizing (public) frontend (:7233), where external SDK
//     clients connect: the pyck claim mapper turns the presented bearer
//     token into per-namespace roles, so a tenant PAT driven at another
//     tenant's namespace is denied across the read, list, and write
//     surfaces — while the same PAT succeeds against its own namespace, so
//     a denial reflects authorization, not a broken token or a missing
//     namespace.
//
// Together these guard the invariant behind the shared-connection factory:
// collapsing per-tenant connections into one must not widen any tenant's
// reach, because namespace scoping is enforced per client (internal
// frontend) and per request (public frontend), never per connection.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/temporal-isolation/...
package temporalisolation

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestTemporalIsolation is the runner; testify runs the suite's Test*
// methods in alphabetical order.
func TestTemporalIsolation(t *testing.T) {
	suite.Run(t, new(IsolationSuite))
}
