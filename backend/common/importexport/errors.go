package importexport

import "errors"

// Sentinel errors for the importexport package. Callers can use errors.Is to
// check for specific failure conditions.
var (
	// ErrNoEntityTypes is returned when no entity types match the export/import request.
	ErrNoEntityTypes = errors.New("no entity types to export")

	// ErrUnknownEntityType is returned when a record references an unregistered type.
	ErrUnknownEntityType = errors.New("unknown entity type")

	// ErrMissingTypename is returned when a record is missing the __typename field.
	ErrMissingTypename = errors.New("missing required field __typename")

	// ErrInvalidTypeName is returned when __typename is present but empty or not a string.
	ErrInvalidTypeName = errors.New("__typename must be a non-empty string")

	// ErrRefInvalidValue is returned when a $ref value is not an object.
	ErrRefInvalidValue = errors.New("$ref value must be an object")

	// ErrRefMissingTypename is returned when a $ref is missing __typename.
	ErrRefMissingTypename = errors.New("$ref missing __typename")

	// ErrRefEmptyTypename is returned when a $ref __typename is empty or not a string.
	ErrRefEmptyTypename = errors.New("$ref __typename must be a non-empty string")

	// ErrRefUnknownType is returned when a $ref references an unregistered type.
	ErrRefUnknownType = errors.New("$ref references unknown type")

	// ErrRefMissingIdentity is returned when a $ref is missing the identity field.
	ErrRefMissingIdentity = errors.New("$ref missing identity field")

	// ErrRefNotFound is returned when a $ref cannot be resolved to an existing entity.
	ErrRefNotFound = errors.New("$ref not found")

	// ErrRefNoID is returned when a resolved entity has no string id field.
	ErrRefNoID = errors.New("resolved entity has no string id field")

	// ErrRefAmbiguous is returned when a $ref matches multiple entities.
	ErrRefAmbiguous = errors.New("$ref ambiguous")

	// ErrEmptyTypeName is returned when registering a descriptor with an empty TypeName.
	ErrEmptyTypeName = errors.New("EntityDescriptor.TypeName must not be empty")

	// ErrDuplicateRegistration is returned when registering a type that is already registered.
	ErrDuplicateRegistration = errors.New("duplicate registration")

	// ErrMissingID is returned when a created entity has no string id in the response.
	ErrMissingID = errors.New("created entity missing id in response")

	// ErrDuplicateAlias is returned when a $refid alias is used more than once.
	ErrDuplicateAlias = errors.New("duplicate $refid alias")

	// ErrUnknownAlias is returned when a string $ref references an alias not defined earlier.
	ErrUnknownAlias = errors.New("unknown $ref alias")
)
