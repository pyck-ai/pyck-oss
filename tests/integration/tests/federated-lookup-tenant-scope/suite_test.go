//go:build integration

// Package federatedlookuptenantscope_test checks that a federated relation
// which follows a stored cross-service id never resolves to a row of another
// tenant.
//
// Two relations follow such an id: PickingOrder.customer (main-data looks the
// customer up by the order's customerID) and <entity>.file (the file service
// lists files by refid for Customer, Supplier, InventoryItem, Repository and
// PickingOrder). Cross-service ids are not foreign keys, so tenant A can store
// tenant B's id. A reader limited to one tenant never sees B's row, because
// the lookup runs under its tenant filter. A reader whose acting set holds
// both tenants, and the system token (PYCK_SERVICE_TOKEN, which holds
// ROLE_SYSTEM and skips the tenant filter) under any header, used to get B's
// row: the lookup filtered by id alone. The relation must resolve only rows of the
// referencing row's own tenant, whoever reads it.
//
// Each relation is read by (a) a single-tenant user of the row's own tenant,
// (b) a plain writer granted in both A and B sending "A,B" and "all", and
// (c) the system token with the row's own tenant, "A,B" and "all".
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -count=1 -run TestFederatedLookupTenantScope ./tests/federated-lookup-tenant-scope/
package federatedlookuptenantscope_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// FederatedLookupSuite embeds tests.Base for ctx, config, the Zitadel
// connection and fresh-tenant cleanup.
type FederatedLookupSuite struct {
	tests.Base
}

// TestFederatedLookupTenantScope is the runner.
func TestFederatedLookupTenantScope(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(FederatedLookupSuite))
}
