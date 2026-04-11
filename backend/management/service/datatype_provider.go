package service

import (
	"context"

	"github.com/google/uuid"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/datatype"
)

// DatabaseDataTypeProvider provides datatype access via direct database queries
// This is used by the management service itself since it can't use the GraphQL cache
type DatabaseDataTypeProvider struct {
	client *ent.Client
}

// NewDatabaseDataTypeProvider creates a new database-backed datatype provider
func NewDatabaseDataTypeProvider(client *ent.Client) *DatabaseDataTypeProvider {
	return &DatabaseDataTypeProvider{
		client: client,
	}
}

// ReadByID retrieves a datatype by its ID
func (p *DatabaseDataTypeProvider) ReadByID(ctx context.Context, id uuid.UUID) (*json_schema.DataType, error) {
	// Use debug client to log the actual SQL query
	dt, err := p.client.DataType.
		Query().
		Where(datatype.ID(id)).
		First(ctx)

	if err != nil {
		return nil, err
	}

	return &json_schema.DataType{
		ID:         dt.ID,
		Slug:       dt.Slug,
		TenantID:   dt.TenantID,
		JsonSchema: dt.JSONSchema,
	}, nil
}

// ReadBySlug retrieves a datatype by its slug and tenant ID
func (p *DatabaseDataTypeProvider) ReadBySlug(ctx context.Context, slug string) (*json_schema.DataType, error) {
	// Use debug client to log the actual SQL query
	dt, err := p.client.DataType.
		Query().
		Where(datatype.Slug(slug)).
		First(ctx)

	if err != nil {
		return nil, err
	}

	return &json_schema.DataType{
		ID:         dt.ID,
		Slug:       dt.Slug,
		TenantID:   dt.TenantID,
		JsonSchema: dt.JSONSchema,
	}, nil
}
