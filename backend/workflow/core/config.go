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
}

var Config config

func LoadEnv() (err error) {
	_, Config, err = env.Load[config](context.TODO())
	return err
}
