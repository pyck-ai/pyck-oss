package types

import "errors"

// Sentinel errors returned by brunogen operations.
var (
	ErrGraphQLFileNotExist  = errors.New("GraphQL file does not exist")
	ErrInvalidGraphQLFile   = errors.New("invalid GraphQL file")
	ErrNoOperations         = errors.New("no operations found in GraphQL file")
	ErrInvalidServicePath   = errors.New("invalid service path: cannot detect service name")
	ErrOperationRequired    = errors.New("operation is required")
	ErrOperationNotFound    = errors.New("operation not found")
	ErrScenarioNameRequired = errors.New("name is required")
	ErrUnqualifiedOperation = errors.New("operation must be fully qualified as \"service.operationName\"")
	ErrServiceNameRequired  = errors.New("service name is required for tests generation")
	ErrMissingOperation     = errors.New("missing required 'operation' field")
)
