//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors
//go:generate enumer -output=fieldname_gen.go -type=FieldName -trimprefix=Field

// Package fieldnames provides type-safe struct field name constants used across packages.
// This is an internal package to avoid circular dependencies.
//
// FieldName is an enum representing Go struct field names (PascalCase) for reflection operations.
// These are NOT database column names - for those, use the DBColumn* constants.
//
// This type provides compile-time safety when passing field names between functions.
// Convert to string using .String() only when calling reflection APIs:
//
//	func extractField(entity any, field FieldName) error {
//	    rv := reflect.ValueOf(entity)
//	    fv := rv.FieldByName(field.String())  // Convert at reflection boundary
//	    // ...
//	}
package fieldnames

// FieldName represents a Go struct field name for reflection operations.
type FieldName int

const (
	// FieldInvalid is the zero value sentinel for invalid/unspecified field names.
	// Using 0 as invalid follows Go enum best practices.
	FieldInvalid FieldName = iota

	// FieldData is the "Data" field name (JSON data maps in entities with DataMixin).
	FieldData

	// FieldJSONSchema is the "JSONSchema" field name.
	FieldJSONSchema

	// FieldTenantID is the "TenantID" field name (from TenantMixin).
	FieldTenantID

	// FieldID is the "ID" field name (primary key on all entities).
	FieldID

	// FieldEntityEventsOutbox is the "EntityEventsOutbox" field name (outbox schema).
	FieldEntityEventsOutbox

	// FieldDataTypeSlug is the "DataTypeSlug" field name (from DataMixin).
	FieldDataTypeSlug

	// FieldDeletedAt is the "DeletedAt" field name (from HistoryMixin).
	FieldDeletedAt
)
