package config

import (
	"context"
	"time"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
)

// EventWorkerConfig configures the event handler's worker pool and queue sizing.
type EventWorkerConfig struct {
	EventWorkerPoolSize       int           `env:"PYCK_EVENT_WORKER_POOL_SIZE,notEmpty" envDefault:"10"`
	EventWorkerQueueSize      int           `env:"PYCK_EVENT_WORKER_QUEUE_SIZE,notEmpty" envDefault:"1000"`
	EventWorkerPublishTimeout time.Duration `env:"PYCK_EVENT_WORKER_PUBLISH_TIMEOUT,notEmpty" envDefault:"100ms"`
}

// EventAdapterConfig configures the temporal event adapter for workflow event broadcasting.
type EventAdapterConfig struct {
	EventAdapter                      AdapterType `env:"PYCK_EVENT_ADAPTER" envDefault:"default"`
	EventAdapterPostgresListenChannel string      `env:"PYCK_EVENT_ADAPTER_POSTGRES_LISTEN_CHANNEL" envDefault:"pyck_temporal_workflow_events"`
	// How long Start() should keep retrying to connect to the Temporal/Postgres DB
	EventAdapterPostgresConnectTimeout time.Duration `env:"PYCK_EVENT_ADAPTER_POSTGRES_CONNECT_TIMEOUT" envDefault:"120s"`
	// Interval between individual retry attempts
	EventAdapterPostgresRetryInterval time.Duration `env:"PYCK_EVENT_ADAPTER_POSTGRES_RETRY_INTERVAL" envDefault:"1s"`
}

type config struct {
	envconfig.EnvironmentConfig
	envconfig.LogConfig
	envconfig.NatsConfig
	envconfig.ZitadelConfig

	EventWorkerConfig
	EventAdapterConfig
}

var Config config

func LoadEnv(ctx context.Context) (err error) {
	_, Config, err = env.Load[config](ctx)
	return err
}
