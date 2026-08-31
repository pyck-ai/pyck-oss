package validator

import (
	"errors"
)

var (
	ErrNoUser               = errors.New("no user found in context")
	ErrNoDataType           = errors.New("no data type id or slug provided")
	ErrDataTypeNotFound     = errors.New("data type not found")
	ErrDataTypeNotSet       = errors.New("data type not set for validation")
	ErrDataTypeDeleted      = errors.New("data type is deleted")
	ErrDataTypeSlugMismatch = errors.New("data type id and slug refer to different data types")
	ErrFieldNotUnique       = errors.New("field value unique constraint violated")
	ErrInvalidTable         = errors.New("invalid table name")
	ErrInvalidJSONColumn    = errors.New("invalid column name")
	ErrInvalidJSONField     = errors.New("invalid field name")
	ErrUnsupportedDialect   = errors.New("unsupported SQL dialect")
)
