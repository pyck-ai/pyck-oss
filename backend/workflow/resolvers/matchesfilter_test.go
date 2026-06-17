package resolvers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/workflow/model"
	"github.com/pyck-ai/pyck/backend/workflow/resolvers"
)

func TestMatchesFilter(t *testing.T) {
	t.Parallel()

	const (
		name    = "shipOrder"
		enabled = true
	)

	tests := []struct {
		name     string
		where    *model.WorkflowActionsWhereInput
		expected bool
	}{
		{
			name:     "nil where matches everything",
			where:    nil,
			expected: true,
		},
		{
			name:     "empty where matches everything",
			where:    &model.WorkflowActionsWhereInput{},
			expected: true,
		},

		// name (exact)
		{
			name:     "name exact match",
			where:    &model.WorkflowActionsWhereInput{Name: stringPtr("shipOrder")},
			expected: true,
		},
		{
			name:     "name exact mismatch",
			where:    &model.WorkflowActionsWhereInput{Name: stringPtr("cancelOrder")},
			expected: false,
		},

		// nameNEQ
		{
			name:     "nameNEQ differs - match",
			where:    &model.WorkflowActionsWhereInput{NameNeq: stringPtr("cancelOrder")},
			expected: true,
		},
		{
			name:     "nameNEQ equal - no match",
			where:    &model.WorkflowActionsWhereInput{NameNeq: stringPtr("shipOrder")},
			expected: false,
		},

		// nameIn
		{
			name:     "nameIn contains - match",
			where:    &model.WorkflowActionsWhereInput{NameIn: []string{"a", "shipOrder", "b"}},
			expected: true,
		},
		{
			name:     "nameIn excludes - no match",
			where:    &model.WorkflowActionsWhereInput{NameIn: []string{"a", "b"}},
			expected: false,
		},
		{
			// An explicit empty list (nameIn: []) deserializes to a non-nil
			// empty slice; it must be treated as "unset" (match everything),
			// not "match nothing".
			name:     "nameIn empty slice - treated as unset",
			where:    &model.WorkflowActionsWhereInput{NameIn: []string{}},
			expected: true,
		},

		// nameNotIn
		{
			name:     "nameNotIn excludes - match",
			where:    &model.WorkflowActionsWhereInput{NameNotIn: []string{"a", "b"}},
			expected: true,
		},
		{
			name:     "nameNotIn contains - no match",
			where:    &model.WorkflowActionsWhereInput{NameNotIn: []string{"shipOrder"}},
			expected: false,
		},

		// nameContains
		{
			name:     "nameContains substring - match",
			where:    &model.WorkflowActionsWhereInput{NameContains: stringPtr("Order")},
			expected: true,
		},
		{
			name:     "nameContains is case-sensitive - no match",
			where:    &model.WorkflowActionsWhereInput{NameContains: stringPtr("order")},
			expected: false,
		},

		// nameHasPrefix
		{
			name:     "nameHasPrefix - match",
			where:    &model.WorkflowActionsWhereInput{NameHasPrefix: stringPtr("ship")},
			expected: true,
		},
		{
			name:     "nameHasPrefix wrong prefix - no match",
			where:    &model.WorkflowActionsWhereInput{NameHasPrefix: stringPtr("Order")},
			expected: false,
		},

		// nameHasSuffix
		{
			name:     "nameHasSuffix - match",
			where:    &model.WorkflowActionsWhereInput{NameHasSuffix: stringPtr("Order")},
			expected: true,
		},
		{
			name:     "nameHasSuffix wrong suffix - no match",
			where:    &model.WorkflowActionsWhereInput{NameHasSuffix: stringPtr("ship")},
			expected: false,
		},

		// nameEqualFold
		{
			name:     "nameEqualFold case-insensitive - match",
			where:    &model.WorkflowActionsWhereInput{NameEqualFold: stringPtr("SHIPORDER")},
			expected: true,
		},
		{
			name:     "nameEqualFold different value - no match",
			where:    &model.WorkflowActionsWhereInput{NameEqualFold: stringPtr("cancelorder")},
			expected: false,
		},

		// nameContainsFold
		{
			name:     "nameContainsFold case-insensitive substring - match",
			where:    &model.WorkflowActionsWhereInput{NameContainsFold: stringPtr("ORDER")},
			expected: true,
		},
		{
			name:     "nameContainsFold absent substring - no match",
			where:    &model.WorkflowActionsWhereInput{NameContainsFold: stringPtr("cancel")},
			expected: false,
		},

		// empty-string pointers are treated as unset (match everything),
		// mirroring FormatPredicate in the WorkflowExecutions path.
		{
			name:     "empty name pointer - treated as unset",
			where:    &model.WorkflowActionsWhereInput{Name: stringPtr("")},
			expected: true,
		},
		{
			name:     "empty nameNEQ pointer - treated as unset",
			where:    &model.WorkflowActionsWhereInput{NameNeq: stringPtr("")},
			expected: true,
		},
		{
			name:     "empty nameContains pointer - treated as unset",
			where:    &model.WorkflowActionsWhereInput{NameContains: stringPtr("")},
			expected: true,
		},
		{
			name:     "empty nameHasPrefix pointer - treated as unset",
			where:    &model.WorkflowActionsWhereInput{NameHasPrefix: stringPtr("")},
			expected: true,
		},

		// enabled (unchanged behaviour)
		{
			name:     "enabled matches",
			where:    &model.WorkflowActionsWhereInput{Enabled: boolPtr(true)},
			expected: true,
		},
		{
			name:     "enabled mismatch",
			where:    &model.WorkflowActionsWhereInput{Enabled: boolPtr(false)},
			expected: false,
		},

		// combinations are AND-joined
		{
			name: "name predicate and enabled both match",
			where: &model.WorkflowActionsWhereInput{
				NameHasPrefix: stringPtr("ship"),
				Enabled:       boolPtr(true),
			},
			expected: true,
		},
		{
			name: "name predicate matches but enabled mismatches",
			where: &model.WorkflowActionsWhereInput{
				NameHasPrefix: stringPtr("ship"),
				Enabled:       boolPtr(false),
			},
			expected: false,
		},
		{
			name: "enabled matches but name predicate fails",
			where: &model.WorkflowActionsWhereInput{
				NameContains: stringPtr("zzz"),
				Enabled:      boolPtr(true),
			},
			expected: false,
		},
		{
			name: "multiple name predicates all hold",
			where: &model.WorkflowActionsWhereInput{
				NameHasPrefix: stringPtr("ship"),
				NameHasSuffix: stringPtr("Order"),
				NameNeq:       stringPtr("cancelOrder"),
			},
			expected: true,
		},
		{
			name: "multiple name predicates one fails",
			where: &model.WorkflowActionsWhereInput{
				NameHasPrefix: stringPtr("ship"),
				NameHasSuffix: stringPtr("Ship"),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := resolvers.MatchesFilter(tt.where, name, enabled)
			assert.Equal(t, tt.expected, result)
		})
	}
}
