//go:build integration

package eventdelivery_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	workflowapi "github.com/pyck-ai/pyck/backend/workflow/api"
	workflowmodel "github.com/pyck-ai/pyck/backend/workflow/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/internal/workerproc"
	"github.com/pyck-ai/pyck/tests/integration/tests"
	"github.com/pyck-ai/pyck/tests/integration/tests/event-delivery/observer"
)

const (
	// stepTimeout bounds every wait in the scenarios. The slowest link is
	// Temporal's visibility index, which lags by a second or two.
	stepTimeout  = 60 * time.Second
	pollInterval = time.Second

	// stopTimeout is how long a worker gets to exit after SIGTERM before it is
	// killed: the SDK waits up to PYCK_WORKER_UNREGISTER_TIMEOUT (5s) for the
	// unregister call, then stops the Temporal workers.
	stopTimeout = 30 * time.Second

	workerPkg = "github.com/pyck-ai/pyck/tests/integration/tests/event-delivery/testworker"
)

// DeliverySuite provisions a fresh tenant with a writer per test method and
// runs real workflowsdk worker processes against it.
type DeliverySuite struct {
	tests.Base

	// workerBin is the compiled testworker, built once per suite run.
	workerBin string

	// Per-test state.
	dir      string
	tenant   string
	pat      string
	temporal temporalclient.Client
	inv      inventoryapi.Client
	wfc      workflowapi.Client
	procs    []*workerproc.Proc
	// workflows are the workflow row IDs and executions the workflow IDs the
	// test created, removed again on teardown.
	workflows  []string
	executions []string
}

func (s *DeliverySuite) SetupSuite() {
	s.Base.SetupSuite()

	bin, err := workerproc.Build(workerPkg, s.T().TempDir())
	s.Require().NoError(err, "build testworker")
	s.workerBin = bin
}

// TearDownTest stops every worker first (so nothing re-registers), removes the
// workflow rows and executions, then lets Base soft-delete the tenant. On
// failure it dumps each worker's output tail.
func (s *DeliverySuite) TearDownTest() {
	for _, p := range s.procs {
		if s.T().Failed() {
			s.T().Logf("---- worker %s output (tail) ----\n%s", p.Label, p.LogTail(60))
		}

		if err := s.stopWorker(p); err != nil {
			s.T().Logf("cleanup: stop worker %s: %v", p.Label, err)
		}
	}

	s.procs = nil

	if s.temporal != nil {
		for _, id := range s.executions {
			if err := s.temporal.TerminateWorkflow(s.Ctx, id, "", "test cleanup"); err != nil {
				s.T().Logf("cleanup: terminate %s: %v (already closed is fine)", id, err)
			}
		}

		s.temporal.Close()
		s.temporal = nil
	}

	s.executions = nil

	if s.wfc != nil {
		for _, id := range s.workflows {
			if _, err := s.wfc.DeleteWorkflow(s.Ctx, workflowapi.DeleteWorkflowArgs{Id: id}); err != nil {
				s.T().Logf("cleanup: deleteWorkflow %s: %v (already gone is fine)", id, err)
			}
		}
	}

	s.workflows = nil

	s.Base.TearDownTest()
}

// provision registers a fresh tenant with a writer, waits for its Temporal
// namespace and opens the clients the test uses.
func (s *DeliverySuite) provision() {
	r := s.Require()

	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant")
	s.DeferTenantCleanup(rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision writer")

	s.tenant, s.pat = rt.ID, p.PAT
	s.dir = s.T().TempDir()
	s.inv = gateway.NewInventoryClientForTenant(s.Cfg, s.pat, s.tenant)
	s.wfc = gateway.NewWorkflowClientForTenant(s.Cfg, s.pat, s.tenant)

	s.waitForNamespace()

	s.temporal, err = temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, s.tenant, s.pat)
	r.NoError(err, "dial temporal")
}

// waitForNamespace polls until the tenant's Temporal namespace exists; the
// register-tenant workflow creates it asynchronously.
func (s *DeliverySuite) waitForNamespace() {
	nc, err := temporal.NewNamespaceClient(s.Cfg.TemporalAddress, s.pat)
	s.Require().NoError(err)

	defer nc.Close()

	err = tests.PollUntil(s.Ctx, stepTimeout, 500*time.Millisecond, func() error {
		if _, err := nc.Describe(s.Ctx, s.tenant); err != nil {
			return fmt.Errorf("describe namespace %q: %w", s.tenant, err)
		}

		return nil
	})
	s.Require().NoError(err, "tenant namespace never became ready")
}

// poll is tests.PollUntil with the suite's bounds, aborting when a worker died.
func (s *DeliverySuite) poll(fn func() error) error {
	return tests.PollUntil(s.Ctx, stepTimeout, pollInterval, func() error {
		for _, p := range s.procs {
			if err := p.CrashedError(); err != nil {
				return err
			}
		}

		return fn()
	})
}

// ---------------------------------------------------------------------------
// Workers
// ---------------------------------------------------------------------------

// workerKind is one workflow: its name and the task queue it is served on.
// Names are unique per tenant and the queue is immutable, so every worker of a
// kind uses the same pair.
type workerKind struct {
	workflow string
	queue    string
}

// newKind returns a fresh workflow name and task queue for one test.
func (s *DeliverySuite) newKind(label string) workerKind {
	nonce := strings.ReplaceAll(uuid.NewString()[:8], "-", "")

	return workerKind{workflow: "Ed" + label + "_" + nonce, queue: "ed-" + strings.ToLower(label) + "-" + nonce}
}

// startWorker launches one worker process of the given kind. start adds a start
// subscription on item create; signals adds an intermediate subscription per
// name on item update. The whole configuration is env: the SDK's own variables
// plus ED_* for testworker.
func (s *DeliverySuite) startWorker(k workerKind, label string, start bool, signals ...string) *workerproc.Proc {
	s.T().Helper()

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		// HOME is the test dir so no user-level Temporal profile is picked up by
		// the SDK env config.
		"HOME=" + s.dir,
		"PYCK_ENV=integration-test",
		"PYCK_LOG_FORMAT=json",
		// SDK -> workflow service, through the gateway, as the tenant's writer.
		"PYCK_GATEWAY_URL=" + s.Cfg.GatewayURL,
		"PYCK_API_TOKEN=" + s.pat,
		"PYCK_API_TENANT_ID=" + s.tenant,
		// SDK -> Temporal: the public frontend takes the PAT as API key over
		// plain TCP; the namespace is the tenant ID.
		"TEMPORAL_ADDRESS=" + s.Cfg.TemporalAddress,
		"TEMPORAL_NAMESPACE=" + s.tenant,
		"TEMPORAL_API_KEY=" + s.pat,
		"TEMPORAL_TLS=false",
		"PYCK_WORKER_REGISTRATION_HEARTBEAT_INTERVAL=5s",
		"PYCK_WORKER_UNREGISTER_ON_STOP=true",
		"PYCK_WORKER_UNREGISTER_TIMEOUT=5s",
		"ED_WORKFLOW=" + k.workflow,
		"ED_TASK_QUEUE=" + k.queue,
		"ED_TENANT_ID=" + s.tenant,
		fmt.Sprintf("ED_START=%t", start),
		"ED_SIGNALS=" + strings.Join(signals, ","),
	}

	p, err := workerproc.Start(s.Ctx, s.workerBin, env, filepath.Join(s.dir, label+".log"), label)
	s.Require().NoError(err)

	s.procs = append(s.procs, p)
	s.T().Logf("started worker %s (pid %d): workflow=%s queue=%s start=%t signals=%v log=%s",
		label, p.Pid(), k.workflow, k.queue, start, signals, p.LogPath())

	return p
}

func (s *DeliverySuite) stopWorker(p *workerproc.Proc) error { return p.Stop(stopTimeout) }

// waitReady polls until the worker logged "worker started", which
// RunDefaultWorker prints only after Start returned, i.e. after the worker
// registered its workflows and signals and began polling.
func (s *DeliverySuite) waitReady(p *workerproc.Proc) {
	s.T().Helper()

	err := s.poll(func() error {
		ok, err := p.LogContains("worker started")
		if err != nil {
			return err
		}

		if !ok {
			return fmt.Errorf("worker %s not started yet", p.Label)
		}

		return nil
	})
	s.Require().NoError(err, "worker %s never became ready:\n%s", p.Label, p.LogTail(30))
}

// ---------------------------------------------------------------------------
// Workflow rows and subscriptions (through the gateway)
// ---------------------------------------------------------------------------

// workflowIDs returns the IDs of the tenant's live workflow rows with this name.
func (s *DeliverySuite) workflowIDs(name string) ([]string, error) {
	resp, err := s.wfc.GetWorkflows(s.Ctx, workflowapi.GetWorkflowsArgs{Where: &workflowapi.WorkflowWhereInput{Name: &name}})
	if err != nil {
		return nil, err
	}

	var ids []string

	for _, e := range resp.GetWorkflows().GetEdges() {
		if e != nil && e.GetNode() != nil {
			ids = append(ids, e.GetNode().GetID())
		}
	}

	return ids, nil
}

// waitWorkflow polls until exactly one workflow row with this name exists and
// returns its ID, scheduling the row for deletion on teardown.
func (s *DeliverySuite) waitWorkflow(name string) string {
	var id string

	err := s.poll(func() error {
		ids, err := s.workflowIDs(name)
		if err != nil {
			return err
		}

		if len(ids) != 1 {
			return fmt.Errorf("workflow %q: want exactly 1 row, have %d", name, len(ids))
		}

		id = ids[0]

		return nil
	})
	s.Require().NoError(err, "workflow %q never appeared", name)

	s.workflows = append(s.workflows, id)

	return id
}

// assertWorkflowKept fails unless the one workflow row with this name still has
// the given ID.
func (s *DeliverySuite) assertWorkflowKept(name, wantID string) {
	s.T().Helper()

	ids, err := s.workflowIDs(name)
	s.Require().NoError(err)
	s.Require().Equal([]string{wantID}, ids, "workflow %q must keep its row (same id)", name)
}

// signalRow is one live subscription row of a workflow.
type signalRow struct {
	typ    string
	signal string
}

func (r signalRow) String() string { return r.typ + ":" + r.signal }

func errRows(msg string, rows []signalRow) error {
	return fmt.Errorf("%s: have %v", msg, rows)
}

// waitSignalRows polls until check accepts the workflow's live subscription rows.
func (s *DeliverySuite) waitSignalRows(workflowID string, check func([]signalRow) error) {
	s.T().Helper()

	err := s.poll(func() error {
		resp, err := s.wfc.GetWorkflowSignals(s.Ctx, workflowapi.GetWorkflowSignalsArgs{
			Where: &workflowapi.WorkflowSignalWhereInput{WorkflowID: &workflowID},
		})
		if err != nil {
			return err
		}

		var rows []signalRow

		for _, e := range resp.GetWorkflowSignals().GetEdges() {
			if e == nil || e.GetNode() == nil || e.GetNode().GetDeletedAt() != nil {
				continue
			}

			n := e.GetNode()
			row := signalRow{typ: fmt.Sprint(n.GetTemporalSignalType())}

			if n.GetTemporalSignal() != nil {
				row.signal = *n.GetTemporalSignal()
			}

			rows = append(rows, row)
		}

		return check(rows)
	})
	s.Require().NoError(err)
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// mutated is what an inventory mutation returns: the handle to its events.
type mutated struct {
	itemID        string
	transactionID string
	eventCount    int
}

// createItem creates an inventory item and returns the mutation's handle.
func (s *DeliverySuite) createItem(label string) mutated {
	s.T().Helper()

	sku := "ed-" + strings.ToLower(label) + "-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")

	resp, err := s.inv.CreateInventoryItem(s.Ctx, inventoryapi.CreateInventoryItemArgs{
		Input: inventoryapi.CreateInventoryItemInput{Sku: sku},
	})
	s.Require().NoError(err, "createInventoryItem")

	out := resp.GetCreateInventoryItem()
	s.Require().NotNil(out)
	s.Require().NotNil(out.GetInventoryItem(), "createInventoryItem returned no item")

	return mutated{itemID: out.GetInventoryItem().GetID(), transactionID: out.GetTransactionID(), eventCount: out.GetEventCount()}
}

// updateItem renames an inventory item, which publishes one update event.
func (s *DeliverySuite) updateItem(itemID, sku string) mutated {
	s.T().Helper()

	sku += "-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")

	resp, err := s.inv.UpdateInventoryItem(s.Ctx, inventoryapi.UpdateInventoryItemArgs{
		Id:    itemID,
		Input: inventoryapi.UpdateInventoryItemInput{Sku: &sku},
	})
	s.Require().NoError(err, "updateInventoryItem")

	out := resp.GetUpdateInventoryItem()
	s.Require().NotNil(out)

	return mutated{itemID: itemID, transactionID: out.GetTransactionID(), eventCount: out.GetEventCount()}
}

// ---------------------------------------------------------------------------
// Routing status
// ---------------------------------------------------------------------------

// routingTarget and routingEntry are the parts of transactionRouting the suite
// checks, in plain strings.
type routingTarget struct {
	Kind       string
	Workflow   string
	WorkflowID string
	RunID      string
	Signal     string
}

type routingEntry struct {
	EventID string
	Outcome string
	Targets []routingTarget
}

func (s *DeliverySuite) routing(transactionID string) ([]routingEntry, error) {
	resp, err := s.wfc.GetTransactionRouting(s.Ctx, workflowapi.GetTransactionRoutingArgs{TransactionID: transactionID})
	if err != nil {
		return nil, err
	}

	var out []routingEntry

	for _, e := range resp.GetTransactionRouting().GetEntries() {
		if e == nil {
			continue
		}

		// The client is built with ParseDataAlongWithErrors, under which gqlgenc
		// swallows a response it could not fully decode: the error is dropped
		// and the fields after the failing one stay at their zero value (a
		// number the client typed as a string once hid every entry's targets).
		// A real entry always has its tenant and recording time, so their
		// absence means a partial decode and must not read as "no targets".
		if e.GetTenantID() == "" || e.GetRecordedAt().IsZero() {
			return nil, fmt.Errorf("routing entry %q was only partly decoded by the GraphQL client (tenant %q, recordedAt %v): %+v",
				e.GetEventID(), e.GetTenantID(), e.GetRecordedAt(), e)
		}

		entry := routingEntry{EventID: e.GetEventID(), Outcome: string(*e.GetOutcome())}

		for _, t := range e.GetTargets() {
			if t == nil {
				continue
			}

			entry.Targets = append(entry.Targets, routingTarget{
				Kind:       string(*t.GetKind()),
				Workflow:   deref(t.GetWorkflow()),
				WorkflowID: deref(t.GetWorkflowID()),
				RunID:      deref(t.GetRunID()),
				Signal:     deref(t.GetSignal()),
			})
		}

		out = append(out, entry)
	}

	return out, nil
}

// waitRouted polls transactionRouting until the transaction has eventCount
// entries, then requires that there are exactly that many and all are DONE.
// That is the "routing is complete" rule the query documents: fewer entries
// means not routed yet, so the poll keeps asking.
func (s *DeliverySuite) waitRouted(m mutated) []routingEntry {
	s.T().Helper()

	var entries []routingEntry

	err := s.poll(func() error {
		var err error

		entries, err = s.routing(m.transactionID)
		if err != nil {
			return err
		}

		if len(entries) < m.eventCount {
			return fmt.Errorf("transaction %s: %d of %d events routed", m.transactionID, len(entries), m.eventCount)
		}

		return nil
	})
	s.Require().NoError(err, "routing never completed for transaction %s (eventCount %d)", m.transactionID, m.eventCount)

	s.Require().Len(entries, m.eventCount, "one routing entry per published event")

	for _, e := range entries {
		s.Require().Equal(string(workflowmodel.RoutingOutcomeDone), e.Outcome, "event %s: %+v", e.EventID, e)
	}

	return entries
}

// located is a routing target together with the entry that holds it.
type located struct {
	entry  routingEntry
	target routingTarget
}

func targetsOfKind(entries []routingEntry, kind workflowmodel.RoutingTargetKind) []located {
	var out []located

	for _, e := range entries {
		for _, t := range e.Targets {
			if t.Kind == string(kind) {
				out = append(out, located{entry: e, target: t})
			}
		}
	}

	return out
}

func startedTargets(entries []routingEntry) []located {
	return targetsOfKind(entries, workflowmodel.RoutingTargetKindStarted)
}

func signalledTargets(entries []routingEntry) []located {
	return targetsOfKind(entries, workflowmodel.RoutingTargetKindSignalled)
}

// ---------------------------------------------------------------------------
// Executions
// ---------------------------------------------------------------------------

// waitListedByTransaction polls the gateway's workflowExecutions lookup by
// transaction ID until it lists the execution with this workflow ID.
func (s *DeliverySuite) waitListedByTransaction(transactionID, workflowID string) {
	s.T().Helper()

	txID, err := uuid.Parse(transactionID)
	s.Require().NoError(err)

	err = s.poll(func() error {
		resp, err := s.wfc.GetWorkflowExecutions(s.Ctx, workflowapi.GetWorkflowExecutionsArgs{
			Where: &workflowmodel.WorkflowExecutionsWhereInput{TransactionID: &txID},
		})
		if err != nil {
			return err
		}

		var seen []string

		for _, e := range resp.GetWorkflowExecutions().GetEdges() {
			if e == nil || e.GetNode() == nil {
				continue
			}

			id := e.GetNode().GetExecution().GetWorkflowID()
			if id == workflowID {
				return nil
			}

			seen = append(seen, id)
		}

		return fmt.Errorf("workflowExecutions(transactionID %s) lists %v, want %s", transactionID, seen, workflowID)
	})
	s.Require().NoError(err)
}

// waitRunning polls Temporal for a running execution of the workflow, with
// the query the router's signal fan-out uses to find its targets: an update
// routed before visibility lists the execution would find nothing to signal
// and be settled as dropped, never retried.
func (s *DeliverySuite) waitRunning(k workerKind) {
	s.T().Helper()

	query := fmt.Sprintf("CloseTime is null AND TaskQueue = %q AND pyck_workflow_name = %q", k.queue, k.workflow)

	err := s.poll(func() error {
		resp, err := s.temporal.ListWorkflow(s.Ctx, &workflowservice.ListWorkflowExecutionsRequest{Query: query})
		if err != nil {
			return err
		}

		if len(resp.GetExecutions()) == 0 {
			return fmt.Errorf("no running %s execution visible yet", k.workflow)
		}

		return nil
	})
	s.Require().NoError(err)
}

// observed queries the workflow for what it saw.
func (s *DeliverySuite) observed(workflowID, runID string) (observer.Observed, error) {
	var out observer.Observed

	ctx, cancel := context.WithTimeout(s.Ctx, 30*time.Second)
	defer cancel()

	v, err := s.temporal.QueryWorkflow(ctx, workflowID, runID, observer.QueryObserved)
	if err != nil {
		return out, err
	}

	return out, v.Get(&out)
}

// waitObserved polls the workflow's query until done accepts what it saw. The
// query needs a worker to answer, so it also proves one picked the execution up.
func (s *DeliverySuite) waitObserved(workflowID, runID string, done func(observer.Observed) bool) observer.Observed {
	s.T().Helper()

	var obs observer.Observed

	err := s.poll(func() error {
		var err error

		obs, err = s.observed(workflowID, runID)
		if err != nil {
			return err
		}

		if !done(obs) {
			return fmt.Errorf("workflow has not seen everything yet: %+v", obs)
		}

		return nil
	})
	s.Require().NoError(err, "workflow %s never reported what it saw", workflowID)

	return obs
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
