package resolvers

import "fmt"

// maxWorkflowIDLength is Temporal's documented upper bound for workflow IDs.
// Run IDs are always UUID strings (36 chars) but are validated with the same
// cap as a cheap defense-in-depth against pathological inputs.
const maxWorkflowIDLength = 1000

// validateWorkflowExecutionIDs enforces non-empty, bounded-length IDs before
// handing them off to Temporal. Temporal itself rejects oversize IDs, but
// bounding here lets us fail fast and keeps the error under our control
// (sentinel + %q), rather than surfacing a raw gRPC InvalidArgument.
func validateWorkflowExecutionIDs(workflowID, executionID string) error {
	if workflowID == "" || len(workflowID) > maxWorkflowIDLength {
		return fmt.Errorf("%w %q: must be 1-%d bytes", ErrInvalidWorkflowID, workflowID, maxWorkflowIDLength)
	}
	if executionID == "" || len(executionID) > maxWorkflowIDLength {
		return fmt.Errorf("%w %q: must be 1-%d bytes", ErrInvalidWorkflowExecutionID, executionID, maxWorkflowIDLength)
	}
	return nil
}
