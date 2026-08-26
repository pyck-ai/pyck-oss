//go:build integration

package tenantclaim

import (
	"time"

	"github.com/pyck-ai/pyck/backend/common/authn"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// claimPropagationTimeout bounds the wait for a freshly-minted machine key
// + project grant to project in Zitadel, so the private_key_jwt token
// request succeeds and the Action fires. Generous because two separate
// eventually-consistent projections (key + grant) gate the first token.
const claimPropagationTimeout = 20 * time.Second

// TenantClaimSuite asserts the pyck_tenant_id derivation and emission agree
// across three independent vantage points:
//
//   - the pyck DB tenant UUID (registerTenant response)
//   - ComputeUUID(audience, orgID), the canonical server-side formula
//   - the plain top-level pyck_tenant_id claim on an introspected OIDC token
//
// All three must equal the same UUID. Because the expected value is a
// canonical UUID string, the exact-equality wait on the claim also pins its
// shape: a base64-encoded claim (the legacy urn:zitadel:iam:user:metadata
// encoding) can never compare equal.
type TenantClaimSuite struct {
	tests.Base
}

func (s *TenantClaimSuite) TestTenantClaimReachesOIDCToken() {
	tenant := fixtures.NewTenant()
	s.T().Logf("tenant=%s", tenant.Name)

	var (
		registered *gateway.RegisteredTenant
		keyfile    []byte
		computed   string
	)

	// Provision a tenant + machine user, then mint a JWT-profile keyfile for
	// that user (AddMachineKey supplies the OIDC keyfile bytes the token
	// mints below need). The bare writer role suffices: this suite only
	// introspects tokens and queries management, never a gated subgraph, so
	// the per-service gate roles are not needed.
	if !s.Run("register tenant and provision machine key", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err)
		registered = rt
		s.DeferTenantCleanup(registered.ID)

		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, registered, []string{"writer"})
		r.NoError(err, "provision machine user")

		_, kf, err := zitadelclient.AddMachineKey(s.Ctx, s.ZConn, p.UserID)
		r.NoError(err)
		r.NotEmpty(kf, "machine keyfile bytes")
		keyfile = kf
		s.T().Logf("tenantID=%s orgID=%s userID=%s", registered.ID, registered.IdpOrgRef, p.UserID)
	}) {
		return
	}

	// The pyck DB tenant UUID must equal ComputeUUID(audience, orgID) — the
	// same derivation register-tenant and the webhook use. A mismatch means
	// the audience wiring drifted between the test env and the stack.
	if !s.Run("tenant UUID equals ComputeUUID(audience, orgID)", func() {
		r := s.Require()
		computed = authn.ComputeUUID(s.Cfg.ZitadelAudience, registered.IdpOrgRef).String()
		r.Equal(computed, registered.ID, "management.tenants.id must equal ComputeUUID(audience, orgID)")
	}) {
		return
	}

	// Mint a machine OIDC token (this token request fires the Actions v2
	// Execution) and introspect it. The webhook computes pyck_tenant_id from
	// the user's resource_owner and splices it in as a plain top-level
	// claim. We poll because the key + grant projection is eventually
	// consistent.
	s.Run("OIDC token carries plain top-level pyck_tenant_id", func() {
		r := s.Require()
		err := waitForTenantClaim(s.Ctx, s.Cfg, keyfile, computed, claimPropagationTimeout)
		r.NoError(err, "introspected token never surfaced %s=%s", tenantClaimKey, computed)
		s.T().Logf("%s claim = %s", tenantClaimKey, computed)
	})
}
