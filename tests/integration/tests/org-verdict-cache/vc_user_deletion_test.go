//go:build integration

package orgverdictcache //nolint:testpackage // in-package test required: the case is a method on VerdictCacheSuite.

import (
	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// The liveness verdict is one per tenant, shared by every user homed in
// it, so the source must answer from the tenant row alone. This is that
// property in its production shape: one Zitadel user of a healthy tenant is
// deleted out of band, and the tenant, plus every other user of it, must
// be unaffected.
//
// Two assertions, and they fail for different reasons if the validator ever
// consults the calling user again:
//
//   - the surviving co-tenant keeps its access. This is the safety property;
//     a per-user validator denies it whenever its request shares the deleted
//     user's collapsed lookup or its cached verdict.
//   - the deleted user keeps its access too, up to PYCK_ZITADEL_PAT_CACHE_TTL
//     (default 1h, so for the whole run): this axis does not answer user
//     liveness — see docs/tenant-access-control.md and
//     https://github.com/pyck-ai/pyck/issues/1433, which adds the axis that
//     does.
//
// The second assertion is therefore a consequence of the missing user axis,
// not a property worth keeping: once #1433 ships it must be inverted to "the
// deleted user is refused", while the first assertion stays as it is.
//
// The co-tenant half can no longer fail by construction (the liveness source
// is handed tenant IDs and never sees the caller, so a verdict cannot be
// per-user) but it is kept as the live guard on that, and as the fixture
// #1433 inverts.
func (s *VerdictCacheSuite) TestDeletedUserDoesNotRevokeCoTenant() {
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant")
	s.DeferTenantCleanup(rt.ID)

	// Two machine users in the same Zitadel org, hence one shared verdict.
	// Gate roles because the inventory probe crosses a gated subgraph.
	roles := tests.RolesWithServiceGates("writer")
	deleted, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, roles)
	r.NoError(err, "provision the user that gets deleted")
	survivor, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, roles)
	r.NoError(err, "provision the co-tenant")

	// Warm both auth paths for both users: management runs the validator
	// in-process, every other service round-trips through the gateway.
	for name, pat := range map[string]string{"deleted-user": deleted.PAT, "survivor": survivor.PAT} {
		present, err := tests.TenantInList(s.Ctx, s.Cfg, pat, rt.ID)
		r.NoError(err, "management probe (%s)", name)
		r.True(present, "%s should see its own tenant", name)
		r.NoError(s.probeInventory(pat), "inventory probe (%s)", name)
	}

	r.NoError(zitadelclient.DeleteMachineUser(s.Ctx, s.ZConn, deleted.UserID),
		"delete one user out of band")

	// Nothing about the organization changed, so nothing about the verdict
	// may change either. Asserted rather than assumed: an INACTIVE org would
	// make both probes below meaningless.
	state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, rt.IdpOrgRef)
	r.NoError(err)
	r.Equal("ACTIVE", state, "deleting a user must not affect the organization")

	present, err := tests.TenantInList(s.Ctx, s.Cfg, survivor.PAT, rt.ID)
	r.NoError(err, "management must still accept the co-tenant of a deleted user")
	r.True(present, "the co-tenant must still see its tenant")
	r.NoError(s.probeInventory(survivor.PAT),
		"inventory must still accept the co-tenant of a deleted user")

	present, err = tests.TenantInList(s.Ctx, s.Cfg, deleted.PAT, rt.ID)
	r.NoError(err, "the tenant liveness axis must not revoke on individual user liveness")
	r.True(present, "this axis does not answer user liveness; see #1433")
}
