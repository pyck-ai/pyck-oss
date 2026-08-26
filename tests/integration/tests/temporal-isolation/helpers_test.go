//go:build integration

package temporalisolation

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// namespaceReadyTimeout bounds the wait for a freshly-registered tenant's
// per-tenant Temporal namespace to become describable. register-tenant runs
// the namespace creation asynchronously after the mutation returns.
const namespaceReadyTimeout = 30 * time.Second

// IsolationSuite provisions two symmetric tenants once for the whole suite,
// each with a writer PAT. The shared-connection tests use both PATs to drive
// the workflow service into serving both namespaces; the cross-tenant denial
// tests use only A's PAT (as the attacker) against B's namespace (as the
// victim) — B's PAT is never presented to Temporal there, so a denial can
// only come from A's claims, not from anything B did.
type IsolationSuite struct {
	tests.Base

	tenantA *tests.ProvisionedTenant // tenant A + writer PAT (attacker in the denial tests)
	tenantB *tests.ProvisionedTenant // tenant B + writer PAT (victim in the denial tests)

	// suiteTenants collects the suite-scoped tenants for TearDownSuite.
	// Deliberately NOT Base.DeferTenantCleanup: that list is drained by
	// TearDownTest after EVERY test method, which would soft-delete the
	// shared tenants after the first test and leave the rest of the suite
	// asserting against disabled orgs — where a denial passes for the wrong
	// reason (revoked org, not cross-tenant authorization).
	suiteTenants []string

	// probeCleanups collects teardown closures for probe executions, run by
	// TearDownSuite before the tenants are deleted. Suite-scoped for the
	// same reason as suiteTenants: inside an s.Run stage, s.T().Cleanup
	// binds to the stage's T and would fire mid-test.
	probeCleanups []func()
}

// deferProbeCleanup queues fn to run in TearDownSuite.
func (s *IsolationSuite) deferProbeCleanup(fn func()) {
	s.probeCleanups = append(s.probeCleanups, fn)
}

func (s *IsolationSuite) SetupSuite() {
	s.Base.SetupSuite()

	// A writer role maps to Reader|Writer on the tenant's own namespace
	// under the pyck claim mapper — enough for every positive assertion in
	// this suite (describe, list, start in the own namespace).
	s.tenantA = s.provisionTenant()
	s.tenantB = s.provisionTenant()

	// Barrier: both namespaces must exist before the assertions run, or a
	// denial could be masking a not-yet-created namespace, and a probe
	// execution could fail to start for the wrong reason.
	_, err := s.waitForNamespace(s.tenantA.Tenant.ID, s.tenantA.PAT)
	s.Require().NoError(err, "namespace A becomes describable")
	_, err = s.waitForNamespace(s.tenantB.Tenant.ID, s.tenantB.PAT)
	s.Require().NoError(err, "namespace B becomes describable")
}

// provisionTenant registers a fresh tenant and provisions a machine user
// with a writer PAT inside it. The tenant is queued for TearDownSuite
// immediately after registration, so a run failing mid-provisioning still
// tears it down.
func (s *IsolationSuite) provisionTenant() *tests.ProvisionedTenant {
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	s.Require().NoError(err, "register tenant")
	s.suiteTenants = append(s.suiteTenants, rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, []string{"writer"})
	s.Require().NoError(err, "provision PAT in tenant %s", rt.ID)
	return p
}

// TearDownSuite terminates any probe executions, soft-deletes the
// suite-scoped tenants, then releases the Base resources. Runs even when
// SetupSuite aborted partway (testify defers it), so whatever was
// registered by then is cleaned up.
func (s *IsolationSuite) TearDownSuite() {
	for _, fn := range s.probeCleanups {
		fn()
	}
	for _, id := range s.suiteTenants {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := gateway.DeleteTenant(ctx, s.Cfg, id); err != nil {
			s.T().Logf("cleanup: deleteTenant %s: %v", id, err)
		}
		cancel()
	}
	s.Base.TearDownSuite()
}

// waitForNamespace polls the Temporal namespace client (public frontend)
// until the given namespace is describable with the given credential.
func (s *IsolationSuite) waitForNamespace(namespace, apiKey string) (time.Duration, error) {
	nc, err := temporal.NewNamespaceClient(s.Cfg.TemporalAddress, apiKey)
	if err != nil {
		return 0, err
	}
	defer nc.Close()
	return tests.PollUntilElapsed(s.Ctx, namespaceReadyTimeout, 500*time.Millisecond, func() error {
		_, describeErr := nc.Describe(s.Ctx, namespace)
		return describeErr
	})
}

// requireDenied asserts err is a pyck-temporal authorization denial for a
// cross-tenant attempt. pyck-temporal enforces per-namespace claims at two
// points, so the denial can surface either way and both are acceptable:
//
//   - as a typed serviceerror.PermissionDenied / Unauthenticated on the RPC
//     (e.g. DescribeNamespace, which the authorizer rejects on the call), or
//   - at dial time, when the SDK's eager capability handshake against the
//     target namespace is rejected and the SDK wraps it as a connection error
//     ("failed reaching server: Request unauthorized").
//
// It is deliberately stricter than "any error": a NotFound would mean the
// authorizer let the request reach the persistence layer (leaking which
// namespaces exist), and a nil error would mean the attack succeeded.
func (s *IsolationSuite) requireDenied(err error, op string) {
	s.T().Helper()
	s.Require().Error(err, "%s across tenants must be denied, not succeed", op)

	var permErr *serviceerror.PermissionDenied
	if errors.As(err, &permErr) {
		return
	}

	// The dial-time handshake rejection (gRPC Unauthenticated) is not
	// surfaced as a typed serviceerror through the SDK's connection wrapper,
	// so fall back to matching the authorization vocabulary. Any other error
	// (NotFound, connection refused, ...) fails: it would mean the request
	// was not stopped by authorization.
	msg := strings.ToLower(err.Error())
	denied := strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "forbidden")
	s.Require().Truef(denied,
		"%s across tenants must be denied by authorization, got %T: %v", op, err, err)
}

// listRequest builds a minimal visibility list request for the namespace.
func listRequest(namespace string) *workflowservice.ListWorkflowExecutionsRequest {
	return &workflowservice.ListWorkflowExecutionsRequest{Namespace: namespace}
}
