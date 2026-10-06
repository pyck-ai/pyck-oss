package resolvers

import "errors"

// Static errors for err113 compliance

var (
	ErrInvalidWorkflowClient = errors.New("invalid workflowClient")
	ErrWorkflowNotRunning    = errors.New("selected workflow is not running")
	ErrInvalidName           = errors.New("invalid name: leading/trailing spaces are not allowed")
	ErrInvalidTaskQueue      = errors.New("invalid task queue: leading/trailing spaces are not allowed")
	ErrWorkerIDRequired      = errors.New("invalid worker id: a worker id is required")
	// ErrTaskQueueChanged is returned when a registration names a different
	// task queue than the workflow already has; the queue is immutable. To move
	// a workflow, an operator stops the old workers, deletes the workflow, then
	// deploys on the new queue.
	ErrTaskQueueChanged            = errors.New("workflow task queue cannot be changed")
	ErrWorkerIDTooLong             = errors.New("invalid worker id: exceeds the maximum length")
	ErrInvalidSignalTopic          = errors.New("invalid workflow signal: invalid nats topic")
	ErrSignalTopicNoTenant         = errors.New("invalid workflow signal: topic must contain tenant information")
	ErrSignalTopicPermission       = errors.New("invalid workflow signal: invalid nats topic: permission denied")
	ErrDuplicateSignalSubscription = errors.New("duplicate signal subscription")
	ErrInvalidSignalName           = errors.New("invalid workflow signal: signal name is required")
	ErrWorkflowNotFound            = errors.New("workflow not found")
	ErrWorkflowClientNotAvailable  = errors.New("workflow client not available")
	ErrInvalidWorkflowID           = errors.New("invalid workflow id")
	ErrInvalidWorkflowExecutionID  = errors.New("invalid workflow execution id")
	ErrTenantNotFound              = errors.New("tenant not found")
	ErrTenantUITemplatesNotSet     = errors.New("tenant has no UI bundle URL templates")
	ErrSingleTenantRequired        = errors.New("a single tenant must be selected")
	ErrAdminRoleRequired           = errors.New("admin role required")
	ErrWriterRoleRequired          = errors.New("writer role required")
	ErrExecutionTenantUnknown      = errors.New("cannot resolve workflow execution tenant")
)
