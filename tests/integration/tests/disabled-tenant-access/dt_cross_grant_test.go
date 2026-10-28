//go:build integration

package disabledtenantaccess_test

import (
	"time"

	"github.com/google/uuid"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// evictionPollBudget is the poll budget after a lifecycle mutation for the
	// verdict to flip in every service. On a healthy stack the disable /
	// restore event reaches each replica within milliseconds; the budget
	// absorbs outbox drain + JetStream lag. It is deliberately longer than
	// the asserted evictionBound (TTL/2, see helpers) so a broken eviction
	// path fails with a latency in the message ("flipped after 61s") rather
	// than a bare timeout.
	evictionPollBudget = 90 * time.Second
	// tokenReadyTimeout covers Zitadel projecting the PAT + both grants.
	tokenReadyTimeout = 30 * time.Second
	// minLivenessTTL is the smallest configured verdict TTL under which
	// evictionBound (TTL/2) can still separate "evicted" from "expired"
	// against ordinary NATS/outbox lag; below it the suite skips.
	minLivenessTTL = 10 * time.Second
)

// DisabledTenantAccessSuite drives one cross-tenant user (home org A, role
// grant into B) through B's disable, restore and second disable, asserting
// the acting-tenant access matrix at each step from the management and
// inventory vantage points.
type DisabledTenantAccessSuite struct {
	tests.Base
}

func (s *DisabledTenantAccessSuite) TestCrossTenantGrantIntoDisabledTenant() {
	if ttl := s.Cfg.LivenessCacheTTL; ttl < minLivenessTTL {
		s.T().Skipf("PYCK_LIVENESS_CACHE_TTL=%s is too short for TTL/2 to distinguish eviction from expiry (need >= %s)", ttl, minLivenessTTL)
	}

	var (
		tenantA, tenantB *gateway.RegisteredTenant
		pat              string
		repoA, repoB     string
	)
	// Probes bound to "header B, expect B's row" on both vantage points —
	// the request shape whose verdict flips with B's lifecycle.
	probeMgmtB := func() (bool, error) { return s.probeManagement(pat, tenantB.ID, tenantB.ID) }
	probeInvB := func() (bool, error) { return s.probeInventory(pat, tenantB.ID, repoB) }

	if !s.Run("provision A, B and a user homed in A with a grant into B", func() {
		r := s.Require()
		var err error

		tenantA, err = gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		r.NoError(err, "register tenant A")
		s.DeferTenantCleanup(tenantA.ID)
		tenantB, err = gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		r.NoError(err, "register tenant B")
		s.DeferTenantCleanup(tenantB.ID)

		// Both grants are created BEFORE the PAT is first used: the first
		// request caches the introspected role set for the PAT cache TTL,
		// and a grant added afterwards would stay invisible for that long.
		// (tests.ProvisionUserInTenant grants one org and warms the PAT, so
		// it cannot be used for a cross-tenant member.)
		roles := tests.RolesWithServiceGates("writer")
		uid, err := zitadelclient.EnsureMachineUser(s.Ctx, s.ZConn, tenantA.IdpOrgRef, fixtures.NewMachineUser())
		r.NoError(err, "ensure machine user in A")
		r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, tenantA.IdpOrgRef, s.Cfg.ZitadelProjectID, uid, roles), "grant in A")
		r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, tenantB.IdpOrgRef, s.Cfg.ZitadelProjectID, uid, roles), "grant into B")
		_, pat, err = zitadelclient.AddPAT(s.Ctx, s.ZConn, uid)
		r.NoError(err, "add PAT")

		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantA.ID, tokenReadyTimeout)
		r.NoError(err, "PAT must see its home tenant A")
		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantB.ID, tokenReadyTimeout)
		r.NoError(err, "PAT must see the granted tenant B")
		s.T().Logf("PAT ready (A=%s B=%s)", tenantA.ID, tenantB.ID)
	}) {
		return
	}

	if !s.Run("baseline: header B reaches B on management and inventory", func() {
		r := s.Require()
		repoA = s.createRepository(pat, tenantA.ID)
		repoB = s.createRepository(pat, tenantB.ID)

		visible, err := probeMgmtB()
		r.NoError(err, "management with header B")
		r.True(visible, "tenant B must be listed under header B")

		visible, err = probeInvB()
		r.NoError(err, "inventory with header B")
		r.True(visible, "B's repository must be listed under header B")

		visible, err = s.probeInventory(pat, "", repoB)
		r.NoError(err, "inventory with no header (all)")
		r.True(visible, "B's repository must be listed under 'all' while B is active")
	}) {
		return
	}

	if !s.Run("disable B", func() {
		s.Require().NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, tenantB.ID))
	}) {
		return
	}

	if !s.Run("explicit header B is denied on management and inventory", func() {
		s.waitDeniedWithinBound(probeMgmtB, probeInvB)
	}) {
		return
	}

	if !s.Run("explicit list A,B and mixed all,B are denied (explicit part wins)", func() {
		r := s.Require()
		for _, header := range []string{tenantA.ID + "," + tenantB.ID, "all," + tenantB.ID} {
			_, err := s.probeManagement(pat, header, tenantB.ID)
			r.Error(err, "management must deny header %q", header)
			r.True(tests.IsAuthDenial(err.Error()), "management header %q must be an auth denial, got: %v", header, err)
			_, err = s.probeInventory(pat, header, repoB)
			r.Error(err, "inventory must deny header %q", header)
			r.True(tests.IsAuthDenial(err.Error()), "inventory header %q must be an auth denial, got: %v", header, err)
		}
	}) {
		return
	}

	if !s.Run("'all' and no header still work but hide B; header A unaffected", func() {
		r := s.Require()
		for _, header := range []string{"", "all"} {
			visible, err := s.probeInventory(pat, header, repoB)
			r.NoError(err, "inventory header=%q must still be accepted", header)
			r.False(visible, "B's repository must be filtered out under header=%q", header)

			visible, err = s.probeInventory(pat, header, repoA)
			r.NoError(err, "inventory header=%q must still be accepted", header)
			r.True(visible, "A's repository must stay visible under header=%q", header)

			visible, err = s.probeManagement(pat, header, tenantA.ID)
			r.NoError(err, "management header=%q must still be accepted", header)
			r.True(visible, "tenant A must stay visible under header=%q", header)
		}

		visible, err := s.probeInventory(pat, tenantA.ID, repoA)
		r.NoError(err, "inventory header=A must be unaffected")
		r.True(visible, "A's repository must be visible under header A")
	}) {
		return
	}

	if !s.Run("system token still reaches the disabled tenant by explicit header", func() {
		// The bypass is what lets restoreTenant / deleteTenant name a
		// disabled tenant; it applies on every service, not only management.
		r := s.Require()
		visible, err := s.probeManagement(s.Cfg.ServiceToken, tenantB.ID, tenantB.ID)
		r.NoError(err, "management must accept the system token with header B")
		r.False(visible, "the soft-deleted row is hidden from a plain tenants query even for system")
		_, err = s.probeInventory(s.Cfg.ServiceToken, tenantB.ID, repoB)
		r.NoError(err, "inventory must accept the system token with header B")
	}) {
		return
	}

	if !s.Run("restore B re-admits header B and 'all' shows B again", func() {
		r := s.Require()
		r.NoError(gateway.RestoreTenant(s.Ctx, s.Cfg, tenantB.ID))

		// The denial probes above cached a "disabled" verdict for B in every
		// service; re-admission must come from the restore-event eviction.
		s.waitAcceptedWithinBound(probeMgmtB, probeInvB)

		visible, err := s.probeInventory(pat, "", repoB)
		r.NoError(err, "inventory with no header (all) after restore")
		r.True(visible, "B's repository must be back in the 'all' expansion after restore")
	}) {
		return
	}

	// The re-admit probes above re-populated the "enabled" verdict for B in
	// every service's cache. A second disable must evict that fresh entry
	// just like the first one — proving eviction works on a re-cached
	// verdict, not only on the initial one, and that the disable → restore
	// → disable cycle never leaves a stale verdict of either sign behind.
	s.Run("second disable evicts the re-cached verdict", func() {
		s.Require().NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, tenantB.ID))
		s.waitDeniedWithinBound(probeMgmtB, probeInvB)
	})
}

// createRepository creates one inventory repository as the given tenant and
// returns its ID — the tenant-owned row the inventory probes look for.
func (s *DisabledTenantAccessSuite) createRepository(token, tenantID string) string {
	c := gateway.NewInventoryClientForTenant(s.Cfg, token, tenantID)
	out, err := c.CreateInventoryRepository(s.Ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{
			Name: "dt-repo-" + uuid.NewString(),
			Type: entrepository.TypeStatic,
		},
	})
	s.Require().NoError(err, "create repository in tenant %s", tenantID)
	return out.CreateInventoryRepository.InventoryRepository.ID
}
