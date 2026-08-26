//go:build integration

package authroles

import (
	"net/http"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TestGateDeniesWithoutServiceRole asserts every gated service denies (403) a
// user who holds a ladder role (writer) but not that service's gate role. The
// ladder role is required so the request clears the tenant middleware and
// actually reaches the gate.
func (s *AuthRolesSuite) TestGateDeniesWithoutServiceRole() {
	// One user serves all five probes: the assertion is per-service gate
	// behavior, and the user's capabilities (writer, no gate role) are the
	// same for every service.
	token := s.provisionUser("writer")
	for _, role := range serviceroles.All {
		role := role
		s.Run(role.String(), func() {
			status := s.callService(s.serviceURL(role), token, s.tenant.ID)
			s.Equal(http.StatusForbidden, status,
				"%s gate must deny a user without %s", role, role)
		})
	}
}

// TestGateAllowsWithServiceRole asserts every gated service admits (200) a user
// who holds the ladder role plus that service's gate role.
func (s *AuthRolesSuite) TestGateAllowsWithServiceRole() {
	for _, role := range serviceroles.All {
		role := role
		s.Run(role.String(), func() {
			token := s.provisionUser("writer", role.String())
			status := s.callService(s.serviceURL(role), token, s.tenant.ID)
			s.Equal(http.StatusOK, status,
				"%s gate must allow a user holding %s", role, role)
		})
	}
}

// TestGateDeniesWrongServiceRole asserts the gate is per-service: holding some
// OTHER service's gate role (plus writer) does not open a service. This guards
// against a gate that checks "holds any service role" rather than the specific
// one.
func (s *AuthRolesSuite) TestGateDeniesWrongServiceRole() {
	all := serviceroles.All
	for i, role := range all {
		role := role
		wrong := all[(i+1)%len(all)] // a different service's role
		s.Run(role.String(), func() {
			token := s.provisionUser("writer", wrong.String())
			status := s.callService(s.serviceURL(role), token, s.tenant.ID)
			s.Equal(http.StatusForbidden, status,
				"%s gate must deny a user holding only %s", role, wrong)
		})
	}
}

// TestSystemTokenBypassesAllGates asserts the system service token clears every
// gate without holding any service role.
func (s *AuthRolesSuite) TestSystemTokenBypassesAllGates() {
	for _, role := range serviceroles.All {
		role := role
		s.Run(role.String(), func() {
			status := s.callService(s.serviceURL(role), s.Cfg.ServiceToken, s.tenant.ID)
			s.Equal(http.StatusOK, status, "system token must bypass the %s gate", role)
		})
	}
}

// TestUnauthenticatedFallsThrough asserts the gate is transparent to
// unauthenticated requests: it does not itself return 403, leaving auth to the
// downstream handler. A public introspection query (__typename) therefore
// succeeds with no Authorization header, exactly as it did before the gate
// existed.
func (s *AuthRolesSuite) TestUnauthenticatedFallsThrough() {
	for _, role := range serviceroles.All {
		role := role
		s.Run(role.String(), func() {
			status := s.callService(s.serviceURL(role), "", "")
			s.NotEqual(http.StatusForbidden, status,
				"%s gate must not deny an unauthenticated request (it falls through)", role)
		})
	}
}

// TestManagementNotGated confirms the ungated control: a writer with no service
// role reaches management (via the gateway) and sees its own tenant.
func (s *AuthRolesSuite) TestManagementNotGated() {
	token := s.provisionUser("writer")

	ok, err := tests.TenantInList(s.Ctx, s.Cfg, token, s.tenant.ID)

	s.Require().NoError(err, "management must be reachable without a service role")
	s.True(ok, "writer user should see its own tenant via management")
}
