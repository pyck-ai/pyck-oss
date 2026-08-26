//go:build integration

package orgverdictcache

import (
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// orgInactiveTimeout covers Zitadel applying DeactivateOrganization to
	// the ListOrganizations projection — the same read the org-active
	// validator consults, so reaching INACTIVE here is the barrier that
	// makes the stale-positive probes meaningful.
	orgInactiveTimeout = 30 * time.Second
	// rejectionTimeout bounds the revocation phase: deleteTenant's
	// synchronous soft delete emits the tenant CRUD event, NATS propagation
	// evicts the token + verdict caches, and the next introspection runs
	// the validator against the deactivated org. Sub-second on a healthy
	// stack; the budget absorbs outbox drain + JetStream lag and, worst
	// case, one full verdict TTL (1m default) if the eviction event is lost.
	rejectionTimeout = 90 * time.Second
)

// VerdictCacheSuite drives one tenant through warm → out-of-band org
// deactivation → pyck-side disable, asserting the auth path's caching and
// revocation behaviour at each step.
type VerdictCacheSuite struct {
	tests.Base

	// tenant is kept on the suite so TearDownSuite can clean up when the
	// lifecycle test bails out mid-way. The final phase disables it through
	// pyck and nils this out; teardown then has nothing to do.
	tenant *gateway.RegisteredTenant
}

func (s *VerdictCacheSuite) TearDownSuite() {
	if s.tenant != nil {
		// Best effort: reactivate the org first so the disable workflow's
		// DeactivateZitadelOrgActivity finds it in the state it expects.
		_ = zitadelclient.ActivateOrg(s.Ctx, s.ZConn, s.tenant.IdpOrgRef)
		_ = gateway.DeleteTenant(s.Ctx, s.Cfg, s.tenant.ID)
	}
	s.Base.TearDownSuite()
}

func (s *VerdictCacheSuite) TestVerdictCacheLifecycle() {
	var pat string

	if !s.Run("provision tenant and PAT", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		r.NoError(err, "register tenant")
		r.NotEmpty(rt.IdpOrgRef)
		s.tenant = rt

		// The gate roles matter: the inventory probe below crosses a gated
		// subgraph, and the gate is orthogonal to the writer ladder.
		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt,
			tests.RolesWithServiceGates("writer"))
		r.NoError(err, "provision machine user + PAT")
		pat = p.PAT
		s.T().Logf("PAT ready (tenantID=%s idpOrgRef=%s)", rt.ID, rt.IdpOrgRef)
	}) {
		return
	}

	if !s.Run("warm both validator paths", func() {
		r := s.Require()
		present, err := tests.TenantInList(s.Ctx, s.Cfg, pat, s.tenant.ID)
		r.NoError(err, "management probe")
		r.True(present, "tenant should be visible to its own PAT")
		r.NoError(s.probeInventory(pat), "inventory probe")
	}) {
		return
	}

	if !s.Run("deactivate org out of band", func() {
		r := s.Require()
		r.NoError(zitadelclient.DeactivateOrg(s.Ctx, s.ZConn, s.tenant.IdpOrgRef))
		took, err := zitadelclient.WaitForOrgState(s.Ctx, s.ZConn, s.tenant.IdpOrgRef, "INACTIVE", orgInactiveTimeout)
		r.NoError(err)
		s.T().Logf("org INACTIVE in Zitadel projection after %s", took.Round(time.Millisecond))
	}) {
		return
	}

	if !s.Run("stale positive verdict still accepted", func() {
		r := s.Require()
		// The org is INACTIVE in the very projection the validator reads,
		// and no pyck mutation ran, so no eviction event exists. A build
		// without the verdict cache runs the validator on this request and
		// 401s; acceptance here can only come from the cached verdict.
		_, err := tests.TenantInList(s.Ctx, s.Cfg, pat, s.tenant.ID)
		r.NoError(err, "management must still accept the token while the verdict is cached")
		r.NoError(s.probeInventory(pat), "inventory must still accept the token while the verdict is cached")

		// Guard the assertion against the tenant-reconcile sweeper: it
		// treats "DB row live, org INACTIVE" as drift and restores the org
		// (~5s interval locally). If it won the race the probes prove
		// nothing — fail loudly rather than pass on a restored org.
		state, err := zitadelclient.OrgState(s.Ctx, s.ZConn, s.tenant.IdpOrgRef)
		r.NoError(err)
		r.Equal("INACTIVE", state,
			"org was restored before the probes ran (tenant-reconcile won the race); rerun the suite")
	}) {
		return
	}

	s.Run("pyck-side disable revokes the token", func() {
		r := s.Require()
		// Disable through pyck: the mutation soft-deletes the row and the
		// tenant CRUD event fans out to every service's revocation
		// subscriber, evicting the cached token + verdict. The next
		// introspection runs the validator against the INACTIVE org and
		// rejects. Also proves negative verdicts are not served from cache.
		r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, s.tenant.ID))

		took, err := tests.WaitUntilTokenRejected(s.Ctx, s.Cfg, pat, s.tenant.ID, rejectionTimeout)
		r.NoError(err, "management must reject the token after tenant disable")
		s.T().Logf("management rejected after %s", took.Round(time.Millisecond))

		took, err = s.waitInventoryRejected(pat, rejectionTimeout)
		r.NoError(err, "inventory must reject the token after tenant disable")
		s.T().Logf("inventory rejected after %s", took.Round(time.Millisecond))

		s.tenant = nil // disabled through pyck; nothing left to tear down
	})
}
