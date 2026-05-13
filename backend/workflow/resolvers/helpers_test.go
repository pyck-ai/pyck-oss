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
		{
			name:          "WorkflowTarget enum slice with IN operator",
			temporalField: "pyck_workflow_targets",
			value:         []model.WorkflowTarget{model.WorkflowTargetWeb, model.WorkflowTargetMobile},
			operator:      "IN",
			expected:      `pyck_workflow_targets IN ("WEB", "MOBILE")`,
		},
		{
			name:          "WorkflowTarget enum slice with NOT IN operator",
			temporalField: "pyck_workflow_targets",
			value:         []model.WorkflowTarget{model.WorkflowTargetSetup},
			operator:      "NOT IN",
			expected:      `pyck_workflow_targets NOT IN ("SETUP")`,
		},
		{
			name:          "bool pointer true",
			temporalField: "pyck_workflow_is_assignable",
			value:         boolPtr(true),
			operator:      "=",
			expected:      `pyck_workflow_is_assignable = true`,
		},
		{
			name:          "bool pointer false",
			temporalField: "pyck_workflow_is_assignable",
			value:         boolPtr(false),
			operator:      "=",
			expected:      `pyck_workflow_is_assignable = false`,
		},
		{
			name:          "nil bool pointer - empty result",
			temporalField: "pyck_workflow_is_assignable",
			value:         (*bool)(nil),
			operator:      "=",
			expected:      "",
		},
		{
			name:          "int pointer with equals operator",
			temporalField: "pyck_sort_key",
			value:         intPtr(42),
			operator:      "=",
			expected:      `pyck_sort_key = 42`,
		},
		{
			name:          "int pointer with greater-than operator",
			temporalField: "pyck_sort_key",
			value:         intPtr(100),
			operator:      ">",
			expected:      `pyck_sort_key > 100`,
		},
		{
			name:          "nil int pointer - empty result",
			temporalField: "pyck_sort_key",
			value:         (*int)(nil),
			operator:      "=",
			expected:      "",
		},
		{
			name:          "int slice with IN operator",
			temporalField: "pyck_sort_key",
			value:         []int{1, 2, 3},
			operator:      "IN",
			expected:      `pyck_sort_key IN (1, 2, 3)`,
		},
		{
			name:          "int slice with NOT IN operator",
			temporalField: "pyck_sort_key",
			value:         []int{10, 20},
			operator:      "NOT IN",
			expected:      `pyck_sort_key NOT IN (10, 20)`,
		},
		{
			name:          "empty int slice - empty result",
			temporalField: "pyck_sort_key",
			value:         []int{},
			operator:      "IN",
			expected:      "",
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
			// true matches explicit-true OR absent-attribute — workflows
			// that never opted in via WorkflowIsAssignableSetter are
			// considered assignable by default (mirrors SDK semantics).
			name: "is_assignable true predicate (null-matches-true)",
			where: &model.WorkflowExecutionsWhereInput{
				IsAssignable: boolPtr(true),
			},
			expected: `(pyck_workflow_is_assignable = true OR pyck_workflow_is_assignable IS NULL)`,
		},
		{
			// false matches only workflows that explicitly wrote false.
			// Absent-attribute is treated as true (see previous case).
			name: "is_assignable false predicate (explicit only)",
			where: &model.WorkflowExecutionsWhereInput{
				IsAssignable: boolPtr(false),
			},
			expected: `pyck_workflow_is_assignable = false`,
		},
		{
			name: "is_assignable combined with other filters",
			where: &model.WorkflowExecutionsWhereInput{
				Status:       stringPtr("Running"),
				IsAssignable: boolPtr(true),
			},
			expected: `(pyck_workflow_is_assignable = true OR pyck_workflow_is_assignable IS NULL) AND ExecutionStatus = "Running"`,
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
		{
			name: "Targets emits IN against pyck_workflow_targets",
			where: &model.WorkflowExecutionsWhereInput{
				Targets: []model.WorkflowTarget{model.WorkflowTargetWeb, model.WorkflowTargetMobile},
			},
			expected: `pyck_workflow_targets IN ("WEB", "MOBILE")`,
		},
		{
			name: "TargetsNotIn emits NOT IN against pyck_workflow_targets",
			where: &model.WorkflowExecutionsWhereInput{
				TargetsNotIn: []model.WorkflowTarget{model.WorkflowTargetSetup},
			},
			expected: `pyck_workflow_targets NOT IN ("SETUP")`,
		},
		{
			name: "Targets combines with status",
			where: &model.WorkflowExecutionsWhereInput{
				Status:  stringPtr("Running"),
				Targets: []model.WorkflowTarget{model.WorkflowTargetWeb},
			},
			expected: `ExecutionStatus = "Running" AND pyck_workflow_targets IN ("WEB")`,
		},
		{
			name: "title equality emits pyck_title",
			where: &model.WorkflowExecutionsWhereInput{
				Title: stringPtr("SKU-123"),
			},
			expected: `pyck_title = "SKU-123"`,
		},
		{
			name: "titleIsNil treats missing or empty as null (nullIncludesEmpty)",
			where: &model.WorkflowExecutionsWhereInput{
				TitleIsNil: boolPtr(true),
			},
			expected: `(pyck_title IS NULL OR pyck_title = "")`,
		},
		{
			name: "groupTitle equality emits pyck_group_title",
			where: &model.WorkflowExecutionsWhereInput{
				GroupTitle: stringPtr("ORDER-42"),
			},
			expected: `pyck_group_title = "ORDER-42"`,
		},
		{
			name: "groupTitleIsNil treats missing or empty as null (nullIncludesEmpty)",
			where: &model.WorkflowExecutionsWhereInput{
				GroupTitleIsNil: boolPtr(true),
			},
			expected: `(pyck_group_title IS NULL OR pyck_group_title = "")`,
		},
		{
			name: "sortKey equality emits pyck_sort_key",
			where: &model.WorkflowExecutionsWhereInput{
				SortKey: intPtr(42),
			},
			expected: `pyck_sort_key = 42`,
		},
		{
			name: "sortKeyGt emits range predicate",
			where: &model.WorkflowExecutionsWhereInput{
				SortKeyGt: intPtr(100),
			},
			expected: `pyck_sort_key > 100`,
		},
		{
			name: "sortKeyIn emits IN against pyck_sort_key",
			where: &model.WorkflowExecutionsWhereInput{
				SortKeyIn: []int{1, 2, 3},
			},
			expected: `pyck_sort_key IN (1, 2, 3)`,
		},
		{
			name: "sortKeyIsNil emits IS NULL",
			where: &model.WorkflowExecutionsWhereInput{
				SortKeyIsNil: boolPtr(true),
			},
			expected: `pyck_sort_key IS NULL`,
		},
		{
			name: "sortKeyNotNil emits IS NOT NULL",
			where: &model.WorkflowExecutionsWhereInput{
				SortKeyNotNil: boolPtr(true),
			},
			expected: `pyck_sort_key IS NOT NULL`,
		},
		{
			name: "sortKeyGte and sortKeyLte combine with AND",
			where: &model.WorkflowExecutionsWhereInput{
				SortKeyGte: intPtr(10),
				SortKeyLte: intPtr(99),
			},
			expected: `pyck_sort_key >= 10 AND pyck_sort_key <= 99`,
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
