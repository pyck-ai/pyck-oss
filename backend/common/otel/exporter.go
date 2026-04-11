package otel

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var ErrUnsupportedOTLPProtocol = errors.New("unsupported OTLP protocol")

// createExporter creates the appropriate OpenTelemetry span exporter based on the protocol.
// Supported protocols are ProtocolTypeGRPC, ProtocolTypeHTTP, and ProtocolTypeHTTPProtobuf.
// For gRPC, TLS is enabled by default. Only endpoints with explicit "http://" scheme use insecure connections.
// For HTTP, the endpoint URL is used as-is.
//
//nolint:ireturn // Returning an interface is idiomatic for factory functions
func createExporter(ctx context.Context, cfg *OTelConfig) (sdktrace.SpanExporter, error) {
	switch cfg.OpenTelemetryProtocol {
	case ProtocolTypeGRPC:
		return createGRPCExporter(ctx, cfg)
	case ProtocolTypeHTTP, ProtocolTypeHTTPProtobuf:
		return createHTTPExporter(ctx, cfg)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedOTLPProtocol, cfg.OpenTelemetryProtocol)
	}
}

// createGRPCExporter creates a gRPC OTLP exporter with TLS enabled by default.
//
//nolint:ireturn // Returning an interface is idiomatic for factory functions
func createGRPCExporter(ctx context.Context, cfg *OTelConfig) (sdktrace.SpanExporter, error) {
	// Parse the endpoint URL to extract host and port
	parsedURL, err := url.Parse(cfg.OpenTelemetryEndpoint)
	if err != nil {
		// If parsing fails, assume it's just a host:port format (secure by default)
		parsedURL = &url.URL{Host: cfg.OpenTelemetryEndpoint, Scheme: "https"}
	}

	// If no scheme provided, default to secure
	if parsedURL.Scheme == "" {
		parsedURL.Scheme = "https"
	}

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(parsedURL.Host),
	}

	// Add authorization header if configured
	if cfg.OpenTelemetryAuthorization != "" {
		headers := map[string]string{
			"Authorization": cfg.OpenTelemetryAuthorization,
		}
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}

	// Only use insecure connection if explicitly set to http://
	// TLS is the default for gRPC connections
	if parsedURL.Scheme == "http" {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	return otlptracegrpc.New(ctx, opts...)
}

// createHTTPExporter creates an HTTP OTLP exporter.
//
//nolint:ireturn // Returning an interface is idiomatic for factory functions
func createHTTPExporter(ctx context.Context, cfg *OTelConfig) (sdktrace.SpanExporter, error) {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.OpenTelemetryEndpoint),
	}

	// Add authorization header if configured
	if cfg.OpenTelemetryAuthorization != "" {
		headers := map[string]string{
			"Authorization": cfg.OpenTelemetryAuthorization,
		}
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}

	return otlptracehttp.New(ctx, opts...)
}
