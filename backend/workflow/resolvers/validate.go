package resolvers

import (
	"fmt"
	"slices"

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

// reservedUpdateNames are the SDK's built-in updates that have their own
// mutation, which checks its input; the workflow itself cannot, e.g. whether
// an assignee is a user of the tenant. SetTargets is not reserved: it is also
// the ID of the WorkflowTargetsUpdater step, which the UI submits here, and
// the workflow decodes its targets into the enum, refusing unknown ones.
var reservedUpdateNames = []string{ //nolint:gochecknoglobals
	commonworkflow.WorkflowQueryTypeSetAssignee.String(),
	commonworkflow.WorkflowQueryTypeSetIsAssignable.String(),
}

// validateUpdateName refuses a blank or over-long update name, or one reserved
// for a dedicated mutation.
func validateUpdateName(name string) error {
	if commonworkflow.ValidateUpdateName(name) != nil {
		return fmt.Errorf("%w: must be 1-%d bytes and not blank", ErrInvalidUpdateName, commonworkflow.MaxWorkflowIDLength)
	}
	if slices.Contains(reservedUpdateNames, name) {
		return fmt.Errorf("%w %q: reserved for a dedicated mutation", ErrInvalidUpdateName, name)
	}
	return nil
}
