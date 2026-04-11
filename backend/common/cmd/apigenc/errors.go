package main

import "errors"

// ErrNoImportPath is returned when import path cannot be determined
var ErrNoImportPath = errors.New("could not determine import path")

// ErrMissingEntReturnType is returned when a method lacks an Ent return type from resolvers
var ErrMissingEntReturnType = errors.New("method missing Ent return type from resolvers")

// ErrInvalidPackagePath is returned when service name and module base cannot be extracted from package path
var ErrInvalidPackagePath = errors.New("failed to extract service name and module base from package path")

// ErrDetectModelImport is returned when there is an error detecting model imports
var ErrDetectModelImport = errors.New("error detecting model imports")
