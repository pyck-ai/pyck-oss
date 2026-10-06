//go:build integration

package workersubscriptions_test

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// WorkerSubscriptionsSuite provisions a fresh tenant with a writer and runs real
// workflowsdk worker processes against it.
type WorkerSubscriptionsSuite struct {
	tests.Base

	// workerBin is the compiled testworker, built once per suite run.
	workerBin string

	// Per-test state.
	dir      string
	tenant   string
	pat      string
	temporal temporalclient.Client
	procs    []*proc
	// workflows are the row IDs the test created, deleted again on teardown.
	workflows []string
}

func (s *WorkerSubscriptionsSuite) SetupSuite() {
	s.Base.SetupSuite()

	bin, err := buildWorker(s.T().TempDir())
	s.Require().NoError(err, "build testworker")
	s.workerBin = bin
}

// TearDownTest stops every worker first (so nothing re-registers), removes the
// workflow rows, then lets Base soft-delete the tenant. On failure it dumps each
// worker's output tail.
func (s *WorkerSubscriptionsSuite) TearDownTest() {
	for _, p := range s.procs {
		if s.T().Failed() {
			s.T().Logf("---- worker %s output (tail) ----\n%s", p.label, p.logTail(60))
		}
		if err := s.stopWorker(p); err != nil {
			s.T().Logf("cleanup: stop worker %s: %v", p.label, err)
		}
	}
	s.procs = nil

	if s.pat != "" {
		for _, id := range s.workflows {
			if err := s.gql(mutationDeleteWorkflow, map[string]any{"id": id}, nil); err != nil {
				s.T().Logf("cleanup: deleteWorkflow %s: %v (already gone is fine)", id, err)
			}
		}
	}
	s.workflows = nil

	if s.temporal != nil {
		s.temporal.Close()
		s.temporal = nil
	}

	s.Base.TearDownTest()
}

// TestWorkersDoNotDeleteEachOthersWorkflows is the #1564 scenario end to end:
// two worker types register on the same tenant, restart in turn, and keep each
// other's workflows, routing and subscriptions intact.
func (s *WorkerSubscriptionsSuite) TestWorkersDoNotDeleteEachOthersWorkflows() {
	r := s.Require()

	// Fresh tenant + writer, cleanup registered immediately.
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant")
	s.DeferTenantCleanup(rt.ID)
	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision writer")
	s.tenant, s.pat = rt.ID, p.PAT
	s.dir = s.T().TempDir()

	s.waitForNamespace()
	s.temporal = s.dialTemporal()

	nonce := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	kindA := workerKind{workflow: "WsA_" + nonce, queue: "ws-a-" + nonce}
	kindB := workerKind{workflow: "WsB_" + nonce, queue: "ws-b-" + nonce}

	var idA, idB string

	// 1. Start A; its workflow row appears.
	var a1 *proc
	if !s.Run("start worker A", func() {
		a1 = s.startWorker(kindA, "A1")
		s.waitReady(a1)
		idA = s.waitWorkflow(kindA.workflow)
		s.workflows = append(s.workflows, idA)
		s.T().Logf("%s id=%s", kindA.workflow, idA)
	}) {
		return
	}

	// 2. Start B. Its startup must not delete A's workflow (the old reconcile
	// removed every workflow missing from B's registry).
	var b1 *proc
	if !s.Run("start worker B keeps A's workflow", func() {
		b1 = s.startWorker(kindB, "B1")
		s.waitReady(b1)
		idB = s.waitWorkflow(kindB.workflow)
		s.workflows = append(s.workflows, idB)
		s.assertWorkflowKept(kindA.workflow, idA)
	}) {
		return
	}

	// 3. Restart A: same ids for both workflows.
	if !s.Run("restart worker A", func() {
		r := s.Require() // the subtest's own T, not the parent's
		r.NoError(s.stopWorker(a1), "worker A1 graceful exit")
		a1 = s.startWorker(kindA, "A1b")
		s.waitReady(a1)
		s.assertWorkflowKept(kindA.workflow, idA)
		s.assertWorkflowKept(kindB.workflow, idB)
	}) {
		return
	}

	// 4. Restart B: same ids for both workflows.
	if !s.Run("restart worker B", func() {
		r := s.Require() // the subtest's own T, not the parent's
		r.NoError(s.stopWorker(b1), "worker B1 graceful exit")
		b1 = s.startWorker(kindB, "B1b")
		s.waitReady(b1)
		s.assertWorkflowKept(kindA.workflow, idA)
		s.assertWorkflowKept(kindB.workflow, idB)
	}) {
		return
	}

	// 5. One event starts both workflows. Checked in Temporal (list by workflow
	// type in the tenant namespace) and through the gateway.
	if !s.Run("one event starts both workflows", func() {
		r := s.Require() // the subtest's own T, not the parent's
		baseA, err := s.executions(kindA.workflow)
		r.NoError(err)
		baseB, err := s.executions(kindB.workflow)
		r.NoError(err)

		s.createItem("ws-" + nonce + "-1")

		s.waitExecutionsAtLeast(kindA.workflow, baseA+1)
		s.waitExecutionsAtLeast(kindB.workflow, baseB+1)
	}) {
		return
	}

	// 6. A clean stop is a hint, decided per event by the router. Worker IDs and
	// stopped_at are not exposed by the API, so the suite proves it by routing,
	// telling the workers' subscriptions apart by their filter rule. A stopped
	// worker's rows stay until their TTL (an hour here, far beyond the test), so
	// every step below runs with them still present.
	//
	// 6a. Stop A's only running worker. Its rows are stopped, not deleted, and no
	// running worker is left on A, so they keep routing: the gap between a clean
	// stop and the replacement's first registration is bridged.
	if !s.Run("stopped last worker keeps routing", func() {
		r := s.Require() // the subtest's own T, not the parent's
		r.NoError(s.stopWorker(a1), "worker A1b graceful exit")
		s.assertWorkflowKept(kindA.workflow, idA)

		baseA, err := s.executions(kindA.workflow)
		r.NoError(err)
		s.createItem("ws-" + nonce + "-2")
		s.waitExecutionsAtLeast(kindA.workflow, baseA+1)
	}) {
		return
	}

	// 6b. A replacement of A registers with a rule that only matches its own
	// SKU (the rolling-deploy shape: the old version, now stopped, matched every
	// item). The old stopped rows must not route any more, so an event only they
	// would match starts nothing. B's execution on that same event is the
	// positive control that the router handled it; then the new version's own
	// SKU does route.
	s.Run("running replacement shadows the stopped worker", func() {
		r := s.Require() // the subtest's own T, not the parent's
		mySKU := "ws-" + nonce + "-v2"
		kindA2 := workerKind{workflow: kindA.workflow, queue: kindA.queue, filter: fmt.Sprintf("sku = %q", mySKU)}
		a2 := s.startWorker(kindA2, "A2")
		s.waitReady(a2)
		s.assertWorkflowKept(kindA.workflow, idA)

		baseA, err := s.executions(kindA.workflow)
		r.NoError(err)
		baseB, err := s.executions(kindB.workflow)
		r.NoError(err)

		// Only the stopped worker's unfiltered subscription matches this event.
		s.createItem("ws-" + nonce + "-3")
		s.waitExecutionsAtLeast(kindB.workflow, baseB+1)
		s.assertNoNewExecutions(kindA.workflow, baseA, stableWindow)

		// The running replacement still routes its own SKU.
		s.createItem(mySKU)
		s.waitExecutionsAtLeast(kindA.workflow, baseA+1)
	})
}
