//nolint:testpackage // in-package test required: validateWorkflowExecutionIDs is unexported.
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
