package validator

import (
	"context"

	"github.com/google/uuid"
	common_jsonschema "github.com/pyck-ai/pyck/backend/common/json-schema"
)

type DataTypeReader interface {
	ReadBySlug(ctx context.Context, slug string) (*common_jsonschema.DataType, error)
	ReadByID(ctx context.Context, id uuid.UUID) (*common_jsonschema.DataType, error)
}
