package temporalsetup

import (
	"context"
	"fmt"

	"github.com/pyck-ai/pyck/backend/common/services/temporal"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
)

func AddSearchAttributes(ctx context.Context, input addSearchAttributesInput) error {
	client, err := temporal.NewTemporalClient(ctx, input.TemporalUrl)
	if err != nil {
		return err
	}

	operatorService := client.OperatorService()

	searchAttributes := make(map[string]enums.IndexedValueType)

	for name, attributeType := range input.SearchAttributes {
		searchAttributes[name] = enums.IndexedValueType(SearchAttributeTypes[attributeType])
	}

	request := &operatorservice.AddSearchAttributesRequest{
		Namespace:        input.Namespace,
		SearchAttributes: searchAttributes,
	}

	_, err = operatorService.AddSearchAttributes(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to add search attributes: %w", err)
	}
	return nil
}
