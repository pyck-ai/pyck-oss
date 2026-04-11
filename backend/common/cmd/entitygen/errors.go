package main

import "errors"

// Sentinel errors for entitygen operations.
var (
	// ErrExtractEntities is returned when extracting entities from schema files fails.
	ErrExtractEntities = errors.New("failed to extract entities")

	// ErrGenerateFile is returned when generating the output file fails.
	ErrGenerateFile = errors.New("failed to generate file")

	// ErrGlobSchemaFiles is returned when globbing schema files fails.
	ErrGlobSchemaFiles = errors.New("failed to glob schema files")

	// ErrExtractType is returned when extracting type from a file fails.
	ErrExtractType = errors.New("failed to extract type")

	// ErrParseFile is returned when parsing a file fails.
	ErrParseFile = errors.New("failed to parse file")

	// ErrParseTemplate is returned when parsing a template fails.
	ErrParseTemplate = errors.New("failed to parse template")

	// ErrExecuteTemplate is returned when executing a template fails.
	ErrExecuteTemplate = errors.New("failed to execute template")
)
