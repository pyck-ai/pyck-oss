//go:build integration

// Package tenants exercises the tenant lifecycle (register, disable,
// restore, delete) end-to-end against a live pyck stack — including
// cross-service revocation cache propagation and the Zitadel-side
// effects of the disable/restore Temporal workflows.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/tenant-lifecycle/...
package tenants

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestProvisioning is the test runner entrypoint; testify discovers
// Test* methods on the suite and runs them in alphabetical order.
func TestProvisioning(t *testing.T) {
	suite.Run(t, new(ProvisioningSuite))
}

// TestReconcile drives the TnReconcileSuite, which exercises the
// periodic tenant-reconcile sweeper that heals out-of-band drift
// between the management DB and Zitadel org state.
func TestReconcile(t *testing.T) {
	suite.Run(t, new(TnReconcileSuite))
}

// TestExpiry drives the TnExpirySuite, which exercises the
// expiry-driven soft-delete pipeline end-to-end (registerTenant with
// expiresAt → zitadel-sync projection → tenant-expiry-check sweep →
// outbox/NATS → DisableTenantWorkflow + revocation cache propagation).
func TestExpiry(t *testing.T) {
	suite.Run(t, new(TnExpirySuite))
}
