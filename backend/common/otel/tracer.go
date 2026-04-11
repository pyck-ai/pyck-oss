package otel

import (
	"context"
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/backend/common/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Tracer wraps the OpenTelemetry TracerProvider and provides automatic shutdown handling.
type Tracer struct {
	provider        *sdktrace.TracerProvider
	shutdownTimeout time.Duration
	shutdownCalled  bool
}

// SetupTracer initializes OpenTelemetry tracing and returns a Tracer wrapper.
// The returned Tracer provides a Shutdown method that should be called on application exit.
// If cfg.OpenTelemetryEndpoint is empty, tracing is disabled (noop mode).
//
// Example usage:
//
//	tracer, err := otel.SetupTracer(serviceName, environment, cfg)
//	if err != nil {
//		log.Fatal().Err(err).Msg("failed to setup tracer")
//	}
//	defer tracer.ShutdownAndLog()
func SetupTracer(serviceName, environment string, cfg *OTelConfig) (*Tracer, error) {
	logger := log.DefaultLogger().With().
		Str("component", "otel-tracer").
		Str("service", serviceName).
		Logger()

	// Always initialize provider to ensure trace IDs are generated for correlation,
	// even when no exporter endpoint is configured
	tp, err := initProvider(serviceName, environment, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tracer: %w", err)
	}

	if cfg.OpenTelemetryEndpoint == "" {
		logger.Info().Msg("tracing initialized without exporter (no endpoint configured)")
	} else {
		logger.Info().
			Str("environment", environment).
			Str("endpoint", cfg.OpenTelemetryEndpoint).
			Str("protocol", cfg.OpenTelemetryProtocol.String()).
			Str("sampler", cfg.OpenTelemetrySampler.String()).
			Float64("sampler_arg", cfg.OpenTelemetrySamplerArg).
			Msg("tracer initialized and running")
	}

	return &Tracer{
		provider:        tp,
		shutdownTimeout: cfg.OpenTelemetryShutdownTimeout,
	}, nil
}

// Shutdown gracefully shuts down the tracer provider, ensuring all spans are flushed.
// Uses the configured shutdown timeout. It should be called when the application exits.
// Safe to call multiple times.
func (t *Tracer) Shutdown() error {
	if t.shutdownCalled {
		return nil
	}
	t.shutdownCalled = true

	if t.provider == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.shutdownTimeout)
	defer cancel()

	if err := t.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown tracer provider: %w", err)
	}
	return nil
}

// Close is a convenience wrapper that shuts down the tracer and logs any errors.
// This is intended to be used with defer in service main functions.
func (t *Tracer) Close() {
	if err := t.Shutdown(); err != nil {
		logger := log.DefaultLogger().With().
			Str("component", "otel-tracer").
			Logger()
		logger.Error().Err(err).Msg("failed to shutdown tracer provider")
	}
}
