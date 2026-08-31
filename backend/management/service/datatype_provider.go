package service

import (
	"context"

	"github.com/google/uuid"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"

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

// ReadByID retrieves a datatype by its ID within the caller's mutation tenant.
// The tenant predicate is explicit because TenantMixin's privacy filter scopes
// regular users but SKIPS system users: without it a system caller acting on
// tenant A resolves tenant B's DataType by id and validates A's row against B's
// schema.
//
// Soft-deleted rows are filtered out by the HistoryMixin query policy (this
// path runs without FEATURE_SHOW_DELETED), so a missing, soft-deleted or
// foreign-tenant row is reported as the shared json_schema.ErrDataTypeNotFound
// — matching the cache/client DataTypeReader implementations the validator's
// error handling relies on.
func (p *DatabaseDataTypeProvider) ReadByID(ctx context.Context, id uuid.UUID) (*json_schema.DataType, error) {
	tenantID, ok := mutationTenant(ctx)
	if !ok {
		return nil, json_schema.ErrDataTypeNotFound
	}

	dt, err := p.client.DataType.
		Query().
		Where(
			datatype.ID(id),
			datatype.TenantID(tenantID),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, json_schema.ErrDataTypeNotFound
		}
		return nil, err
	}

	return toJSONSchemaDataType(dt), nil
}

// ReadBySlug returns the latest non-deleted DataType matching slug within the
// caller's mutation tenant. It serves entities that carry a data_type_slug
// without a data_type_id. The tenant predicate is explicit for the same reason
// as in ReadByID: relying on the privacy filter alone would resolve the
// globally highest version of the slug for a system caller — another tenant's
// row on a cross-tenant slug collision. Missing rows are reported as
// json_schema.ErrDataTypeNotFound (see ReadByID).
func (p *DatabaseDataTypeProvider) ReadBySlug(ctx context.Context, slug string) (*json_schema.DataType, error) {
	tenantID, ok := mutationTenant(ctx)
	if !ok {
		return nil, json_schema.ErrDataTypeNotFound
	}

	dt, err := p.client.DataType.
		Query().
		Where(
			datatype.Slug(slug),
			datatype.DeletedAtIsNil(),
			datatype.TenantID(tenantID),
		).
		Order(ent.Desc(datatype.FieldVersion)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, json_schema.ErrDataTypeNotFound
		}
		return nil, err
	}

	return toJSONSchemaDataType(dt), nil
}

// mutationTenant returns the single tenant the caller acts on, or false when
// the context names none or several. Callers refuse the read in that case
// rather than fall back to the privacy filter, which is a no-op for system
// users. MutationTenantID panics on an ambiguous context, hence the count check.
func mutationTenant(ctx context.Context) (uuid.UUID, bool) {
	req := request.ForContext(ctx)
	if !req.HasMutationTenantID() {
		return uuid.Nil, false
	}
	return req.MutationTenantID(), true
}

func toJSONSchemaDataType(dt *ent.DataType) *json_schema.DataType {
	out := &json_schema.DataType{
		ID:         dt.ID,
		Slug:       dt.Slug,
		TenantID:   dt.TenantID,
		JsonSchema: dt.JSONSchema,
		CreatedAt:  dt.CreatedAt,
		Version:    dt.Version,
	}
	if !dt.DeletedAt.IsZero() {
		t := dt.DeletedAt
		out.DeletedAt = &t
	}
	return out
}
