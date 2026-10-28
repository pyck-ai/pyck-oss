//go:build integration

package tenants

import (
	"fmt"
	"slices"
	"time"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TestRegisterTenant_UserGrantIsWritableUnderTheTenantOrgScope proves that
// the platform service account may read and replace a user authorization
// on a connection scoped to the tenant's own organization
// (x-zitadel-orgid), which is how AddUserGrantActivity reconciles a grant
// that already exists. Every other test drives the grant APIs at instance
// scope, so a permission difference under the org header would first show
// up as a registration retrying itself into an error row.
//
// reader is the role added because the registration delegates the whole
// ladder to the org's project grant while granting the admin only admin:
// a user authorization may only carry roles the org's menu lists, so this
// exercises the replace without widening the menu first.
func (s *TnRegisterSuite) TestRegisterTenant_UserGrantIsWritableUnderTheTenantOrgScope() {
	r := s.Require()
	tenant := fixtures.NewTenant()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)
	r.Equal("done", rt.Status)

	// The admin's Zitadel id is never reported to the harness, so it is
	// read back off the authorization the registration created. Polled:
	// the authorization projection is eventually consistent.
	var adminID string
	err = tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		id, idErr := zitadelclient.GrantedUserID(s.Ctx, s.ZConn, rt.IdpOrgRef, s.Cfg.ZitadelProjectID, tenant.AdminUsername)
		adminID = id
		return idErr
	})
	r.NoError(err, "the registration admin's authorization must be readable")

	orgConn, err := zitadelclient.DialForOrg(s.Ctx, s.Cfg, rt.IdpOrgRef)
	r.NoError(err, "dial zitadel scoped to the tenant org")
	defer func() { r.NoError(orgConn.Close(), "close org-scoped connection") }()

	_, updated, err := sdk.ExtendUserAuthorizationRoles(s.Ctx, sdk.NewGrantClient(orgConn),
		s.Cfg.ZitadelProjectID, rt.IdpOrgRef, adminID, []string{authn.ROLE_READER.String()})
	r.NoError(err, "the service account must be able to replace an authorization under the tenant-org header")
	r.True(updated, "reader is not part of the registration's admin-only grant, so the set must be replaced")

	// The union, not the addition: the replace is whole-set, so a lost
	// admin here is the cascade the union exists to prevent.
	err = tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		keys, kerr := zitadelclient.GrantedRoleKeys(s.Ctx, s.ZConn, rt.IdpOrgRef, s.Cfg.ZitadelProjectID, tenant.AdminUsername)
		if kerr != nil {
			return kerr
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"admin", "reader"}) {
			return fmt.Errorf("admin grant carries %v, want [admin reader]", keys)
		}
		return nil
	})
	r.NoError(err, "the widened authorization must keep the roles it already held")
}
