//go:build integration

package disabledtenantaccess_test

import (
	"fmt"
	"time"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// probeManagement lists management.tenants with the given bearer token and
// X-Pyck-Tenant-Id header value (empty = no header) filtered to wantTenant.
// Returns (visible, nil) when the request was accepted, or the request error.
func (s *DisabledTenantAccessSuite) probeManagement(token, header, wantTenant string) (bool, error) {
	var c managementapi.Client
	if header == "" {
		c = gateway.NewClient(s.Cfg, token)
	} else {
		c = gateway.NewClientForTenant(s.Cfg, token, header)
	}
	first := 1
	resp, err := c.GetTenants(s.Ctx, managementapi.GetTenantsArgs{
		First: &first,
		Where: &managementapi.TenantWhereInput{ID: &wantTenant},
	})
	if err != nil {
		return false, fmt.Errorf("getTenants: %w", err)
	}
	for _, e := range resp.GetTenants().GetEdges() {
		if e != nil && e.GetNode() != nil && e.GetNode().GetID() == wantTenant {
			return true, nil
		}
	}
	return false, nil
}

// probeInventory lists inventory.repositories with the given bearer token
// and X-Pyck-Tenant-Id header value (empty = no header) filtered to
// wantRepo. Returns (visible, nil) when the request was accepted, or the
// request error. Inventory is the vantage point for the non-management
// services: its acting-tenant check round-trips through the gateway to
// management, whereas management consults its own tenant table.
func (s *DisabledTenantAccessSuite) probeInventory(token, header, wantRepo string) (bool, error) {
	var c inventoryapi.Client
	if header == "" {
		c = gateway.NewInventoryClient(s.Cfg, token)
	} else {
		c = gateway.NewInventoryClientForTenant(s.Cfg, token, header)
	}
	first := 1
	resp, err := c.GetRepositories(s.Ctx, inventoryapi.GetRepositoriesArgs{
		First: &first,
		Where: &inventoryapi.RepositoryWhereInput{ID: &wantRepo},
	})
	if err != nil {
		return false, fmt.Errorf("getRepositories: %w", err)
	}
	for _, e := range resp.GetRepositories().GetEdges() {
		if e != nil && e.GetNode() != nil && e.GetNode().ID == wantRepo {
			return true, nil
		}
	}
	return false, nil
}

// probe is the shape shared by probeManagement / probeInventory once the
// suite has bound the token, header and target row.
type probe func() (bool, error)

// acceptedAndVisible adapts a probe to the error-only shape of
// tests.WaitUntilAccepted: the request must be accepted AND the target row
// visible — re-admission only counts when the data is actually back.
func (p probe) acceptedAndVisible() error {
	visible, err := p()
	if err != nil {
		return err
	}
	if !visible {
		return fmt.Errorf("accepted but target row not visible")
	}
	return nil
}

// evictionBound is the latency inside which a denial or re-admission must
// land to count as event-driven: half the verdict TTL leaves the other half
// as slack for outbox drain + JetStream fan-out while still separating
// "evicted" from "expired" by construction.
func (s *DisabledTenantAccessSuite) evictionBound() time.Duration {
	return s.Cfg.LivenessCacheTTL / 2
}

// waitDeniedWithinBound waits until p is denied on both vantage points and
// asserts each denial arrived inside evictionBound. A denial that only
// lands once the cached verdict expires would still be within the poll
// timeout but is a broken eviction path, so it fails here.
func (s *DisabledTenantAccessSuite) waitDeniedWithinBound(mgmt, inv probe) {
	s.waitWithinBound("management", func() (time.Duration, error) {
		return tests.WaitUntilDenied(s.Ctx, "management", evictionPollBudget, func() error { _, err := mgmt(); return err })
	})
	s.waitWithinBound("inventory", func() (time.Duration, error) {
		return tests.WaitUntilDenied(s.Ctx, "inventory", evictionPollBudget, func() error { _, err := inv(); return err })
	})
}

// waitAcceptedWithinBound is the restore-side counterpart of
// waitDeniedWithinBound: both vantage points must accept the request AND
// show the target row, inside evictionBound.
func (s *DisabledTenantAccessSuite) waitAcceptedWithinBound(mgmt, inv probe) {
	s.waitWithinBound("management", func() (time.Duration, error) {
		return tests.WaitUntilAccepted(s.Ctx, "management", evictionPollBudget, mgmt.acceptedAndVisible)
	})
	s.waitWithinBound("inventory", func() (time.Duration, error) {
		return tests.WaitUntilAccepted(s.Ctx, "inventory", evictionPollBudget, inv.acceptedAndVisible)
	})
}

func (s *DisabledTenantAccessSuite) waitWithinBound(what string, wait func() (time.Duration, error)) {
	took, err := wait()
	s.Require().NoError(err, "%s did not reach the expected state", what)
	s.T().Logf("%s reached the expected state after %s", what, took.Round(time.Millisecond))
	s.Require().Less(took, s.evictionBound(),
		"%s must be driven by the lifecycle-event eviction, not by the verdict TTL (%s) expiring",
		what, s.Cfg.LivenessCacheTTL)
}
