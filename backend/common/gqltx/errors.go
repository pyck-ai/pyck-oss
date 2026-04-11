package gqltx

import (
	"errors"

	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// ErrNoTransaction is returned when no transaction is found in context.
var ErrNoTransaction = errors.New("no transaction found in context")

// ErrNoPostCommitContainer is returned when post-commit container is missing from context.
var ErrNoPostCommitContainer = errors.New("gqltx: post-commit container not initialized; ensure Tx middleware was executed before resolver execution")

// ErrPostCommitAlreadyClosed is returned when post-commit hooks are registered or run after finalization.
var ErrPostCommitAlreadyClosed = errors.New("gqltx: post-commit container already closed or runPostCommit called more than once for the same transaction")

// ErrResponsePatchAlreadyClosed is returned when response patches are registered or run after finalization.
var ErrResponsePatchAlreadyClosed = errors.New("gqltx: response patches already closed or RunResponsePatches called more than once for the same transaction")

// ErrIsRetryable checks if any error in the list is retryable (e.g., a serialization failure).
func ErrIsRetryable(errs gqlerror.List) bool {
	for _, e := range errs {
		if e == nil {
			continue
		}
		if db.ErrIsRetryable(e.Unwrap()) {
			return true
		}
	}
	return false
}
