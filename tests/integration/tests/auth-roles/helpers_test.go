//go:build integration

package authroles

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/serviceroles"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// AuthRolesSuite provisions one tenant for the whole suite and creates
// per-test machine users with different role grants on top of it. A second
// tenant — needed by the cross-tenant and multi-tenant-header tests — is
// registered lazily on first use and shared for the same reason: tenant
// registration is by far the most expensive fixture in the stack.
type AuthRolesSuite struct {
	tests.Base

	tenant *gateway.RegisteredTenant

	tenantBOnce sync.Once
	tenantB     *gateway.RegisteredTenant
}

func (s *AuthRolesSuite) SetupSuite() {
	s.Base.SetupSuite()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	s.Require().NoError(err, "register tenant")
	s.tenant = rt
}

func (s *AuthRolesSuite) TearDownSuite() {
	for _, t := range []*gateway.RegisteredTenant{s.tenant, s.tenantB} {
		if t != nil {
			_ = gateway.DeleteTenant(s.Ctx, s.Cfg, t.ID)
		}
	}
	s.Base.TearDownSuite()
}

// secondaryTenant returns the suite's shared second tenant, registering it on
// first use and tearing it down in TearDownSuite. The cross-tenant tests share
// this one tenant (with per-test distinct users) rather than each registering
// their own. A test that soft-deletes a tenant as part of its assertion must
// NOT use this — it would break every later test sharing it — and registers
// its own disposable tenant instead.
func (s *AuthRolesSuite) secondaryTenant() *gateway.RegisteredTenant {
	s.tenantBOnce.Do(func() {
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		s.Require().NoError(err, "register shared secondary tenant")
		s.tenantB = rt
	})
	s.Require().NotNil(s.tenantB, "shared secondary tenant registration failed in an earlier test")
	return s.tenantB
}

// provisionedUser bundles the identifiers a test needs to drive a freshly
// created machine user: its bearer token, its Zitadel user id (to mint more
// tokens), and its username (to locate the synced management user).
type provisionedUser struct {
	token    string
	idpID    string
	username string
}

// provisionUser creates a fresh machine user in the suite tenant's sub-org,
// grants it the given project roles, and returns a freshly minted PAT. The PAT
// is new each call, so its introspection is never served from a stale cache.
func (s *AuthRolesSuite) provisionUser(roleKeys ...string) string {
	return s.provisionUserDetailed(roleKeys...).token
}

// provisionUserDetailed is provisionUser but also returns the Zitadel user id
// and username, for tests that later mint fresh tokens or resolve the synced
// management user.
func (s *AuthRolesSuite) provisionUserDetailed(roleKeys ...string) provisionedUser {
	s.T().Helper()
	return s.provisionUserInTenant(s.tenant, roleKeys...)
}

// provisionUserInTenant creates a fresh machine user in the given tenant's
// sub-org, grants it the given project roles there, and returns a freshly
// minted PAT plus identifiers. Factored out of provisionUserDetailed so
// cross-tenant tests can place a user (and grants) in an arbitrary tenant.
func (s *AuthRolesSuite) provisionUserInTenant(t *gateway.RegisteredTenant, roleKeys ...string) provisionedUser {
	s.T().Helper()

	u := fixtures.NewMachineUser()
	userID, err := zitadelclient.EnsureMachineUser(s.Ctx, s.ZConn, t.IdpOrgRef, u)
	s.Require().NoError(err, "ensure machine user")

	err = zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, t.IdpOrgRef, s.Cfg.ZitadelProjectID, userID, roleKeys)
	s.Require().NoError(err, "grant project roles")

	_, token, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, userID)
	s.Require().NoError(err, "add PAT")

	return provisionedUser{token: token, idpID: userID, username: u.Username}
}

// provisionUserNoRoles creates a fresh machine user in the suite tenant's
// sub-org with NO project grant at all — no ladder role and no service role —
// and returns a freshly minted PAT plus identifiers. The user is authenticated
// (it has a valid token and belongs to the tenant org) but holds nothing, which
// is the baseline "deny" case for both the tenant middleware and the gate.
func (s *AuthRolesSuite) provisionUserNoRoles() provisionedUser {
	s.T().Helper()

	u := fixtures.NewMachineUser()
	userID, err := zitadelclient.EnsureMachineUser(s.Ctx, s.ZConn, s.tenant.IdpOrgRef, u)
	s.Require().NoError(err, "ensure machine user")

	_, token, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, userID)
	s.Require().NoError(err, "add PAT")

	return provisionedUser{token: token, idpID: userID, username: u.Username}
}

// grantInTenant adds project roles for an existing user in a second tenant's
// sub-org, making the user a cross-tenant member (one authorization per org on
// the central Pyck project). Used to build the cross-tenant scoping scenario:
// a user that holds a service role in one tenant but not another.
func (s *AuthRolesSuite) grantInTenant(t *gateway.RegisteredTenant, userID string, roleKeys ...string) {
	s.T().Helper()

	err := zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, t.IdpOrgRef, s.Cfg.ZitadelProjectID, userID, roleKeys)
	s.Require().NoError(err, "grant project roles in second tenant")
}

// freshToken mints a brand-new PAT for an existing user. A never-before-seen
// token forces a fresh introspection, so the gate observes the user's current
// roles rather than a cached snapshot.
func (s *AuthRolesSuite) freshToken(idpID string) string {
	s.T().Helper()

	_, token, err := zitadelclient.AddPAT(s.Ctx, s.ZConn, idpID)
	s.Require().NoError(err, "mint fresh PAT")

	return token
}

// waitForSyncedUser polls management (system token) until the machine user with
// the given username is projected into the suite tenant by the zitadel-sync
// workflow, returning its management user id.
func (s *AuthRolesSuite) waitForSyncedUser(username string) string {
	s.T().Helper()
	return s.waitForSyncedUserInTenant(s.tenant.ID, username)
}

// waitForSyncedUserInTenant is waitForSyncedUser generalized to an arbitrary
// tenant, for cross-tenant tests where a synced user's management id is needed
// in a tenant other than the suite's own. The lookup filters by username
// server-side, so it stays O(1) however many users the tenant accumulates.
func (s *AuthRolesSuite) waitForSyncedUserInTenant(tenantID, username string) string {
	s.T().Helper()

	c := gateway.NewClient(s.Cfg, s.Cfg.ServiceToken)
	first := 1
	var userID string
	err := tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		resp, err := c.GetUsers(s.Ctx, managementapi.GetUsersArgs{
			First: &first,
			Where: &managementapi.UserWhereInput{TenantID: &tenantID, Username: &username},
		})
		if err != nil {
			return fmt.Errorf("getUsers: %w", err)
		}
		users := resp.GetUsers()
		if users == nil || len(users.GetEdges()) == 0 || users.GetEdges()[0].GetNode() == nil {
			return fmt.Errorf("user %q not yet synced into tenant %s", username, tenantID)
		}
		userID = users.GetEdges()[0].GetNode().GetID()
		return nil
	})
	s.Require().NoError(err, "machine user %q not projected into tenant %s", username, tenantID)
	return userID
}

// serviceURL maps a per-service gate role to that service's direct HTTP
// endpoint. The switch is exhaustive over serviceroles.All; a new role added to
// the catalog without a URL here fails loudly (see TestServiceCatalogHasURLs)
// rather than silently going untested.
func (s *AuthRolesSuite) serviceURL(role serviceroles.ServiceRole) string {
	switch role {
	case serviceroles.Inventory:
		return s.Cfg.InventoryURL
	case serviceroles.Picking:
		return s.Cfg.PickingURL
	case serviceroles.Receiving:
		return s.Cfg.ReceivingURL
	case serviceroles.File:
		return s.Cfg.FileURL
	case serviceroles.MainData:
		return s.Cfg.MainDataURL
	default:
		s.FailNowf("no direct URL", "service role %q has no direct URL configured", role)
		return ""
	}
}

// callService issues a trivial GraphQL query against a gated service directly
// (bypassing the gateway) with the given operative-tenant header, and returns
// the HTTP status. The gate runs before the GraphQL handler, so the query body
// is irrelevant: 403 means the gate denied, 200 means the request reached the
// handler. tenantHeader is sent verbatim as X-Pyck-Tenant-Id (it may be a
// single id, a comma-separated list, or "all"); an empty string omits the
// header entirely, exercising the middleware's default-to-"all" path.
func (s *AuthRolesSuite) callService(url, token, tenantHeader string) int {
	s.T().Helper()

	req, err := http.NewRequestWithContext(
		s.Ctx, http.MethodPost, url+"/query",
		strings.NewReader(`{"query":"{ __typename }"}`),
	)
	s.Require().NoError(err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenantHeader != "" {
		req.Header.Set("X-Pyck-Tenant-Id", tenantHeader)
	}

	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}

// adminClient returns a management client in tenant-admin context for the suite
// tenant. The system service token satisfies the admin gate, so it stands in
// for a tenant admin in the role-management endpoint tests.
func (s *AuthRolesSuite) adminClient() managementapi.Client {
	return gateway.NewClientForTenant(s.Cfg, s.Cfg.ServiceToken, s.tenant.ID)
}

// unauthedClient returns a management client whose bearer token never
// introspects to an authenticated user, for exercising the authentication
// requirement on the endpoints. Management's auth middleware rejects the
// invalid token with HTTP 401 before the request reaches any resolver, and
// the gateway surfaces that as a subgraph fetch error whose message contains
// "401: Unauthorized" — the substring the RequiresAuth tests pin.
func (s *AuthRolesSuite) unauthedClient() managementapi.Client {
	return gateway.NewClient(s.Cfg, "this-is-not-a-valid-token")
}

// tenantUUID is the suite tenant's id parsed as a uuid, for endpoint inputs.
func (s *AuthRolesSuite) tenantUUID() uuid.UUID {
	s.T().Helper()
	id, err := uuid.Parse(s.tenant.ID)
	s.Require().NoError(err, "parse tenant id")
	return id
}

// syncedUser provisions a fresh writer machine user in the suite tenant and
// returns its management user id once the zitadel-sync workflow projects it.
// Each call yields a distinct user, so role-mutating endpoint tests do not
// interfere with one another.
func (s *AuthRolesSuite) syncedUser() uuid.UUID {
	s.T().Helper()
	u := s.provisionUserDetailed("writer")
	id, err := uuid.Parse(s.waitForSyncedUser(u.username))
	s.Require().NoError(err, "parse synced user id")
	return id
}

// provisionReadyWriter provisions a fresh writer machine user (no service
// roles) and blocks until its PAT introspects with the writer grant visible.
// Tests that pin a resolver-level error message need this barrier: before the
// grant projects through Zitadel, the tenant middleware rejects the request
// outright and the test would observe that transient error instead of the
// guard under test.
func (s *AuthRolesSuite) provisionReadyWriter() string {
	s.T().Helper()

	token := s.provisionUser("writer")
	_, err := tests.WaitTokenReady(s.Ctx, s.Cfg, token, s.tenant.ID, 30*time.Second)
	s.Require().NoError(err, "writer PAT never became usable")
	return token
}
