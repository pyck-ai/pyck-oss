//go:build integration

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/events"
	workflowapi "github.com/pyck-ai/pyck/backend/workflow/api"
	workflowsignalent "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	workflowmodel "github.com/pyck-ai/pyck/backend/workflow/model"
	"github.com/pyck-ai/pyck/backend/workflowsdk"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// signalServiceName / signalOperationName are the parts of the NATS
// topic that stay constant across every test in this package — they
// identify the event as an integration-test create event. Only the
// schema name varies per test (see wfIdentity), so two tests running
// against the same tenant don't see each other's events.
const (
	signalServiceName   = "integration"
	signalOperationName = "create"
)

// wfIdentity bundles the names that must be unique per workflow
// integration test so concurrently-running tests don't share a
// Temporal workflow type, a task queue, a pyck workflow registration,
// or — most importantly — the NATS topic the signalrouter listens on.
//
// SignalSchema lives in the NATS subject; the signalrouter reads the
// inbound subject, matches it against each registered workflow's
// NatsTopic, and starts only the workflows whose schema matches.
type wfIdentity struct {
	WorkflowName string // Temporal workflow type registered with the worker
	TaskQueue    string // worker's task queue, also stored on the pyck row
	SignalSchema string // varying part of the NATS topic
}

// newWfIdentity builds a fresh identity scoped to a test. The name is
// the test's logical identifier (e.g. "WfIsAssignable", "WfTargets"),
// used as a human-readable prefix on the workflow type, task queue,
// and signal schema. A random letters-only suffix guards against
// collisions when the same test runs multiple times against shared
// state (e.g. a long-lived dev stack).
//
// The schema is lowercased and any non-alphanumeric characters in the
// name are stripped for two reasons: (1) the signalrouter's topic
// escape rewrites non-alphanumerics to dashes, which would silently
// change the matched topic; (2) NATS subject tokens are easier to
// reason about when we know exactly what they look like.
func newWfIdentity(name string) wfIdentity {
	suffix := strings.ToLower(gofakeit.LetterN(8))
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return -1
		}
	}, name)
	if safe == "" {
		safe = "wf"
	}
	return wfIdentity{
		WorkflowName: name + "_" + suffix,
		TaskQueue:    "integrations-" + safe + "-" + suffix,
		SignalSchema: safe + "-" + suffix,
	}
}

// wfLifecycle threads the state shared by the lifecycle stages every
// suite in this package runs: provision a tenant identity, register
// the workflow with pyck, host a worker, trigger a run over NATS,
// prove the run survives a worker outage, cancel it, and unregister.
// The SA-specific stages stay inline in each test and read the fields
// this struct exposes (pat, worker, workflowID, runID).
type wfLifecycle struct {
	base *tests.Base
	id   wfIdentity
	wf   any // keepOpenAwait instantiation hosting this suite's state type

	registered     *gateway.RegisteredTenant
	pat            string
	worker         *wfWorker
	workflowID     string
	runID          string
	pyckWorkflowID string
}

// newWfLifecycle builds the per-test identity for the given logical
// name and binds the workflow function every worker in the lifecycle
// will host.
func newWfLifecycle(base *tests.Base, name string, wf any) *wfLifecycle {
	id := newWfIdentity(name)
	base.T().Logf("workflow=%s taskQueue=%s schema=%s",
		id.WorkflowName, id.TaskQueue, id.SignalSchema)
	return &wfLifecycle{base: base, id: id, wf: wf}
}

// provisionWorkerIdentity registers a fresh tenant with a machine user
// and a writer PAT — the tenant-scoped principal the worker dials
// pyck-temporal as — then waits for the per-tenant Temporal namespace.
// Tenant teardown is registered as a test cleanup right after
// registration succeeds, so it runs even when a later stage fails and
// short-circuits the lifecycle.
func (l *wfLifecycle) provisionWorkerIdentity() bool {
	if !l.base.Run("register tenant and provision worker identity", func() {
		r := l.base.Require()
		rt, err := gateway.RegisterTenant(l.base.Ctx, l.base.Cfg, fixtures.NewTenant())
		r.NoError(err)
		l.registered = rt
		l.base.DeferTenantCleanup(rt.ID)

		p, err := tests.ProvisionUserInTenant(l.base.Ctx, l.base.Cfg, l.base.ZConn, rt, []string{"writer"})
		r.NoError(err)
		l.pat = p.PAT
		l.base.T().Logf("tenantID=%s userID=%s", rt.ID, p.UserID)
	}) {
		return false
	}

	// register-tenant runs an async Temporal workflow that creates the
	// per-tenant namespace. Wait for it to become describable before
	// the worker tries to dial — otherwise the dial fails with a
	// generic "namespace not found" that looks like an auth bug.
	return l.base.Run("Temporal namespace becomes describable", func() {
		r := l.base.Require()
		took, err := waitForNamespace(l.base.Ctx, l.base.Cfg.TemporalAddress, l.registered.ID, l.pat, 30*time.Second)
		r.NoError(err)
		l.base.T().Logf("namespace ready after %s", took.Round(time.Millisecond))
	})
}

// startLifecycle registers the workflow with pyck, builds and starts
// the worker, and triggers one execution over NATS. On success
// l.worker is polling and l.workflowID/l.runID identify a RUNNING
// execution blocked on Await, ready for the SA-specific stages.
func (l *wfLifecycle) startLifecycle() bool {
	t := l.base.T()

	// Tell pyck about the workflow. This is the same call the
	// workflowsdk worker makes on Start, broken out as its own phase
	// so register / unregister are observable independently of worker
	// lifecycle. Without this, the workflow still runs in Temporal,
	// but it doesn't show up in pyck's workflow listings and can't be
	// triggered via the NATS signal-router path.
	if !l.base.Run("register workflow with pyck", func() {
		r := l.base.Require()
		rowID, err := registerWithPyck(l.base.Ctx, l.base.Cfg, l.pat, l.id)
		r.NoError(err)
		r.NotEmpty(rowID)
		l.pyckWorkflowID = rowID
		l.base.T().Logf("pyck workflow id=%s", l.pyckWorkflowID)
	}) {
		return false
	}

	// Whichever worker is live when the test ends (the initial one, or
	// the replacement from reRegisterAndCancel) gets stopped exactly
	// once; stages that stop a worker themselves set l.worker to nil.
	t.Cleanup(func() {
		if l.worker != nil {
			l.worker.Stop()
			l.worker = nil
		}
	})

	// Construct the worker (dial Temporal, register the workflow) and
	// start polling. After this phase the namespace has a live worker
	// for the test's task queue.
	if !l.base.Run("register and start worker", func() {
		r := l.base.Require()
		w, err := newWfWorker(l.base.Ctx, l.id, l.base.Cfg.TemporalAddress, l.registered.ID, l.pat, l.wf)
		r.NoError(err)
		// Assign before Start so the cleanup above closes the client
		// even when Start fails.
		l.worker = w
		r.NoError(w.Start())
	}) {
		return false
	}

	// Trigger a workflow run via NATS, exercising the full client
	// path — same as myworkflow/cmd/trigger publishes:
	//   nats.Request → pyck-workflow signalrouter → SignalWithStartWorkflow
	// → pyck-temporal → our in-process worker picks up the task →
	// the test workflow runs and blocks on Await.
	//
	// The signalrouter constructs the workflow ID as
	// "<workflowName>_<entityID>", so the entity UUID we publish
	// determines the resulting workflow ID; we capture both via the
	// router's reply payload.
	return l.base.Run("trigger workflow via NATS", func() {
		r := l.base.Require()
		entityID, wfID, rID, err := publishWfTrigger(l.base.Ctx, l.base.Cfg, l.id, l.registered.ID)
		r.NoError(err)
		l.workflowID = wfID
		l.runID = rID
		l.base.T().Logf("trigger entity=%s workflowID=%s runID=%s", entityID, l.workflowID, l.runID)

		took, err := waitForWorkflowStatus(l.base.Ctx, l.worker.client, l.workflowID, l.runID, enums.WORKFLOW_EXECUTION_STATUS_RUNNING, 10*time.Second)
		r.NoError(err)
		l.base.T().Logf("workflow %s RUNNING after %s", l.workflowID, took.Round(time.Millisecond))
	})
}

// assertStaysRunningWithoutWorker stops the worker and verifies the
// execution stays RUNNING: no one is polling the task queue to advance
// it, but Temporal itself doesn't notice — from its perspective the
// workflow is just blocked. The probe is a fresh client with the
// tenant PAT so the assertion doesn't depend on the stopped worker's
// client.
func (l *wfLifecycle) assertStaysRunningWithoutWorker() bool {
	return l.base.Run("stop worker; execution stays running", func() {
		r := l.base.Require()
		l.worker.Stop()
		l.worker = nil

		probe, err := temporal.Dial(l.base.Ctx, l.base.Cfg.TemporalAddress, l.registered.ID, l.pat)
		r.NoError(err)
		defer probe.Close()

		desc, err := probe.DescribeWorkflowExecution(l.base.Ctx, l.workflowID, l.runID)
		r.NoError(err)
		r.Equal(enums.WORKFLOW_EXECUTION_STATUS_RUNNING, desc.GetWorkflowExecutionInfo().GetStatus(),
			"execution should remain RUNNING with no worker polling")
	})
}

// reRegisterAndCancel builds a fresh worker, starts it, and has it
// process a cancel. This is the "register/unregister" round-trip —
// proves a tenant can swap workers in and out without recreating
// anything in pyck or Temporal.
func (l *wfLifecycle) reRegisterAndCancel() bool {
	return l.base.Run("re-register worker and cancel", func() {
		r := l.base.Require()
		w, err := newWfWorker(l.base.Ctx, l.id, l.base.Cfg.TemporalAddress, l.registered.ID, l.pat, l.wf)
		r.NoError(err)
		l.worker = w
		r.NoError(w.Start())

		// CancelWorkflow only records the cancel intent; the running
		// worker has to pick it up before the execution actually
		// transitions. Hence the wait below.
		r.NoError(w.client.CancelWorkflow(l.base.Ctx, l.workflowID, l.runID))

		took, err := waitForWorkflowStatus(l.base.Ctx, w.client, l.workflowID, l.runID, enums.WORKFLOW_EXECUTION_STATUS_CANCELED, 10*time.Second)
		r.NoError(err)
		l.base.T().Logf("workflow %s CANCELED after %s", l.workflowID, took.Round(time.Millisecond))
	})
}

// unregisterFromPyck deletes the previously-registered pyck workflow
// row — the mirror of "register workflow with pyck" — so the tenant
// doesn't keep a dangling registration after cleanup.
func (l *wfLifecycle) unregisterFromPyck() bool {
	return l.base.Run("unregister workflow from pyck", func() {
		r := l.base.Require()
		r.NoError(unregisterWithPyck(l.base.Ctx, l.base.Cfg, l.pat, l.pyckWorkflowID))
	})
}

// wfWorker bundles a Temporal client + worker for one tenant
// namespace. Constructed once per phase that needs the worker running;
// Stop() releases both the worker and the underlying gRPC connection.
type wfWorker struct {
	client temporalclient.Client
	worker temporalworker.Worker
}

// newWfWorker dials Temporal as the tenant (PAT used as API key) and
// registers the supplied workflow function under the per-test
// identity. wf is registered with the Temporal SDK exactly as if a
// workflowsdk-driven worker had registered it — the SDK keys workflow
// types by the Name option, so id.WorkflowName is what the
// signalrouter must store on the pyck row.
//
// Does NOT start polling — call Start() separately so the test can
// observe the "registered but inactive" state if it wants to.
func newWfWorker(ctx context.Context, id wfIdentity, address, namespace, apiKey string, wf any) (*wfWorker, error) {
	c, err := temporal.Dial(ctx, address, namespace, apiKey)
	if err != nil {
		return nil, err
	}

	w := temporalworker.New(c, id.TaskQueue, temporalworker.Options{})
	w.RegisterWorkflowWithOptions(wf, temporalworkflow.RegisterOptions{
		Name: id.WorkflowName,
	})

	return &wfWorker{client: c, worker: w}, nil
}

// Start begins polling the task queue. Idempotent in the sense that
// the underlying worker.Start() is one-shot; callers should pair every
// successful Start with a Stop.
func (kw *wfWorker) Start() error {
	if err := kw.worker.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	return nil
}

// Stop shuts the worker down and closes the underlying client. After
// Stop, the worker cannot be started again — construct a new wfWorker
// if you need to re-register.
func (kw *wfWorker) Stop() {
	kw.worker.Stop()
	kw.client.Close()
}

// signalTopicString is the topic pattern stored on the workflow row
// (NatsTopic) — derived once per identity and reused for both
// registration (where it acts as the match pattern, with empty
// TenantID/EntityID becoming "*" wildcards) and trigger-subject
// construction (where the same fields are filled in with concrete
// values).
func signalTopicString(id wfIdentity) string {
	return events.MutationEventWithReplyTopic{
		ServiceName:   signalServiceName,
		SchemaName:    id.SignalSchema,
		OperationName: signalOperationName,
	}.String()
}

// triggerSubject builds the concrete NATS subject the publisher uses
// for one workflow trigger. The signalrouter parses incoming subjects
// of this exact shape and matches them against the registered
// signalTopicString(id).
func triggerSubject(stream string, id wfIdentity, tenantID, entityID uuid.UUID) string {
	return events.MutationEventWithReplyTopic{
		StreamName:    stream,
		TenantID:      tenantID,
		ServiceName:   signalServiceName,
		SchemaName:    id.SignalSchema,
		EntityID:      entityID,
		OperationName: signalOperationName,
	}.String()
}

// publishWfTrigger publishes a MutationEventWithReplyTopic event
// matching the workflow's start signal, using nats.Request so we get
// the signalrouter's reply (the workflow ID + run ID). Mirrors what
// myworkflow/cmd/trigger does, except scoped to the integration test's
// service/schema/operation tuple.
//
// Returns the workflow ID and run ID extracted from the router's
// response so the caller can poll Temporal for status. tenantID is
// the management tenant row ID (string form of the per-tenant UUID).
func publishWfTrigger(ctx context.Context, cfg *config.Config, id wfIdentity, tenantID string) (entityID uuid.UUID, workflowID, runID string, err error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("parse tenant id %q: %w", tenantID, err)
	}

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()

	entityID = uuid.New()
	subject := triggerSubject(cfg.NatsStream, id, tenantUUID, entityID)

	payload, err := json.Marshal(events.MutationEventMessage{
		Service:   signalServiceName,
		Type:      id.WorkflowName,
		Schema:    id.SignalSchema,
		Operation: signalOperationName,
		ID:        entityID,
		TenantID:  tenantUUID,
		DataAfter: map[string]any{
			"id":   entityID,
			"name": "integration-" + id.SignalSchema,
		},
		// Must be supplied by the publisher — the pyck signalrouter
		// only propagates attributes listed here as Temporal typed
		// search attributes. Without pyck_tenant_id the execution
		// won't be visible via the workflowExecutions resolver, even
		// though it is running in Temporal.
		WfSearchAttributes: map[string]string{
			"pyck_tenant_id": tenantUUID.String(),
			"pyck_data_id":   entityID.String(),
		},
	})
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("marshal event: %w", err)
	}

	reply, err := nc.RequestWithContext(ctx, subject, payload)
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("nats request %s: %w", subject, err)
	}

	// The signalrouter replies with {data: [{type,id,runID},...], success}.
	// One trigger event can fan out to multiple workflows, but the
	// integration test only registers one — so we expect a single entry.
	var envelope routerReplyEnvelope
	if err := json.Unmarshal(reply.Data, &envelope); err != nil {
		return uuid.Nil, "", "", fmt.Errorf("decode router reply (%s): %w", string(reply.Data), err)
	}
	if !envelope.Success {
		return uuid.Nil, "", "", fmt.Errorf("router reply success=false: %s", string(reply.Data))
	}
	if len(envelope.Data) == 0 {
		return uuid.Nil, "", "", fmt.Errorf("router reply contained no workflows: %s", string(reply.Data))
	}
	if envelope.Data[0].ID == "" {
		return uuid.Nil, "", "", fmt.Errorf("router reply missing workflow id: %s", string(reply.Data))
	}

	return entityID, envelope.Data[0].ID, envelope.Data[0].RunID, nil
}

// routerReplyEnvelope is the wrapper the signalrouter returns over
// NATS request/reply. data holds a TemporalWorkflow list because one
// event can fan out to multiple registered workflows; success
// distinguishes "router got the event but skipped it" from an actual
// hit.
type routerReplyEnvelope struct {
	Success bool          `json:"success"`
	Data    []routerReply `json:"data"`
}

type routerReply struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	RunID string `json:"runID"`
}

// waitForWorkflowStatus polls DescribeWorkflowExecution until the
// execution reaches the requested status, or the timeout fires. Used
// as a substitute for a client.WorkflowRun.Get(...) await because
// Get blocks indefinitely on a workflow that's stuck on Await.
func waitForWorkflowStatus(ctx context.Context, c temporalclient.Client, workflowID, runID string, want enums.WorkflowExecutionStatus, timeout time.Duration) (time.Duration, error) {
	return tests.PollUntilElapsed(ctx, timeout, 200*time.Millisecond, func() error {
		desc, err := c.DescribeWorkflowExecution(ctx, workflowID, runID)
		if err != nil {
			return fmt.Errorf("describeWorkflow: %w", err)
		}
		got := desc.GetWorkflowExecutionInfo().GetStatus()
		if got != want {
			return fmt.Errorf("workflow status %s, want %s", got, want)
		}
		return nil
	})
}

// registerWithPyck calls the pyck workflow API (via the gateway) to
// register the workflow under the calling tenant and wires up a start
// signal so the signalrouter knows how to map an inbound NATS event
// to this workflow row. Returns the row ID so the test can later
// DeleteWorkflow to undo. Equivalent to what
// workflowsdk.worker.registerWorkflowWithPyck does inside worker.Start
// — extracted here as a separate phase so the test can observe
// registration / unregistration explicitly.
func registerWithPyck(ctx context.Context, cfg *config.Config, token string, id wfIdentity) (string, error) {
	c := gateway.NewWorkflowClient(cfg, token)
	resp, err := c.RegisterWorkflow(ctx, workflowapi.RegisterWorkflowArgs{
		Input: workflowmodel.RegisterWorkflowWithSignalsInput{
			Name:      id.WorkflowName,
			TaskQueue: id.TaskQueue,
			Signals: []*workflowmodel.RegisterWorkflowSignalInput{
				{
					NatsTopic:          signalTopicString(id),
					TemporalSignalType: workflowsignalent.TemporalSignalTypeStart,
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("registerWorkflow: %w", err)
	}
	rw := resp.GetRegisterWorkflow()
	if rw == nil {
		return "", fmt.Errorf("registerWorkflow: nil response")
	}
	return rw.ID, nil
}

// unregisterWithPyck deletes the previously-registered workflow row.
// Idempotent on the resolver side — re-deletion is a no-op (or returns
// not-found, which we ignore since the post-condition is the same).
func unregisterWithPyck(ctx context.Context, cfg *config.Config, token, workflowID string) error {
	c := gateway.NewWorkflowClient(cfg, token)
	if _, err := c.DeleteWorkflow(ctx, workflowapi.DeleteWorkflowArgs{Id: workflowID}); err != nil {
		return fmt.Errorf("deleteWorkflow %s: %w", workflowID, err)
	}
	return nil
}

// waitForNamespace polls the Temporal namespace client until the
// per-tenant namespace exists. The register-tenant workflow creates
// it asynchronously after the registerTenant mutation returns, so
// every test that goes on to talk to Temporal must wait first.
//
// Uses an authenticated client (the tenant's own PAT) — that token's
// claim must include this namespace, otherwise pyck-temporal's authz
// layer rejects the Describe call.
func waitForNamespace(ctx context.Context, address, namespace, apiKey string, timeout time.Duration) (time.Duration, error) {
	nc, err := temporal.NewNamespaceClient(address, apiKey)
	if err != nil {
		return 0, err
	}
	defer nc.Close()
	return tests.PollUntilElapsed(ctx, timeout, 500*time.Millisecond, func() error {
		if _, err := nc.Describe(ctx, namespace); err != nil {
			return fmt.Errorf("describe namespace %q: %w", namespace, err)
		}
		return nil
	})
}

// keepOpenAwait is the standard test workflow body shared by every
// workflow integration test in this package. The workflow allocates
// a fresh state struct of type S, hands it to SetupDefaults so the
// SDK wires whatever query/update handlers S's interfaces opt into
// (WorkflowIsAssignableSetter, WorkflowTargetsSetter, …), then blocks
// forever on a never-true Await. The test toggles the attribute via
// gateway RPCs while the goroutine is parked.
//
// Use it by passing a generic instantiation as the workflow function:
//
//	worker, err := newWfWorker(ctx, id, addr, ns, pat, keepOpenAwait[targetsState])
//
// The "any" payload parameter is required because the signalrouter
// forwards event.DataAfter as the workflow input; an unparameterised
// workflow would refuse the start request with a mismatched-args
// error.
func keepOpenAwait[S any](ctx temporalworkflow.Context, _ any) error {
	var state S

	wfCtx, err := workflowsdk.SetupDefaults(ctx, &state)
	if err != nil {
		return err
	}

	return temporalworkflow.Await(wfCtx, func() bool { return false })
}

// describeSearchAttributePayload fetches the raw payload for one
// search attribute from DescribeWorkflowExecution. Tests use it as
// the first stage of a typed-SA read, then call
// converter.GetDefaultDataConverter().FromPayload(...) with a
// destination of the appropriate concrete type (bool for is_assignable,
// []string for KeywordList targets, etc.).
//
// present=false means the SA is genuinely absent for this execution
// — distinct from "present but empty/false". Callers must inspect
// present before reading value; an unset SA may not have a payload
// at all.
func describeSearchAttributePayload(ctx context.Context, c temporalclient.Client, workflowID, runID, key string) (payload *commonpb.Payload, present bool, err error) {
	desc, err := c.DescribeWorkflowExecution(ctx, workflowID, runID)
	if err != nil {
		return nil, false, fmt.Errorf("describeWorkflow: %w", err)
	}
	sa := desc.GetWorkflowExecutionInfo().GetSearchAttributes()
	if sa == nil {
		return nil, false, nil
	}
	p, ok := sa.GetIndexedFields()[key]
	if !ok {
		return nil, false, nil
	}
	return p, true, nil
}
