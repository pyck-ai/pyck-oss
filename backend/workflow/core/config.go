package core

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/otel"
)

type config struct {
	envconfig.DbConfig
	envconfig.EnvironmentConfig
	envconfig.IdempotencyConfig
	envconfig.EventOutboxConfig
	envconfig.GatewayConfig
	envconfig.HTTPConfig
	envconfig.LogConfig
	envconfig.NatsConfig
	envconfig.ServiceConfig
	envconfig.ServiceInstanceConfig
	envconfig.TemporalConfig
	envconfig.ZitadelConfig

	otel.OTelConfig

	RemoteUIConfig
}

// RemoteUIConfig holds the system-wide fallbacks for per-workflow UI bundle
// resolution. All fields are optional: when a value is empty the corresponding
// fallback is disabled and the resolver errors instead (the pre-fallback
// behaviour). The per-tenant template (setTenantUITemplate) overrides the
// default template; a pinned deployment version overrides the default bundle.
type RemoteUIConfig struct {
	// DefaultWebUITemplate / DefaultMobileUITemplate are the system-wide URL
	// templates (with {{.Slug}}/{{.Version}} placeholders) used when a tenant has none
	// stored.
	DefaultWebUITemplate    string `env:"PYCK_REMOTE_UI_DEFAULT_WEB_TEMPLATE"`
	DefaultMobileUITemplate string `env:"PYCK_REMOTE_UI_DEFAULT_MOBILE_TEMPLATE"`

	// DefaultBundleSlug / DefaultBundleVersion are the bundle served when the
	// bundle can't be read from a pinned version — no pinned deployment version
	// (pre-versioning executions, workers not opted in, namespace without
	// versioning) or a pinned version not stamped yet. Defaulted to default/latest
	// so remoteUI keeps working through the #1132 rollout; the resolver logs a
	// warning when it falls back so a broken CI stamp is visible.
	DefaultBundleSlug    string `env:"PYCK_REMOTE_UI_DEFAULT_BUNDLE_SLUG" envDefault:"default"`
	DefaultBundleVersion string `env:"PYCK_REMOTE_UI_DEFAULT_BUNDLE_VERSION" envDefault:"latest"`
}

var Config config

func LoadEnv() (err error) {
	_, Config, err = env.Load[config](context.TODO())
	return err
}
