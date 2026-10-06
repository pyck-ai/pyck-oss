package resolvers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
)

// registerWithFilterRule takes the rule as a GraphQL string literal, so rules
// with quotes and newlines reach the resolver byte for byte.
var registerWithFilterRule = resolver.ParseTemplate(`mutation {
	registerWorkflow(input: {
		name: "{{.Name}}",
		taskQueue: "rule-queue",
		workerID: "test-worker",
		dataTypeID: "{{.DataTypeID}}",
		data: { type: "custom", sum: 15, meta: { name: "rule", weight: 1, tags: ["rule"] } },
		signals: [{
			natsTopic: "{{.Topic}}",
			temporalSignal: "Start",
			temporalSignalType: start,
			filterRule: {{.Rule}}
		}]
	}) { id tenantID name }
}`)

func graphQLString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

func filterRuleTopic() string {
	return strings.Join([]string{
		"request", "reply", "pyck", tenantA.String(), "crud", "inventory", "item",
		"123e4567-e89b-12d3-a456-426614174000", "created",
	}, ".")
}

// TestRegisterWorkflowValidatesFilterRule pins that registerWorkflow refuses a
// filter rule the router could never evaluate safely, and stores nothing.
// Before, the rule was only trimmed: a rule that does not parse failed every
// matching event, and a deeply nested one crashed the workflow service.
func TestRegisterWorkflowValidatesFilterRule(t *testing.T) {
	t.Parallel()

	refused := []struct {
		name    string
		rule    string
		wantMsg string
	}{
		{"unparseable", "this is ((not feel", "failed to parse FEEL expression"},
		{"nested past the bound", strings.Repeat("(", 129) + "true" + strings.Repeat(")", 129), "nests too deeply"},
		{"longer than the bound", `status = "` + strings.Repeat("a", 5000) + `"`, "longer than"},
	}
	for _, tt := range refused {
		t.Run("refuses "+tt.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t)
			defer te.Close(t)
			ctx := te.ctx(userA)

			execErr(te, ctx, registerWithFilterRule, map[string]any{
				"Name": "wf_rule_refused", "DataTypeID": itemDataTypeID,
				"Topic": filterRuleTopic(), "Rule": graphQLString(t, tt.rule),
			}, tt.wantMsg)

			wfs, err := te.Ent.Workflow.Query().Count(ctx)
			require.NoError(t, err)
			sigs, err := te.Ent.WorkflowSignal.Query().Count(ctx)
			require.NoError(t, err)
			assert.Zero(t, wfs, "a refused registration stored a workflow")
			assert.Zero(t, sigs, "a refused registration stored a signal")
		})
	}

	t.Run("accepts a valid rule", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execOK[registerWorkflowData](te, ctx, registerWithFilterRule, map[string]any{
			"Name": "wf_rule_ok", "DataTypeID": itemDataTypeID,
			"Topic": filterRuleTopic(), "Rule": graphQLString(t, `status = "active" and quantity > 100`),
		})

		sigs, err := te.Ent.WorkflowSignal.Query().Limit(2).All(ctx)
		require.NoError(t, err)
		require.Len(t, sigs, 1)
		assert.Equal(t, `status = "active" and quantity > 100`, sigs[0].FilterRule)
	})
}
