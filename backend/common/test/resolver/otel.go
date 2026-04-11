package resolver

import (
	"github.com/pyck-ai/pyck/backend/common/otel"
)

// SetupTestTracer initializes an OTel tracer for testing.
// Returns a cleanup function that should be called in TestMain after m.Run().
func SetupTestTracer(serviceName string) (func(), error) {
	tracer, err := otel.SetupTracer(serviceName+"-test", "test", &otel.OTelConfig{})
	if err != nil {
		return nil, err
	}
	return tracer.Close, nil
}

// MustSetupTestTracer initializes an OTel tracer for testing and panics on error.
// Returns a cleanup function that should be called in TestMain after m.Run().
func MustSetupTestTracer(serviceName string) func() {
	cleanup, err := SetupTestTracer(serviceName)
	if err != nil {
		panic("failed to setup test tracer: " + err.Error())
	}
	return cleanup
}
