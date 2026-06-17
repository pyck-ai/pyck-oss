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

func (f *dataTypeClient) GetDataTypes(ctx context.Context) ([]json_schema.DataType, error) {
	logger := log.ForContext(ctx)

	pageSize := dataTypesPageSize
	var cursor *string
	var result []json_schema.DataType

	for {
		res, err := f.client.GetDataTypes(ctx, managementapi.GetDataTypesArgs{
			First: &pageSize,
			After: cursor,
		})
		if err != nil {
			logger.Err(err).Msg("opening connection to management")
			return nil, err
		}

		dataTypes := res.GetDataTypes()
		for _, edge := range dataTypes.GetEdges() {
			node := edge.GetNode()
			id, err := uuid.Parse(node.ID)
			if err != nil {
				return nil, fmt.Errorf("parse data type id %q: %w", node.ID, err)
			}
			slug := ""
			if node.Slug != nil {
				slug = *node.Slug
			}
			result = append(result, json_schema.DataType{
				ID:         id,
				JsonSchema: node.JSONSchema,
				Slug:       slug,
				TenantID:   node.TenantID,
			})
		}

		pageInfo := dataTypes.GetPageInfo()
		if !pageInfo.GetHasNextPage() {
			break
		}
		cursor = pageInfo.GetEndCursor()
	}

	return result, nil
}
