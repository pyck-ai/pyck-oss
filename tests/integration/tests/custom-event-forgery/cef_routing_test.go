//go:build integration

package customeventforgery_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	mRegisterWorkflow = `mutation($i:RegisterWorkflowWithSignalsInput!){registerWorkflow(input:$i){id}}`
	mSendEventTx      = `mutation($i:SendCustomEventInput!){sendCustomEvent(input:$i){transactionID eventCount}}`
	qRouting          = `query($id:ID!){transactionRouting(transactionID:$id){entries{outcome targets{kind workflow}}}}`

	// routeWindow bounds how long the router gets to settle a custom event.
	routeWindow = 30 * time.Second

	sigStart           = "start"
	sigSignalWithStart = "signal_with_start"
)

// TestCustomEventStartsOnlyItsTenantsCustomWorkflows: A's custom event starts
// and signals A's workflows subscribed to it, and neither starts nor signals
// B's workflows on the same event type or a workflow subscribed to the CRUD
// subject of that type.
func (s *CustomEventSuite) TestCustomEventStartsOnlyItsTenantsCustomWorkflows() {
	a, b := s.provision("A"), s.provision("B")
	eventType := uniq("cef-route")

	custom := func(tenant string) string {
		return fmt.Sprintf("%s.%s.custom.%s.*.create", s.Cfg.NatsStream, tenant, eventType)
	}
	crud := fmt.Sprintf("%s.%s.crud.management.%s.*.create", s.Cfg.NatsStream, a.RT.ID, eventType)

	own := s.register(a, "cef_own", custom(a.RT.ID), sigStart)
	ownSig := s.register(a, "cef_own_sig", custom(a.RT.ID), sigSignalWithStart)
	crudWF := s.register(a, "cef_crud", crud, sigStart)
	crudSig := s.register(a, "cef_crud_sig", crud, sigSignalWithStart)
	other := s.register(b, "cef_other", custom(b.RT.ID), sigStart)
	otherSig := s.register(b, "cef_other_sig", custom(b.RT.ID), sigSignalWithStart)

	s.Run("A cannot subscribe to B's custom events", func() {
		res := s.gql(a.PAT, a.RT.ID, mRegisterWorkflow, registerInput("cef_steal", custom(b.RT.ID), sigStart))
		s.Require().True(res.failed(), "registerWorkflow accepted a signal on another tenant's custom events")
		s.Contains(res.errText(), "permission denied")
	})

	targets := s.routedTargets(a, s.sendCustom(a, eventType, "create"))

	s.Contains(targets, "STARTED "+own, "A's custom subscription did not start")
	s.True(hasTarget(targets, ownSig), "A's custom signal subscription was not signalled: %v", targets)
	for _, wf := range []string{crudWF, crudSig} {
		s.False(hasTarget(targets, wf), "a CRUD subscription received the custom event: %v", targets)
	}
	for _, wf := range []string{other, otherSig} {
		s.False(hasTarget(targets, wf), "B's workflow received A's custom event: %v", targets)
	}
}

// TestCustomOperationStartsItsWorkflow: the sender of a custom event chooses
// its operation, so one other than create, update and delete starts the
// workflow subscribed to it, and only that one.
func (s *CustomEventSuite) TestCustomOperationStartsItsWorkflow() {
	a := s.provision("A")
	eventType := uniq("cef-op")

	topic := func(op string) string {
		return fmt.Sprintf("%s.%s.custom.%s.*.%s", s.Cfg.NatsStream, a.RT.ID, eventType, op)
	}
	approve := s.register(a, "cef_approve", topic("approve"), sigStart)
	reject := s.register(a, "cef_reject", topic("reject"), sigStart)

	targets := s.routedTargets(a, s.sendCustom(a, eventType, "approve"))

	s.Contains(targets, "STARTED "+approve, "the approve subscription did not start")
	s.False(hasTarget(targets, reject), "the reject subscription received an approve event: %v", targets)
}

// sendCustom sends a custom event of t and returns its transaction ID.
func (s *CustomEventSuite) sendCustom(t *tenantCtx, eventType, operation string) string {
	s.T().Helper()

	d := s.ok(t.PAT, t.RT.ID, mSendEventTx, map[string]any{"i": map[string]any{
		"type": eventType, "operation": operation, "payload": map[string]any{"id": uuid.NewString()},
	}})
	tx := digStr(d, "sendCustomEvent", "transactionID")
	s.Require().NotEmpty(tx)

	return tx
}

// routedTargets waits until the router has settled transaction tx of t and
// returns its targets as "<kind> <workflow>".
func (s *CustomEventSuite) routedTargets(t *tenantCtx, tx string) []string {
	s.T().Helper()

	var targets []string
	s.Require().NoError(tests.PollUntil(s.Ctx, routeWindow, 500*time.Millisecond, func() error {
		r := s.ok(t.PAT, t.RT.ID, qRouting, map[string]any{"id": tx})
		entries, _ := dig(r, "transactionRouting", "entries").([]any)
		if len(entries) == 0 {
			return fmt.Errorf("transaction %s not routed yet", tx)
		}
		targets = targets[:0]
		for i := range entries {
			ts, _ := dig(entries[i], "targets").([]any)
			for j := range ts {
				targets = append(targets, digStr(ts[j], "kind")+" "+digStr(ts[j], "workflow"))
			}
		}
		return nil
	}), "the router never settled transaction %s", tx)

	return targets
}

// hasTarget reports whether any routing target, "<kind> <workflow>", names wf.
func hasTarget(targets []string, wf string) bool {
	for _, t := range targets {
		if strings.HasSuffix(t, " "+wf) {
			return true
		}
	}

	return false
}

// register registers a workflow of t with one signal of sigType on topic and
// returns its name. It goes with t, a tenant TearDownTest deletes.
func (s *CustomEventSuite) register(t *tenantCtx, prefix, topic, sigType string) string {
	s.T().Helper()

	name := prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.ok(t.PAT, t.RT.ID, mRegisterWorkflow, registerInput(name, topic, sigType))

	return name
}

func registerInput(name, topic, sigType string) map[string]any {
	return map[string]any{"i": map[string]any{
		"name":      name,
		"taskQueue": "cef-" + name,
		"workerID":  "cef-" + name,
		"signals": []map[string]any{{
			"natsTopic":          topic,
			"temporalSignal":     "Start",
			"temporalSignalType": sigType,
			"filterRule":         "",
		}},
	}}
}
