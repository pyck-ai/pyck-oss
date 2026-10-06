package config

import (
	"context"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
)

// EventWorkerConfig configures the event handler's worker pool and queue sizing.
type EventWorkerConfig struct {
	EventWorkerPoolSize  int `env:"PYCK_EVENT_WORKER_POOL_SIZE,notEmpty" envDefault:"10"`
	EventWorkerQueueSize int `env:"PYCK_EVENT_WORKER_QUEUE_SIZE,notEmpty" envDefault:"1000"`
	// EventWorkerPublishTimeout bounds a single publish attempt, not the whole retry loop.
	EventWorkerPublishTimeout time.Duration `env:"PYCK_EVENT_WORKER_PUBLISH_TIMEOUT,notEmpty" envDefault:"100ms"`
	// EventWorkerRetryInitialBackoff is the wait after the first failed publish attempt; it doubles per attempt.
	EventWorkerRetryInitialBackoff time.Duration `env:"PYCK_EVENT_WORKER_RETRY_INITIAL_BACKOFF,notEmpty" envDefault:"100ms"`
	// EventWorkerRetryMaxBackoff caps the wait between publish attempts.
	EventWorkerRetryMaxBackoff time.Duration `env:"PYCK_EVENT_WORKER_RETRY_MAX_BACKOFF,notEmpty" envDefault:"5s"`
	// EventWorkerShutdownGrace is how long Close lets workers drain queued events
	// before cancelling in-flight retries and dropping what is left.
	EventWorkerShutdownGrace time.Duration `env:"PYCK_EVENT_WORKER_SHUTDOWN_GRACE,notEmpty" envDefault:"5s"`
}

// EventAdapterConfig configures the temporal event adapter for workflow event broadcasting.
type EventAdapterConfig struct {
	EventAdapter                      AdapterType `env:"PYCK_EVENT_ADAPTER" envDefault:"default"`
	EventAdapterPostgresListenChannel string      `env:"PYCK_EVENT_ADAPTER_POSTGRES_LISTEN_CHANNEL" envDefault:"pyck_temporal_workflow_events"`
	// How long Start() should keep retrying to connect to the Temporal/Postgres DB
	EventAdapterPostgresConnectTimeout time.Duration `env:"PYCK_EVENT_ADAPTER_POSTGRES_CONNECT_TIMEOUT" envDefault:"120s"`
	// Interval between individual retry attempts
	EventAdapterPostgresRetryInterval time.Duration `env:"PYCK_EVENT_ADAPTER_POSTGRES_RETRY_INTERVAL" envDefault:"1s"`
	// Bound on each dial attempt to the Temporal frontend in Start's retry loop
	EventAdapterTemporalDialTimeout time.Duration `env:"PYCK_EVENT_ADAPTER_TEMPORAL_DIAL_TIMEOUT" envDefault:"30s"`
	// Bound on the lazy per-namespace Temporal client setup in the adapter's client factory
	EventAdapterTemporalClientCreationTimeout time.Duration `env:"PYCK_EVENT_ADAPTER_TEMPORAL_CLIENT_CREATION_TIMEOUT" envDefault:"30s"`
	// EventAdapterServices lists the Temporal services (frontend, internal-frontend,
	// history, matching, worker) whose process runs the PostgreSQL LISTEN adapter.
	// Empty means every process runs it, except one whose local TEMPORAL_ADDRESS it
	// does not serve (skipped). A listed role that does not serve it fails startup.
	// It does not affect the gRPC adapter.
	EventAdapterServices []string `env:"PYCK_EVENT_ADAPTER_SERVICES" envSeparator:","`
}

type config struct {
	envconfig.EnvironmentConfig
	envconfig.GatewayConfig
	envconfig.LogConfig
	envconfig.NatsConfig
	envconfig.ServiceConfig
	envconfig.ZitadelConfig

	EventWorkerConfig
	EventAdapterConfig
}

var Config config

func LoadEnv(ctx context.Context) (err error) {
	_, Config, err = env.Load[config](ctx)
	return err
}
