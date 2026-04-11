package workflowsdk

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/otel"
)

type WorkflowConfig struct {
	WorkflowMocking bool `env:"PYCK_WORKFLOW_MOCKING,notEmpty"`
}

type config struct {
	envconfig.EnvironmentConfig
	envconfig.GatewayConfig
	envconfig.LogConfig
	otel.OTelConfig
}

var Config config

func LoadEnv(ctx context.Context) error {
	_, c, err := env.Load[config](ctx)
	if err != nil {
		return err
	}

	Config = c

	return nil
}
