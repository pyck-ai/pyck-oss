package resolvers

import "errors"

// Static errors for err113 compliance

var (
	ErrUnauthenticated             = errors.New("unauthenticated")
	ErrInvalidWorkflowClient       = errors.New("invalid workflowClient")
	ErrWorkflowNotRunning          = errors.New("selected workflow is not running")
	ErrInvalidName                 = errors.New("invalid name: leading/trailing spaces are not allowed")
	ErrInvalidTaskQueue            = errors.New("invalid task queue: leading/trailing spaces are not allowed")
	ErrInvalidSignalTopic          = errors.New("invalid workflow signal: invalid nats topic")
	ErrSignalTopicNoTenant         = errors.New("invalid workflow signal: topic must contain tenant information")
	ErrSignalTopicPermission       = errors.New("invalid workflow signal: invalid nats topic: permission denied")
	ErrDuplicateSignalSubscription = errors.New("duplicate signal subscription")
	ErrWorkflowNotFound            = errors.New("workflow not found")
	ErrWorkflowClientNotAvailable  = errors.New("workflow client not available")
	ErrInvalidWorkflowID           = errors.New("invalid workflow id")
	ErrInvalidWorkflowExecutionID  = errors.New("invalid workflow execution id")
	ErrTenantNotFound              = errors.New("tenant not found")
	ErrTenantUITemplatesNotSet     = errors.New("tenant has no UI bundle URL templates")
	ErrSingleTenantRequired        = errors.New("a single tenant must be selected for this query")
	ErrAdminRoleRequired           = errors.New("admin role required")
)
