//nolint:testpackage // in-package test required: the validators are unexported.
package resolvers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

func TestValidateWorkflowExecutionIDs(t *testing.T) {
	t.Parallel()

	tooLong := strings.Repeat("a", commonworkflow.MaxWorkflowIDLength+1)

	testCases := []struct {
		name        string
		workflowID  string
		executionID string
		wantErr     error
	}{
		{"valid", "wf", "run", nil},
		{"empty workflow id", "", "run", ErrInvalidWorkflowID},
		{"oversize workflow id", tooLong, "run", ErrInvalidWorkflowID},
		{"NUL in workflow id", "wf\x00id", "run", ErrInvalidWorkflowID},
		{"empty run id", "wf", "", ErrInvalidWorkflowExecutionID},
		{"oversize run id", "wf", tooLong, ErrInvalidWorkflowExecutionID},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateWorkflowExecutionIDs(tc.workflowID, tc.executionID)
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestValidateUpdateName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		updateName string
		wantErr    bool
	}{
		{"valid", "input", false},
		{"dot", "pkg.Input", false},
		{"slash", "a/b", false},
		{"generic type name", "Confirm[example.com/pkg.Foo]", false},
		{"non-ASCII letter", "Café", false},
		{"at the limit", strings.Repeat("a", commonworkflow.MaxWorkflowIDLength), false},
		{"empty", "", true},
		{"blank", "   ", true},
		{"over the limit", strings.Repeat("a", commonworkflow.MaxWorkflowIDLength+1), true},
		{"SetAssignee", commonworkflow.WorkflowQueryTypeSetAssignee.String(), true},
		{"SetTargets, a step ID", commonworkflow.WorkflowQueryTypeSetTargets.String(), false},
		{"SetIsAssignable", commonworkflow.WorkflowQueryTypeSetIsAssignable.String(), true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateUpdateName(tc.updateName)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidUpdateName)
		})
	}
}
