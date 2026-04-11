package main

import "errors"

// Sentinel errors for apigen operations.
// Use these only when there's no underlying error to wrap.
var (
	// ErrSkipInterfaceUnion is returned when skipping interface/union types
	ErrSkipInterfaceUnion = errors.New("skipping interface/union type")

	// ErrNoGraphQLFiles is returned when no GraphQL files are found
	ErrNoGraphQLFiles = errors.New("no .graphql files found in directory")

	// ErrSchemaDirNotExist is returned when the schema directory does not exist
	ErrSchemaDirNotExist = errors.New("schema directory does not exist")
)
