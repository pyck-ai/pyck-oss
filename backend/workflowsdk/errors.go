package workflowsdk

import (
	"context"
	"errors"
	"net/http"

	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WorkflowError wraps an error with metadata for Temporal workflows.
// It provides retryability information and converts to Temporal ApplicationError.
type WorkflowError struct {
	err         error
	message     string
	isRetryable bool
}

// Error implements the error interface.
func (e *WorkflowError) Error() string {
	return e.err.Error()
}

// IsRetryable returns whether this error should trigger a retry.
func (e *WorkflowError) IsRetryable() bool {
	return e.isRetryable
}

// Message returns the error message.
func (e *WorkflowError) Message() string {
	if e.message != "" {
		return e.message
	}
	if e.err != nil {
		return e.err.Error()
	}
	return "unknown error"
}

// ToApplicationError converts this WorkflowError to a Temporal ApplicationError.
func (e *WorkflowError) ToApplicationError() error {
	opts := temporal.ApplicationErrorOptions{
		Cause:        e.err,
		NonRetryable: !e.isRetryable,
	}

	return temporal.NewApplicationErrorWithOptions(
		e.Message(),
		e.Error(),
		opts,
	)
}

// Standard static errors aligned with stdlib conventions
var (
	errNotFoundBase         = errors.New("not found")
	errInvalidBase          = errors.New("invalid")
	errUnavailableBase      = errors.New("unavailable")
	errAlreadySetBase       = errors.New("already set")
	errInvalidArgsBase      = errors.New("invalid arguments")
	errRegisterActivityBase = errors.New("register activity failed")
	errRegisterWorkflowBase = errors.New("register workflow failed")

	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = &WorkflowError{
		err:         errNotFoundBase,
		message:     "resource not found",
		isRetryable: false,
	}

	// ErrInvalid is returned when input validation fails.
	ErrInvalid = &WorkflowError{
		err:         errInvalidBase,
		message:     "invalid input",
		isRetryable: false,
	}

	// ErrUnavailable is returned when an upstream service is unavailable.
	ErrUnavailable = &WorkflowError{
		err:         errUnavailableBase,
		message:     "service unavailable",
		isRetryable: true,
	}

	// ErrAlreadySet is returned when attempting to set a value that's already
	// set.
	ErrAlreadySet = &WorkflowError{
		err:         errAlreadySetBase,
		message:     "value already set",
		isRetryable: false,
	}

	// ErrInvalidArgs is returned when invalid arguments are provided.
	ErrInvalidArgs = &WorkflowError{
		err:         errInvalidArgsBase,
		message:     "invalid arguments",
		isRetryable: false,
	}

	// ErrRegisterActivity is returned when activity registration fails.
	ErrRegisterActivity = &WorkflowError{
		err:         errRegisterActivityBase,
		message:     "failed to register activity",
		isRetryable: false,
	}

	// ErrRegisterWorkflow is returned when workflow registration fails.
	ErrRegisterWorkflow = &WorkflowError{
		err:         errRegisterWorkflowBase,
		message:     "failed to register workflow",
		isRetryable: false,
	}
)

// WrapError creates a new WorkflowError wrapping an existing error.
func WrapError(err error, message string, isRetryable bool) *WorkflowError {
	return &WorkflowError{
		err:         err,
		message:     message,
		isRetryable: isRetryable,
	}
}

// ApplicationError wraps a WorkflowError and converts it to a Temporal
// ApplicationError. If cause is nil, it uses the base error directly.
// Otherwise, it wraps the cause.
func ApplicationError(base *WorkflowError, cause error) error {
	if cause == nil {
		return base.ToApplicationError()
	}

	wrapped := WrapError(cause, base.Message(), base.IsRetryable())
	return wrapped.ToApplicationError()
}

// HandleError classifies and wraps errors from HTTP, gRPC, or GraphQL calls.
//
// Deprecated: Use HandleNetworkError() instead.
func HandleError(ctx context.Context, err error, msg string) error {
	return HandleNetworkError(ctx, err, msg)
}

// HandleNetworkError classifies and wraps errors from HTTP, gRPC, or GraphQL
// calls. returns an appropriate WorkflowError converted to a Temporal
// ApplicationError.
//
// Error classification:
// - HTTP: 404 → ErrNotFound, 4xx → ErrInvalid, 5xx → ErrUnavailable
// - gRPC: NotFound → ErrNotFound, InvalidArgument/FailedPrecondition → ErrInvalid, others → ErrUnavailable
func HandleNetworkError(ctx context.Context, err error, msg string) error {
	// Check gRPC status errors
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.NotFound:
			return ApplicationError(ErrNotFound, err)
		case codes.InvalidArgument, codes.FailedPrecondition:
			return ApplicationError(ErrInvalid, err)
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
			return ApplicationError(ErrUnavailable, err)
		default:
			return ApplicationError(ErrUnavailable, err)
		}
	}

	// Check for HTTP-style errors (custom HTTPError type or status code check)
	type httpError interface {
		StatusCode() int
	}
	if he, ok := err.(httpError); ok {
		code := he.StatusCode()
		switch {
		case code == http.StatusNotFound:
			return ApplicationError(ErrNotFound, err)
		case code >= 400 && code < 500:
			return ApplicationError(ErrInvalid, err)
		case code >= 500 && code < 600:
			return ApplicationError(ErrUnavailable, err)
		}
	}

	// Default to unavailable for unknown errors (retryable)
	return ApplicationError(ErrUnavailable, err)
}

func IsError(err error, target *WorkflowError) bool {
	var ae *temporal.ApplicationError
	if errors.As(err, &ae) {
		if ae.Type() == target.Error() {
			return true
		}
	}

	var we *WorkflowError
	if errors.As(err, &we) {
		if we.Error() == target.Error() {
			return true
		}
	}

	return errors.Is(err, target)
}
