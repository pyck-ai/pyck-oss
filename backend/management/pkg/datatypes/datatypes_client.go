// Package datatypes provides a client wrapper for fetching data type
// definitions from the management service.
package datatypes

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/log"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"
)

// dataTypesPageSize is the page size used when paginating the management
// service's dataTypes query.
const dataTypesPageSize = 50

// dataTypesFetcher is the narrow subset of managementapi.Client this package
// depends on. Defining it here keeps the package decoupled from the full
// management API surface and makes the pagination loop straightforward to
// test with a small fake.
type dataTypesFetcher interface {
	GetDataTypes(ctx context.Context, input managementapi.GetDataTypesArgs) (*managementapi.GetDataTypes, error)
}

// dataTypeClient implements json_schema.DataTypesClient by paginating through
// the management service's GetDataTypes GraphQL query.
type dataTypeClient struct {
	client dataTypesFetcher
}

// NewDataTypeClient returns a dataTypeClient that retrieves all data types
// from the management service via the GraphQL API with cursor-based pagination.
func NewDataTypeClient(client managementapi.Client) *dataTypeClient {
	return &dataTypeClient{client: client}
}

// GetDataTypes paginates over every non-deleted DataType row, ordered by
// version ASC so that the cache's slug index ends up pointing at the latest
// (highest) version of each (tenant, slug) pair after the initial load.
func (f *dataTypeClient) GetDataTypes(ctx context.Context) ([]json_schema.DataType, error) {
	logger := log.ForContext(ctx)

	pageSize := dataTypesPageSize
	var cursor *string
	var result []json_schema.DataType

	orderField := managementapi.DataTypeOrderFieldVersion
	orderBy := &managementapi.DataTypeOrder{
		Direction: managementapi.OrderDirectionAsc,
		Field:     &orderField,
	}

	for {
		res, err := f.client.GetDataTypes(ctx, managementapi.GetDataTypesArgs{
			First:   &pageSize,
			After:   cursor,
			OrderBy: orderBy,
		})
		if err != nil {
			logger.Err(err).Msg("opening connection to management")
			return nil, err
		}

		dataTypes := res.GetDataTypes()
		for _, edge := range dataTypes.GetEdges() {
			dt, err := mapNode(edge.GetNode())
			if err != nil {
				return nil, err
			}
			result = append(result, dt)
		}

		pageInfo := dataTypes.GetPageInfo()
		if !pageInfo.GetHasNextPage() {
			break
		}
		cursor = pageInfo.GetEndCursor()
	}

	return result, nil
}

// GetDataTypeByID returns the DataType matching (tenantID, id). The tenant
// is filtered explicitly via the WhereInput predicate — NOT left to the
// management query's tenant context: every downstream service runs this
// client under a system token, and management's tenant privacy filter skips
// system users entirely, so an id-only query would resolve a foreign
// tenant's row (GetDataTypeBySlug scopes explicitly for the same reason).
// Returns ErrDataTypeNotFound when the row is missing or soft-deleted. Used
// as the cache-miss fallback path in DataTypesCache.ReadByID — needed to
// bridge the NATS-propagation race window between management's create and
// the downstream services receiving the event.
func (f *dataTypeClient) GetDataTypeByID(ctx context.Context, id uuid.UUID, tenantID uuid.UUID) (*json_schema.DataType, error) {
	one := 1
	idStr := id.String()
	res, err := f.client.GetDataTypes(ctx, managementapi.GetDataTypesArgs{
		First: &one,
		Where: &managementapi.DataTypeWhereInput{
			ID:       &idStr,
			TenantID: &tenantID,
		},
	})
	if err != nil {
		return nil, err
	}

	edges := res.GetDataTypes().GetEdges()
	if len(edges) == 0 {
		return nil, json_schema.ErrDataTypeNotFound
	}
	dt, err := mapNode(edges[0].GetNode())
	if err != nil {
		return nil, err
	}
	if dt.DeletedAt != nil {
		return nil, json_schema.ErrDataTypeNotFound
	}
	return &dt, nil
}

// GetDataTypeBySlug returns the latest non-deleted DataType matching
// (tenantID, slug). The query orders by version DESC + first:1 to pick the
// highest (newest) version; entries are still filtered for DeletedAt nil at the
// cache layer for defense in depth. tenantID is filtered explicitly via the
// WhereInput predicate (not the request context), so the result is correctly
// scoped even when the caller authenticates as a system user — slugs are not
// unique across tenants, so an unscoped query would resolve the global
// highest-version row.
func (f *dataTypeClient) GetDataTypeBySlug(ctx context.Context, slug string, tenantID uuid.UUID) (*json_schema.DataType, error) {
	one := 1
	orderField := managementapi.DataTypeOrderFieldVersion
	res, err := f.client.GetDataTypes(ctx, managementapi.GetDataTypesArgs{
		First: &one,
		OrderBy: &managementapi.DataTypeOrder{
			Direction: managementapi.OrderDirectionDesc,
			Field:     &orderField,
		},
		Where: &managementapi.DataTypeWhereInput{
			Slug:     &slug,
			TenantID: &tenantID,
		},
	})
	if err != nil {
		return nil, err
	}

	edges := res.GetDataTypes().GetEdges()
	if len(edges) == 0 {
		return nil, json_schema.ErrDataTypeNotFound
	}
	dt, err := mapNode(edges[0].GetNode())
	if err != nil {
		return nil, err
	}
	if dt.DeletedAt != nil {
		return nil, json_schema.ErrDataTypeNotFound
	}
	return &dt, nil
}

// mapNode adapts the apigen GraphQL edge node to the cache's DataType shape.
func mapNode(node *managementapi.GetDataTypes_DataTypes_Edges_Node) (json_schema.DataType, error) {
	id, err := uuid.Parse(node.ID)
	if err != nil {
		return json_schema.DataType{}, fmt.Errorf("parse data type id %q: %w", node.ID, err)
	}
	slug := ""
	if node.Slug != nil {
		slug = *node.Slug
	}
	return json_schema.DataType{
		ID:         id,
		Name:       node.Name,
		JsonSchema: node.JSONSchema,
		Slug:       slug,
		TenantID:   node.TenantID,
		Version:    node.Version,
		CreatedAt:  node.CreatedAt,
		DeletedAt:  node.DeletedAt,
	}, nil
}
