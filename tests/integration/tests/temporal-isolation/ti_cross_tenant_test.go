//go:build integration

package temporalisolation

import (
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
)

// TestControlAttackerReachesOwnNamespace is the positive control: the
// attacker's PAT must succeed against the attacker's OWN namespace, across the
// same read and list surfaces the cross-tenant tests probe. Without this, a
// blanket denial (e.g. a broken PAT) would make the cross-tenant assertions
// pass for the wrong reason.
func (s *IsolationSuite) TestControlAttackerReachesOwnNamespace() {
	nsA := s.tenantA.Tenant.ID

	nc, err := temporal.NewNamespaceClient(s.Cfg.TemporalAddress, s.tenantA.PAT)
	s.Require().NoError(err, "namespace client for attacker")
	defer nc.Close()

	_, err = nc.Describe(s.Ctx, nsA)
	s.Require().NoError(err, "attacker must be able to describe its own namespace")

	wc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, nsA, s.tenantA.PAT)
	s.Require().NoError(err, "workflow client for attacker on own namespace")
	defer wc.Close()

	_, err = wc.ListWorkflow(s.Ctx, listRequest(nsA))
	s.Require().NoError(err, "attacker must be able to list workflows in its own namespace")
}

// TestDescribeVictimNamespaceDenied drives the attacker PAT at the victim's
// namespace on the namespace-management surface. DescribeNamespace carries the
// target namespace in its payload, so the authorizer evaluates the attacker's
// claims against namespace B — which they do not include — and must deny.
func (s *IsolationSuite) TestDescribeVictimNamespaceDenied() {
	nc, err := temporal.NewNamespaceClient(s.Cfg.TemporalAddress, s.tenantA.PAT)
	s.Require().NoError(err, "namespace client for attacker")
	defer nc.Close()

	_, err = nc.Describe(s.Ctx, s.tenantB.Tenant.ID)
	s.requireDenied(err, "describe victim namespace")
}

// TestListVictimWorkflowsDenied drives the attacker PAT at the victim's
// namespace on the visibility (read) surface. pyck-temporal denies this at the
// dial handshake — the SDK's eager capability check against namespace B is
// rejected — so the attacker never obtains a usable client; if a build ever
// let the dial through, the ListWorkflow call must still be denied.
func (s *IsolationSuite) TestListVictimWorkflowsDenied() {
	wc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, s.tenantB.Tenant.ID, s.tenantA.PAT)
	if err != nil {
		s.requireDenied(err, "dial victim namespace")
		return
	}
	defer wc.Close()

	_, err = wc.ListWorkflow(s.Ctx, listRequest(s.tenantB.Tenant.ID))
	s.requireDenied(err, "list victim workflows")
}

// TestStartWorkflowInVictimDenied drives the attacker PAT at the victim's
// namespace on the write surface — the most damaging cross-tenant action.
// As with the read surface, the denial lands at the dial handshake; the
// workflow type need not exist, because authorization stops the attacker
// before any dispatch.
func (s *IsolationSuite) TestStartWorkflowInVictimDenied() {
	wc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, s.tenantB.Tenant.ID, s.tenantA.PAT)
	if err != nil {
		s.requireDenied(err, "dial victim namespace")
		return
	}
	defer wc.Close()

	_, err = wc.ExecuteWorkflow(s.Ctx, temporalclient.StartWorkflowOptions{
		TaskQueue: "cross-tenant-probe",
	}, "CrossTenantProbeWorkflow")
	s.requireDenied(err, "start workflow in victim namespace")
}
