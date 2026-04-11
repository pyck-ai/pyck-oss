package resolvers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/workflow/model"
	"github.com/pyck-ai/pyck/backend/workflow/resolvers"
)

func TestQuotedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{
			name:     "empty slice",
			input:    []string{},
			expected: "",
		},
		{
			name:     "single value",
			input:    []string{"test"},
			expected: `"test"`,
		},
		{
			name:     "multiple values",
			input:    []string{"a", "b", "c"},
			expected: `"a", "b", "c"`,
		},
		{
			name:     "values with special characters",
			input:    []string{"hello world", "test@example.com"},
			expected: `"hello world", "test@example.com"`,
		},
		{
			name:     "values with quotes",
			input:    []string{`test"quote`, `another"one`},
			expected: `"test\"quote", "another\"one"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := resolvers.QuotedValues(tt.input)
			assert.Equal(t, tt.expected, result, "QuotedValues(%v)", tt.input)
		})
	}
}

func TestFormatPredicate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		temporalField string
		value         interface{}
		operator      string
		expected      string
	}{
		{
			name:          "nil value - empty result",
			temporalField: "WorkflowType",
			value:         nil,
			operator:      "=",
			expected:      "",
		},
		{
			name:          "empty string pointer - empty result",
			temporalField: "WorkflowType",
			value:         stringPtr(""),
			operator:      "=",
			expected:      "",
		},
		{
			name:          "string pointer with equals operator",
			temporalField: "WorkflowType",
			value:         stringPtr("MyWorkflow"),
			operator:      "=",
			expected:      `WorkflowType = "MyWorkflow"`,
		},
		{
			name:          "string pointer with not equals operator",
			temporalField: "WorkflowType",
			value:         stringPtr("MyWorkflow"),
			operator:      "!=",
			expected:      `WorkflowType != "MyWorkflow"`,
		},
		{
			name:          "string pointer with CONTAINS operator",
			temporalField: "WorkflowType",
			value:         stringPtr("Workflow"),
			operator:      "CONTAINS",
			expected:      `WorkflowType CONTAINS "Workflow"`,
		},
		{
			name:          "string pointer with greater than operator",
			temporalField: "StartTime",
			value:         stringPtr("2024-01-01T00:00:00Z"),
			operator:      ">",
			expected:      `StartTime > "2024-01-01T00:00:00Z"`,
		},
		{
			name:          "empty slice - empty result",
			temporalField: "WorkflowType",
			value:         []string{},
			operator:      "IN",
			expected:      "",
		},
		{
			name:          "single item slice with IN operator",
			temporalField: "WorkflowType",
			value:         []string{"MyWorkflow"},
			operator:      "IN",
			expected:      `WorkflowType IN ("MyWorkflow")`,
		},
		{
			name:          "multiple items slice with IN operator",
			temporalField: "WorkflowType",
			value:         []string{"Workflow1", "Workflow2", "Workflow3"},
			operator:      "IN",
			expected:      `WorkflowType IN ("Workflow1", "Workflow2", "Workflow3")`,
		},
		{
			name:          "multiple items slice with NOT IN operator",
			temporalField: "ExecutionStatus",
			value:         []string{"COMPLETED", "FAILED"},
			operator:      "NOT IN",
			expected:      `ExecutionStatus NOT IN ("COMPLETED", "FAILED")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := resolvers.FormatPredicate(tt.temporalField, tt.value, tt.operator)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildWhereClause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		where    *model.WorkflowExecutionsWhereInput
		expected string
	}{
		{
			name:     "nil where",
			where:    nil,
			expected: "",
		},
		{
			name:     "empty where",
			where:    &model.WorkflowExecutionsWhereInput{},
			expected: "",
		},
		{
			name: "single field predicate",
			where: &model.WorkflowExecutionsWhereInput{
				Status: stringPtr("Running"),
			},
			expected: `ExecutionStatus = "Running"`,
		},
		{
			name: "multiple field predicates are AND-joined",
			where: &model.WorkflowExecutionsWhereInput{
				Status:   stringPtr("Running"),
				TypeName: stringPtr("MyWorkflow"),
			},
			expected: `WorkflowType = "MyWorkflow" AND ExecutionStatus = "Running"`,
		},
		{
			name: "OR with two branches",
			where: &model.WorkflowExecutionsWhereInput{
				Or: []*model.WorkflowExecutionsWhereInput{
					{AssigneeIsNil: boolPtr(true)},
					{AssigneeNeq: stringPtr("user-123")},
				},
			},
			expected: `((pyck_workflow_assignee IS NULL OR pyck_workflow_assignee = "") OR pyck_workflow_assignee != "user-123")`,
		},
		{
			name: "field predicates with OR",
			where: &model.WorkflowExecutionsWhereInput{
				Status: stringPtr("Running"),
				Or: []*model.WorkflowExecutionsWhereInput{
					{AssigneeIsNil: boolPtr(true)},
					{AssigneeNeq: stringPtr("user-123")},
				},
			},
			expected: `ExecutionStatus = "Running" AND ((pyck_workflow_assignee IS NULL OR pyck_workflow_assignee = "") OR pyck_workflow_assignee != "user-123")`,
		},
		{
			name: "AND with two branches",
			where: &model.WorkflowExecutionsWhereInput{
				And: []*model.WorkflowExecutionsWhereInput{
					{Status: stringPtr("Running")},
					{TypeName: stringPtr("MyWorkflow")},
				},
			},
			expected: `ExecutionStatus = "Running" AND WorkflowType = "MyWorkflow"`,
		},
		{
			name: "NOT clause",
			where: &model.WorkflowExecutionsWhereInput{
				Not: &model.WorkflowExecutionsWhereInput{
					Status: stringPtr("Completed"),
				},
			},
			expected: `NOT (ExecutionStatus = "Completed")`,
		},
		{
			name: "NOT with multiple predicates",
			where: &model.WorkflowExecutionsWhereInput{
				Not: &model.WorkflowExecutionsWhereInput{
					Status:   stringPtr("Completed"),
					TypeName: stringPtr("CleanupWorkflow"),
				},
			},
			expected: `NOT (WorkflowType = "CleanupWorkflow" AND ExecutionStatus = "Completed")`,
		},
		{
			name: "combined field predicates with AND, OR, and NOT",
			where: &model.WorkflowExecutionsWhereInput{
				Status: stringPtr("Running"),
				And: []*model.WorkflowExecutionsWhereInput{
					{Service: stringPtr("picking")},
				},
				Or: []*model.WorkflowExecutionsWhereInput{
					{AssigneeIsNil: boolPtr(true)},
					{Assignee: stringPtr("user-123")},
				},
				Not: &model.WorkflowExecutionsWhereInput{
					TypeName: stringPtr("CleanupWorkflow"),
				},
			},
			expected: `ExecutionStatus = "Running" AND pyck_service = "picking" AND ((pyck_workflow_assignee IS NULL OR pyck_workflow_assignee = "") OR pyck_workflow_assignee = "user-123") AND NOT (WorkflowType = "CleanupWorkflow")`,
		},
		{
			name: "nested OR with compound branches",
			where: &model.WorkflowExecutionsWhereInput{
				Or: []*model.WorkflowExecutionsWhereInput{
					{
						Status:   stringPtr("Running"),
						TypeName: stringPtr("PickingWorkflow"),
					},
					{
						Status:   stringPtr("Failed"),
						TypeName: stringPtr("ReceivingWorkflow"),
					},
				},
			},
			expected: `(WorkflowType = "PickingWorkflow" AND ExecutionStatus = "Running" OR WorkflowType = "ReceivingWorkflow" AND ExecutionStatus = "Failed")`,
		},
		{
			name: "single OR branch (no wrapping)",
			where: &model.WorkflowExecutionsWhereInput{
				Or: []*model.WorkflowExecutionsWhereInput{
					{Status: stringPtr("Running")},
				},
			},
			expected: `ExecutionStatus = "Running"`,
		},
		{
			name: "deeply nested NOT inside OR",
			where: &model.WorkflowExecutionsWhereInput{
				Or: []*model.WorkflowExecutionsWhereInput{
					{AssigneeIsNil: boolPtr(true)},
					{
						Not: &model.WorkflowExecutionsWhereInput{
							Assignee: stringPtr("user-123"),
						},
					},
				},
			},
			expected: `((pyck_workflow_assignee IS NULL OR pyck_workflow_assignee = "") OR NOT (pyck_workflow_assignee = "user-123"))`,
		},
		{
			name: "OR with empty branches ignored",
			where: &model.WorkflowExecutionsWhereInput{
				Or: []*model.WorkflowExecutionsWhereInput{
					{},
					{Status: stringPtr("Running")},
				},
			},
			expected: `ExecutionStatus = "Running"`,
		},
		{
			name: "NOT with empty clause ignored",
			where: &model.WorkflowExecutionsWhereInput{
				Status: stringPtr("Running"),
				Not:    &model.WorkflowExecutionsWhereInput{},
			},
			expected: `ExecutionStatus = "Running"`,
		},
		{
			name: "OR with assigneeIsNil and assigneeEqualFold (bug report case)",
			where: &model.WorkflowExecutionsWhereInput{
				Status: stringPtr("Running"),
				Or: []*model.WorkflowExecutionsWhereInput{
					{AssigneeIsNil: boolPtr(true)},
					{AssigneeEqualFold: stringPtr("fa558e3f-ca07-5dad-92dc-5cd12b7eb3e8")},
				},
			},
			expected: `ExecutionStatus = "Running" AND ((pyck_workflow_assignee IS NULL OR pyck_workflow_assignee = "") OR pyck_workflow_assignee = "fa558e3f-ca07-5dad-92dc-5cd12b7eb3e8")`,
		},
		{
			name: "EqualFold maps to equals operator",
			where: &model.WorkflowExecutionsWhereInput{
				TypeNameEqualFold: stringPtr("MyWorkflow"),
			},
			expected: `WorkflowType = "MyWorkflow"`,
		},
		{
			name: "ContainsFold maps to CONTAINS operator",
			where: &model.WorkflowExecutionsWhereInput{
				WorkflowNameContainsFold: stringPtr("pick"),
			},
			expected: `pyck_workflow_name CONTAINS "pick"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := resolvers.BuildWhereClause(tt.where)
			assert.Equal(t, tt.expected, result)
		})
	}
}
