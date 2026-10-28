//go:build integration

package singletenantguard_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// singleTenantError is request.ErrSingleTenantRequired's message.
	singleTenantError = "a single tenant must be selected"
	// tokenReadyTimeout covers Zitadel projecting the PAT and both grants.
	tokenReadyTimeout = 30 * time.Second
)

// operation is one single-tenant operation as a raw GraphQL document; the
// workflow IDs name no real run, the guard must refuse before any lookup.
type operation struct {
	name  string
	query string
}

var (
	workflowRef = `{workflowId: "e2e-single-tenant-workflow", workflowExecutionId: "e2e-single-tenant-run"}`
	userDataRef = `{workflowID: "e2e-single-tenant-workflow", workflowExecutionID: "e2e-single-tenant-run"}`
)

var singleTenantOperations = []operation{
	{"workflowTargets", `query { workflowTargets(input: ` + workflowRef + `) { targets } }`},
	{"workflowActions", `query { workflowActions(input: ` + workflowRef + `) { queries { name } } }`},
	{"workflowIsAssignable", `query { workflowIsAssignable(input: ` + workflowRef + `) { isAssignable } }`},
	{"workflowAssignee", `query { workflowAssignee(input: ` + workflowRef + `) { assignee } }`},
	{"currentUserDataInput", `query { currentUserDataInput(input: ` + userDataRef + `) { activityCount } }`},
	{"remoteUI", `query { remoteUI(input: ` + userDataRef + `) { webURL } }`},
	{"workerDeploymentUIBundles", `query { workerDeploymentUIBundles(first: 10) { totalCount } }`},
	{"createInventoryRepository", `mutation { createInventoryRepository(input: {name: "e2e-single-tenant-guard", type: static}) { inventoryRepository { id } } }`},
}

// SingleTenantGuardSuite registers two live tenants and a machine user
// holding a role in both, then sends every single-tenant operation with a
// selection that names two tenants or none.
type SingleTenantGuardSuite struct {
	tests.Base
}

// errorsOf posts query through the gateway with the given token and
// X-Pyck-Tenant-Id header and returns the GraphQL error messages; a
// transport failure or a non-200 answer fails the test.
func (s *SingleTenantGuardSuite) errorsOf(token, tenantHeader, query string) []string {
	s.T().Helper()
	r := s.Require()
	body, err := json.Marshal(map[string]any{"query": query})
	r.NoError(err)
	req, err := nethttp.NewRequestWithContext(s.Ctx, nethttp.MethodPost, s.Cfg.GatewayURL, bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Pyck-Tenant-Id", tenantHeader)
	resp, err := nethttp.DefaultClient.Do(req)
	r.NoError(err)
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			s.T().Logf("close response body: %v", cerr)
		}
	}()
	r.Equal(nethttp.StatusOK, resp.StatusCode, "the selection must pass the tenant middleware and reach the resolver")
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))
	msgs := make([]string, 0, len(out.Errors))
	for _, e := range out.Errors {
		msgs = append(msgs, e.Message)
	}
	return msgs
}

// assertRefused checks every single-tenant operation answers the guard's
// error, and only it, under the given token and selection.
func (s *SingleTenantGuardSuite) assertRefused(token, tenantHeader string) {
	s.T().Helper()
	for _, op := range singleTenantOperations {
		msgs := s.errorsOf(token, tenantHeader, op.query)
		s.Require().Len(msgs, 1, "%s under %q: %v", op.name, tenantHeader, msgs)
		s.Contains(msgs[0], singleTenantError, "%s under %q", op.name, tenantHeader)
	}
}

func (s *SingleTenantGuardSuite) TestOperationsRefuseAnythingButOneTenant() {
	var tenantA, tenantB *gateway.RegisteredTenant
	var pat string

	if !s.Run("provision two live tenants and a user in both", func() {
		r := s.Require()
		var err error
		tenantA, err = gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		r.NoError(err, "register tenant A")
		s.DeferTenantCleanup(tenantA.ID)
		tenantB, err = gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		r.NoError(err, "register tenant B")
		s.DeferTenantCleanup(tenantB.ID)

		// Both grants before the PAT is first used: the first request caches
		// the introspected role set.
		roles := tests.RolesWithServiceGates("writer")
		uid, err := zitadelclient.EnsureMachineUser(s.Ctx, s.ZConn, tenantA.IdpOrgRef, fixtures.NewMachineUser())
		r.NoError(err)
		r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, tenantA.IdpOrgRef, s.Cfg.ZitadelProjectID, uid, roles))
		r.NoError(zitadelclient.EnsureProjectGrant(s.Ctx, s.ZConn, tenantB.IdpOrgRef, s.Cfg.ZitadelProjectID, uid, roles))
		_, pat, err = zitadelclient.AddPAT(s.Ctx, s.ZConn, uid)
		r.NoError(err)
		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantA.ID, tokenReadyTimeout)
		r.NoError(err, "PAT must see tenant A")
		_, err = tests.WaitTokenReady(s.Ctx, s.Cfg, pat, tenantB.ID, tokenReadyTimeout)
		r.NoError(err, "PAT must see tenant B")
	}) {
		return
	}

	s.Run("a user naming both of its tenants is refused", func() {
		s.assertRefused(pat, fmt.Sprintf("%s,%s", tenantA.ID, tenantB.ID))
	})

	s.Run("a user under all, which expands to both of its tenants, is refused", func() {
		s.assertRefused(pat, "all")
	})

	s.Run("the system principal under all, which holds no tenant, is refused", func() {
		s.assertRefused(s.Cfg.ServiceToken, "all")
	})

	// The positive control: one tenant reaches the operation itself, so
	// the refusals above are about the selection, not about the operation
	// or the user.
	s.Run("one tenant passes the guard", func() {
		for _, op := range singleTenantOperations {
			for _, msg := range s.errorsOf(pat, tenantA.ID, op.query) {
				s.NotContains(msg, singleTenantError, "%s under one tenant", op.name)
			}
		}
	})
}
