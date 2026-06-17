// Package temporal implements the Temporal bootstrap logic for creating
// namespaces required by the platform.
package temporal

import (
	"context"
	"fmt"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/pyck-ai/pyck/backend/common/log"
	logadapter "github.com/pyck-ai/pyck/backend/common/log/adapter"
	"github.com/pyck-ai/pyck/backend/common/services/temporal"
	"github.com/pyck-ai/pyck/backend/common/workflow"
)

// bootstrap creates the given Temporal namespace if it does not already exist
// and registers the platform's custom search attributes.
func bootstrap(ctx context.Context, url string, namespace string) error {
	namespaceClient, err := temporal.NewTemporalNamespaceClient(ctx, url)
	if err != nil {
		return fmt.Errorf("failed to create temporal namespace client: %w", err)
	}
	defer namespaceClient.Close()

	if err := temporal.CreateTemporalNamespace(ctx, namespaceClient, namespace); err != nil {
		return fmt.Errorf("failed to create temporal namespace %q: %w", namespace, err)
	}

	if err := addSearchAttributes(ctx, url, namespace); err != nil {
		return fmt.Errorf("failed to add search attributes to namespace %q: %w", namespace, err)
	}

	return nil
}

// addSearchAttributes registers the platform's custom search attributes
// (e.g. pyck_workflow_name, pyck_tenant_id) on the given Temporal namespace.
//
// Lists existing attributes first and sends one batched AddSearchAttributes
// call for only the missing ones. The previous per-attribute loop tripped
// Temporal's per-namespace AddSearchAttributes rate limit on cold bootstrap.
func addSearchAttributes(ctx context.Context, url string, namespace string) error {
	logger := log.ForContext(ctx)

	client, err := temporalclient.Dial(temporalclient.Options{
		HostPort:  url,
		Namespace: namespace,
		Logger:    logadapter.TemporalSDKLogAdapter(*log.ForContext(ctx)),
	})
	if err != nil {
		return fmt.Errorf("failed to create temporal client: %w", err)
	}
	defer client.Close()

	operatorService := client.OperatorService()

	existing, err := operatorService.ListSearchAttributes(ctx, &operatorservice.ListSearchAttributesRequest{
		Namespace: namespace,
	})
	if err != nil {
		return fmt.Errorf("failed to list existing search attributes: %w", err)
	}

	missingSearchAttributes := make(map[string]enums.IndexedValueType)
	for _, attr := range workflow.SearchAttributes {
		if _, ok := existing.GetCustomAttributes()[attr.GetName()]; ok {
			continue
		}
		missingSearchAttributes[attr.GetName()] = attr.GetValueType()
	}

	if len(missingSearchAttributes) == 0 {
		logger.Debug().
			Int("registered", len(workflow.SearchAttributes)).
			Str("namespace", namespace).
			Msg("All search attributes already registered")
		return nil
	}

	logger.Debug().
		Int("count", len(missingSearchAttributes)).
		Str("namespace", namespace).
		Msg("Registering search attributes on Temporal namespace")

	if _, err := operatorService.AddSearchAttributes(ctx, &operatorservice.AddSearchAttributesRequest{
		Namespace:        namespace,
		SearchAttributes: missingSearchAttributes,
	}); err != nil {
		return fmt.Errorf("failed to add search attributes: %w", err)
	}

	logger.Debug().Msg("Search attributes registered successfully")
	return nil
}
