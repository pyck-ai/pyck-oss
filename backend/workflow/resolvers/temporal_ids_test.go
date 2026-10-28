package resolvers_test

import (
	"cmp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// TestRegisterWorkflowBoundsTemporalIDs pins that registerWorkflow refuses a
// name, task queue or signal name Temporal or its store would refuse, and
// stores nothing. Before, such a registration was stored and every routed
// event then failed.
func TestRegisterWorkflowBoundsTemporalIDs(t *testing.T) {
	t.Parallel()

	// The router starts workflows as <name>_<uuid>, which the store must hold.
	nameLimit := workflow.MaxStoredIDLength - len("_"+uuid.NewString())
	require.Equal(t, nameLimit, services.MaxWorkflowNameLength)

	testCases := []struct {
		name    string
		wfName  string
		queue   string
		signal  string
		wantMsg string // empty: accepted
	}{
		{name: "name at the limit", wfName: strings.Repeat("n", nameLimit)},
		{name: "name over the limit", wfName: strings.Repeat("n", nameLimit+1), wantMsg: "invalid name: exceeds the maximum length"},
		{name: "task queue at the limit", queue: strings.Repeat("q", workflow.MaxStoredIDLength)},
		{name: "task queue over the limit", queue: strings.Repeat("q", workflow.MaxStoredIDLength+1), wantMsg: "invalid task queue: exceeds the maximum length"},
		{name: "signal name at the limit", signal: strings.Repeat("s", workflow.MaxWorkflowIDLength)},
		{name: "signal name over the limit", signal: strings.Repeat("s", workflow.MaxWorkflowIDLength+1), wantMsg: "signal name exceeds the maximum length"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			ctx := te.ctx(userA)

			name := cmp.Or(tc.wfName, "wf_temporal_ids")
			queue := cmp.Or(tc.queue, "ids-queue")
			signal := cmp.Or(tc.signal, "OrderCreated")
			vars := map[string]any{
				"Name":       name,
				"TaskQueue":  queue,
				"DataTypeID": itemDataTypeID,
				"DataName":   "temporalIDs",
				"DataWeight": 0,
				"Signals": []SignalInput{{
					NATSTopic:          natsSignalTopicAttrOp(t, &tenantA, "created"),
					TemporalSignal:     signal,
					TemporalSignalType: "intermediate",
					FilterRule:         "true",
				}},
			}

			if tc.wantMsg != "" {
				execErr(te, ctx, registerWorkflow, vars, tc.wantMsg)
				assert.Zero(t, te.Ent.Workflow.Query().CountX(ctx), "a refused registration stored a workflow")
				assert.Zero(t, te.Ent.WorkflowSignal.Query().CountX(ctx), "a refused registration stored a signal")
				return
			}

			execOK[registerWorkflowData](te, ctx, registerWorkflow, vars)
			wf := te.Ent.Workflow.Query().OnlyX(ctx)
			assert.Equal(t, name, wf.Name)
			assert.Equal(t, queue, wf.TaskQueue)
			assert.Equal(t, signal, te.Ent.WorkflowSignal.Query().OnlyX(ctx).TemporalSignal)
		})
	}
}

var submitUserDataInputNamed = resolver.ParseTemplate(`mutation {
	submitUserDataInput(input: {
		name: {{.Name}},
		data: {},
		workflowID: "wf-id",
		workflowRunID: "run-id"
	}) {
		result
	}
}`)

// TestSubmitUserDataInputValidatesName pins that submitUserDataInput refuses
// a blank or over-long update name, or a built-in update reserved for its
// own mutation, before opening a Temporal client. Before, the caller got Temporal's raw gRPC
// error, and SetAssignee stored an assignee setWorkflowAssignee would refuse.
func TestSubmitUserDataInputValidatesName(t *testing.T) {
	t.Parallel()

	refused := []struct {
		name       string
		updateName string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"over the limit", strings.Repeat("u", workflow.MaxWorkflowIDLength+1)},
		{"SetAssignee", workflow.WorkflowQueryTypeSetAssignee.String()},
		{"SetIsAssignable", workflow.WorkflowQueryTypeSetIsAssignable.String()},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			t.Parallel()
			te := setupWithMockWorkflow(t)
			defer te.Close(t)

			execErr(te, te.ctx(userA), submitUserDataInputNamed,
				map[string]any{"Name": graphQLString(t, tc.updateName)}, "invalid update name")
			assert.Zero(t, te.clientFactory.opened.Load(), "a refused update opened a Temporal client")
		})
	}

	accepted := []struct {
		name       string
		updateName string
	}{
		{"a name at the limit", strings.Repeat("u", workflow.MaxWorkflowIDLength)},
		// The WorkflowTargetsUpdater step's ID.
		{"SetTargets", workflow.WorkflowQueryTypeSetTargets.String()},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			t.Parallel()
			te := setupWithMockWorkflow(t)
			defer te.Close(t)

			execOK[map[string]any](te, te.ctx(userA), submitUserDataInputNamed,
				map[string]any{"Name": graphQLString(t, tc.updateName)})
			require.Equal(t, int64(1), te.clientFactory.opened.Load())
		})
	}
}
