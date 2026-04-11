//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package otel

import (
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SamplerType defines the sampling strategy for OpenTelemetry traces.
//
//go:generate enumer -output=sampler_gen.go -type=SamplerType -linecomment
type SamplerType uint

const (
	SamplerTypeInvalid                 SamplerType = iota // invalid
	SamplerTypeAlwaysOn                                   // always_on
	SamplerTypeAlwaysOff                                  // always_off
	SamplerTypeTraceIDRatio                               // traceidratio
	SamplerTypeParentBasedAlwaysOn                        // parentbased_always_on
	SamplerTypeParentBasedAlwaysOff                       // parentbased_always_off
	SamplerTypeParentBasedTraceIDRatio                    // parentbased_traceidratio
)

// NewSampler creates a sampler based on the configuration.
// Uses the SamplerType enum for type-safe sampler selection.
//
//nolint:ireturn // Returning an interface is idiomatic for factory functions
func NewSampler(cfg *OTelConfig) sdktrace.Sampler {
	ratio := cfg.OpenTelemetrySamplerArg

	// Clamp ratio to valid range [0, 1]
	if ratio < 0.0 {
		ratio = 0.0
	} else if ratio > 1.0 {
		ratio = 1.0
	}

	switch cfg.OpenTelemetrySampler {
	case SamplerTypeAlwaysOn:
		return sdktrace.AlwaysSample()
	case SamplerTypeAlwaysOff:
		return sdktrace.NeverSample()
	case SamplerTypeTraceIDRatio:
		return sdktrace.TraceIDRatioBased(ratio)
	case SamplerTypeParentBasedAlwaysOn:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case SamplerTypeParentBasedAlwaysOff:
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case SamplerTypeParentBasedTraceIDRatio:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	default:
		// Default to parent-based with configured ratio
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	}
}
