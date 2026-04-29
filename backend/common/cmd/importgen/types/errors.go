package types

import "errors"

// Sentinel errors returned by importgen operations.
var (
	ErrMissingIdentityField = errors.New("missing identityField argument")
	ErrNoGraphQLFiles       = errors.New("no graphql files found")
	ErrNoBackendSegment     = errors.New("cannot find backend segment in import path")
	ErrClientFileNotFound   = errors.New("client file not found")
	ErrMethodNotFound       = errors.New("method not found in client interface")
)
