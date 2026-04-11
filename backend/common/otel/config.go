package otel

import "time"

type OTelConfig struct {
	OpenTelemetryEndpoint        string        `env:"PYCK_OTEL_EXPORTER_OTLP_ENDPOINT"`
	OpenTelemetryAuthorization   string        `env:"PYCK_OTEL_EXPORTER_OTLP_AUTHORIZATION" json:"-"`
	OpenTelemetryProtocol        ProtocolType  `env:"PYCK_OTEL_EXPORTER_OTLP_PROTOCOL" envDefault:"http"`
	OpenTelemetryTimeout         time.Duration `env:"PYCK_OTEL_EXPORTER_OTLP_TIMEOUT" envDefault:"5s"`
	OpenTelemetrySampler         SamplerType   `env:"PYCK_OTEL_TRACES_SAMPLER" envDefault:"always_on"`
	OpenTelemetrySamplerArg      float64       `env:"PYCK_OTEL_TRACES_SAMPLER_ARG" envDefault:"0.1"`
	OpenTelemetryShutdownTimeout time.Duration `env:"PYCK_OTEL_SHUTDOWN_TIMEOUT" envDefault:"5s"`
}
