package json_schema

import (
	"context"

	"github.com/google/uuid"
)

// DataTypeValidator is the narrow subset of *validator.Validator that the
// generated SetInputWithDataType ent builder methods depend on.
//
// It lives in this package (not common/validator) so generated ent code can
// import it without pulling in the full validator package and without an import
// cycle — validator already depends on json-schema, so the reverse import would
// be a cycle. *validator.Validator satisfies this interface structurally
// (its ValidateDataTypeInput returns *json_schema.DataType).
type DataTypeValidator interface {
	ValidateDataTypeInput(
		ctx context.Context,
		strict bool,
		input map[string]any,
		dataTypeID *uuid.UUID,
		dataTypeSlug *string,
	) (*DataType, error)
}
