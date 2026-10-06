//go:build integration

package workflowfilterrule_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/events"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// crashDepth is a nesting depth past the point where the FEEL parser's
// recursion exhausts the goroutine stack.
const crashDepth = 200_000

// FilterRuleSuite provisions a fresh tenant with a writer per test.
type FilterRuleSuite struct {
	tests.Base
}

// TestRegisterRefusesInvalidRules registers one workflow per invalid rule and
// expects each to be refused with nothing stored.
func (s *FilterRuleSuite) TestRegisterRefusesInvalidRules() {
	tenant, pat := s.provision()
	topic := itemCreatedTopic(s, tenant)

	cases := []struct {
		name    string
		rule    string
		wantMsg string
	}{
		{"unparseable", "this is ((not feel", "failed to parse FEEL expression"},
		{"nested brackets", strings.Repeat("(", crashDepth) + "true" + strings.Repeat(")", crashDepth), "longer than"},
		{"nested keywords", strings.Repeat("if true then ", 129) + "true" + strings.Repeat(" else false", 129), "nests too deeply"},
		{"too long", `status = "` + strings.Repeat("a", 5000) + `"`, "longer than"},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			name := "WfrRefused_" + strings.ToLower(gofakeit.LetterN(8))
			id, errs := s.registerWorkflow(pat, tenant, name, topic, tc.rule)
			if id != "" {
				// Accepted: remove it at once so no event ever evaluates it.
				s.deleteWorkflow(pat, tenant, id)
			}
			s.Require().NotEmpty(errs, "registerWorkflow accepted the %s rule and stored workflow %s", tc.name, id)
			s.Contains(strings.Join(errs, " | "), tc.wantMsg)
			s.Zero(s.countWorkflows(pat, tenant, name), "a refused registration stored a workflow")
		})
	}
}

// TestStoredDeepRuleDoesNotCrashWorkflowService plants a rule of crashDepth
// nested parentheses on a registered signal, as a row written before the
// registration check would hold it, and creates one matching event. The
// workflow service must stay up: the rule fails its evaluation instead.
func (s *FilterRuleSuite) TestStoredDeepRuleDoesNotCrashWorkflowService() {
	r := s.Require()
	tenant, pat := s.provision()
	topic := itemCreatedTopic(s, tenant)
	nonce := strings.ToLower(gofakeit.LetterN(8))

	// Control: a benign rule on the same signal is evaluated by the workflow
	// service, which starts the workflow for the matching event.
	controlName := "WfrStored_" + nonce
	wfID, errs := s.registerWorkflow(pat, tenant, controlName, topic, "true")
	r.Empty(errs, "registering the control workflow failed")
	defer func() {
		s.deleteWorkflow(pat, tenant, wfID)
		s.ensureWorkflowServiceUp()
	}()

	control := s.createItem(pat, tenant, "wfr-ctl-"+nonce)
	r.NoError(tests.PollUntil(s.Ctx, 30*time.Second, 500*time.Millisecond, func() error {
		return s.routedTo(pat, tenant, control, controlName)
	}), "the workflow service never routed the matching event, so the test proves nothing")

	// Plant the deep rule on the stored signal.
	s.psql(fmt.Sprintf(`UPDATE workflow."workflow-signals" SET filter_rule = repeat('(', %d) || 'true' || repeat(')', %d) WHERE workflow_id = '%s';`,
		crashDepth, crashDepth, wfID))
	r.Equal(fmt.Sprint(2*crashDepth+4),
		strings.TrimSpace(s.psql(fmt.Sprintf(`SELECT length(filter_rule) FROM workflow."workflow-signals" WHERE workflow_id = '%s';`, wfID))),
		"the deep rule was not planted")

	restarts, started := s.restartCount(), s.containerState("{{.State.StartedAt}}")
	s.createItem(pat, tenant, "wfr-deep-"+nonce)

	// The container must neither restart nor exit while the event is retried.
	err := tests.PollStable(s.Ctx, 45*time.Second, time.Second, func() error {
		if rc := s.restartCount(); rc != restarts {
			return fmt.Errorf("workflow service restarted (%d -> %d): %s", restarts, rc, s.crashSignature(started))
		}
		if s.containerState("{{.State.Running}}") != "true" {
			return fmt.Errorf("workflow service is not running: %s", s.crashSignature(started))
		}
		return nil
	})
	r.NoError(err, "one event on a signal with a deeply nested stored rule took the workflow service down")
}

// TestPanickingRuleDoesNotCrashWorkflowService registers a rule that parses
// but makes the FEEL evaluator panic (modulo by zero) and creates one matching
// event. The workflow service must stay up: the panic becomes an evaluation
// error.
func (s *FilterRuleSuite) TestPanickingRuleDoesNotCrashWorkflowService() {
	r := s.Require()
	tenant, pat := s.provision()
	topic := itemCreatedTopic(s, tenant)
	nonce := strings.ToLower(gofakeit.LetterN(8))

	wfID, errs := s.registerWorkflow(pat, tenant, "WfrPanic_"+nonce, topic, "1 % 0 = 0")
	r.Empty(errs, "a rule that parses must be accepted")
	defer func() {
		s.deleteWorkflow(pat, tenant, wfID)
		s.ensureWorkflowServiceUp()
	}()

	restarts, started := s.restartCount(), s.containerState("{{.State.StartedAt}}")
	s.createItem(pat, tenant, "wfr-panic-"+nonce)

	err := tests.PollStable(s.Ctx, 45*time.Second, time.Second, func() error {
		if rc := s.restartCount(); rc != restarts {
			return fmt.Errorf("workflow service restarted (%d -> %d): %s", restarts, rc, s.crashSignature(started))
		}
		if s.containerState("{{.State.Running}}") != "true" {
			return fmt.Errorf("workflow service is not running: %s", s.crashSignature(started))
		}
		return nil
	})
	r.NoError(err, "one event on a signal whose rule divides by zero took the workflow service down")
}

func itemCreatedTopic(s *FilterRuleSuite, tenant string) string {
	id, err := uuid.Parse(tenant)
	s.Require().NoError(err)
	return events.MutationEventWithReplyTopic{
		TenantID:      id,
		ServiceName:   "inventory",
		SchemaName:    "Item",
		OperationName: "create",
	}.String()
}
