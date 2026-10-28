//go:build integration

package orgverdictcache

import (
	"fmt"
	"time"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// probeInventory issues a minimal authenticated read against the inventory
// subgraph through the gateway. A nil error means the token passed
// inventory's full auth chain (introspection, tenant liveness, gate).
func (s *VerdictCacheSuite) probeInventory(token string) error {
	c := gateway.NewInventoryClient(s.Cfg, token)
	first := 1
	if _, err := c.GetStocks(s.Ctx, inventoryapi.GetStocksArgs{First: &first}); err != nil {
		return fmt.Errorf("getStocks: %w", err)
	}
	return nil
}

// waitInventoryRejected is tests.WaitUntilDenied over the inventory probe,
// needed because the revocation event reaches every service's cache
// independently.
func (s *VerdictCacheSuite) waitInventoryRejected(token string, timeout time.Duration) (time.Duration, error) {
	return tests.WaitUntilDenied(s.Ctx, "inventory", timeout, func() error { return s.probeInventory(token) })
}
