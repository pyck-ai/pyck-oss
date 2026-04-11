package otel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/99designs/gqlgen/graphql"
	"github.com/go-chi/chi/v5"
	"github.com/riandyrn/otelchi"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware wraps a tracing middleware and allows skipping specific paths.
// The skipPaths are converted to a map for O(1) lookup performance.
func HTTPMiddleware(serviceName string, router *chi.Mux) func(http.Handler) http.Handler {
	return otelchi.Middleware(serviceName, otelchi.WithChiRoutes(router))
}

// GraphQLTracingMiddleware is a GraphQL extension that adds query information to OpenTelemetry traces
type GraphQLTracingMiddleware struct{}

// ExtensionName returns the name of the extension
func (e *GraphQLTracingMiddleware) ExtensionName() string {
	return "GraphQLTracing"
}

// Validate validates the extension against the schema
func (e *GraphQLTracingMiddleware) Validate(schema graphql.ExecutableSchema) error {
	return nil
}

// InterceptOperation intercepts GraphQL operations and adds tracing information
func (e *GraphQLTracingMiddleware) InterceptOperation(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	return next(ctx)
}

// InterceptField intercepts GraphQL field resolution and adds query information to the trace
func (e *GraphQLTracingMiddleware) InterceptField(ctx context.Context, next graphql.Resolver) (interface{}, error) {
	fieldCtx := graphql.GetFieldContext(ctx)

	if fieldCtx == nil {
		return next(ctx)
	}

	// Only add query information at the root level to avoid duplicate attributes
	if len(fieldCtx.Path()) > 1 {
		return next(ctx)
	}

	opCtx := graphql.GetOperationContext(ctx)
	if opCtx == nil {
		return next(ctx)
	}

	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return next(ctx)
	}

	// Add GraphQL-specific attributes to the trace
	attributes := []attribute.KeyValue{
		attribute.String("graphql.operation.type", string(opCtx.Operation.Operation)),
	}

	// Extract the operation name from the operation definition if available
	operationName := extractOperationName(opCtx)
	if operationName != "" {
		attributes = append(attributes, attribute.String("graphql.operation.name", operationName))
	}

	// Add the GraphQL query
	if opCtx.RawQuery != "" {
		attributes = append(attributes, attribute.String("graphql.query", opCtx.RawQuery))
	}

	// Add variables if available (as a JSON-like string)
	if len(opCtx.Variables) > 0 {
		variablesStr := formatVariables(opCtx.Variables)
		attributes = append(attributes, attribute.String("graphql.variables", variablesStr))
	}

	// Add the attributes to the span
	span.SetAttributes(attributes...)

	// Also update the span name to include the operation name
	if operationName != "" {
		span.SetName(fmt.Sprintf("%s %s", string(opCtx.Operation.Operation), operationName))
	} else {
		span.SetName(string(opCtx.Operation.Operation))
	}

	return next(ctx)
}

// extractOperationName extracts the operation name from the GraphQL operation context
// It first checks for the explicit operationName, then falls back to the operation definition name
func extractOperationName(opCtx *graphql.OperationContext) string {
	// First, check if an explicit operation name was provided in the request
	if opCtx.OperationName != "" {
		return opCtx.OperationName
	}

	// If no explicit operation name, try to extract from the operation definition
	if opCtx.Operation != nil && opCtx.Operation.Name != "" {
		return opCtx.Operation.Name
	}

	return ""
}

// formatVariables converts the variables map to a string representation
func formatVariables(variables map[string]interface{}) string {
	jsonData, err := json.Marshal(variables)
	if err != nil {
		return "{}"
	}

	return string(jsonData)
}

// NewTracingMiddleware creates a new GraphQL tracing extension
//
//nolint:ireturn // Returning an interface is idiomatic for factory functions
func NewTracingMiddleware() graphql.HandlerExtension {
	return &GraphQLTracingMiddleware{}
}
