//go:build integration

// Package federatedskutenantscope_test checks that a picking order item's
// inventory item and stock, which the router resolves from inventory by the
// order item's SKU, always come from the order item's own tenant.
//
// A SKU is unique per tenant only, so two tenants can hold the same SKU
// without anyone doing anything wrong. A reader limited to one tenant only
// ever sees that tenant's item, because the lookup runs under its tenant
// filter. A plain reader granted in both tenants that sends both, and the
// system token (PYCK_SERVICE_TOKEN, which holds ROLE_SYSTEM and skips the
// tenant filter), used to get whichever tenant's item and warehouse came
// first: the lookup matched the SKU alone.
//
// Each order item is read by (a) a single-tenant writer of the order's own
// tenant, (b) a plain reader granted in both A and B sending "A,B" and "all",
// and (c) the system token with the order's own tenant, "A,B" and "all".
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestFederatedSkuTenantScope ./tests/federated-sku-tenant-scope/
package federatedskutenantscope_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// FederatedSkuSuite embeds tests.Base for ctx, config, the Zitadel
// connection and fresh-tenant cleanup.
type FederatedSkuSuite struct {
	tests.Base
}

// TestFederatedSkuTenantScope is the runner.
func TestFederatedSkuTenantScope(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(FederatedSkuSuite))
}
