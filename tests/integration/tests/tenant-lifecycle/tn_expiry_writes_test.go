//go:build integration

package tenants

import (
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TnExpiryWritesSuite pins what the two expiry writers store, as opposed
// to what the sweep does with it (TnExpirySuite). Both mutations are
// system-caller only and both write expires_at on the tenant row, so the
// tenants query is the read side for every assertion here. Each test
// registers its own tenant: the expiry decides when the sweep disables a
// tenant, so a test that moves it must never touch a shared one.
type TnExpiryWritesSuite struct {
	tests.Base
}

// futureExpiry returns a timestamp far enough out that the expiry sweep
// can never fire on it during the run, truncated to whole seconds so a
// round-trip through Postgres compares equal.
func futureExpiry(hours int) time.Time {
	return time.Now().UTC().Add(time.Duration(hours) * time.Hour).Truncate(time.Second)
}

// TestRestoreTenantOverridesThePriorExpiry proves restoreTenant's
// expiresAt replaces the expiry the tenant carried, rather than being
// ignored when one is already set. This is what closes the re-disable
// window: a tenant disabled by the sweep still holds a past expires_at,
// so a restore that left it in place would make the tenant sweep-eligible
// again the moment deleted_at clears, and setTenantExpiry cannot run
// first, because the tenant is still soft-deleted.
func (s *TnExpiryWritesSuite) TestRestoreTenantOverridesThePriorExpiry() {
	r := s.Require()

	initial := futureExpiry(24)
	rt, err := gateway.RegisterTenantWithExpiry(s.Ctx, s.Cfg, fixtures.NewTenant(), &initial)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	stored, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.NotNil(stored, "registerTenant must persist the expiry it was given")
	r.True(stored.Equal(initial), "stored expiry %v must be the registered one %v", stored, initial)

	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID), "disable the tenant so the restore has something to undo")

	// The restore must undo a disable that actually landed: restoring
	// while DisableTenantWorkflow is still in flight would leave the org
	// deactivated afterwards, and the ACTIVE assertion below would pass on
	// an org the disable had not reached yet.
	_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "INACTIVE", 30*time.Second)
	r.NoError(err, "the disable must have reached Zitadel before the restore")

	override := futureExpiry(72)
	r.NoError(gateway.RestoreTenantWithExpiry(s.Ctx, s.Cfg, rt.ID, &override))

	after, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err, "a restored tenant must be visible again")
	r.NotNil(after, "the restore must leave an expiry behind")
	r.True(after.Equal(override), "restoreTenant must overwrite the prior expiry: got %v, want %v", after, override)
	r.False(after.Equal(initial), "the pre-disable expiry must not survive the restore")

	// Second vantage point: the restore is only real once its event has
	// reactivated the org, otherwise the row says active while every token
	// in the tenant is still refused.
	_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "ACTIVE", 30*time.Second)
	r.NoError(err, "the restore event must reactivate the tenant's Zitadel org")
}

// TestRestoreTenantWithoutExpiryKeepsTheStoredOne is the counterpart: a
// nil expiresAt means "leave it alone", not "clear it". Clearing it would
// silently promote every sweep-disabled tenant to permanent on restore.
func (s *TnExpiryWritesSuite) TestRestoreTenantWithoutExpiryKeepsTheStoredOne() {
	r := s.Require()

	initial := futureExpiry(24)
	rt, err := gateway.RegisterTenantWithExpiry(s.Ctx, s.Cfg, fixtures.NewTenant(), &initial)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID))
	_, err = zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, rt.IdpOrgRef, "INACTIVE", 30*time.Second)
	r.NoError(err, "the disable must have reached Zitadel before the restore")
	r.NoError(gateway.RestoreTenant(s.Ctx, s.Cfg, rt.ID))

	after, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.NotNil(after, "a restore without an expiry must not clear the stored one")
	r.True(after.Equal(initial), "stored expiry %v must be unchanged (%v)", after, initial)
}

// TestSetTenantExpiryOverwritesClearsAndIsIdempotent walks the three
// writes the mutation has to get right on one tenant: replacing an expiry
// with a different one (not merging, not refusing), clearing it with an
// explicit null, and clearing an expiry that is already absent, the
// re-run an operator script performs whenever it is unsure of the state.
func (s *TnExpiryWritesSuite) TestSetTenantExpiryOverwritesClearsAndIsIdempotent() {
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	initial, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.Nil(initial, "a tenant registered without an expiry must start with none")

	first := futureExpiry(24)
	r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, &first))
	stored, err := tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.NotNil(stored)
	r.True(stored.Equal(first), "stored expiry %v must be the one just set (%v)", stored, first)

	second := futureExpiry(96)
	r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, &second))
	stored, err = tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.NotNil(stored)
	r.True(stored.Equal(second), "a second setTenantExpiry must replace the first: got %v, want %v", stored, second)

	r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, nil), "an explicit null must be accepted as a clear")
	stored, err = tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.Nil(stored, "a cleared expiry must read as absent, not as the previous value")

	r.NoError(gateway.SetTenantExpiry(s.Ctx, s.Cfg, rt.ID, nil), "clearing an already-absent expiry must stay a success")
	stored, err = tests.TenantExpiresAt(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.Nil(stored, "the idempotent clear must leave the tenant without an expiry")

	// The tenant must still be usable after the clear: a permanent tenant
	// is the normal state, not an edge case the sweep may disable.
	status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err)
	r.Equal("done", status, "clearing an expiry must not change the tenant's registration status")
}
