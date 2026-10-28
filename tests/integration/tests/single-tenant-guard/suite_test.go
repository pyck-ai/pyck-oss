//go:build integration

// Package singletenantguard proves that operations which act on exactly
// one tenant (the namespace-scoped workflow queries and the inventory
// mutations) refuse a request that selects several tenants, or none, with
// "a single tenant must be selected" rather than the opaque "internal
// system error" of a recovered panic. A user that holds two live tenants
// is the only way to reach that guard through the gateway: a header
// naming a tenant without a live row is refused by the tenant middleware
// first, so the guard would never be exercised.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/single-tenant-guard/...
package singletenantguard_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestSingleTenantGuard(t *testing.T) {
	suite.Run(t, new(SingleTenantGuardSuite))
}
