package otel

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.20.0"
)

// initProvider initializes the OpenTelemetry tracer provider.
// If cfg.OpenTelemetryEndpoint is empty, creates a TracerProvider without an exporter (noop).
func initProvider(serviceName, environment string, cfg *OTelConfig) (*sdktrace.TracerProvider, error) {
	logger := logr.Discard()

	// Create resource with service identification
	res, err := createResource(serviceName, environment)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create sampler from configuration
	sampler := NewSampler(cfg)

	// Build provider options
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	}

	// Create and add exporter if endpoint is configured
	if cfg.OpenTelemetryEndpoint != "" {
		createCtx, cancel := context.WithTimeout(context.Background(), cfg.OpenTelemetryTimeout)
		defer cancel()

		traceExporter, err := createExporter(createCtx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create trace exporter: %w", err)
		}

		opts = append(opts, sdktrace.WithBatcher(traceExporter))
	}

	// Create and register the tracer provider
	tp := sdktrace.NewTracerProvider(opts...)

	otel.SetLogger(logger)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp, nil
}

// createResource creates an OpenTelemetry resource with service identification.
func createResource(serviceName, environment string) (*resource.Resource, error) {
	return resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.DeploymentEnvironment(environment),
		),
	)
}
