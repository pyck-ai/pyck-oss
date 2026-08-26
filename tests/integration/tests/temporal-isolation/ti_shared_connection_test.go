//go:build integration

package temporalisolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	temporalclient "go.temporal.io/sdk/client"
	temporalsdk "go.temporal.io/sdk/temporal"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	workflowapi "github.com/pyck-ai/pyck/backend/workflow/api"
	workflowmodel "github.com/pyck-ai/pyck/backend/workflow/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// workflowContainer is the compose service container whose Temporal
	// connections the shared-connection assertion counts.
	workflowContainer = "pyck-workflow"

	// workflowTemporalPort is the internal (non-authorizing) Temporal
	// frontend the backend services connect to — TemporalUrl in
	// config/compose. The public frontend the suite's own SDK clients use
	// (config.TemporalAddress, :7233) is a different port, so counting by
	// this port isolates the service's connections from the test's own.
	workflowTemporalPort = 7236

	// visibilityTimeout bounds the wait for a started execution to appear
	// in Temporal visibility (advanced visibility indexing is async).
	visibilityTimeout = 30 * time.Second

	// connectionStabilityWindow bounds the quiescence check on the workflow
	// service's connection count. The count is asserted to hold for the
	// whole window rather than to settle within it: namespace setup
	// registers over the shared connection, so a second connection is never
	// expected at any point, not even briefly.
	connectionStabilityWindow = 3 * time.Second
)

// TestSharedConnectionAndNamespaceScoping proves the per-namespace client
// cache in the workflow service's factory works over ONE shared connection:
// each tenant's derived client is bound to that tenant's namespace (a tenant
// sees its own executions and never a foreign tenant's), and serving both
// namespaces leaves the service holding exactly one established TCP
// connection to Temporal.
func (s *IsolationSuite) TestSharedConnectionAndNamespaceScoping() {
	suffix := strings.ToLower(gofakeit.LetterN(8))
	probeA := "isolation-probe-a-" + suffix
	probeB := "isolation-probe-b-" + suffix

	// Start one probe execution in each tenant's namespace, directly over
	// the public frontend with the tenant's own PAT. The workflow type has
	// no worker: the execution just sits RUNNING with a pending task, which
	// is all visibility needs. Terminated again on cleanup.
	if !s.Run("start one probe execution per tenant", func() {
		s.startProbe(s.tenantA, probeA)
		s.startProbe(s.tenantB, probeB)
	}) {
		return
	}

	// Each tenant queries workflowExecutions through the gateway, which the
	// workflow service serves via its factory client for that tenant's
	// namespace — the first query per tenant forces the namespace-client
	// derivation. Polled: visibility indexing is async.
	if !s.Run("each tenant sees its own execution through the service", func() {
		s.Require().NoError(s.waitExecutionVisible(s.tenantA.PAT, probeA), "tenant A sees probe A")
		s.Require().NoError(s.waitExecutionVisible(s.tenantB.PAT, probeB), "tenant B sees probe B")
	}) {
		return
	}

	// Both probes are now proven present in visibility, so a zero result
	// for the foreign probe is namespace scoping, not indexing lag. If the
	// cache ever handed a tenant a client bound to the wrong namespace,
	// this or the positive stage above would light up. Probed on both query
	// surfaces a client actually uses: targeted retrieval by workflow ID,
	// and the unfiltered listing.
	s.Run("neither tenant sees the other's execution through the service", func() {
		n, err := s.countExecutions(s.tenantA.PAT, probeB)
		s.Require().NoError(err, "tenant A queries probe B")
		s.Require().Zero(n, "tenant A must not retrieve tenant B's execution by ID")

		n, err = s.countExecutions(s.tenantB.PAT, probeA)
		s.Require().NoError(err, "tenant B queries probe A")
		s.Require().Zero(n, "tenant B must not retrieve tenant A's execution by ID")

		idsA, err := s.listExecutionIDs(s.tenantA.PAT)
		s.Require().NoError(err, "tenant A lists executions")
		s.Require().Contains(idsA, probeA, "tenant A's unfiltered listing must include its own probe")
		s.Require().NotContains(idsA, probeB, "tenant A's unfiltered listing must not include tenant B's probe")

		idsB, err := s.listExecutionIDs(s.tenantB.PAT)
		s.Require().NoError(err, "tenant B lists executions")
		s.Require().Contains(idsB, probeB, "tenant B's unfiltered listing must include its own probe")
		s.Require().NotContains(idsB, probeA, "tenant B's unfiltered listing must not include tenant A's probe")
	})

	// Injection direction: tenant B starts an execution in its OWN namespace
	// but stamps tenant A's ID into pyck_tenant_id (search attributes are
	// client-settable on the public frontend). The namespace layer must keep
	// it out of A's view regardless: the service reads A's data only from
	// A's namespace, so an attribute spoofed in B's namespace can never
	// surface. If the start itself is rejected, spoofing is prevented even
	// earlier — also a pass.
	s.Run("a spoofed tenant attribute cannot inject into the other tenant's view", func() {
		spoofID := "isolation-probe-spoof-" + suffix
		started := s.startSpoofedProbe(s.tenantB, spoofID, s.tenantA.Tenant.ID)
		if !started {
			return
		}

		// Prove the spoofed execution is really indexed (raw visibility in
		// B's namespace, no tenant-attribute filter) before asserting its
		// absence anywhere in the service views.
		s.Require().NoError(s.waitRawVisible(s.tenantB, spoofID), "spoofed execution reaches raw visibility")

		n, err := s.countExecutions(s.tenantA.PAT, spoofID)
		s.Require().NoError(err, "tenant A queries the spoofed execution")
		s.Require().Zero(n, "an execution in B's namespace must not appear in A's view, whatever its tenant attribute claims")

		idsA, err := s.listExecutionIDs(s.tenantA.PAT)
		s.Require().NoError(err, "tenant A lists executions")
		s.Require().NotContains(idsA, spoofID, "tenant A's listing must not include the spoofed execution")
	})

	// Both tenant namespaces have now been served, so both derived clients
	// exist — and they must ride one connection. More than one would mean a
	// derived client dialed its own (the pre-factory leak); zero would mean
	// the count is measuring the wrong thing. Asserted as a quiescence
	// invariant, not an eventual one: namespace setup registers over the
	// shared connection, so a second connection is never expected even
	// briefly, and PollStable fails the moment one appears.
	s.Run("the service holds exactly one Temporal connection", func() {
		observed := false
		var readErr error
		err := tests.PollStable(s.Ctx, connectionStabilityWindow, 500*time.Millisecond, func() error {
			count, pollErr := workflowServiceTemporalConns()
			if pollErr != nil {
				// A failed read says nothing about the invariant, so the
				// sample is skipped rather than breaking the window. A
				// window where no sample ever read becomes a skip below.
				readErr = pollErr
				return nil
			}
			observed = true
			if count != 1 {
				return fmt.Errorf("%d connections", count)
			}
			return nil
		})
		if !observed {
			// Not one sample could read the container's net tables: the
			// environment cannot support the observation (no docker CLI,
			// remote daemon, non-Linux). An observed wrong count —
			// including zero — is a failure, never a skip.
			s.T().Skipf("cannot observe the workflow container's connections here: %v", readErr)
		}
		s.Require().NoError(err,
			"workflow service must hold exactly one established connection to Temporal (:%d) for the whole %s window after serving two tenant namespaces", workflowTemporalPort, connectionStabilityWindow)
	})
}

// startProbe starts a workerless probe execution in the tenant's own
// namespace. Teardown (terminate + close) is queued on the suite — NOT via
// s.T().Cleanup: this runs inside an s.Run stage, where testify has swapped
// s.T() to the stage's own subtest T, so a cleanup registered here would
// fire the moment the stage ends and terminate the probes while the later
// stages still assert on them (see tasks/lessons.md, 2026-07-15).
func (s *IsolationSuite) startProbe(t *tests.ProvisionedTenant, workflowID string) {
	ns := t.Tenant.ID
	wc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, ns, t.PAT)
	s.Require().NoError(err, "dial own namespace %s", ns)

	// Stamp pyck_tenant_id the way pyck services do when starting workflows:
	// the workflowExecutions resolver scopes every visibility query with
	// `pyck_tenant_id IN (<tenant>)` on top of the namespace, so a probe
	// without the attribute would be invisible through the service even in
	// its own namespace.
	_, err = wc.ExecuteWorkflow(s.Ctx, temporalclient.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: "temporal-isolation-probe",
		TypedSearchAttributes: temporalsdk.NewSearchAttributes(
			commonworkflow.PyckTenantID.ValueSet(ns),
		),
	}, "TemporalIsolationProbeWorkflow")
	s.Require().NoError(err, "start probe %s in namespace %s", workflowID, ns)

	s.deferProbeCleanup(func() {
		// Best-effort: the tenant (and namespace) is torn down by the suite
		// anyway; terminating just avoids leaving a pending task behind.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = wc.TerminateWorkflow(ctx, workflowID, "", "temporal-isolation suite cleanup")
		cancel()
		wc.Close()
	})
}

// startSpoofedProbe starts an execution in the tenant's own namespace with a
// FOREIGN tenant ID stamped into pyck_tenant_id, and reports whether the
// start was accepted. A rejected start means spoofing is blocked upstream —
// logged and treated as the property holding, not a failure.
func (s *IsolationSuite) startSpoofedProbe(t *tests.ProvisionedTenant, workflowID, foreignTenantID string) bool {
	ns := t.Tenant.ID
	wc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, ns, t.PAT)
	s.Require().NoError(err, "dial own namespace %s", ns)

	_, err = wc.ExecuteWorkflow(s.Ctx, temporalclient.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: "temporal-isolation-probe",
		TypedSearchAttributes: temporalsdk.NewSearchAttributes(
			commonworkflow.PyckTenantID.ValueSet(foreignTenantID),
		),
	}, "TemporalIsolationProbeWorkflow")
	if err != nil {
		wc.Close()
		s.T().Logf("spoofed start rejected upstream (also fine): %v", err)
		return false
	}

	s.deferProbeCleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = wc.TerminateWorkflow(ctx, workflowID, "", "temporal-isolation suite cleanup")
		cancel()
		wc.Close()
	})
	return true
}

// waitRawVisible polls raw Temporal visibility (public frontend, the
// tenant's own namespace, no tenant-attribute filter) until the execution is
// indexed — establishing existence independently of the service's scoped
// views.
func (s *IsolationSuite) waitRawVisible(t *tests.ProvisionedTenant, workflowID string) error {
	wc, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, t.Tenant.ID, t.PAT)
	if err != nil {
		return err
	}
	defer wc.Close()
	req := listRequest(t.Tenant.ID)
	req.Query = fmt.Sprintf("WorkflowId = %q", workflowID)
	return tests.PollUntil(s.Ctx, visibilityTimeout, 500*time.Millisecond, func() error {
		resp, listErr := wc.ListWorkflow(s.Ctx, req)
		if listErr != nil {
			return listErr
		}
		if len(resp.GetExecutions()) == 0 {
			return fmt.Errorf("execution %s not in raw visibility yet", workflowID)
		}
		return nil
	})
}

// waitExecutionVisible polls the gateway's workflowExecutions query with the
// given PAT until the execution with the given workflow ID shows up, and
// demands exactly one match — a duplicate would mean the service surfaced
// the same execution twice (e.g. a multi-namespace scan).
func (s *IsolationSuite) waitExecutionVisible(pat, workflowID string) error {
	return tests.PollUntil(s.Ctx, visibilityTimeout, 500*time.Millisecond, func() error {
		n, err := s.countExecutions(pat, workflowID)
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("execution %s: %d matches (want exactly 1)", workflowID, n)
		}
		return nil
	})
}

// listExecutionIDs queries workflowExecutions through the gateway with the
// given PAT and NO filter — the listing a real client renders — and returns
// the workflow IDs on the page. The suite's tenants are fresh, so the page
// comfortably holds everything in the namespace.
func (s *IsolationSuite) listExecutionIDs(pat string) ([]string, error) {
	first := 100
	resp, err := gateway.NewWorkflowClient(s.Cfg, pat).GetWorkflowExecutions(s.Ctx, workflowapi.GetWorkflowExecutionsArgs{
		First: &first,
	})
	if err != nil {
		return nil, err
	}
	conn := resp.GetWorkflowExecutions()
	if conn == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(conn.GetEdges()))
	for _, edge := range conn.GetEdges() {
		if edge == nil || edge.GetNode() == nil || edge.GetNode().GetExecution() == nil {
			continue
		}
		ids = append(ids, edge.GetNode().GetExecution().GetWorkflowID())
	}
	return ids, nil
}

// countExecutions queries workflowExecutions through the gateway with the
// given PAT, filtered to the exact workflow ID, and returns the match count.
// The tenant (and thus the Temporal namespace the service reads) is derived
// from the PAT — that derivation is the property under test.
func (s *IsolationSuite) countExecutions(pat, workflowID string) (int, error) {
	first := 5
	resp, err := gateway.NewWorkflowClient(s.Cfg, pat).GetWorkflowExecutions(s.Ctx, workflowapi.GetWorkflowExecutionsArgs{
		Where: &workflowmodel.WorkflowExecutionsWhereInput{WorkflowID: &workflowID},
		First: &first,
	})
	if err != nil {
		return 0, err
	}
	conn := resp.GetWorkflowExecutions()
	if conn == nil {
		return 0, nil
	}
	return len(conn.GetEdges()), nil
}

// workflowServiceTemporalConns counts the workflow container's established
// TCP connections to the internal Temporal frontend by reading the
// container's /proc net tables from the host (the image ships no shell, but
// with rootless docker the host user owns the container process, so
// /proc/<pid>/net is readable directly). Returns an error when the
// environment cannot support the observation (no docker CLI, remote daemon,
// non-Linux) — the caller skips rather than fails on those.
func workflowServiceTemporalConns() (int, error) {
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Pid}}", workflowContainer).Output()
	if err != nil {
		return 0, fmt.Errorf("docker inspect %s: %w", workflowContainer, err)
	}
	pid := strings.TrimSpace(string(out))

	count, tablesRead := 0, 0
	for _, table := range []string{"/proc/" + pid + "/net/tcp", "/proc/" + pid + "/net/tcp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			continue // tcp6 may be absent; both missing is caught below
		}
		tablesRead++
		count += countEstablishedTo(string(data), workflowTemporalPort)
	}
	if tablesRead == 0 {
		return 0, fmt.Errorf("no /proc net tables readable for pid %s (remote docker daemon?)", pid)
	}
	return count, nil
}

// countEstablishedTo counts rows of a /proc/net/tcp-format table that are
// ESTABLISHED (state 01) to the given remote port. Ports in those tables are
// four uppercase hex digits.
func countEstablishedTo(table string, port int) int {
	wantSuffix := fmt.Sprintf(":%04X", port)
	n := 0
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		// sl local_address rem_address st ...
		if len(fields) < 4 || !strings.HasSuffix(fields[2], wantSuffix) {
			continue
		}
		if fields[3] == "01" {
			n++
		}
	}
	return n
}
