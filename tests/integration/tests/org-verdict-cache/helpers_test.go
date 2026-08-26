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
// inventory's full auth chain (introspection, org-active check, gate).
func (s *VerdictCacheSuite) probeInventory(token string) error {
	c := gateway.NewInventoryClient(s.Cfg, token)
	first := 1
	if _, err := c.GetStocks(s.Ctx, inventoryapi.GetStocksArgs{First: &first}); err != nil {
		return fmt.Errorf("getStocks: %w", err)
	}
	return nil
}

// waitInventoryRejected polls the inventory probe until it fails with a
// definitive auth denial (tests.IsAuthDenial), needed because the revocation
// event reaches every service's cache independently. Non-auth errors —
// transport faults, 5xx — keep polling: counting them as rejection would let
// a broken revocation path pass on the back of an unrelated outage.
func (s *VerdictCacheSuite) waitInventoryRejected(token string, timeout time.Duration) (time.Duration, error) {
	return tests.PollUntilElapsed(s.Ctx, timeout, 200*time.Millisecond, func() error {
		err := s.probeInventory(token)
		switch {
		case err == nil:
			return fmt.Errorf("inventory still accepts the token")
		case tests.IsAuthDenial(err.Error()):
			return nil
		default:
			return fmt.Errorf("inventory probe infra error (not a rejection): %w", err)
		}
	})
}
