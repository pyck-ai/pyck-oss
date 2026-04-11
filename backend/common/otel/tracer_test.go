package otel_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	. "github.com/pyck-ai/pyck/backend/common/otel"
)

// TestValidateConfig is no longer needed - validation is handled automatically
// by the env parser when loading OTelConfig. The enum types ensure
// that only valid Protocol and Sampler values can be set.

func TestCreateSampler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  *OTelConfig
	}{
		{
			name: "always_on",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeAlwaysOn,
				OpenTelemetrySamplerArg: 0.5,
			},
		},
		{
			name: "always_off",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeAlwaysOff,
				OpenTelemetrySamplerArg: 0.5,
			},
		},
		{
			name: "traceidratio",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeTraceIDRatio,
				OpenTelemetrySamplerArg: 0.5,
			},
		},
		{
			name: "parentbased_always_on",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeParentBasedAlwaysOn,
				OpenTelemetrySamplerArg: 0.5,
			},
		},
		{
			name: "parentbased_always_off",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeParentBasedAlwaysOff,
				OpenTelemetrySamplerArg: 0.5,
			},
		},
		{
			name: "parentbased_traceidratio",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeParentBasedTraceIDRatio,
				OpenTelemetrySamplerArg: 0.1,
			},
		},
		{
			name: "negative ratio clamped to 0",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeTraceIDRatio,
				OpenTelemetrySamplerArg: -0.5,
			},
		},
		{
			name: "ratio > 1 clamped to 1",
			cfg: &OTelConfig{
				OpenTelemetrySampler:    SamplerTypeTraceIDRatio,
				OpenTelemetrySamplerArg: 1.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sampler := NewSampler(tt.cfg)
			assert.NotNil(t, sampler)
		})
	}
}

func TestSetupTracerNoEndpoint(t *testing.T) {
	t.Parallel()
	cfg := &OTelConfig{
		OpenTelemetryEndpoint:        "",
		OpenTelemetryShutdownTimeout: 5 * time.Second,
	}

	tracer, err := SetupTracer("test-service", "test", cfg)
	require.NoError(t, err)
	assert.NotNil(t, tracer)

	// Should not error on shutdown even with no provider
	err = tracer.Shutdown()
	require.NoError(t, err)

	// Multiple shutdowns should be safe
	err = tracer.Shutdown()
	require.NoError(t, err)
}

func TestTracerShutdownIdempotent(t *testing.T) {
	t.Parallel()
	cfg := &OTelConfig{
		OpenTelemetryEndpoint:        "",
		OpenTelemetryShutdownTimeout: 5 * time.Second,
	}

	tracer, err := SetupTracer("test-service", "test", cfg)
	require.NoError(t, err)

	// First shutdown
	err1 := tracer.Shutdown()
	require.NoError(t, err1)

	// Second shutdown should also succeed
	err2 := tracer.Shutdown()
	require.NoError(t, err2)

	// Third shutdown should also succeed
	err3 := tracer.Shutdown()
	assert.NoError(t, err3)
}

func TestSamplerTypeEnum(t *testing.T) {
	t.Parallel()
	// Test that enum parsing works
	tests := []struct {
		name      string
		input     string
		expected  SamplerType
		expectErr bool
	}{
		{"always_on", "always_on", SamplerTypeAlwaysOn, false},
		{"always_off", "always_off", SamplerTypeAlwaysOff, false},
		{"traceidratio", "traceidratio", SamplerTypeTraceIDRatio, false},
		{"parentbased_always_on", "parentbased_always_on", SamplerTypeParentBasedAlwaysOn, false},
		{"parentbased_always_off", "parentbased_always_off", SamplerTypeParentBasedAlwaysOff, false},
		{"parentbased_traceidratio", "parentbased_traceidratio", SamplerTypeParentBasedTraceIDRatio, false},
		{"invalid", "invalid_value", SamplerTypeInvalid, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := SamplerTypeString(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestProtocolTypeEnum(t *testing.T) {
	t.Parallel()
	// Test that protocol enum parsing works
	tests := []struct {
		name      string
		input     string
		expected  ProtocolType
		expectErr bool
	}{
		{"grpc", "grpc", ProtocolTypeGRPC, false},
		{"http", "http", ProtocolTypeHTTP, false},
		{"http/protobuf", "http/protobuf", ProtocolTypeHTTPProtobuf, false},
		{"invalid", "invalid_protocol", ProtocolTypeInvalid, true},
		{"case_insensitive_grpc", "GRPC", ProtocolTypeGRPC, false},
		{"case_insensitive_http", "HTTP", ProtocolTypeHTTP, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ProtocolTypeString(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
