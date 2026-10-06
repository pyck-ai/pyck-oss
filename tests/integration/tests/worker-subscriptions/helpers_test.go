//go:build integration

package workersubscriptions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/tests/integration/internal/temporal"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

var errExitedEarly = errors.New("process is gone")

const (
	// stepTimeout bounds every wait in the scenario.
	stepTimeout  = 60 * time.Second
	pollInterval = time.Second
	// stableWindow is how long "nothing routed" must hold. Routing takes well
	// under a second once the router has the event, and a positive control on
	// another workflow gates the window, so this only covers handler ordering.
	stableWindow = 6 * time.Second
	// stopTimeout is how long a worker gets to exit after SIGTERM before it is
	// killed. Stop waits up to PYCK_WORKER_UNREGISTER_TIMEOUT (5s) for the
	// unregister call, then stops the Temporal workers.
	stopTimeout = 30 * time.Second

	workerPkg = "github.com/pyck-ai/pyck/tests/integration/tests/worker-subscriptions/testworker"

	queryWorkflowsByName = `query($name: String!) {
  workflows(where: {name: $name}) { totalCount edges { node { id } } }
}`
	queryExecutionsByName = `query($name: String!) {
  workflowExecutions(where: {workflowName: $name}) { edges { node { execution { workflowId id } status } } }
}`
	mutationCreateItem = `mutation($input: CreateInventoryItemInput!) {
  createInventoryItem(input: $input) { inventoryItem { id } }
}`
	mutationDeleteWorkflow = `mutation($id: ID!) { deleteWorkflow(id: $id) { deletedID } }`
)

// workerKind is one worker type: the workflow it serves and its task queue,
// plus an optional FEEL filter rule on its start signal. Workers of one
// workflow may differ in the filter, which is how the suite tells their
// subscriptions apart (worker IDs and stopped_at are not exposed by the API).
type workerKind struct {
	workflow string
	queue    string
	filter   string
}

// proc is one running testworker process.
type proc struct {
	label   string
	cmd     *exec.Cmd
	logPath string
	done    chan struct{}
	// waitErr is the result of cmd.Wait, valid once done is closed.
	waitErr error
	// stopping is set once the suite has asked the process to exit, so an exit
	// after that is not reported as a crash.
	stopping bool
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// logTail returns the last n lines of the process output.
func (p *proc) logTail(n int) string {
	raw, err := os.ReadFile(p.logPath)
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", p.logPath, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// buildWorker compiles testworker once per suite run.
func buildWorker(dir string) (string, error) {
	bin := filepath.Join(dir, "testworker")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, workerPkg).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build %s: %w\n%s", workerPkg, err, out)
	}
	return bin, nil
}

// startWorker launches one worker process of the given kind. Its whole
// configuration is env: the SDK's own variables plus WS_* for the helper.
func (s *WorkerSubscriptionsSuite) startWorker(k workerKind, label string) *proc {
	s.T().Helper()

	logPath := filepath.Join(s.dir, label+".log")
	logFile, err := os.Create(logPath)
	s.Require().NoError(err)

	// s.Ctx is never cancelled, so the SIGTERM path in stopWorker stays the way
	// workers exit; CommandContext only guards against a leaked process.
	cmd := exec.CommandContext(s.Ctx, s.workerBin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	dieWithParent(cmd)
	// Deliberately not os.Environ(): nothing from the shell (bootstrap.env,
	// TEMPORAL_*) may leak into the worker's configuration.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		// HOME is the suite dir so no user-level Temporal profile is picked up by
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
		"WS_WORKFLOW=" + k.workflow,
		"WS_TASK_QUEUE=" + k.queue,
		"WS_TENANT_ID=" + s.tenant,
		"WS_FILTER_RULE=" + k.filter,
	}

	s.Require().NoError(cmd.Start(), "start worker %s", label)

	p := &proc{label: label, cmd: cmd, logPath: logPath, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		_ = logFile.Close() //nolint:errcheck // log file only
		close(p.done)
	}()

	s.procs = append(s.procs, p)
	s.T().Logf("started worker %s (pid %d): workflow=%s queue=%s log=%s", label, cmd.Process.Pid, k.workflow, k.queue, logPath)

	return p
}

// stopWorker sends SIGTERM (the SDK's graceful path: unregister, then stop the
// Temporal workers) and waits for the process to exit.
func (s *WorkerSubscriptionsSuite) stopWorker(p *proc) error {
	p.stopping = true
	if p.exited() {
		return p.waitErr
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal worker %s: %w", p.label, err)
	}
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(stopTimeout):
		_ = p.cmd.Process.Kill() //nolint:errcheck // already failing
		<-p.done
		return fmt.Errorf("worker %s did not exit within %s of SIGTERM (killed)", p.label, stopTimeout)
	}
}

// checkWorkersAlive fails the wait fast when a worker died on its own.
func (s *WorkerSubscriptionsSuite) checkWorkersAlive() error {
	for _, p := range s.procs {
		if !p.stopping && p.exited() {
			return fmt.Errorf("worker %s exited unexpectedly (wait: %w)\n%s", p.label, errors.Join(p.waitErr, errExitedEarly), p.logTail(30))
		}
	}
	return nil
}

// poll is tests.PollUntil with the suite's bounds, aborting when a worker died.
func (s *WorkerSubscriptionsSuite) poll(fn func() error) error {
	return tests.PollUntil(s.Ctx, stepTimeout, pollInterval, func() error {
		if err := s.checkWorkersAlive(); err != nil {
			return err
		}
		return fn()
	})
}

// gql posts one GraphQL request to the gateway as the tenant's writer.
func (s *WorkerSubscriptionsSuite) gql(query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.Ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Cfg.GatewayURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.pat)
	req.Header.Set("X-Pyck-Tenant-Id", s.tenant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("non-JSON gateway reply (HTTP %d): %.256s", resp.StatusCode, raw)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("graphql errors: %s", strings.Join(msgs, "; "))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// workflowIDs returns the ids of the tenant's live workflow rows with this name.
func (s *WorkerSubscriptionsSuite) workflowIDs(name string) ([]string, error) {
	var out struct {
		Workflows struct {
			Edges []struct {
				Node struct {
					ID string `json:"id"`
				} `json:"node"`
			} `json:"edges"`
		} `json:"workflows"`
	}
	if err := s.gql(queryWorkflowsByName, map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Workflows.Edges))
	for _, e := range out.Workflows.Edges {
		ids = append(ids, e.Node.ID)
	}
	return ids, nil
}

// waitWorkflow polls until exactly one workflow row with this name exists and
// returns its id.
func (s *WorkerSubscriptionsSuite) waitWorkflow(name string) string {
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
	return id
}

// assertWorkflowKept fails unless the one workflow row with this name still has
// the given id (the old startup reconcile deleted it and a re-register would
// have minted a new id).
func (s *WorkerSubscriptionsSuite) assertWorkflowKept(name, wantID string) {
	s.T().Helper()
	ids, err := s.workflowIDs(name)
	s.Require().NoError(err)
	s.Require().Equal([]string{wantID}, ids, "workflow %q must keep its row (same id)", name)
}

// createItem creates an inventory item, the event every worker's start signal
// matches.
func (s *WorkerSubscriptionsSuite) createItem(sku string) {
	s.T().Helper()
	s.Require().NoError(s.gql(mutationCreateItem, map[string]any{"input": map[string]any{"sku": sku}}, nil), "createInventoryItem")
}

// temporalExecutions counts a workflow type's executions straight from Temporal
// (namespace = tenant ID).
func (s *WorkerSubscriptionsSuite) temporalExecutions(name string) (int, error) {
	ctx, cancel := context.WithTimeout(s.Ctx, 30*time.Second)
	defer cancel()
	resp, err := s.temporal.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Query: fmt.Sprintf("WorkflowType = %q", name),
	})
	if err != nil {
		return 0, err
	}
	return len(resp.GetExecutions()), nil
}

// gatewayExecutions counts the same executions through the gateway.
func (s *WorkerSubscriptionsSuite) gatewayExecutions(name string) (int, error) {
	var out struct {
		WorkflowExecutions struct {
			Edges []json.RawMessage `json:"edges"`
		} `json:"workflowExecutions"`
	}
	if err := s.gql(queryExecutionsByName, map[string]any{"name": name}, &out); err != nil {
		return 0, err
	}
	return len(out.WorkflowExecutions.Edges), nil
}

// executions returns the current execution count of a workflow type from both
// vantage points, requiring them to agree.
func (s *WorkerSubscriptionsSuite) executions(name string) (int, error) {
	viaTemporal, err := s.temporalExecutions(name)
	if err != nil {
		return 0, fmt.Errorf("temporal list %q: %w", name, err)
	}
	viaGateway, err := s.gatewayExecutions(name)
	if err != nil {
		return 0, fmt.Errorf("gateway list %q: %w", name, err)
	}
	if viaTemporal != viaGateway {
		return 0, fmt.Errorf("%q: temporal sees %d executions, gateway %d", name, viaTemporal, viaGateway)
	}
	return viaTemporal, nil
}

// assertNoNewExecutions fails if the workflow type gains an execution beyond
// base at any point in the window. It reads Temporal directly (the gateway view
// is checked by the positive waits) and is only meaningful after a positive
// control proved events were routed past the point in question.
func (s *WorkerSubscriptionsSuite) assertNoNewExecutions(name string, base int, window time.Duration) {
	s.T().Helper()
	err := tests.PollStable(s.Ctx, window, pollInterval, func() error {
		got, err := s.temporalExecutions(name)
		if err != nil {
			return err
		}
		if got != base {
			return fmt.Errorf("%q: %d executions, want %d (an event routed to a subscription that must not route)", name, got, base)
		}
		return nil
	})
	s.Require().NoError(err)
}

// waitExecutionsAtLeast polls until the workflow type has at least min
// executions, seen identically through Temporal and the gateway.
func (s *WorkerSubscriptionsSuite) waitExecutionsAtLeast(name string, minCount int) {
	s.T().Helper()
	err := s.poll(func() error {
		got, err := s.executions(name)
		if err != nil {
			return err
		}
		if got < minCount {
			return fmt.Errorf("%q: %d executions, want at least %d", name, got, minCount)
		}
		return nil
	})
	s.Require().NoError(err)
}

// waitReady polls until the worker logged "worker started", which RunDefaultWorker
// prints only after Start returned, i.e. after the worker registered its
// workflows and signals and began polling.
func (s *WorkerSubscriptionsSuite) waitReady(p *proc) {
	s.T().Helper()
	err := s.poll(func() error {
		raw, err := os.ReadFile(p.logPath)
		if err != nil {
			return err
		}
		if !strings.Contains(string(raw), "worker started") {
			return fmt.Errorf("worker %s not started yet", p.label)
		}
		return nil
	})
	s.Require().NoError(err, "worker %s never became ready:\n%s", p.label, p.logTail(30))
}

// waitForNamespace polls until the tenant's Temporal namespace exists; the
// register-tenant workflow creates it asynchronously.
func (s *WorkerSubscriptionsSuite) waitForNamespace() {
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

// dialTemporal opens the suite's Temporal client on the tenant namespace.
func (s *WorkerSubscriptionsSuite) dialTemporal() temporalclient.Client {
	c, err := temporal.Dial(s.Ctx, s.Cfg.TemporalAddress, s.tenant, s.pat)
	s.Require().NoError(err)
	return c
}
