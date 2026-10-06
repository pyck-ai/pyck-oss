package resolvers

import (
	"fmt"

	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

// validateWorkflowExecutionIDs fails fast, with our own sentinels, on IDs
// Temporal would reject or stall on. Run IDs are UUIDs; their cap is a backstop.
func validateWorkflowExecutionIDs(workflowID, executionID string) error {
	if commonworkflow.ValidateWorkflowID(workflowID) != nil {
		return fmt.Errorf("%w %q: must be 1-%d bytes without NUL", ErrInvalidWorkflowID, workflowID, commonworkflow.MaxWorkflowIDLength)
	}
	if executionID == "" || len(executionID) > commonworkflow.MaxWorkflowIDLength {
		return fmt.Errorf("%w %q: must be 1-%d bytes", ErrInvalidWorkflowExecutionID, executionID, commonworkflow.MaxWorkflowIDLength)
	}
	return nil
}
