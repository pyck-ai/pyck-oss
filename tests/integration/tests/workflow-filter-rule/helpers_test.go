//go:build integration

package workflowfilterrule_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// workflowContainer runs the signal router for every tenant.
	workflowContainer = "pyck-workflow"
	dbContainer       = "db"
	dockerTimeout     = 30 * time.Second

	mutationRegisterWorkflow = `mutation($input: RegisterWorkflowWithSignalsInput!) {
  registerWorkflow(input: $input) { id }
}`
	mutationDeleteWorkflow = `mutation($id: ID!) { deleteWorkflow(id: $id) { deletedID } }`
	mutationCreateItem     = `mutation($input: CreateInventoryItemInput!) {
  createInventoryItem(input: $input) { inventoryItem { id } transactionID eventCount }
}`
	queryTransactionRouting = `query($id: ID!) {
  transactionRouting(transactionID: $id) {
    entries { eventID outcome targets { kind workflow workflowID } }
  }
}`
	queryWorkflowsByName = `query($name: String!) { workflows(where: {name: $name}) { totalCount } }`
)

func (s *FilterRuleSuite) provision() (tenantID, pat string) {
	r := s.Require()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant")
	s.DeferTenantCleanup(rt.ID)
	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision writer")
	return rt.ID, p.PAT
}

// registerWorkflow returns the new workflow's id, or the GraphQL errors of a
// refused registration.
func (s *FilterRuleSuite) registerWorkflow(pat, tenant, name, topic, rule string) (string, []string) {
	data, errs, err := s.post(pat, tenant, mutationRegisterWorkflow, map[string]any{"input": map[string]any{
		"name":      name,
		"taskQueue": "wfr-" + strings.ToLower(name),
		"workerID":  "wfr-" + strings.ToLower(name),
		"signals": []map[string]any{{
			"natsTopic":          topic,
			"temporalSignal":     "Start",
			"temporalSignalType": "start",
			"filterRule":         rule,
		}},
	}})
	s.Require().NoError(err, "registerWorkflow transport")
	if len(errs) > 0 {
		return "", errs
	}
	var out struct {
		RegisterWorkflow struct {
			ID string `json:"id"`
		} `json:"registerWorkflow"`
	}
	s.Require().NoError(json.Unmarshal(data, &out))
	return out.RegisterWorkflow.ID, nil
}

// deleteWorkflow removes a workflow, retrying while the workflow service
// restarts.
func (s *FilterRuleSuite) deleteWorkflow(pat, tenant, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := tests.PollUntil(ctx, 45*time.Second, time.Second, func() error {
		_, errs, err := s.post(pat, tenant, mutationDeleteWorkflow, map[string]any{"id": id})
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return fmt.Errorf("deleteWorkflow refused: %v", errs)
		}
		return nil
	})
	if err != nil {
		s.T().Logf("deleteWorkflow %s failed (%v); deleting its signals directly", id, err)
		s.psql(fmt.Sprintf(`DELETE FROM workflow."workflow-signals" WHERE workflow_id = '%s';`, id))
	}
}

func (s *FilterRuleSuite) countWorkflows(pat, tenant, name string) int {
	data, errs, err := s.post(pat, tenant, queryWorkflowsByName, map[string]any{"name": name})
	s.Require().NoError(err)
	s.Require().Empty(errs)
	var out struct {
		Workflows struct {
			TotalCount int `json:"totalCount"`
		} `json:"workflows"`
	}
	s.Require().NoError(json.Unmarshal(data, &out))
	return out.Workflows.TotalCount
}

// createdItem is what createInventoryItem returned: the item and the handle
// to look up what its events did.
type createdItem struct {
	ID            string
	TransactionID string
	EventCount    int
}

func (s *FilterRuleSuite) createItem(pat, tenant, sku string) createdItem {
	data, errs, err := s.post(pat, tenant, mutationCreateItem, map[string]any{"input": map[string]any{"sku": sku}})
	s.Require().NoError(err, "createInventoryItem transport")
	s.Require().Empty(errs, "createInventoryItem refused")
	var out struct {
		CreateInventoryItem struct {
			InventoryItem struct {
				ID string `json:"id"`
			} `json:"inventoryItem"`
			TransactionID string `json:"transactionID"`
			EventCount    int    `json:"eventCount"`
		} `json:"createInventoryItem"`
	}
	s.Require().NoError(json.Unmarshal(data, &out))
	return createdItem{
		ID:            out.CreateInventoryItem.InventoryItem.ID,
		TransactionID: out.CreateInventoryItem.TransactionID,
		EventCount:    out.CreateInventoryItem.EventCount,
	}
}

// routedTo returns nil once the router has settled every event of the
// transaction (the mutation's eventCount) and one of them started or signalled
// the workflow named workflowName: proof the workflow service evaluated the
// event and the rule matched. Otherwise it says what is missing.
func (s *FilterRuleSuite) routedTo(pat, tenant string, item createdItem, workflowName string) error {
	data, errs, err := s.post(pat, tenant, queryTransactionRouting, map[string]any{"id": item.TransactionID})
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return fmt.Errorf("transactionRouting refused: %s", strings.Join(errs, " | "))
	}
	var out struct {
		TransactionRouting struct {
			Entries []struct {
				EventID string `json:"eventID"`
				Outcome string `json:"outcome"`
				Targets []struct {
					Kind     string `json:"kind"`
					Workflow string `json:"workflow"`
				} `json:"targets"`
			} `json:"entries"`
		} `json:"transactionRouting"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	entries := out.TransactionRouting.Entries
	if len(entries) < item.EventCount {
		return fmt.Errorf("transaction %s: %d of %d events routed", item.TransactionID, len(entries), item.EventCount)
	}
	for _, e := range entries {
		for _, t := range e.Targets {
			if t.Workflow == workflowName && (t.Kind == "STARTED" || t.Kind == "SIGNALLED") {
				return nil
			}
		}
	}
	return fmt.Errorf("transaction %s: %d events routed, none started or signalled %s: %+v", item.TransactionID, len(entries), workflowName, entries)
}

func (s *FilterRuleSuite) post(token, tenant, query string, vars map[string]any) (json.RawMessage, []string, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(s.Ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Cfg.GatewayURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Pyck-Tenant-Id", tenant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, fmt.Errorf("non-JSON gateway reply (HTTP %d): %.256s", resp.StatusCode, raw)
	}
	msgs := make([]string, 0, len(env.Errors))
	for _, e := range env.Errors {
		msgs = append(msgs, e.Message)
	}
	return env.Data, msgs, nil
}

func (s *FilterRuleSuite) psql(query string) string {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", "-i", dbContainer,
		"psql", "-U", "admin", "-d", "pyck_dev", "-tA", "-c", query).CombinedOutput()
	s.Require().NoError(err, "psql: %s", out)
	return string(out)
}

func (s *FilterRuleSuite) containerState(format string) string {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", format, workflowContainer).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *FilterRuleSuite) restartCount() int {
	n, err := strconv.Atoi(s.containerState("{{.RestartCount}}"))
	if err != nil {
		return -1
	}
	return n
}

// crashSignature returns the runtime's verdict from the container log since
// the given timestamp, or its last lines.
func (s *FilterRuleSuite) crashSignature(since string) string {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "docker", "logs", "--since", since, workflowContainer).CombinedOutput() //nolint:errcheck // best-effort diagnosis
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.Contains(line, "stack overflow") || strings.Contains(line, "goroutine stack exceeds") {
			return strings.TrimSpace(line)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ")
}

// ensureWorkflowServiceUp restarts the workflow container if a crash left it
// down, and waits until it runs without restarting.
func (s *FilterRuleSuite) ensureWorkflowServiceUp() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "start", workflowContainer).Run() //nolint:errcheck // already running is fine
	stable := s.restartCount()
	if err := tests.PollStable(ctx, 10*time.Second, time.Second, func() error {
		if s.containerState("{{.State.Running}}") != "true" || s.restartCount() != stable {
			return fmt.Errorf("workflow service not stable")
		}
		return nil
	}); err != nil {
		s.T().Logf("WARNING: workflow service did not stabilise: %v", err)
	}
}
