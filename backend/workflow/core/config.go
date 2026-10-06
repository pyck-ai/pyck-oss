package core

import (
	"context"
	"time"

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
	WorkflowSubscriptionConfig
	SignalRouterConsumerConfig
}

// SignalRouterConsumerConfig tunes the signal router's durable JetStream
// consumer; see services.ConsumerConfig. The defaults are the services
// defaults, repeated here so the env documentation and the code agree.
type SignalRouterConsumerConfig struct {
	ConsumerAckWait       time.Duration   `env:"PYCK_WORKFLOW_ROUTER_ACK_WAIT" envDefault:"60s"`
	ConsumerMaxAckPending int             `env:"PYCK_WORKFLOW_ROUTER_MAX_ACK_PENDING" envDefault:"256"`
	ConsumerMaxDeliver    int             `env:"PYCK_WORKFLOW_ROUTER_MAX_DELIVER" envDefault:"20"`
	ConsumerNakBackoff    []time.Duration `env:"PYCK_WORKFLOW_ROUTER_NAK_BACKOFF" envDefault:"1s,5s,30s,2m,5m"`
	ConsumerConcurrency   int             `env:"PYCK_WORKFLOW_ROUTER_CONCURRENCY" envDefault:"16"`

	// The health gate pauses fetching while Temporal or the database is down.
	HealthInterval  time.Duration `env:"PYCK_WORKFLOW_ROUTER_HEALTH_INTERVAL" envDefault:"5s"`
	HealthTimeout   time.Duration `env:"PYCK_WORKFLOW_ROUTER_HEALTH_TIMEOUT" envDefault:"2s"`
	HealthResumeMin time.Duration `env:"PYCK_WORKFLOW_ROUTER_RESUME_MIN_INTERVAL" envDefault:"1s"`
	HealthResumeMax time.Duration `env:"PYCK_WORKFLOW_ROUTER_RESUME_MAX_INTERVAL" envDefault:"5s"`

	// HealthPausedWarnInterval is how often a router that stays paused logs it.
	HealthPausedWarnInterval time.Duration `env:"PYCK_WORKFLOW_ROUTER_PAUSED_WARN_INTERVAL" envDefault:"1m"`
}

// WorkflowSubscriptionConfig tunes the worker-owned signal subscription
// lifecycle. Workers refresh their subscriptions (every 5m by default) well
// within SubscriptionTTL (1h by default); the janitor removes any that outlive
// it (e.g. crashed workers). It never removes workflow rows.
type WorkflowSubscriptionConfig struct {
	SubscriptionTTL             time.Duration `env:"PYCK_WORKFLOW_SUBSCRIPTION_TTL" envDefault:"1h"`
	SubscriptionJanitorInterval time.Duration `env:"PYCK_WORKFLOW_SUBSCRIPTION_JANITOR_INTERVAL" envDefault:"5m"`
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
