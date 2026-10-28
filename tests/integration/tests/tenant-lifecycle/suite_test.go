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
// expiresAt → tenant-expiry-check sweep →
// outbox/NATS → DisableTenantWorkflow + revocation cache propagation).
func TestExpiry(t *testing.T) {
	suite.Run(t, new(TnExpirySuite))
}

// TestExpiryWrites drives the TnExpiryWritesSuite, which pins what
// setTenantExpiry and restoreTenant store on the tenant row (overwrite,
// clear, idempotent clear, restore-time override), the write side of the
// expiry the TestExpiry sweep acts on.
func TestExpiryWrites(t *testing.T) {
	suite.Run(t, new(TnExpiryWritesSuite))
}

// TestRegisterFlow drives the TnRegisterSuite, which proves the
// event-driven registration wiring (pending row → outbox → JetStream
// consumer → workflow start → row load → org creation → status done) from
// the registration run's own history, and that the admin imported with a
// hashed password can authenticate.
func TestRegisterFlow(t *testing.T) {
	suite.Run(t, new(TnRegisterSuite))
}

// TestLifecycleRoles drives the TnLifecycleRolesSuite, which pins who may
// disable, restore and expire a tenant (writer, own admin, cross-tenant
// admin and the system principal) against an active tenant, one the
// admin disabled and one the expiry sweep disabled.
func TestLifecycleRoles(t *testing.T) {
	suite.Run(t, new(TnLifecycleRolesSuite))
}

// TestNameReservation drives the TnNameReservationSuite: tenant names are
// unique by their Zitadel-style case fold (name_key) across every row,
// retired ones included.
func TestNameReservation(t *testing.T) {
	suite.Run(t, new(TnNameReservationSuite))
}

// TestDeletePending drives the TnDeletePendingSuite: deleteTenant retires a
// pending row whether or not a registration run is still open, and leaves
// an open run alone.
func TestDeletePending(t *testing.T) {
	suite.Run(t, new(TnDeletePendingSuite))
}
