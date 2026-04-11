package events

import "github.com/pyck-ai/pyck/backend/common/internal/fieldnames"

// FieldName is a type-safe enum for Go struct field names used in reflection operations.
// This type is re-exported from the internal package for use by services.
type FieldName = fieldnames.FieldName

// Field name constants re-exported from internal/fieldnames.
const (
	// FieldInvalid is the zero value sentinel for invalid/unspecified field names.
	FieldInvalid      = fieldnames.FieldInvalid
	FieldData         = fieldnames.FieldData
	FieldJSONSchema   = fieldnames.FieldJSONSchema
	FieldTenantID     = fieldnames.FieldTenantID
	FieldID           = fieldnames.FieldID
	FieldDataTypeSlug = fieldnames.FieldDataTypeSlug
	FieldDeletedAt    = fieldnames.FieldDeletedAt
)
